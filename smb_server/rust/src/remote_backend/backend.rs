//! ============================================================================
//! remote_backend/backend.rs —— RemoteBackend:实现库的 ShareBackend
//! ============================================================================
//!
//! 把 SMB 操作翻译为对 Go 网关的帧协议 RPC(经 client.rs 的 GatewayClient):
//! - RemoteBackend 实现库的 `ShareBackend`(存储抽象层,每共享一实例);
//! - call_operate:控制面调用统一入口(编码总操作 JSON → call → 解析总响应);
//! - intent_str / map_gateway_err:OpenIntent 翻译与哨兵错误码 → SmbError 映射;
//! - 一次打开的远程句柄存根(RemoteHandle)见 handle.rs。
//!
//! 线程安全:RemoteBackend 只持 Arc<GatewayClient>(内部锁 + 帧编解码),
//! 满足 ShareBackend 的 Send + Sync + 'static 约束。

use std::sync::Arc; // Arc:共享网关连接

use async_trait::async_trait; // async trait 支持(库的 trait 为 async)
use smb_server::{
    BackendCapabilities, Handle, OpenIntent, OpenOptions, ShareBackend, SmbError, SmbPath, SmbResult,
}; // 库的 SMB 抽象类型(trait 与结果类型)

use super::client::GatewayClient; // 帧协议客户端
use super::user_index::UserIDIndex; // 用户名 → users.id 索引(操作身份)
use crate::types::*; // 帧协议契约(操作码/参数/响应)

/// 控制面调用统一入口:编码总操作 JSON → call → 解析总响应。
///
/// 参数:
/// - `conn`:GatewayClient(帧协议连接);
/// - `req`:总操作请求(单次反序列化目标,见 types.rs);
/// - `stream`:请求流数据段(仅 Write 携带)。
///
/// 返回值:
/// - Ok((OperateResponse, Vec<u8>)):响应结构 + 响应流数据段(仅 Read 携带);
/// - Err(u32):哨兵错误码(ERR_*;含网关业务错误与连接/超时)。
///
/// 实现步骤:
/// 1. encode_operate_body(req, stream) 组装 [4B jsonLen]+JSON+流段;
/// 2. conn.call(MSG_OPERATE, body) 等待应答;
/// 3. split_operate_body 拆响应 JSON 与流段,反序列化 OperateResponse;
/// 4. resp.err 非 None → Err(哨兵 code)。
/// `pub(super)`:handle.rs 的 RemoteHandle 复用。
pub(super) async fn call_operate(
    conn: &Arc<GatewayClient>, // 网关连接
    req: &OperateRequest, // 总操作请求
    stream: &[u8], // 请求流数据段(仅 Write)
) -> Result<(OperateResponse, Vec<u8>), u32> {
    let body = encode_operate_body(req, stream).map_err(|_| ERR_IO)?; // 编码 body([len]+JSON+流段),失败记 ERR_IO
    let resp_body = conn.call(MSG_OPERATE, body).await?; // 发 RPC 等应答(连接/超时错误原样上抛)
    let (json_bytes, stream_out) = split_operate_body(&resp_body).map_err(|_| ERR_IO)?; // 拆 JSON 与流段
    let resp: OperateResponse = serde_json::from_slice(json_bytes).map_err(|_| ERR_IO)?; // 反序列化总响应
    if let Some(err) = &resp.err {
        // 业务错误?
        return Err(err.code); // 上抛哨兵错误码
    }
    Ok((resp, stream_out.to_vec())) // 成功:返回 (响应, 流数据段)
}

// ============================================================================
// RemoteBackend:实现 ShareBackend(转发到 Go)
// ============================================================================

