//! ============================================================================
//! sync.rs —— 用户/共享/ACL 同步(驱动桶实例注册表 + ConfigHandle)
//! ============================================================================
//!
//! 数据流(用户维度已改为"按需缓存",见 user_cache.rs):
//! ```text
//!   Go 网关(权威源:users/buckets/ACL 表)
//!      │ ① 启动/重连:MSG_OPERATE(CodeAuthSnapshot) 全量快照(仅取共享段)
//!      │ ② 常驻:MSG_AUTH_PUSH 增量变更(用户/共享/ACL upsert|delete)
//!      │ ③ 按需:MSG_OPERATE(CodeAuthQueryUser) 认证未命中时拉单用户
//!      ▼
//!   sync.rs ──► BucketRegistry(桶实例表,一个桶 = 一台 SMB 服务端)
//!      │           │  桶实例增删查 + 桶级元数据(配额/状态)
//!      │           ▼  同步调用
//!      │        ConfigHandle(add_user_nt_hash / add_share / grant...)
//!      ▼
//!   用户表:不再全量镜像,认证时经 UserCache 按需拉取并写入内存表;
//!   桶(共享)一律经注册表 upsert/delete(保持全量对账)。
//! ```

use std::sync::Arc; // Arc:连接/注册表跨任务共享
use std::time::Duration; // 对账周期类型

use smb_server::ConfigHandle; // 库的配置句柄(用户表 + 共享注册)

use crate::registry::BucketRegistry; // 桶实例注册表
use crate::remote_backend::GatewayClient; // 网关客户端(发起快照 RPC)
use crate::types::*; // 帧协议契约(统一取用)
use crate::user_cache::UserCache; // 用户快照缓存(大 key 库,按需拉取)

// ============================================================================
// 同步任务入口
// ============================================================================

/// 启动同步任务(后台常驻):共享全量对账 + 增量推送;用户表按需缓存。
///
/// 参数:
/// - `conn`:到 Go 网关的连接(共享);
/// - `registry`:桶实例注册表;
/// - `handle`:库的 ConfigHandle(协议层共享注册;用户表由缓存按需写入);
/// - `cache`:用户快照缓存(认证按需拉取 + 推送即时维护);
/// - `push_rx`:变更推送接收端(MSG_AUTH_PUSH body);
/// - `interval`:共享对账周期(增量推送丢失时的兜底)。
///
/// 返回:该任务常驻;断线重连由调用方(main)驱动重跑本函数。
pub async fn sync_loop(
    conn: Arc<GatewayClient>, // 网关连接(共享引用)
    registry: Arc<BucketRegistry>, // 桶注册表(共享引用)
    handle: ConfigHandle, // 配置句柄
    cache: UserCache, // 用户快照缓存(认证按需)
    mut push_rx: tokio::sync::mpsc::Receiver<Vec<u8>>, // 增量推送接收端(mut:被 recv 消费)
    interval: Duration, // 共享对账周期
) {
    if let Err(e) = reconcile(&conn, &registry, &handle).await { // 启动即共享全量对账
        errorf!("同步:初始快照失败:{e}", &["SMB"]); // 失败不退出(仅记日志):推送/对账会再收敛
    }

    let mut ticker = tokio::time::interval(interval); // 定时器:按对账周期触发
    loop {
        tokio::select! { // 双路事件驱动:增量推送 vs 定时对账,谁先到处理谁
            maybe = push_rx.recv() => { // 分支 a:收到增量变更推送(MSG_AUTH_PUSH body)
                match maybe {
                    Some(body) => { // 有推送数据
                        match serde_json::from_slice::<AclEntry>(&body) { // body 就是一条 AclEntry 的 JSON
                            Ok(entry) => { // 解析成功
                                if entry.op == "notify" && entry.kind == "dir" { // 目录变更通知?
                                    continue; // 无 CHANGE_NOTIFY 能力,直接忽略(等下次对账)
                                }
                                if let Err(e) = apply_push(&registry, &conn, &handle, &cache, entry).await { // 应用单条变更
                                    warnf!("同步:应用推送失败:{e}", &["SMB"]); // 失败仅告警,不打断推送流
                                }
                            }
                            Err(e) => warnf!("同步:解析推送条目失败:{e}", &["SMB"]), // 单条解析失败仅告警
                        }
                    }
                    None => { // 通道关闭 = 连接生命周期结束
                        warnf!("同步:推送通道已关闭,停止同步循环", &["SMB"]); // 记录后退出
                        return; // 本任务结束(重连后 main 会重启)
                    }
                }
            }
            _ = ticker.tick() => { // 分支 b:定时共享对账(兜底一致性;用户维度已按需,不再全量)
                if let Err(e) = reconcile(&conn, &registry, &handle).await { // 重拉快照并应用共享部分
                    errorf!("同步:对账失败:{e}", &["SMB"]); // 失败仅记错误日志
                }
            }
        }
    }
}

