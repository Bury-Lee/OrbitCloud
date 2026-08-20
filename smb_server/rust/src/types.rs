//! ============================================================================
//! types.rs —— Rust 侧 SMB 网关共享类型定义(伪代码级设计)
//! ============================================================================
//!
//! 本文件与 Go 侧 `smb_server/go/types.go` 一一对应:
//! **帧协议、消息类型、结构体字段语义/名称/顺序完全一致**(验收标准 3)。
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
//! body:结构化消息为 JSON(serde,字段名与 Go 侧 json tag 一致);
//! FILE_READ_RESP = [4B 实际长度 u32] + 原始数据;
//! FILE_WRITE_REQ = [8B 偏移 u64] + 原始数据。
//!
//! 错误:统一回 ErrorEnvelope(JSON),code 用 ErrCode* 哨兵常量,
//! 映射到 ixr-smb-server 的 SmbError(NTSTATUS 语义)。

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

// 握手与保活。
pub const MSG_HELLO: u16 = 0x0001; // 握手请求
pub const MSG_HELLO_ACK: u16 = 0x0002; // 握手应答
pub const MSG_HEARTBEAT: u16 = 0x0003; // 心跳(单向)

// 动态认证(级别 A:动态用户/ACL 管理)。
pub const MSG_AUTH_QUERY_USER: u16 = 0x0011; // 查询用户凭据(NT hash)
pub const MSG_AUTH_QUERY_USER_RESP: u16 = 0x0012; // 用户凭据结果
pub const MSG_AUTH_QUERY_ACL: u16 = 0x0013; // 查询用户可见共享
pub const MSG_AUTH_QUERY_ACL_RESP: u16 = 0x0014; // 共享清单结果
pub const MSG_AUTH_PUSH: u16 = 0x0015; // 变更推送(Go→Rust 单向)
pub const MSG_AUTH_SYNC_SNAPSHOT: u16 = 0x0016; // 全量快照请求
pub const MSG_AUTH_SYNC_SNAPSHOT_RESP: u16 = 0x0017; // 全量快照响应

// 文件操作 RPC。
pub const MSG_FILE_OPEN: u16 = 0x0101; // 打开/创建
pub const MSG_FILE_OPEN_RESP: u16 = 0x0102;
pub const MSG_FILE_READ: u16 = 0x0103; // 按偏移读
pub const MSG_FILE_READ_RESP: u16 = 0x0104;
pub const MSG_FILE_WRITE: u16 = 0x0105; // 按偏移写(写回缓存)
pub const MSG_FILE_WRITE_RESP: u16 = 0x0106;
pub const MSG_FILE_FLUSH: u16 = 0x0107; // 冲刷写回缓存
pub const MSG_FILE_FLUSH_RESP: u16 = 0x0108;
pub const MSG_FILE_STAT: u16 = 0x0109; // 查询元信息
pub const MSG_FILE_STAT_RESP: u16 = 0x010A;
pub const MSG_FILE_SET_TIMES: u16 = 0x010B; // 设置时间戳
pub const MSG_FILE_SET_TIMES_RESP: u16 = 0x010C;
pub const MSG_FILE_TRUNCATE: u16 = 0x010D; // 截断/扩展
pub const MSG_FILE_TRUNCATE_RESP: u16 = 0x010E;
pub const MSG_FILE_LIST_DIR: u16 = 0x010F; // 列目录
pub const MSG_FILE_LIST_DIR_RESP: u16 = 0x0110;
pub const MSG_FILE_CLOSE: u16 = 0x0111; // 关闭句柄(触发写回上传)
pub const MSG_FILE_CLOSE_RESP: u16 = 0x0112;
pub const MSG_FILE_UNLINK: u16 = 0x0113; // 删除路径
pub const MSG_FILE_UNLINK_RESP: u16 = 0x0114;
pub const MSG_FILE_RENAME: u16 = 0x0115; // 重命名/移动
pub const MSG_FILE_RENAME_RESP: u16 = 0x0116;

