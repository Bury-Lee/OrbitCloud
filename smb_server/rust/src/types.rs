//! ============================================================================
//! types.rs —— Rust 侧 SMB 网关共享类型定义(伪代码级设计)
//! ============================================================================
//!
//! 本文件与 Go 侧 `smb_server/go/types.go` 一一对应:
//! **帧协议、操作码、结构体字段语义/名称/顺序完全一致**(验收标准 3)。
//!
//! 控制面统一"总操作 JSON + 操作码路由"(单次反序列化):
//! - `OperateRequest`:一次反序列化,Code 路由到具体操作(仿 HTTP 路由组);
//! - `OperateResponse`:同样式总响应(Code 回显 + 结果指针/Err);
//! - 文件数据走流数据段(设计点 8:控制面 JSON、数据面流);
//! - 字段经 `#[serde(rename_all = "camelCase")]` 与 Go 侧 json tag 完全一致。
//!
//! 依赖说明(真实现时 Cargo.toml 需声明,沿用 rust-smb-server 模板):
//! ```toml
//! [dependencies]
//! ixr-smb-server = { version = "0.5.1", default-features = false }  # SMB 协议层
//! tokio = { version = "1", features = ["full"] }
//! serde = { version = "1", features = ["derive"] }   # 帧 body JSON 编解码
//! bytes = "1"
//! async-trait = "0.1"
//! tracing = "0.1.44"
//! ```
//!
//! ============================================================================
//! 帧协议(与 Go 侧完全一致,大端序,帧头 16 字节)
//! ============================================================================
//!
//! ```text
//! offset  size  字段
//! ------  ----  --------------------------------------------------
//! 0       4     magic   = 0x4F435354("OCST")
//! 4       1     version = 1
//! 5       1     flags   : 0x01=响应帧 | 0x02=需响应 | 0x04=心跳
//! 6       2     msg_type(见 MsgType)
//! 8       4     seq     请求序列号(响应原样带回;0=单向通知)
//! 12      4     body_len(≤ 16 MiB)
//! ```
//!
//! body 布局:
//! - MSG_OPERATE / MSG_OPERATE_RESP:
//!   `[4B json_len u32] + 总操作/总响应 JSON + [流数据段(仅 Read/Write)]`;
//! - 其余消息(握手/心跳/推送):JSON 或空。
//!
//! 错误:业务失败统一进 OperateResponse.err(ErrorEnvelope),
//! code 用 ERR_* 哨兵常量,映射到 ixr-smb-server 的 SmbError(NTSTATUS 语义)。

// 协议常量(与 Go 侧常量完全一致)。
pub const MAGIC: u32 = 0x4F435354; // 帧魔数 "OCST"
pub const VERSION: u8 = 1; // 协议版本
pub const MAX_BODY_LEN: u32 = 16 << 20; // 单帧 body 上限 16 MiB

// 帧头 flags 位标记(与 Go 侧一致)。
pub const FLAG_RESPONSE: u8 = 0x01; // 响应帧
pub const FLAG_NEED_REPLY: u8 = 0x02; // 请求需响应
pub const FLAG_HEARTBEAT: u8 = 0x04; // 心跳探测

// ============================================================================
// FrameHeader 帧头(与 Go 侧 FrameHeader 字段/顺序一致)
// ============================================================================

/// SMB 网关私有 TCP 帧头(16 字节,大端序)。
/// 承载:协议识别(magic/version)、帧类型(flags/msg_type)、
/// 请求-响应关联(seq)、载荷长度(body_len)。
#[derive(Debug, Clone, Copy)]
pub struct FrameHeader {
    pub magic: u32,    // 魔数 "OCST";不匹配立即断开
    pub version: u8,   // 协议版本(当前 1)
    pub flags: u8,     // 位标记:FLAG_RESPONSE / FLAG_NEED_REPLY / FLAG_HEARTBEAT
    pub msg_type: u16, // 消息类型(见 MsgType 常量)
    pub seq: u32,      // 请求序列号;响应原样带回;0 = 单向通知
    pub body_len: u32, // body 字节数(≤ MAX_BODY_LEN)
}

// ============================================================================
// MsgType 消息类型(u16;与 Go 侧 MSG_* 常量数值一致)
// ============================================================================

// 握手、心跳与推送。
pub const MSG_HELLO: u16 = 0x0001; // 握手请求
pub const MSG_HELLO_ACK: u16 = 0x0002; // 握手应答
pub const MSG_HEARTBEAT: u16 = 0x0003; // 心跳(单向)
pub const MSG_AUTH_PUSH: u16 = 0x0015; // 变更推送(Go→Rust 单向,body = AclEntry)

