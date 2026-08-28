//! ============================================================================
//! main.rs —— Rust 侧网关入口:构建 SMB 服务器、连接 Go 网关
//! ============================================================================
//!
//! 基于 rust-smb-server 模板(参考:rust-smb-server/src/main.rs + winfs.rs),
//! 改造为"网关模式":
//!
//! ```text
//!   Windows Explorer / Linux cifs
//!            │  2445 (SMB 2.02~3.1.1,ixr-smb-server)
//!            ▼
//!   SmbServer(builder)
//!      ├─ 初始无静态共享:共享全部由 sync 模块经 ConfigHandle 动态注册
//!      │   (启动快照:Go 网关按"每桶一共享"下发桶定义)
//!      └─ 后端 = RemoteBackend(转发文件操作 RPC 到 Go 网关)
//!            │  出站私有 TCP(共享密钥 + 帧协议)
//!            ▼
//!   Go 网关(smb_server/go:auth.go + file_ops.go + gateway.go)
//! ```
//!
//! # 启动流程
//! 1. 先处理命令行指令:打印帮助、打印版本号,或把内置默认配置落盘后退出;
//! 2. 从工作目录读取并校验配置文件,缺失时自动落盘内置默认配置;
//! 3. 初始化日志(与 Go 侧对齐:每日文件轮转 + 本地 SQLite 入库 + 定时清理);
//! 4. 从环境变量读取共享密钥并校验长度;
//! 5. 连接 Go 网关(握手),失败按退避节奏重试(1s/5s/30s 封顶);
//! 6. 构建 SMB 服务器,启动同步任务(全量快照 + 增量推送 + 定期对账);
//! 7. 绑定监听端口进入服务循环;收到退出信号时优雅关闭。

// 帧协议契约类型与常量(与 Go 侧 types.go 镜像)。
#![expect(
    dead_code,
    reason = "帧协议契约类型/常量:部分由 sync/registry 消费,其余为后续扩展预留"
)]

mod core; // 全局配置与运行时单例
#[macro_use]
mod log; // 日志模块(与 Go 侧 orbitcloud/log 对齐;宏经 #[macro_use] 对本 crate 可见)
mod flag; // 配置读取/解析与命令行入口
mod registry; // 桶实例注册表(连接意图 → 桶实例)
mod remote_backend; // 到 Go 网关的帧协议客户端 + SMB 后端适配
mod sync; // 共享对账 + 增量推送(用户维度按需缓存)
mod types; // 帧协议契约类型与常量
mod user_cache; // 用户快照缓存(大 key 库,认证按需拉取)

use std::sync::Arc; // Arc:跨任务共享所有权(连接/注册表等)
use std::time::Duration; // 时长类型(退避等待/对账周期)

use smb_server::ConfigHandle; // 协议层配置句柄(桥接槽位用)
use smb_server::SmbServer; // ixr-smb-server 的 SMB 服务端构建器

use crate::core::enter::Config; // 全局配置结构(解析自 SMB.yaml)
use crate::registry::BucketRegistry; // 桶实例注册表
use crate::remote_backend::GatewayClient; // Go 网关客户端(私有 TCP 长连接)
use crate::sync::sync_loop; // 同步常驻任务(全量快照 + 增量推送 + 对账)