pub const MSG_ERR_RESP: u16 = 0x8001; // 错误响应帧

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
// 动态认证
// ============================================================================

/// 用户凭据快照(驱动内存用户表;对应 model.User)。
/// 关键约束(可行性报告 §4.1):NT hash 由 Go 侧在密码设置时计算下发,
/// 本侧仅做 NTLMv2 校验用,不落盘、不下发到 SMB 客户端。
#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
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
#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
pub struct ShareInfo {
    pub share_name: String,          // SMB 共享名(客户端 \\host\<share_name> 访问)
    pub bucket_id: u64,              // 桶主键 ID(对象桶名 = BucketEncoder(id))
    pub bucket_name: String,         // 桶显示名
    pub mode: String,                // 共享级默认权限:"readwrite" | "readonly"
    pub users: Vec<ShareUserAccess>, // ACL 用户清单(空 = 仅 mode 生效)
}

/// 变更推送条目(Go→Rust 增量;op=upsert/delete,kind 指明载荷)。
#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
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

/// 用户凭据查询请求(级别 B 预留:每次认证实时回调 Go)。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct AuthQueryUserRequest {
    pub username: String, // 待查询用户名
}

/// 用户凭据查询结果。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct AuthQueryUserResponse {
    pub found: bool, // 用户是否存在(否则按 LOGON_FAILURE)
    #[serde(skip_serializing_if = "Option::is_none")]
    pub cred: Option<UserCred>, // found=true 时有效
}

/// 用户可见共享清单请求。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct AuthQueryAclRequest {
    pub username: String, // 用户名
}

/// 用户可见共享清单结果(经 Go 侧可见性 ACL 过滤)。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct AuthQueryAclResponse {
    pub shares: Vec<ShareInfo>, // 该用户可见共享列表
}

/// 全量同步请求(启动/断线重连后拉取;空结构)。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct SyncSnapshotRequest {}

/// 全量同步响应。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct SyncSnapshotResponse {
    pub users: Vec<UserCred>,   // 全部有效用户(含 NT hash)
    pub shares: Vec<ShareInfo>, // 全部共享(含 ACL)
}

// ============================================================================
// 文件操作 RPC
// ============================================================================

/// 打开/创建文件或目录请求。
/// 字段语义与 ixr-smb-server 的 OpenOptions + OpenIntent 一一对应。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct OpenRequest {
    pub path: String,          // SMB 相对路径("/" 分隔,无 "..",协议层已验证)
    pub read: bool,            // 请求读权限
    pub write: bool,           // 请求写权限
    pub intent: String,        // "open"|"create"|"open_or_create"|"overwrite_or_create"|"truncate"
    pub directory: bool,       // 按目录打开
    pub non_directory: bool,   // 按文件打开(目标为目录则报错)
    pub delete_on_close: bool, // FILE_DELETE_ON_CLOSE
}

/// 打开结果。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct OpenResponse {
    pub handle_id: u64,   // 远程句柄 ID(后续 RPC 用)
    pub is_dir: bool,     // 实际打开的是目录
    pub end_of_file: u64, // 打开时文件大小(新建 = 0)
    pub exists: bool,     // 打开时目标已存在
}

/// 按偏移读请求。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct ReadRequest {
    pub handle_id: u64, // 远程句柄 ID
    pub offset: u64,    // 读取起始偏移(字节)
    pub length: u32,    // 期望长度(≤ 1 MiB;可少于请求 = EOF)
}

/// 读结果(body = [4B 实际长度 u32] + 数据)。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct ReadResponse {
    pub data: Vec<u8>, // 实际读到的字节(0 = EOF)
}

/// 按偏移写请求(body = [8B 偏移 u64] + 数据)。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct WriteRequest {
    pub handle_id: u64, // 远程句柄 ID
    pub offset: u64,    // 写入偏移(字节)
    pub data: Vec<u8>,  // 待写入数据
}

/// 写结果。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct WriteResponse {
    pub written: u32, // 实际写入字节数
}