// 控制面统一走总操作帧,内部用 OperateCode 路由。
pub const MSG_OPERATE: u16 = 0x0201; // 总操作请求(OperateRequest + 可选流数据段)
pub const MSG_OPERATE_RESP: u16 = 0x0202; // 总响应(OperateResponse + 可选流数据段)

pub const MSG_ERR_RESP: u16 = 0x8001; // 兜底错误帧(协议级错误)

// ============================================================================
// ErrCode 哨兵错误码(u32;与 Go 侧一一对应,映射 ixr SmbError)
// ============================================================================

pub const ERR_OK: u32 = 0; // 成功
pub const ERR_NOT_FOUND: u32 = 1; // STATUS_OBJECT_NAME_NOT_FOUND
pub const ERR_ACCESS_DENIED: u32 = 2; // STATUS_ACCESS_DENIED
pub const ERR_EXISTS: u32 = 3; // STATUS_OBJECT_NAME_COLLISION
pub const ERR_NOT_EMPTY: u32 = 4; // STATUS_DIRECTORY_NOT_EMPTY
pub const ERR_IS_DIRECTORY: u32 = 5; // STATUS_FILE_IS_A_DIRECTORY
pub const ERR_NOT_A_DIRECTORY: u32 = 6; // STATUS_NOT_A_DIRECTORY
pub const ERR_IO: u32 = 7; // STATUS_UNEXPECTED_IO_ERROR
pub const ERR_NOT_IMPL: u32 = 8; // STATUS_NOT_IMPLEMENTED
pub const ERR_BAD_AUTH: u32 = 9; // STATUS_LOGON_FAILURE
pub const ERR_GATEWAY_DOWN: u32 = 10; // 网关不可达
pub const ERR_TIMEOUT: u32 = 11; // 请求超时
pub const ERR_BAD_REQUEST: u32 = 12; // 请求不合法(Code=0 / 指针与 Code 不匹配)

// ============================================================================
// 握手
// ============================================================================

/// 客户端(本进程)发起握手的请求体。
/// 意义:双向 HMAC 挑战应答鉴权(共享密钥两端预置),防重放。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct HelloRequest {
    pub client_id: String,        // 网关实例标识(多实例隔离远程句柄表用)
    pub nonce: String,            // 客户端随机 16 字节(hex)
    pub challenge_digest: String, // HMAC-SHA256(key, "HELLO:"+client_id+":"+nonce) hex
}

/// 握手应答体。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct HelloResponse {
    pub ok: bool,             // 是否握手成功
    pub server_nonce: String, // 服务端随机 16 字节(hex;双向认证)
    pub error: String,        // 失败原因(仅日志)
}

// ============================================================================
// 总操作 JSON:OperateCode + OperateRequest + OperateResponse
// ============================================================================

/// 操作码:一个码对应一种操作。
/// 约定:不能从 0 开始(0 = 根本没填写 JSON,校验层直接拒绝)。
pub type OperateCode = i32;

// 操作码常量表 —— 与 Go 侧 Code* 完全一致。
pub const CODE_INVALID: OperateCode = 0; // 未填写(哨兵,禁止使用)
pub const CODE_AUTH_QUERY_USER: OperateCode = 1; // 查询用户凭据(NT hash)
pub const CODE_AUTH_QUERY_ACL: OperateCode = 2; // 查询用户可见共享清单
pub const CODE_AUTH_SNAPSHOT: OperateCode = 3; // 全量同步快照
pub const CODE_FILE_OPEN: OperateCode = 4; // 打开/创建文件或目录
pub const CODE_FILE_READ: OperateCode = 5; // 按偏移读(响应带流数据段)
pub const CODE_FILE_WRITE: OperateCode = 6; // 按偏移写(请求带流数据段)
pub const CODE_FILE_FLUSH: OperateCode = 7; // 冲刷写回缓存
pub const CODE_FILE_STAT: OperateCode = 8; // 查询元信息
pub const CODE_FILE_SET_TIMES: OperateCode = 9; // 设置时间戳
pub const CODE_FILE_TRUNCATE: OperateCode = 10; // 截断/扩展
pub const CODE_FILE_LIST_DIR: OperateCode = 11; // 列目录
pub const CODE_FILE_CLOSE: OperateCode = 12; // 关闭句柄
pub const CODE_FILE_UNLINK: OperateCode = 13; // 删除路径
pub const CODE_FILE_RENAME: OperateCode = 14; // 重命名/移动

