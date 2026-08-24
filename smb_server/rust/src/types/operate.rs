//! ============================================================================
//! types/operate.rs —— 总操作 JSON:操作码、请求/响应结构与参数/结果
//! ============================================================================
//!
//! 与 Go 侧 `types.go` 完全对应:
//! - OperateCode 操作码常量表(与 Go 侧 Code* 完全一致);
//! - OperateRequest / OperateResponse:所有操作共用的总 JSON(单次反序列化);
//! - 各操作参数(Args)与结果(Result)结构(字段语义与 Go 侧一致)。
//!
//! 约定:Code 不能从 0 开始(0 = 根本没填写 JSON,校验层直接拒绝)。

// 结果结构引用 identity.rs 的共享类型(经父模块 re-export 亦可,此处显式导入)。
use super::identity::{FileInfo, ShareInfo, UserCred}; // 文件元信息/共享定义/用户凭据

/// serde skip 辅助:u64 零值(未携带 user_id)不序列化。
fn is_zero_u64(v: &u64) -> bool {
    *v == 0
}

/// 操作码:一个码对应一种操作。
/// 约定:不能从 0 开始(0 = 根本没填写 JSON,校验层直接拒绝)。
pub type OperateCode = i32; // 操作码类型(与 Go 侧一致,i32)

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
#[derive(Debug, serde::Serialize, serde::Deserialize, Default)] // 调试/JSON 双向/默认值
#[serde(rename_all = "camelCase")] // 驼峰字段名(与 Go 侧 json tag 一致)
pub struct OperateRequest {
    pub code: OperateCode, // 操作码(0 = 未填写,直接拒绝)
    /// 操作者身份标识(users.id;SMB 会话认证成功后绑定,文件类操作随请求
    /// 携带,Go 侧经 SessionRegistry(map[UserID]Info,60s 刷新)解析审查。
    /// 认证类操作(QueryUser/QueryAcl/Snapshot)按自身 username 参数,可不携带。
    #[serde(default, skip_serializing_if = "is_zero_u64")] // 0 = 未携带(兼容旧帧),不序列化
    pub user_id: u64, // 用户主键(users.id)
    #[serde(skip_serializing_if = "Option::is_none")] // None 不序列化(节省流量)
    pub auth_user: Option<AuthUserArgs>, // code=CODE_AUTH_QUERY_USER 时有效
    #[serde(skip_serializing_if = "Option::is_none")] // None 不序列化
    pub auth_acl: Option<AuthAclArgs>, // code=CODE_AUTH_QUERY_ACL 时有效
    #[serde(skip_serializing_if = "Option::is_none")] // None 不序列化
    pub snapshot: Option<SnapshotArgs>, // code=CODE_AUTH_SNAPSHOT 时有效
    #[serde(skip_serializing_if = "Option::is_none")] // None 不序列化
    pub open: Option<OpenArgs>, // code=CODE_FILE_OPEN 时有效
    #[serde(skip_serializing_if = "Option::is_none")] // None 不序列化
    pub read: Option<ReadArgs>, // code=CODE_FILE_READ 时有效
    #[serde(skip_serializing_if = "Option::is_none")] // None 不序列化
    pub write: Option<WriteArgs>, // code=CODE_FILE_WRITE 时有效
    #[serde(skip_serializing_if = "Option::is_none")] // None 不序列化
    pub flush: Option<FlushArgs>, // code=CODE_FILE_FLUSH 时有效
    #[serde(skip_serializing_if = "Option::is_none")] // None 不序列化
    pub stat: Option<StatArgs>, // code=CODE_FILE_STAT 时有效
    #[serde(skip_serializing_if = "Option::is_none")] // None 不序列化
    pub set_times: Option<SetTimesArgs>, // code=CODE_FILE_SET_TIMES 时有效
    #[serde(skip_serializing_if = "Option::is_none")] // None 不序列化
    pub truncate: Option<TruncateArgs>, // code=CODE_FILE_TRUNCATE 时有效
    #[serde(skip_serializing_if = "Option::is_none")] // None 不序列化
    pub list_dir: Option<ListDirArgs>, // code=CODE_FILE_LIST_DIR 时有效
    #[serde(skip_serializing_if = "Option::is_none")] // None 不序列化
    pub close: Option<CloseArgs>, // code=CODE_FILE_CLOSE 时有效
    #[serde(skip_serializing_if = "Option::is_none")] // None 不序列化
    pub unlink: Option<UnlinkArgs>, // code=CODE_FILE_UNLINK 时有效
    #[serde(skip_serializing_if = "Option::is_none")] // None 不序列化
    pub rename: Option<RenameArgs>, // code=CODE_FILE_RENAME 时有效
}