/// 程序入口(tokio 异步运行器)。
#[tokio::main] // 启动 tokio 多线程异步运行时
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    // ---- 1. 命令行指令(-initConfig / --version / --help / --WithConfig)----
    let args: Vec<String> = std::env::args().collect(); // 收集命令行参数(首元素为程序名)
    let cfg: Config = match flag::enter::parse_args(&args)? { // 解析指令:Some=继续启动,None=指令已处理
        Some(cfg) => cfg, // 需要正常启动,拿到配置
        None => return Ok(()), // 指令已处理(落盘/打印),正常退出
    };

    // ---- 2. 日志初始化(与 Go 侧 orbitcloud/log 对齐)----
    // 文件按日期轮转 + 启动时刻命名、本地 SQLite 入库、定时清理过期记录
    // (开关/保留天数/清理周期见 SMB.yaml log: 分节;RUST_LOG 可覆盖级别)。
    log::init(&cfg.log)?; // 失败终止启动(文件/SQLite 不可用属致命错误)

    // ---- 3. 共享密钥(shared_key 配置直配优先;其次 shared_key_env 环境变量)----
    // 两者皆空:跳过静态鉴权,握手后动态密钥协商(接口后续实现;空密钥下
    // HMAC 双侧一致,握手仍可通过,鉴权降级为无)。
    let key = flag::enter::load_shared_key(&cfg.gateway.shared_key, &cfg.gateway.shared_key_env)?; // 从配置/环境变量读密钥,显式配置过短即报错终止

    // ---- 4. 连接 Go 网关(退避重试:1s → 5s → 30s 封顶)----
    let client_id = format!("{}-{}", hostname(), std::process::id()); // 客户端标识=主机名-进程ID(多实例隔离远程句柄表)
    let rpc_timeout = std::time::Duration::from_secs(cfg.gateway.rpc_timeout_secs.max(1)); // RPC 超时(可配置,下限 1s)
    let (conn, push_rx) = connect_with_retry(
        &cfg.gateway.addr, // 网关地址(如 127.0.0.1:9001)
        key.clone(), // 共享密钥(克隆一份,重连时还要用)
        client_id.clone(), // 客户端标识
        cfg.gateway.channel_buffer, // 发送管道缓冲容量(设计点 6)
        rpc_timeout, // 请求应答超时(大文件 Flush 放宽)
    ).await?;
    infof!("网关:已连接 {} (客户端 {})", &["SMB"], cfg.gateway.addr, hostname()); // 记录连接成功

    // ---- 5. 构建 SMB 服务器(初始无共享,全部动态注册)----
    // 桶实例注册表提前创建:认证按需解析器(user_resolver)需要引用它
    // (users_index 身份映射由按需拉取维护)。
    let registry = Arc::new(BucketRegistry::new()); // 桶实例注册表(Arc:sync 任务写、协议层读)
    // 用户快照缓存(大 key 库):TTL 自动过期 + 容量上限兜底;认证未命中时
    // 经 CODE_AUTH_QUERY_USER 按需拉取并回填(取代全量用户表对账)。
    let user_cache = crate::user_cache::UserCache::new(
        Duration::from_secs(cfg.gateway.user_cache_ttl_secs.max(1)), // TTL(秒,下限 1)
        cfg.gateway.user_cache_capacity.max(1), // 容量上限(下限 1)
    );
    let listen: std::net::SocketAddr = cfg
        .smb
        .listen
        .parse() // 把配置字符串解析为 SocketAddr
        .map_err(|e| format!("配置:smb.listen {:?}: {e}", cfg.smb.listen))?; // 解析失败:带原文报错终止
    // 配置句柄延迟桥:builder 注入 resolver 时 handle 尚未创建(build 后才可用),
    // 认证在运行期发生,届时从槽位取已填充的句柄。
    let handle_slot: Arc<std::sync::Mutex<Option<ConfigHandle>>> =
        Arc::new(std::sync::Mutex::new(None));
    let server: SmbServer = SmbServer::builder() // 构建 SMB 服务端
        .listen(listen) // 监听地址(如 0.0.0.0:2445)
        .netbios_name(&cfg.smb.netbios_name) // NetBIOS 显示名
        .user_resolver({
            // 认证按需解析器(连接用户维度):
            //   1) 凭据:缓存命中直接返回;未命中/过期 → QUERY_USER 拉取并回填;
            //   2) 授权:凭据可用后 → QUERY_ACL 拉该用户可见共享并授权
            //      (注册表 upsert + grant;权限收紧即时 revoke)。
            let registry2 = Arc::clone(&registry);
            let cache2 = user_cache.clone();
            let conn2 = Arc::clone(&conn);
            let handle2 = Arc::clone(&handle_slot);
            move |username: String| {
                let registry3 = Arc::clone(&registry2);
                let cache3 = cache2.clone();
                let conn3 = Arc::clone(&conn2);
                let handle3 = Arc::clone(&handle2);
                Box::pin(async move {
                    let Some(cached) = cache3
                        .ensure(&conn3, &registry3.users_index(), &username)
                        .await
                    else {
                        return None;
                    };
                    // 凭据可用后:先把凭据写入协议层用户表(ServerState.ensure_user
                    // 在 resolver 返回后才写表,而 grant 要求表内已存在该用户),
                    // 再按需刷新共享授权(失败不阻断认证,下次再试)。
                    // 注意:句柄先克隆出锁作用域再 await(guard 非 Send)
                    let handle_opt = handle3.lock().unwrap().clone();
                    if let Some(h) = handle_opt {
                        let hash_hex = hex::encode(cached.creds.nt_hash);
                        if let Err(e) = h.add_user_nt_hash(&username, &hash_hex).await {
                            warnf!("缓存:写入用户 {username} 凭据失败:{e:?}", &["SMB"]);
                        }
                        cache3
                            .ensure_user_acl(&conn3, &registry3, &h, &username)
                            .await;
                    }
                    Some(cached.creds)
                })
            }
        })
        .build() // 完成构建
        .map_err(|e| format!("SMB 服务器构建失败:{e:?}"))?; // 构建失败终止
    let handle = server.config_handle(); // 配置句柄:后续动态注册用户/共享/ACL 都经它
    *handle_slot.lock().unwrap() = Some(handle.clone()); // 填充桥接槽位(认证时取用)

    // ---- 6. 启动同步任务(共享全量对账 + 增量推送;用户按需缓存)----
    let interval = if cfg.gateway.sync_interval_secs > 0 {
        Duration::from_secs(cfg.gateway.sync_interval_secs) // 用配置的对账周期
    } else {
        Duration::from_secs(60) // 配置 ≤0 时兜底 60s
    };

    // ---- 连接监督:断线自动重连(Go 网关重启/网络抖动时自愈)----
    let mut current = Arc::clone(&conn); // 当前连接引用(mut:重连成功后替换为新连接)
    let mut sync_task: Option<tokio::task::JoinHandle<()>> = None; // 同步任务句柄(重连时先 abort 旧的)
    let addr = cfg.gateway.addr.clone(); // 预克隆网关地址(移入后台任务)
    let key2 = key.clone(); // 预克隆密钥(重连握手用)
    let client_id2 = client_id.clone(); // 预克隆客户端标识(重连握手用)
    let chbuf = cfg.gateway.channel_buffer; // 预取管道容量(重连建管道用)
    let rpc_timeout2 = rpc_timeout; // 预取 RPC 超时(重连建客户端用)
    {
        let registry2 = Arc::clone(&registry); // 克隆注册表引用给同步任务
        let handle2 = handle.clone(); // 克隆配置句柄给同步任务
        let cache2 = user_cache.clone(); // 克隆用户缓存给同步任务
        sync_task = Some(tokio::spawn(sync_loop( // 首次拉起同步任务(后台常驻)
            Arc::clone(&current), // 共享当前连接
            registry2, // 桶注册表
            handle2, // 配置句柄
            cache2, // 用户快照缓存(认证按需)
            push_rx, // 增量推送接收端(MSG_AUTH_PUSH)
            interval, // 共享对账周期
        )));
        infof!("同步任务已启动(每 {} 秒对账一次)", &["SMB"], interval.as_secs()); // 记录同步任务启动
    }
    // 后台监督任务:每 2s 检查 is_alive;断线后重连,成功后重启同步任务
    tokio::spawn(async move {
        let mut delay = 1u64; // 退避计时(1s→2s→4s→…→30s 封顶)
        loop {
            tokio::time::sleep(Duration::from_secs(2)).await; // 固定 2s 轮询间隔
            if current.is_alive() { // 连接仍存活?
                delay = 1; // 健康:退避归零
                continue; // 继续下一轮轮询
            }
            warnf!("网关:连接断开,正在重连...", &["SMB"]); // 检测到断线
            match GatewayClient::connect(&addr, key2.clone(), client_id2.clone(), chbuf, rpc_timeout2).await { // 重连+握手
                Ok((new_conn, new_push_rx)) => { // 重连成功
                    current = Arc::clone(&new_conn); // 换用新连接(后续轮询检查它)
                    if let Some(t) = sync_task.take() { // 取出旧同步任务句柄
                        t.abort(); // 终止旧任务(绑定的是已死连接)
                    }
                    let registry3 = Arc::clone(&registry); // 重新克隆注册表
                    let handle3 = handle.clone(); // 重新克隆配置句柄
                    let cache3 = user_cache.clone(); // 重新克隆用户缓存
                    sync_task = Some(tokio::spawn(sync_loop( // 用新连接重建同步任务
                        Arc::clone(&new_conn), // 新连接
                        registry3, // 桶注册表
                        handle3, // 配置句柄
                        cache3, // 用户快照缓存(认证按需)
                        new_push_rx, // 新推送接收端
                        interval, // 共享对账周期
                    ))); // 启动即共享对账:补回断线期间丢失的增量推送
                    infof!("网关:已重新连接", &["SMB"]); // 记录重连成功
                    delay = 1; // 退避归零
                }
                Err(e) => { // 重连失败
                    warnf!("网关:重连失败:{e};{delay} 秒后重试", &["SMB"]); // 记录失败与下次等待
                    tokio::time::sleep(Duration::from_secs(delay)).await; // 退避等待
                    delay = (delay * 2).min(30); // 翻倍延时,封顶 30s 防高频重试
                }
            }
        }
    });

    // ---- 7. 绑定并进入服务循环 ----
    server.bind().await.map_err(|e| format!("SMB 服务器绑定端口失败:{e:?}"))?; // 真正绑定监听端口
    infof!("SMB 网关正在监听 {}", &["SMB"], listen); // 记录监听地址

    let shutdown = server.shutdown_handle(); // 关闭句柄(Ctrl-C 时优雅关闭用)
    tokio::select! {
        r = server.serve() => { // 分支 1:进入 SMB 服务循环(阻塞直到退出)
            r.map_err(|e| format!("SMB 服务器服务出错:{e:?}"))?; // 服务异常退出则上报错误
        }
        _ = tokio::signal::ctrl_c() => { // 分支 2:收到 Ctrl-C 信号
            infof!("SMB 网关:收到退出信号,开始优雅关闭", &["SMB"]); // 记录退出信号
            shutdown.shutdown(); // 触发服务器优雅关闭
        }
    }

    // ---- 8. 优雅退出 ----
    infof!("SMB 网关已正常退出", &["SMB"]); // 走到这里即正常退出
    Ok(())
}