/// 总操作请求:所有操作共用这一个结构,单次反序列化。
/// 每个操作是 Option(可空)字段;未填写的为 None。
/// 校验规则(与 Go 侧 Route 一致):
/// - code 必须在常量表中,为 0 直接拒绝;
/// - code 对应的字段必须为 Some,其余字段必须为 None(防误填)。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct OperateRequest {
    pub code: OperateCode, // 操作码(0 = 未填写,直接拒绝)
    #[serde(skip_serializing_if = "Option::is_none")]
    pub auth_user: Option<AuthUserArgs>, // code=CODE_AUTH_QUERY_USER 时有效
    #[serde(skip_serializing_if = "Option::is_none")]
    pub auth_acl: Option<AuthAclArgs>, // code=CODE_AUTH_QUERY_ACL 时有效
    #[serde(skip_serializing_if = "Option::is_none")]
    pub snapshot: Option<SnapshotArgs>, // code=CODE_AUTH_SNAPSHOT 时有效
    #[serde(skip_serializing_if = "Option::is_none")]
    pub open: Option<OpenArgs>, // code=CODE_FILE_OPEN 时有效
    #[serde(skip_serializing_if = "Option::is_none")]
    pub read: Option<ReadArgs>, // code=CODE_FILE_READ 时有效
    #[serde(skip_serializing_if = "Option::is_none")]
    pub write: Option<WriteArgs>, // code=CODE_FILE_WRITE 时有效
    #[serde(skip_serializing_if = "Option::is_none")]
    pub flush: Option<FlushArgs>, // code=CODE_FILE_FLUSH 时有效
    #[serde(skip_serializing_if = "Option::is_none")]
    pub stat: Option<StatArgs>, // code=CODE_FILE_STAT 时有效
    #[serde(skip_serializing_if = "Option::is_none")]
    pub set_times: Option<SetTimesArgs>, // code=CODE_FILE_SET_TIMES 时有效
    #[serde(skip_serializing_if = "Option::is_none")]
    pub truncate: Option<TruncateArgs>, // code=CODE_FILE_TRUNCATE 时有效
    #[serde(skip_serializing_if = "Option::is_none")]
    pub list_dir: Option<ListDirArgs>, // code=CODE_FILE_LIST_DIR 时有效
    #[serde(skip_serializing_if = "Option::is_none")]
    pub close: Option<CloseArgs>, // code=CODE_FILE_CLOSE 时有效
    #[serde(skip_serializing_if = "Option::is_none")]
    pub unlink: Option<UnlinkArgs>, // code=CODE_FILE_UNLINK 时有效
    #[serde(skip_serializing_if = "Option::is_none")]
    pub rename: Option<RenameArgs>, // code=CODE_FILE_RENAME 时有效
}

/// 总响应:与请求同构,操作结果用 Option 字段承载;失败时 err 非 None。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct OperateResponse {
    pub code: OperateCode, // 回显请求操作码(校验失败时为 0)
    #[serde(skip_serializing_if = "Option::is_none")]
    pub err: Option<ErrorEnvelope>, // 业务错误(成功为 None)
    #[serde(skip_serializing_if = "Option::is_none")]
    pub auth_user: Option<AuthUserResult>, // code=CODE_AUTH_QUERY_USER 时有效
    #[serde(skip_serializing_if = "Option::is_none")]
    pub auth_acl: Option<AuthAclResult>, // code=CODE_AUTH_QUERY_ACL 时有效
    #[serde(skip_serializing_if = "Option::is_none")]
    pub snapshot: Option<SnapshotResult>, // code=CODE_AUTH_SNAPSHOT 时有效
    #[serde(skip_serializing_if = "Option::is_none")]
    pub open: Option<OpenResult>, // code=CODE_FILE_OPEN 时有效
    #[serde(skip_serializing_if = "Option::is_none")]
    pub read: Option<ReadResult>, // code=CODE_FILE_READ 时有效(数据走流段)
    #[serde(skip_serializing_if = "Option::is_none")]
    pub write: Option<WriteResult>, // code=CODE_FILE_WRITE 时有效
    #[serde(skip_serializing_if = "Option::is_none")]
    pub stat: Option<StatResult>, // code=CODE_FILE_STAT 时有效
    #[serde(skip_serializing_if = "Option::is_none")]
    pub list_dir: Option<ListDirResult>, // code=CODE_FILE_LIST_DIR 时有效
                           // 无结果操作(Flush/SetTimes/Truncate/Close/Unlink/Rename):仅 code+err。
}