/// 远程后端:把 SMB 协议操作转发到 Go 网关(每共享一实例)。
///
/// 承载功能:
/// - `conn`:到 Go 网关的连接(共享同一长连接,帧协议复用);
/// - `share`:本共享的静态定义(共享名/桶 ID/权限模式),TREE_CONNECT 时使用;
/// - `users`:用户名 → users.id 索引(会话用户名 → 操作帧 userId,Go 侧审查依据)。
#[derive(Clone)] // 可克隆(每共享一实例,克隆共享同一连接)
pub struct RemoteBackend {
    conn: Arc<GatewayClient>, // 到 Go 网关的共享连接
    share: ShareInfo, // 本共享定义(共享名 = 桶名,桶根 = 虚拟根)
    users: UserIDIndex, // 用户名 → users.id(操作身份映射)
}

impl RemoteBackend {
    /// 构造远程后端(每共享一实例,共享同一网关连接与用户索引)。
    pub fn new(conn: Arc<GatewayClient>, share: ShareInfo, users: UserIDIndex) -> Self {
        Self {
            conn,
            share,
            users,
        }
    }

    /// 会话用户名 → 操作帧 userId(未命中 → 0,Go 侧按"无身份"拒绝)。
    async fn user_id_of(&self, user: &str) -> u64 {
        self.users.get(user).await
    }
}

/// 实现库定义的 `ShareBackend` trait(存储抽象层;trait 定义见
/// rust-smb-server/vendor/ixr-smb-server/src/backend.rs)。
#[async_trait]
impl ShareBackend for RemoteBackend {
    /// 打开(或创建)文件/目录,返回远程句柄。
    ///
    /// 参数:
    /// - `path`:已验证的 SMB 相对路径(SmbPath 已过滤 ".."/绝对路径/非法字符);
    /// - `opts`:协议层翻译好的 SMB CREATE 意图(OpenOptions)。
    ///
    /// 返回值:Ok(Box<dyn Handle>);Err(SmbError —— 哨兵错误按 ERR_* 映射)。
    ///
    /// 实现步骤:
    /// 1. 把路径与打开选项翻译成打开请求(读写意图、目录标志、关闭即删);
    /// 2. 发给网关,取回打开结果(句柄 ID、是否目录、文件大小);
    /// 3. 用结果包成本地远程句柄存根返回;
    /// 4. 网关返回错误码时,映射为对应的 SMB 错误。
    async fn open(&self, path: &SmbPath, opts: OpenOptions, user: &str) -> SmbResult<Box<dyn Handle>> {
        let _t = std::time::Instant::now(); // 计时起点(耗时统计)
        let user_id = self.user_id_of(user).await; // 会话用户 → users.id(操作帧身份)
        debugf!(
            "后端:打开请求 路径={} 意图={} 读={} 写={} 目录={} 共享={} 用户={}",
            &["后端"], // 前缀标签
            path.display_backslash(), // 目标路径
            intent_str(opts.intent), // 创建意图
            opts.read, // 读权限
            opts.write, // 写权限
            opts.directory, // 目录标志
            self.share.share_name, // 所属共享
            user // 会话用户名
        );
        let req = OperateRequest {
            // 组装打开请求
            code: CODE_FILE_OPEN, // 操作码:打开/创建
            user_id, // 操作者身份(Go 侧审查/落库)
            open: Some(OpenArgs {
                // 打开参数
                bucket_id: self.share.bucket_id, // 所属桶(共享上下文)
                path: path.display_backslash(), // 路径转反斜杠形式
                read: opts.read, // 请求读
                write: opts.write, // 请求写
                intent: intent_str(opts.intent).to_string(), // 创建意图 → 帧协议字符串
                directory: opts.directory, // 按目录打开
                non_directory: opts.non_directory, // 按文件打开(目标为目录则错)
                delete_on_close: opts.delete_on_close, // 关闭即删
            }),
            ..Default::default() // 其余字段 None
        };
        let (resp, _) = call_operate(&self.conn, &req, &[]) // 发网关(无流段)
            .await
            .map_err(map_gateway_err)?; // 哨兵错误码 → SMB 错误
        let r = resp.open.ok_or(SmbError::NotSupported)?; // 取打开结果(缺失 = 网关不支持)
        debugf!(
            "后端:打开成功 路径={} → 句柄={} 目录={} 大小={} 已存在={}(耗时 {}µs)",
            &["后端"], // 前缀标签
            path.display_backslash(), // 路径
            r.handle_id, // 分配的远程句柄
            r.is_dir, // 是否目录
            r.end_of_file, // 文件大小
            r.exists, // 是否已存在
            _t.elapsed().as_micros() // 耗时
        );
        Ok(Box::new(super::handle::RemoteHandle::new(
            Arc::clone(&self.conn),
            r.handle_id,
            r.is_dir,
            opts.delete_on_close,
            user_id, // 句柄后续操作帧携带的会话身份
        )))
    }