/// 冲刷写回缓存请求(SMB FLUSH)。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct FlushRequest {
    pub handle_id: u64, // 远程句柄 ID
}

/// 元信息查询请求。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct StatRequest {
    pub handle_id: u64, // 远程句柄 ID
}

/// 文件/目录元信息 —— 与 ixr-smb-server FileInfo 字段一一对应
/// (时间均为 FILETIME:1601-01-01 起 100ns 刻度)。
#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
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

/// 元信息结果。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct StatResponse {
    pub info: FileInfo, // 元信息
}

/// 设置时间戳请求(nil 字段 = 不改)。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct SetTimesRequest {
    pub handle_id: u64, // 远程句柄 ID
    #[serde(skip_serializing_if = "Option::is_none")]
    pub creation_time: Option<u64>, // FILETIME(nil = 不改)
    #[serde(skip_serializing_if = "Option::is_none")]
    pub last_access_time: Option<u64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub last_write_time: Option<u64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub change_time: Option<u64>,
}

/// 截断/扩展请求(SET_END_OF_FILE)。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct TruncateRequest {
    pub handle_id: u64, // 远程句柄 ID
    pub length: u64,    // 目标长度(字节)
}

/// 列目录请求。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct ListDirRequest {
    pub handle_id: u64,  // 目录句柄 ID
    pub pattern: String, // 通配符(后端可不实现,由 SMB 层后过滤)
}

/// 目录条目列表。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct ListDirResponse {
    pub entries: Vec<FileInfo>, // 目录条目
}

/// 关闭句柄请求(触发写回缓存整体上传)。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct CloseRequest {
    pub handle_id: u64, // 远程句柄 ID
}

/// 删除路径请求(目录须为空)。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct UnlinkRequest {
    pub path: String, // SMB 相对路径
}

/// 重命名/移动请求(目标已存在必须拒绝)。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct RenameRequest {
    pub from_path: String, // 源路径
    pub to_path: String,   // 目标路径(须同桶)
}

/// 错误响应体(code 用 ERR_* 哨兵常量;message 仅日志)。
#[derive(Debug, serde::Serialize, serde::Deserialize)]
pub struct ErrorEnvelope {
    pub code: u32,       // 哨兵错误码
    pub message: String, // 人类可读说明(仅日志)
}

#[cfg(test)]
mod tests {
    use super::*;

    /// 帧协议常量须与 Go 侧 types.go 完全一致(验收标准 3)。
    #[test]
    fn frame_constants_match_go_side() {
        assert_eq!(MAGIC, 0x4F435354, "magic \"OCST\" 与 Go 侧 Magic 一致");
        assert_eq!(VERSION, 1, "协议版本与 Go 侧 Version 一致");
        assert_eq!(MAX_BODY_LEN, 16 << 20, "body 上限与 Go 侧 MaxBodyLen 一致");
        assert_eq!(MSG_HELLO, 0x0001);
        assert_eq!(MSG_AUTH_QUERY_USER, 0x0011);
        assert_eq!(MSG_FILE_OPEN, 0x0101);
        assert_eq!(MSG_FILE_READ, 0x0103);
        assert_eq!(MSG_FILE_WRITE, 0x0105);
        assert_eq!(MSG_FILE_CLOSE, 0x0111);
        assert_eq!(MSG_ERR_RESP, 0x8001);
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
        // 布局推导:4 + 1 + 1 + 2 + 4 + 4 = 16 字节
        let hdr = FrameHeader {
            magic: MAGIC,
            version: VERSION,
            flags: FLAG_NEED_REPLY,
            msg_type: MSG_HELLO,
            seq: 1,
            body_len: 0,
        };
        assert_eq!(hdr.magic, MAGIC);
        assert_eq!(hdr.version, 1);
        assert_eq!(hdr.flags, FLAG_NEED_REPLY);
        assert_eq!(hdr.msg_type, MSG_HELLO);
        assert_eq!(hdr.seq, 1);
        assert_eq!(hdr.body_len, 0);
    }
}