/// 错误响应体(err 字段;code 用 ERR_* 哨兵常量;message 仅日志)。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct ErrorEnvelope {
    pub code: u32,       // 哨兵错误码
    pub message: String, // 人类可读说明(仅日志)
}

// ============================================================================
// 操作参数(Args)—— 与 Go 侧各 Args 结构字段/顺序一致
// ============================================================================

/// 查询用户凭据参数(CODE_AUTH_QUERY_USER)。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct AuthUserArgs {
    pub username: String, // 待查询用户名
}

/// 查询用户可见共享清单参数(CODE_AUTH_QUERY_ACL)。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct AuthAclArgs {
    pub username: String, // 用户名
}

/// 全量同步快照参数(CODE_AUTH_SNAPSHOT;空结构,占位)。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct SnapshotArgs {}

/// 打开/创建文件或目录参数(CODE_FILE_OPEN)。
/// 字段语义与 ixr-smb-server 的 OpenOptions + OpenIntent 一一对应。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct OpenArgs {
    pub path: String,          // SMB 相对路径("/" 分隔,无 "..",协议层已验证)
    pub read: bool,            // 请求读权限
    pub write: bool,           // 请求写权限
    pub intent: String,        // "open"|"create"|"open_or_create"|"overwrite_or_create"|"truncate"
    pub directory: bool,       // 按目录打开
    pub non_directory: bool,   // 按文件打开(目标为目录则报错)
    pub delete_on_close: bool, // FILE_DELETE_ON_CLOSE
}

/// 按偏移读参数(CODE_FILE_READ)。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct ReadArgs {
    pub handle_id: u64, // 远程句柄 ID
    pub offset: u64,    // 读取起始偏移(字节)
    pub length: u32,    // 期望长度(≤ 1 MiB;可少于请求 = EOF)
}

/// 按偏移写参数(CODE_FILE_WRITE;数据在流数据段)。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct WriteArgs {
    pub handle_id: u64, // 远程句柄 ID
    pub offset: u64,    // 写入偏移(字节)
}

/// 冲刷写回缓存参数(CODE_FILE_FLUSH)。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct FlushArgs {
    pub handle_id: u64, // 远程句柄 ID
}

/// 查询元信息参数(CODE_FILE_STAT)。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct StatArgs {
    pub handle_id: u64, // 远程句柄 ID
}

/// 设置时间戳参数(CODE_FILE_SET_TIMES;None 字段 = 不改)。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SetTimesArgs {
    pub handle_id: u64, // 远程句柄 ID
    #[serde(skip_serializing_if = "Option::is_none")]
    pub creation_time: Option<u64>, // FILETIME(None = 不改)
    #[serde(skip_serializing_if = "Option::is_none")]
    pub last_access_time: Option<u64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub last_write_time: Option<u64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub change_time: Option<u64>,
}

/// 截断/扩展参数(CODE_FILE_TRUNCATE;SET_END_OF_FILE)。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct TruncateArgs {
    pub handle_id: u64, // 远程句柄 ID
    pub length: u64,    // 目标长度(字节)
}

/// 列目录参数(CODE_FILE_LIST_DIR)。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct ListDirArgs {
    pub handle_id: u64,  // 目录句柄 ID
    pub pattern: String, // 通配符(后端可不实现,由 SMB 层后过滤)
}

/// 关闭句柄参数(CODE_FILE_CLOSE;触发写回缓存整体上传)。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct CloseArgs {
    pub handle_id: u64, // 远程句柄 ID
}

/// 删除路径参数(CODE_FILE_UNLINK;目录须为空)。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct UnlinkArgs {
    pub path: String, // SMB 相对路径
}

/// 重命名/移动参数(CODE_FILE_RENAME;目标已存在必须拒绝)。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct RenameArgs {
    pub from_path: String, // 源路径
    pub to_path: String,   // 目标路径(须同桶)
}

// ============================================================================
// 操作结果(Result)—— 与 Go 侧各 Result 结构字段/顺序一致
// ============================================================================

/// 查询用户凭据结果。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct AuthUserResult {
    pub found: bool, // 用户是否存在(否则按 LOGON_FAILURE)
    #[serde(skip_serializing_if = "Option::is_none")]
    pub cred: Option<UserCred>, // found=true 时有效
}

/// 用户可见共享清单结果(经 Go 侧可见性 ACL 过滤)。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct AuthAclResult {
    pub shares: Vec<ShareInfo>, // 该用户可见共享列表
}