/// 总响应:与请求同构,操作结果用 Option 字段承载;失败时 err 非 None。
#[derive(Debug, serde::Serialize, serde::Deserialize, Default)] // 派生
#[serde(rename_all = "camelCase")] // 驼峰字段名
pub struct OperateResponse {
    pub code: OperateCode, // 回显请求操作码(校验失败时为 0)
    #[serde(skip_serializing_if = "Option::is_none")] // None 不序列化
    pub err: Option<ErrorEnvelope>, // 业务错误(成功为 None)
    #[serde(skip_serializing_if = "Option::is_none")] // None 不序列化
    pub auth_user: Option<AuthUserResult>, // code=CODE_AUTH_QUERY_USER 时有效
    #[serde(skip_serializing_if = "Option::is_none")] // None 不序列化
    pub auth_acl: Option<AuthAclResult>, // code=CODE_AUTH_QUERY_ACL 时有效
    #[serde(skip_serializing_if = "Option::is_none")] // None 不序列化
    pub snapshot: Option<SnapshotResult>, // code=CODE_AUTH_SNAPSHOT 时有效
    #[serde(skip_serializing_if = "Option::is_none")] // None 不序列化
    pub open: Option<OpenResult>, // code=CODE_FILE_OPEN 时有效
    #[serde(skip_serializing_if = "Option::is_none")] // None 不序列化
    pub read: Option<ReadResult>, // code=CODE_FILE_READ 时有效(数据走流段)
    #[serde(skip_serializing_if = "Option::is_none")] // None 不序列化
    pub write: Option<WriteResult>, // code=CODE_FILE_WRITE 时有效
    #[serde(skip_serializing_if = "Option::is_none")] // None 不序列化
    pub stat: Option<StatResult>, // code=CODE_FILE_STAT 时有效
    #[serde(skip_serializing_if = "Option::is_none")] // None 不序列化
    pub list_dir: Option<ListDirResult>, // code=CODE_FILE_LIST_DIR 时有效
    // 无结果操作(Flush/SetTimes/Truncate/Close/Unlink/Rename):仅 code+err。
}

/// 错误响应体(err 字段;code 用 ERR_* 哨兵常量;message 仅日志)。
#[derive(Debug, serde::Serialize, serde::Deserialize)] // 派生
pub struct ErrorEnvelope {
    pub code: u32, // 哨兵错误码
    pub message: String, // 人类可读说明(仅日志)
}

// ============================================================================
// 操作参数(Args)—— 与 Go 侧各 Args 结构字段/顺序一致
// ============================================================================

/// 查询用户凭据参数(CODE_AUTH_QUERY_USER)。
#[derive(Debug, serde::Serialize, serde::Deserialize)] // 派生
pub struct AuthUserArgs {
    pub username: String, // 待查询用户名
}

/// 查询用户可见共享清单参数(CODE_AUTH_QUERY_ACL)。
#[derive(Debug, serde::Serialize, serde::Deserialize)] // 派生
pub struct AuthAclArgs {
    pub username: String, // 用户名
}

/// 全量同步快照参数(CODE_AUTH_SNAPSHOT;空结构,占位)。
#[derive(Debug, serde::Serialize, serde::Deserialize)] // 派生
pub struct SnapshotArgs {} // 空参数:快照无需入参