/// 应用全量快照(启动/重连时把 Go 权威源状态同步到内存)。
///
/// 仅同步**共享/桶注册表**(差集 upsert/delete);用户维度已改为按需缓存
/// (user_cache),快照中的 users 段在此忽略,不再全量重建用户表。
///
/// 参数:
/// - `registry`:桶实例注册表;
/// - `conn`:到 Go 网关的连接(注册表构造 RemoteBackend 用);
/// - `handle`:ConfigHandle(共享注册用);
/// - `snap`:全量快照(用户 + 桶/共享)。
pub async fn apply_snapshot(
    registry: &BucketRegistry, // 桶实例注册表
    conn: &Arc<GatewayClient>, // 网关连接
    handle: &ConfigHandle, // 配置句柄
    snap: &SnapshotResult, // 全量快照(权威源)
) -> Result<(), String> {
    // 用户维度按需缓存:快照 users 段不再全量写入(认证未命中时经 user_cache
    // 按需拉取;变更由推送即时维护),此处仅同步共享注册表。
    registry.apply_snapshot(snap, conn, handle).await?; // 桶/共享:整体交给注册表做差集同步(内部联动协议层)
    Ok(())
}

/// 应用单条增量变更推送(MSG_AUTH_PUSH)。
///
/// 参数:
/// - `registry`:桶实例注册表;
/// - `conn`:到 Go 网关的连接(注册表构造 RemoteBackend 用);
/// - `handle`:ConfigHandle;
/// - `cache`:用户快照缓存(推送即时覆盖/失效);
/// - `entry`:变更条目(op/kind 见 AclEntry)。
pub async fn apply_push(
    registry: &BucketRegistry, // 桶实例注册表
    conn: &Arc<GatewayClient>, // 网关连接
    handle: &ConfigHandle, // 配置句柄
    cache: &UserCache, // 用户快照缓存(推送即时维护)
    entry: AclEntry, // 变更条目(按 op/kind 路由)
) -> Result<(), String> {
    debugf!("同步:推送 操作={} 类型={} 共享={}", &["SMB"], entry.op, entry.kind, entry.share_name); // 记录变更推送(操作/类型/共享)
    let users_index = registry.users_index(); // 用户索引(操作身份映射,随推送维护)
    match (entry.op.as_str(), entry.kind.as_str()) { // 按 (op, kind) 组合路由到对应处理
        ("upsert", "user") => { // 用户新增/更新 → 缓存即时覆盖 + 写入内存用户表
            let user = entry.user.ok_or("推送:条目缺少用户数据")?; // 取用户载荷,缺失即报错
            cache.apply_push_cred(&user); // 缓存即时覆盖/失效(TTL 之外的安全通道)
            if user.status != 1 || user.nt_hash_hex.is_empty() { // 禁用/无 hash 用户?
                let _ = handle.remove_user(&user.username).await; // 主动移除(等价下线,不可登录)
                users_index.remove(&user.username).await; // 同步移除身份映射
                return Ok(());
            }
            handle
                .add_user_nt_hash(&user.username, &user.nt_hash_hex) // 更新用户表条目
                .await
                .map_err(|e| format!("推送:添加用户 {} 失败:{e}", user.username))?; // 失败带上用户名报错
            users_index.upsert(&user.username, user.id).await; // 同步登记身份映射
            Ok(())
        }
        ("delete", "user") => { // 用户删除 → 缓存失效 + 从用户表移除
            let user = entry.user.ok_or("推送:条目缺少用户数据")?; // 取用户载荷
            cache.invalidate(&user.username); // 缓存立即失效(不可再按需拉取)
            let _ = handle.remove_user(&user.username).await; // 移除(此后该用户名登录失败)
            users_index.remove(&user.username).await; // 同步移除身份映射
            infof!("同步:用户 {} 已删除", &["SMB"], user.username); // 记录删除
            Ok(())
        }
        ("upsert", "share") => { // 共享新增/更新 → 注册表 upsert
            let share = entry.share.ok_or("推送:条目缺少共享数据")?; // 取共享定义
            registry.upsert(&share, conn, handle).await // 重建协议层共享 + 逐用户授权
        }
        ("delete", "share") => { // 共享删除 → 注册表 delete
            let share = entry.share.ok_or("推送:条目缺少共享数据")?; // 取共享定义
            registry.delete(&share.share_name, handle).await // 关闭全部活跃树连接并下线
        }
        ("upsert", "acl") => { // ACL 授权更新 → 只动单用户授权
            let acl = entry.acl.ok_or("推送:条目缺少 ACL 数据")?; // 取 ACL 条目
            handle
                .grant_share_user(&entry.share_name, &acl.username, access_from(acl.access)) // 授予/覆盖授权
                .await
                .map_err(|e| format!("推送:授权 {} 于 {} 失败:{e}", acl.username, entry.share_name)) // 失败报错
        }
        ("delete", "acl") => { // ACL 撤销 → 移除单用户授权
            let acl = entry.acl.ok_or("推送:条目缺少 ACL 数据")?; // 取 ACL 条目
            handle
                .revoke_share_user(&entry.share_name, &acl.username) // 撤销授权
                .await
                .map_err(|e| format!("push: revoke {} on {}: {e}", acl.username, entry.share_name)) // 失败报错
        }
        (op, kind) => Err(format!("push: unknown op/kind {op}/{kind}")), // 未知组合:报错(协议不兼容)
    }
}