    /// 删除路径(文件/空目录)。
    ///
    /// 参数:`path` 待删除的 SMB 相对路径。
    /// 返回值:Ok(());Err(NotFound/AccessDenied/NotEmpty 等)。
    ///
    /// 实现步骤:
    /// 1. 组装删除请求发给网关;
    /// 2. 网关的错误码映射为 SMB 错误(非空目录对应目录非空错误)。
    async fn unlink(&self, path: &SmbPath, user: &str) -> SmbResult<()> {
        let _t = std::time::Instant::now(); // 计时起点
        let user_id = self.user_id_of(user).await; // 会话用户 → users.id
        let req = OperateRequest {
            // 组装删除请求
            code: CODE_FILE_UNLINK, // 操作码:删除
            user_id, // 操作者身份
            unlink: Some(UnlinkArgs {
                // 删除参数
                bucket_id: self.share.bucket_id, // 所属桶
                path: path.display_backslash(), // 目标路径
            }),
            ..Default::default() // 其余字段 None
        };
        let (_, _) = call_operate(&self.conn, &req, &[]) // 发网关(无流段)
            .await
            .map_err(map_gateway_err)?; // 错误码映射(非空目录 → 目录非空错误)
        debugf!("后端:删除成功 路径={}(耗时 {}µs)", &["后端"], path.display_backslash(), _t.elapsed().as_micros()); // 记录删除成功与耗时
        Ok(())
    }

    /// 重命名(SMB RENAME;统一声明允许覆盖已存在的文件目标)。
    ///
    /// 参数:`from` 源路径;`to` 目标路径。
    /// 返回值:Ok(());Err(NotFound 等)。
    ///
    /// 实现步骤:
    /// 1. 组装重命名请求(源路径、目标路径、覆盖标志)发给网关;
    /// 2. 网关负责:目标已存在且为文件 → 按覆盖标志删除后移动;
    ///    目标为目录 → 一律拒绝;错误码映射为 SMB 错误。
    ///
    /// 覆盖标志说明:库层(SMB2 FILE_RENAME_INFORMATION)暂不下发 ReplaceIfExists,
    /// 而 Windows/WPS 的"保存/移动覆盖"场景依赖 rename 覆盖目标,故本实现
    /// 统一声明允许覆盖(仅限文件目标;目录目标网关仍拒绝,安全兜底)。
    async fn rename(&self, from: &SmbPath, to: &SmbPath, user: &str) -> SmbResult<()> {
        let _t = std::time::Instant::now(); // 计时起点
        let user_id = self.user_id_of(user).await; // 会话用户 → users.id
        let req = OperateRequest {
            // 组装重命名请求
            code: CODE_FILE_RENAME, // 操作码:重命名/移动
            user_id, // 操作者身份
            rename: Some(RenameArgs {
                // 重命名参数
                bucket_id: self.share.bucket_id, // 所属桶
                from_path: from.display_backslash(), // 源路径
                to_path: to.display_backslash(), // 目标路径(须同桶)
                replace_if_exists: true, // 允许覆盖已存在的文件目标
            }),
            ..Default::default() // 其余字段 None
        };
        let (_, _) = call_operate(&self.conn, &req, &[]) // 发网关
            .await
            .map_err(map_gateway_err)?; // 错误由网关返回
        debugf!("后端:重命名成功 {} → {}(耗时 {}µs)", &["后端"], from.display_backslash(), to.display_backslash(), _t.elapsed().as_micros()); // 记录重命名成功与耗时
        Ok(())
    }