/// 连接 Go 网关,失败按 1s/5s/30s 封顶退避重试(永不放弃,直到成功)。
async fn connect_with_retry(
    addr: &str, // 网关地址
    key: Vec<u8>, // 共享密钥(≥16 字节)
    client_id: String, // 客户端标识
    channel_buffer: usize, // 发送管道容量
    rpc_timeout: std::time::Duration, // 请求应答超时
) -> Result<(Arc<GatewayClient>, tokio::sync::mpsc::Receiver<Vec<u8>>), Box<dyn std::error::Error>> {
    let mut delay = 1u64; // 当前退避秒数(1s→2s→4s→…→30s 封顶)
    loop {
        match GatewayClient::connect(addr, key.clone(), client_id.clone(), channel_buffer, rpc_timeout).await { // 连接并完成双向握手
            Ok(pair) => return Ok(pair), // 成功:返回 (客户端, 推送接收端)
            Err(e) => { // 失败
                warnf!("网关:连接 {} 失败:{e};{delay} 秒后重试", &["SMB"], addr); // 记录失败与退避时长
                tokio::time::sleep(Duration::from_secs(delay)).await; // 退避等待
                delay = (delay * 2).min(30); // 指数退避,封顶 30s(永不放弃,因 SMB 依赖网关)
            }
        }
    }
}

/// 本机标识(握手 client_id 用,多实例隔离远程句柄表)。
/// 返回:主机名;获取失败退化为 "unknown"。
fn hostname() -> String {
    std::env::var("HOSTNAME").unwrap_or_else(|_| { // 先试 HOSTNAME(Linux)
        std::env::var("COMPUTERNAME").unwrap_or_else(|_| "unknown".into()) // 再试 COMPUTERNAME(Windows),都没有则 "unknown"
    })
}
