//! ============================================================================
//! types/identity.rs —— 动态认证与文件元信息共享类型(与 Go 侧镜像)
//! ============================================================================
//!
//! 对应 Go 侧 `protocol.go` 的"动态认证共享类型"一节:
//! - UserCred:用户凭据快照(驱动 Rust 侧内存用户表);
//! - ShareUserAccess / ShareInfo:共享定义与 ACL(驱动桶实例注册表);
//! - AclEntry:增量变更推送条目(Go→Rust,MSG_AUTH_PUSH);
//! - FileInfo:文件/目录元信息(与 ixr-smb-server FileInfo 字段一一对应)。

/// 用户凭据快照(驱动内存用户表;对应 model.User)。
/// 关键约束(可行性报告 §4.1):NT hash 由 Go 侧在密码设置时计算下发,
/// 本侧仅做 NTLMv2 校验用,不落盘、不下发到 SMB 客户端。
#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)] // 调试/克隆/JSON 双向
#[serde(rename_all = "camelCase")] // 驼峰字段名(与 Go 侧 json tag 一致)
pub struct UserCred {
    pub id: u64, // 用户主键 users.id(会话身份标识,操作帧 userId 依据)
    pub username: String, // 用户名(全局唯一)
    pub nt_hash_hex: String, // NT hash 十六进制(32 字符)
    pub permission_level: i8, // 权限级别(0=超管,数值越小权限越高)
    pub status: i32, // 1 正常 / 0 禁用
}

/// 某用户对某共享的访问权限条目(ACL 热更新载荷)。
#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)] // 派生
pub struct ShareUserAccess {
    pub username: String, // 用户名
    pub access: String, // "readwrite" | "readonly"
}

/// 一个 SMB 共享的完整定义(对应一个桶;共享名 = 桶名,共享根 = 桶根)。
/// 桶级元数据(配额/已用/状态)随推送下发,Rust 侧维护桶实例表(registry.rs)。
#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)] // 派生
#[serde(rename_all = "camelCase")] // 驼峰字段名
pub struct ShareInfo {
    pub share_name: String, // SMB 共享名(客户端 \\host\<share_name> 访问)
    pub bucket_id: u64, // 桶主键 ID(对象桶名 = BucketEncoder(id))
    pub bucket_name: String, // 桶显示名
    pub mode: String, // 共享级默认权限:"readwrite" | "readonly"
    pub users: Vec<ShareUserAccess>, // ACL 用户清单(空 = 仅 mode 生效)
    pub quota: u64, // 桶容量配额(字节;0 = 不限)
    pub used_space: u64, // 桶已用空间(字节;剩余容量上报)
    pub status: i32, // 桶状态(1 正常 / 0 禁用 = 实例下线)
}

/// 变更推送条目(Go→Rust 增量;op=upsert/delete/notify,kind 指明载荷)。
#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)] // 派生
#[serde(rename_all = "camelCase")] // 驼峰字段名
pub struct AclEntry {
    pub op: String, // "upsert" | "delete" | "notify"
    pub kind: String, // "user" | "share" | "acl" | "dir"
    #[serde(skip_serializing_if = "Option::is_none")] // None 不序列化
    pub user: Option<UserCred>, // kind=user 时有效
    #[serde(skip_serializing_if = "Option::is_none")] // None 不序列化
    pub share: Option<ShareInfo>, // kind=share 时有效
    #[serde(skip_serializing_if = "Option::is_none")] // None 不序列化
    pub acl: Option<ShareUserAccess>, // kind=acl 时有效
    pub share_name: String, // kind=acl 时指明共享
}

/// 文件/目录元信息 —— 与 ixr-smb-server FileInfo 字段一一对应
/// (时间均为 FILETIME:1601-01-01 起 100ns 刻度)。
#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)] // 派生
#[serde(rename_all = "camelCase")] // 驼峰字段名
pub struct FileInfo {
    pub name: String, // 显示名(末级组件;共享根 = 共享名)
    pub end_of_file: u64, // 文件大小(字节)
    pub allocation_size: u64, // 分配大小(简化 = end_of_file)
    pub creation_time: u64, // FILETIME 创建时间
    pub last_access_time: u64, // FILETIME 最后访问
    pub last_write_time: u64, // FILETIME 最后写入
    pub change_time: u64, // FILETIME 变更(退化 = 最后写入)
    pub is_directory: bool, // 是否目录
    pub file_index: u64, // 唯一文件索引(无则 0)
}