/// 打开/创建文件或目录参数(CODE_FILE_OPEN)。
/// 字段语义与 ixr-smb-server 的 OpenOptions + OpenIntent 一一对应。
#[derive(Debug, serde::Serialize, serde::Deserialize)] // 派生
#[serde(rename_all = "camelCase")] // 驼峰字段名
pub struct OpenArgs {
    #[serde(default)] // 缺失默认 0
    pub bucket_id: u64, // 所属桶(共享上下文;RemoteBackend 填充)
    pub path: String, // SMB 相对路径("/" 分隔,不含 "..",协议层已验证)
    pub read: bool, // 请求读权限
    pub write: bool, // 请求写权限
    pub intent: String, // "open"|"create"|"open_or_create"|"overwrite_or_create"|"truncate"
    pub directory: bool, // 按目录打开
    pub non_directory: bool, // 按文件打开(目标为目录则报错)
    pub delete_on_close: bool, // FILE_DELETE_ON_CLOSE
}

/// 按偏移读参数(CODE_FILE_READ)。
#[derive(Debug, serde::Serialize, serde::Deserialize)] // 派生
#[serde(rename_all = "camelCase")] // 驼峰字段名
pub struct ReadArgs {
    pub handle_id: u64, // 远程句柄 ID
    pub offset: u64, // 读取起始偏移(字节)
    pub length: u32, // 期望长度(≤ 1 MiB;可少于请求 = EOF)
}

/// 按偏移写参数(CODE_FILE_WRITE;数据在流数据段)。
#[derive(Debug, serde::Serialize, serde::Deserialize)] // 派生
#[serde(rename_all = "camelCase")] // 驼峰字段名
pub struct WriteArgs {
    pub handle_id: u64, // 远程句柄 ID
    pub offset: u64, // 写入偏移(字节)
}

/// 冲刷写回缓存参数(CODE_FILE_FLUSH)。
#[derive(Debug, serde::Serialize, serde::Deserialize)] // 派生
#[serde(rename_all = "camelCase")] // 驼峰字段名
pub struct FlushArgs {
    pub handle_id: u64, // 远程句柄 ID
}

/// 查询元信息参数(CODE_FILE_STAT)。
#[derive(Debug, serde::Serialize, serde::Deserialize)] // 派生
#[serde(rename_all = "camelCase")] // 驼峰字段名
pub struct StatArgs {
    pub handle_id: u64, // 远程句柄 ID
}

/// 设置时间戳参数(CODE_FILE_SET_TIMES;None 字段 = 不改)。
#[derive(Debug, serde::Serialize, serde::Deserialize)] // 派生
#[serde(rename_all = "camelCase")] // 驼峰字段名
pub struct SetTimesArgs {
    pub handle_id: u64, // 远程句柄 ID
    #[serde(skip_serializing_if = "Option::is_none")] // None 不序列化
    pub creation_time: Option<u64>, // FILETIME(None = 不改)
    #[serde(skip_serializing_if = "Option::is_none")] // None 不序列化
    pub last_access_time: Option<u64>, // FILETIME(None = 不改)
    #[serde(skip_serializing_if = "Option::is_none")] // None 不序列化
    pub last_write_time: Option<u64>, // FILETIME(None = 不改)
    #[serde(skip_serializing_if = "Option::is_none")] // None 不序列化
    pub change_time: Option<u64>, // FILETIME(None = 不改)
}

/// 截断/扩展参数(CODE_FILE_TRUNCATE;SET_END_OF_FILE)。
#[derive(Debug, serde::Serialize, serde::Deserialize)] // 派生
#[serde(rename_all = "camelCase")] // 驼峰字段名
pub struct TruncateArgs {
    pub handle_id: u64, // 远程句柄 ID
    pub length: u64, // 目标长度(字节)
}

/// 列目录参数(CODE_FILE_LIST_DIR)。
#[derive(Debug, serde::Serialize, serde::Deserialize)] // 派生
#[serde(rename_all = "camelCase")] // 驼峰字段名
pub struct ListDirArgs {
    pub handle_id: u64, // 目录句柄 ID
    pub pattern: String, // 通配符(后端可不实现,由 SMB 层后过滤)
}