    /// 静态能力声明(协议层 TREE_CONNECT 时读取)。
    ///
    /// 参数:无。
    /// 返回值:BackendCapabilities(只读标记 = 共享 mode 为 readonly)。
    ///
    /// 实现:
    /// 1. 只读标志取自共享的权限模式(共享只读则标记只读);
    /// 2. 文件名按大小写不敏感处理(桶内索引用小写规范化名)。
    fn capabilities(&self) -> BackendCapabilities {
        BackendCapabilities {
            is_read_only: self.share.mode == "readonly", // 共享模式为 readonly 才标记只读
            case_sensitive: false, // 大小写不敏感(桶内索引按小写规范化名)
        }
    }
}

// ============================================================================
// 工具:OpenIntent → 字符串(与 Go 侧 Intent 字段语义一致)
// ============================================================================

/// 把库的 OpenIntent 翻译为帧协议的 intent 字符串。
///
/// 参数:`intent` 库的创建处置枚举。
/// 返回值:帧协议字符串(与 Go 侧 OpenArgs.Intent 完全一致)。
/// `pub(super)`:单元测试(本模块 tests.rs)直接调用。
pub(super) fn intent_str(intent: OpenIntent) -> &'static str {
    match intent {
        OpenIntent::Open => "open", // 只打开不创建
        OpenIntent::Create => "create", // 只创建(已存在则失败)
        OpenIntent::OpenOrCreate => "open_or_create", // 打开或创建
        OpenIntent::OverwriteOrCreate => "overwrite_or_create", // 覆盖或创建
        OpenIntent::Truncate => "truncate", // 截断打开
    }
}

/// 把 Go 侧哨兵错误码映射为库的 SmbError(NTSTATUS 语义)。
///
/// 参数:`code` 帧协议错误码(ERR_* 常量)。
/// 返回值:库的 SmbError(协议层翻译为 NTSTATUS)。
/// `pub(super)`:单元测试(本模块 tests.rs)直接调用。
///
/// 内部逻辑:
/// 1. 对照 ERR_* ↔ SmbError 一一映射(NotFound/AccessDenied/Exists/
///    NotEmpty/IsDirectory/NotADirectory/Io);
/// 2. 未识别的 code → SmbError::Io(STATUS_UNEXPECTED_IO_ERROR);
/// 3. ERR_GATEWAY_DOWN / ERR_TIMEOUT → SmbError::Io(连接层上层负责重连)。
pub(super) fn map_gateway_err(code: u32) -> SmbError {
    let io_err = || SmbError::Io(std::io::Error::other(format!("gateway error code {code}"))); // 兜底 IO 错误闭包(带错误码)
    match code {
        ERR_NOT_FOUND => SmbError::NotFound, // 目标不存在
        ERR_ACCESS_DENIED => SmbError::AccessDenied, // 拒绝访问
        ERR_EXISTS => SmbError::Exists, // 目标已存在
        ERR_NOT_EMPTY => SmbError::NotEmpty, // 目录非空
        ERR_IS_DIRECTORY => SmbError::IsDirectory, // 是目录(当文件打开)
        ERR_NOT_A_DIRECTORY => SmbError::NotADirectory, // 不是目录(当目录打开)
        ERR_BAD_AUTH => SmbError::AccessDenied, // 认证失败按拒绝访问
        ERR_GATEWAY_DOWN => io_err(), // 网关不可达 → IO 错误(上层负责重连)
        ERR_TIMEOUT => io_err(), // 请求超时 → IO 错误
        ERR_BAD_REQUEST => SmbError::NotSupported, // 请求不合法 → 不支持
        ERR_IO => io_err(), // 网关 IO 错误
        ERR_NOT_IMPL => SmbError::NotSupported, // 未实现 → 不支持
        _ => io_err(), // 未知错误码一律 IO 错误(STATUS_UNEXPECTED_IO_ERROR)
    }
}