/// 共享全量对账:拉取当前快照并应用共享注册表部分(用户维度按需缓存,不重建)。
///
/// 参数:
/// - `conn`:GatewayClient(发起 MSG_OPERATE(CodeAuthSnapshot));
/// - `registry`:桶实例注册表;
/// - `handle`:ConfigHandle。
pub async fn reconcile(
    conn: &Arc<GatewayClient>, // 网关连接
    registry: &BucketRegistry, // 桶注册表
    handle: &ConfigHandle, // 配置句柄
) -> Result<(), String> {
    let req = OperateRequest { // 组装快照请求
        code: CODE_AUTH_SNAPSHOT, // 操作码:全量同步快照
        snapshot: Some(SnapshotArgs {}), // 空参数占位(快照无入参)
        ..Default::default() // 其余字段 None
    };
    let body = encode_operate_body(&req, &[]).map_err(|e| format!("sync: encode snapshot: {e}"))?; // 编码控制面 body(无流段)
    let resp_body = conn
        .call(MSG_OPERATE, body) // 发起 RPC 等应答(超时/断线返回 ERR_TIMEOUT/ERR_GATEWAY_DOWN)
        .await
        .map_err(|code| format!("sync: snapshot call failed (code {code})"))?; // 连接层失败:带哨兵码报错
    let (json_bytes, _) = split_operate_body(&resp_body) // 拆响应:JSON 段 + 流段(快照无流段,丢弃)
        .map_err(|e| format!("sync: split snapshot resp: {e}"))?;
    let resp: OperateResponse =
        serde_json::from_slice(json_bytes).map_err(|e| format!("sync: snapshot resp json: {e}"))?; // 反序列化总响应
    if let Some(err) = resp.err { // 业务错误?
        return Err(format!( // 带上错误详情返回(未授权/网关内部错误等)
            "sync: snapshot rejected: {} (code {})",
            err.message, err.code
        ));
    }
    let snap = resp.snapshot.ok_or("sync: snapshot result missing")?; // 取快照结果,缺失即协议错误
    debugf!("同步:收到快照:{} 用户,{} 共享", &["SMB"], snap.users.len(), snap.shares.len()); // 记录快照规模

    apply_snapshot(registry, conn, handle, &snap).await // 应用共享注册表部分(用户段忽略)
}

/// access 字符串 → 库的 Access(与 registry::access_from_str 同义,避免循环)。
fn access_from(access: String) -> smb_server::Access {
    crate::registry::access_from_str(&access) // 委托注册表模块的转换函数
}

/// 推送通道的 body 解析便捷(AclEntry)。
#[allow(unused)] // 保留:便捷封装,当前由 sync_loop 内联解析
fn parse_push_body(body: &[u8]) -> Result<AclEntry, serde_json::Error> {
    serde_json::from_slice(body) // 把 JSON body 反序列化为变更条目
}