/// 关闭句柄参数(CODE_FILE_CLOSE;触发写回缓存整体上传)。
#[derive(Debug, serde::Serialize, serde::Deserialize)] // 派生
#[serde(rename_all = "camelCase")] // 驼峰字段名
pub struct CloseArgs {
    pub handle_id: u64, // 远程句柄 ID
}

/// 删除路径参数(CODE_FILE_UNLINK;目录须为空)。
#[derive(Debug, serde::Serialize, serde::Deserialize)] // 派生
#[serde(rename_all = "camelCase")] // 驼峰字段名
pub struct UnlinkArgs {
    #[serde(default)] // 缺失默认 0
    pub bucket_id: u64, // 所属桶(共享上下文;RemoteBackend 填充)
    pub path: String, // SMB 相对路径
}

/// 重命名/移动参数(CODE_FILE_RENAME)。
#[derive(Debug, serde::Serialize, serde::Deserialize)] // 派生
#[serde(rename_all = "camelCase")] // 驼峰字段名
pub struct RenameArgs {
    #[serde(default)] // 缺失默认 0
    pub bucket_id: u64, // 所属桶(共享上下文;RemoteBackend 填充)
    pub from_path: String, // 源路径
    pub to_path: String, // 目标路径(须同桶)
    #[serde(default)] // 缺失默认 false
    pub replace_if_exists: bool, // 目标已存在时允许覆盖(Windows/WPS 保存与移动覆盖;目录目标仍拒绝)
}

// ============================================================================
// 操作结果(Result)—— 与 Go 侧各 Result 结构字段/顺序一致
// ============================================================================

/// 查询用户凭据结果。
#[derive(Debug, serde::Serialize, serde::Deserialize)] // 派生
pub struct AuthUserResult {
    pub found: bool, // 用户是否存在(否则按 LOGON_FAILURE)
    #[serde(skip_serializing_if = "Option::is_none")] // None 不序列化
    pub cred: Option<UserCred>, // found=true 时有效
}

/// 用户可见共享清单结果(经 Go 侧可见性 ACL 过滤)。
#[derive(Debug, serde::Serialize, serde::Deserialize)] // 派生
pub struct AuthAclResult {
    pub shares: Vec<ShareInfo>, // 该用户可见共享列表
}

/// 全量同步快照结果:一次性下发全部用户与共享定义。
#[derive(Debug, serde::Serialize, serde::Deserialize)] // 派生
pub struct SnapshotResult {
    pub users: Vec<UserCred>, // 全部有效用户(含 NT hash)
    pub shares: Vec<ShareInfo>, // 全部共享(含 ACL)
}

/// 打开结果。
#[derive(Debug, serde::Serialize, serde::Deserialize)] // 派生
#[serde(rename_all = "camelCase")] // 驼峰字段名
pub struct OpenResult {
    pub handle_id: u64, // 远程句柄 ID(后续 RPC 用)
    pub is_dir: bool, // 实际打开的是目录
    pub end_of_file: u64, // 打开时文件大小(新建 = 0)
    pub exists: bool, // 打开时目标已存在
}

/// 读结果(数据走流数据段,本结构仅承载元信息)。
#[derive(Debug, serde::Serialize, serde::Deserialize)] // 派生
pub struct ReadResult {
    pub length: u32, // 流数据段实际字节数(0 = EOF)
}

/// 写结果。
#[derive(Debug, serde::Serialize, serde::Deserialize)] // 派生
pub struct WriteResult {
    pub written: u32, // 实际写入字节数
}

/// 元信息结果。
#[derive(Debug, serde::Serialize, serde::Deserialize)] // 派生
pub struct StatResult {
    pub info: FileInfo, // 元信息
}

/// 目录条目列表结果。
#[derive(Debug, serde::Serialize, serde::Deserialize)] // 派生
pub struct ListDirResult {
    pub entries: Vec<FileInfo>, // 目录条目
}