/// 全量同步快照结果:一次性下发全部用户与共享定义。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct SnapshotResult {
    pub users: Vec<UserCred>,   // 全部有效用户(含 NT hash)
    pub shares: Vec<ShareInfo>, // 全部共享(含 ACL)
}

/// 打开结果。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct OpenResult {
    pub handle_id: u64,   // 远程句柄 ID(后续 RPC 用)
    pub is_dir: bool,     // 实际打开的是目录
    pub end_of_file: u64, // 打开时文件大小(新建 = 0)
    pub exists: bool,     // 打开时目标已存在
}

/// 读结果(数据走流数据段,本结构仅承载元信息)。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct ReadResult {
    pub length: u32, // 流数据段实际字节数(0 = EOF)
}

/// 写结果。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct WriteResult {
    pub written: u32, // 实际写入字节数
}

/// 元信息结果。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct StatResult {
    pub info: FileInfo, // 元信息
}

/// 目录条目列表结果。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct ListDirResult {
    pub entries: Vec<FileInfo>, // 目录条目
}

// ============================================================================
// 动态认证与文件元信息共享类型(与 Go 侧镜像)
// ============================================================================

/// 用户凭据快照(驱动内存用户表;对应 model.User)。
/// 关键约束(可行性报告 §4.1):NT hash 由 Go 侧在密码设置时计算下发,
/// 本侧仅做 NTLMv2 校验用,不落盘、不下发到 SMB 客户端。
#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct UserCred {
    pub username: String,     // 用户名(全局唯一)
    pub nt_hash_hex: String,  // NT hash 十六进制(32 字符)
    pub permission_level: i8, // 权限级别(0=超管,数值越小权限越高)
    pub status: i32,          // 1 正常 / 0 禁用
}

/// 某用户对某共享的访问权限条目(ACL 热更新载荷)。
#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
pub struct ShareUserAccess {
    pub username: String, // 用户名
    pub access: String,   // "readwrite" | "readonly"
}

/// 一个 SMB 共享的完整定义(对应一个桶;共享名 = 桶名,共享根 = 桶根)。
/// 桶级元数据(配额/已用/状态)随推送下发,Rust 侧维护桶实例表(registry.rs)。
#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ShareInfo {
    pub share_name: String,          // SMB 共享名(客户端 \\host\<share_name> 访问)
    pub bucket_id: u64,              // 桶主键 ID(对象桶名 = BucketEncoder(id))
    pub bucket_name: String,         // 桶显示名
    pub mode: String,                // 共享级默认权限:"readwrite" | "readonly"
    pub users: Vec<ShareUserAccess>, // ACL 用户清单(空 = 仅 mode 生效)
    pub quota: u64,                  // 桶容量配额(字节;0 = 不限)
    pub used_space: u64,             // 桶已用空间(字节;剩余容量上报)
    pub status: i32,                 // 桶状态(1 正常 / 0 禁用 = 实例下线)
}

/// 变更推送条目(Go→Rust 增量;op=upsert/delete,kind 指明载荷)。
#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct AclEntry {
    pub op: String,   // "upsert" | "delete"
    pub kind: String, // "user" | "share" | "acl"
    #[serde(skip_serializing_if = "Option::is_none")]
    pub user: Option<UserCred>, // kind=user 时有效
    #[serde(skip_serializing_if = "Option::is_none")]
    pub share: Option<ShareInfo>, // kind=share 时有效
    #[serde(skip_serializing_if = "Option::is_none")]
    pub acl: Option<ShareUserAccess>, // kind=acl 时有效
    pub share_name: String, // kind=acl 时指明共享
}

/// 文件/目录元信息 —— 与 ixr-smb-server FileInfo 字段一一对应
/// (时间均为 FILETIME:1601-01-01 起 100ns 刻度)。
#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct FileInfo {
    pub name: String,          // 显示名(末级组件;共享根 = 共享名)
    pub end_of_file: u64,      // 文件大小(字节)
    pub allocation_size: u64,  // 分配大小(简化 = end_of_file)
    pub creation_time: u64,    // FILETIME 创建时间
    pub last_access_time: u64, // FILETIME 最后访问
    pub last_write_time: u64,  // FILETIME 最后写入
    pub change_time: u64,      // FILETIME 变更(退化 = 最后写入)
    pub is_directory: bool,    // 是否目录
    pub file_index: u64,       // 唯一文件索引(无则 0)
}

#[cfg(test)]
mod tests {
    use super::*;

    /// 帧协议与操作码常量须与 Go 侧 types.go 完全一致(验收标准 3)。
    #[test]
    fn frame_constants_match_go_side() {
        assert_eq!(MAGIC, 0x4F435354, "magic \"OCST\" 与 Go 侧 Magic 一致");
        assert_eq!(VERSION, 1, "协议版本与 Go 侧 Version 一致");
        assert_eq!(MAX_BODY_LEN, 16 << 20, "body 上限与 Go 侧 MaxBodyLen 一致");
        assert_eq!(MSG_HELLO, 0x0001);
        assert_eq!(MSG_AUTH_PUSH, 0x0015);
        assert_eq!(MSG_OPERATE, 0x0201);
        assert_eq!(MSG_OPERATE_RESP, 0x0202);
        assert_eq!(MSG_ERR_RESP, 0x8001);
    }

    /// 操作码常量表须与 Go 侧 Code* 一一对应且不重复、不从 0 开始。
    #[test]
    fn operate_codes_match_go_side() {
        assert_eq!(CODE_INVALID, 0, "0 = 未填写哨兵");
        assert_eq!(CODE_AUTH_QUERY_USER, 1);
        assert_eq!(CODE_AUTH_QUERY_ACL, 2);
        assert_eq!(CODE_AUTH_SNAPSHOT, 3);
        assert_eq!(CODE_FILE_OPEN, 4);
        assert_eq!(CODE_FILE_READ, 5);
        assert_eq!(CODE_FILE_WRITE, 6);
        assert_eq!(CODE_FILE_FLUSH, 7);
        assert_eq!(CODE_FILE_STAT, 8);
        assert_eq!(CODE_FILE_SET_TIMES, 9);
        assert_eq!(CODE_FILE_TRUNCATE, 10);
        assert_eq!(CODE_FILE_LIST_DIR, 11);
        assert_eq!(CODE_FILE_CLOSE, 12);
        assert_eq!(CODE_FILE_UNLINK, 13);
        assert_eq!(CODE_FILE_RENAME, 14);
        let codes = [
            CODE_AUTH_QUERY_USER,
            CODE_AUTH_QUERY_ACL,
            CODE_AUTH_SNAPSHOT,
            CODE_FILE_OPEN,
            CODE_FILE_READ,
            CODE_FILE_WRITE,
            CODE_FILE_FLUSH,
            CODE_FILE_STAT,
            CODE_FILE_SET_TIMES,
            CODE_FILE_TRUNCATE,
            CODE_FILE_LIST_DIR,
            CODE_FILE_CLOSE,
            CODE_FILE_UNLINK,
            CODE_FILE_RENAME,
        ];
        let mut seen = std::collections::BTreeSet::new();
        for c in codes {
            assert!(seen.insert(c), "操作码重复: {c}");
        }
    }

    /// 哨兵错误码须与 Go 侧 Err* 常量一一对应且不重复。
    #[test]
    fn error_codes_are_distinct() {
        let codes = [
            ERR_OK,
            ERR_NOT_FOUND,
            ERR_ACCESS_DENIED,
            ERR_EXISTS,
            ERR_NOT_EMPTY,
            ERR_IS_DIRECTORY,
            ERR_NOT_A_DIRECTORY,
            ERR_IO,
            ERR_NOT_IMPL,
            ERR_BAD_AUTH,
            ERR_GATEWAY_DOWN,
            ERR_TIMEOUT,
            ERR_BAD_REQUEST,
        ];
        let mut seen = std::collections::BTreeSet::new();
        for c in codes {
            assert!(seen.insert(c), "错误码重复: {c}");
        }
        assert_eq!(seen.len(), codes.len(), "错误码集合大小与预期一致");
    }

    /// 帧头结构字段顺序与 Go 侧 FrameHeader 一致(大端 16 字节)。
    #[test]
    fn frame_header_layout_is_stable() {
        let hdr = FrameHeader {
            magic: MAGIC,
            version: VERSION,
            flags: FLAG_NEED_REPLY,
            msg_type: MSG_OPERATE,
            seq: 1,
            body_len: 0,
        };
        assert_eq!(hdr.magic, MAGIC);
        assert_eq!(hdr.version, 1);
        assert_eq!(hdr.flags, FLAG_NEED_REPLY);
        assert_eq!(hdr.msg_type, MSG_OPERATE);
        assert_eq!(hdr.seq, 1);
        assert_eq!(hdr.body_len, 0);
    }
}
