//! ============================================================================
//! registry.rs —— 桶实例注册表:"连接意图 → 实例"的动态映射
//! ============================================================================
//!
//! 设计原则:**一个桶 = 一台 SMB 服务端**。
//!
//! ```text
//!   SMB 客户端 TREE_CONNECT \\host\<共享名>
//!              │  连接意图(想访问哪个桶)
//!              ▼
//!   BucketRegistry(本模块:动态 map,键 = 共享名,值 = 桶实例)
//!      │  由 sync 模块经 Go 网关快照 + MSG_AUTH_PUSH 同步驱动
//!      │  (建桶/删桶/ACL 变更 → upsert/delete,活跃连接热失效)
//!      ▼
//!   BucketInstance(桶的运行时形态:后端 + 桶级元数据 + 库级共享)
//!      │  协议层路由由 ConfigHandle 完成(库自带 ShareBindings 表)
//!      └─ 本表是"业务视图",与 ConfigHandle 的"协议视图"两层协同:
//!           upsert/delete 时同步调用 ConfigHandle.add_share / remove_share
//! ```
//!
//! 职责分工:
//! - 本表:桶实例的增删查(连接意图解析)、桶级元数据(配额/状态/ACL);
//! - ConfigHandle:库级共享注册与活跃连接踢出(协议层);
//! - sync 模块:拉快照/收推送,调用本表方法完成两侧同步。

use std::collections::HashMap;
use std::sync::Arc;

use smb_server::{Access, ConfigHandle, Share};

use crate::remote_backend::RemoteBackend;
use crate::types::ShareInfo;

/// 桶实例:一个桶(一台 SMB 服务端)在 Rust 侧的运行时形态。
pub struct BucketInstance {
    /// 共享名(= 桶名;map 的键,也是客户端连接意图)。
    pub share_name: String,
    /// 桶主键 ID(对象存储桶名 = BucketEncoder(id);携带在 RemoteBackend 里)。
    pub bucket_id: u64,
    /// 桶显示名。
    pub bucket_name: String,
    /// 共享级默认权限:"readwrite" | "readonly"。
    pub mode: String,
    /// ACL 用户清单(用户名 → 访问级别;空 = 仅 mode 生效)。
    pub users: HashMap<String, Access>,
    /// 桶容量配额(字节;0 = 不限;FS_SIZE_INFORMATION 上报)。
    pub quota: u64,
    /// 桶已用空间(字节;上报剩余容量)。
    pub used_space: u64,
    /// 桶状态(1 正常 / 0 禁用;禁用 = 实例下线,拒绝 TREE_CONNECT)。
    pub status: i32,
    /// 远程后端(转发文件操作 RPC 到 Go,携带本桶上下文)。
    pub backend: RemoteBackend,
    /// 库级共享(已注册到 ConfigHandle 的形态;注册后持有)。
    pub share: Option<Share>,
}

/// 桶实例注册表:动态 map,键 = 共享名(连接意图),值 = 桶实例。
/// 同步:由 sync 模块经 Go 网关快照 + MSG_AUTH_PUSH 驱动 upsert/delete;
/// 线程安全:内部 RwLock(读多写少,TREE_CONNECT 高频读)。
pub struct BucketRegistry {
    /// 实例表:共享名 → 桶实例(Arc 共享,后端/库共享可被多任务并发使用)。
    inner: tokio::sync::RwLock<HashMap<String, Arc<BucketInstance>>>,
}

impl BucketRegistry {
    /// 构造空注册表。
    pub fn new() -> Self {
        Self {
            inner: tokio::sync::RwLock::new(HashMap::new()),
        }
    }

    /// 按连接意图(共享名)解析桶实例。
    ///
    /// 参数:`share_name` 客户端 TREE_CONNECT 声明的共享名。
    ///
    /// 返回值:
    /// - Some(Arc<BucketInstance>):实例存在且 status=1;
    /// - None:未知共享或已禁用(协议层按 SHARE_UNAVAILABLE 处理)。
    ///
    /// 内部逻辑(伪代码):
    /// 1. 读锁内查表;
    /// 2. 命中且 status == 1 → 返回实例;否则 None。
    pub async fn resolve(&self, share_name: &str) -> Option<Arc<BucketInstance>> {
        let _ = share_name;
        // 伪代码阶段占位:返回未找到;真实现按上方分步注释执行。
        None
    }

    /// 新增/更新桶实例(建桶、桶元数据或 ACL 变更时由 sync 调用)。
    ///
    /// 参数:
    /// - `info`:Go 网关下发的共享定义(桶上下文 + ACL + 配额);
    /// - `config`:库的 ConfigHandle(同步注册协议层共享)。
    ///
    /// 返回值:Ok(());Err(共享名非法/用户缺失/库注册失败)。
    ///
    /// 内部逻辑(伪代码):
    /// 1. 用 info 构造 RemoteBackend 与库级 Share(见 build_share),
    ///    组装 BucketInstance;
    /// 2. 写锁 upsert 到 inner;
    /// 3. 同步协议层:config.add_share(share),再对 users 逐个
    ///    grant_share_user(已有实例先按 diff 撤销再授予);
    /// 4. 更新成功;失败回滚 map 并返回 Err。
    pub async fn upsert(&self, info: &ShareInfo, config: &ConfigHandle) -> Result<(), String> {
        let _ = (info, config);
        // 伪代码阶段占位:返回未实现哨兵;真实现按上方分步注释执行。
        Err("伪代码:未实现".into())
    }

    /// 删除/下线桶实例(删桶、桶禁用时由 sync 调用)。
    ///
    /// 参数:
    /// - `share_name`:共享名;
    /// - `config`:库的 ConfigHandle(同步移除协议层共享)。
    ///
    /// 返回值:Ok(());Err(库移除失败——不存在视为成功)。
    ///
    /// 内部逻辑(伪代码):
    /// 1. 先 revoke_share_user 逐用户收回授权;
    /// 2. config.remove_share(share_name)(库自动关闭该共享全部活跃树连接);
    /// 3. 写锁从 inner 移除实例;
    /// 4. 不存在 → 幂等成功。
    pub async fn delete(&self, share_name: &str, config: &ConfigHandle) -> Result<(), String> {
        let _ = (share_name, config);
        // 伪代码阶段占位:返回未实现哨兵;真实现按上方分步注释执行。
        Err("伪代码:未实现".into())
    }

    /// 全量快照替换(启动/重连/定期对账时由 sync 调用)。
    ///
    /// 参数:
    /// - `snap`:Go 网关下发的全量快照(全部桶定义);
    /// - `config`:库的 ConfigHandle。
    ///
    /// 返回值:Ok(());Err(任一实例注册失败)。
    ///
    /// 内部逻辑(伪代码):
    /// 1. 按快照重建 inner(先清空,再逐条 upsert);
    ///    —— 简化:直接逐条 upsert,删掉快照中不存在的实例;
    /// 2. 逐条调 upsert / delete 同步 ConfigHandle;
    /// 3. 全部成功返回。
    pub async fn apply_snapshot(
        &self,
        snap: &crate::types::SnapshotResult,
        config: &ConfigHandle,
    ) -> Result<(), String> {
        let _ = (snap, config);
        // 伪代码阶段占位:返回未实现哨兵;真实现按上方分步注释执行。
        Err("伪代码:未实现".into())
    }

    /// 列出全部实例(对账/调试用)。
    ///
    /// 返回值:实例列表(Arc 快照,顺序无关)。
    pub async fn all(&self) -> Vec<Arc<BucketInstance>> {
        // 伪代码阶段占位:返回空表;真实现读锁遍历收集。
        Vec::new()
    }

    /// 当前实例数量。
    pub async fn len(&self) -> usize {
        // 伪代码阶段占位:返回 0;真实现读锁取 len。
        0
    }

    /// 是否为空。
    pub async fn is_empty(&self) -> bool {
        self.len().await == 0
    }
}

/// 默认构造实现(规范:与 new 配套)。
impl Default for BucketRegistry {
    fn default() -> Self {
        Self::new()
    }
}

/// 把一个 ShareInfo 组装为库的 Share(带 RemoteBackend)。
///
/// 参数:
/// - `backend`:已按 info 构造的远程后端;
/// - `info`:共享定义(桶上下文)。
///
/// 返回值:
/// - Ok(Share):构建成功的库共享;
/// - Err(String):共享名非法/用户缺失等(由调用方记日志)。
///
/// 内部逻辑(伪代码):
/// 1. 按共享名与后端构造库的共享;
/// 2. 共享为只读模式时标成公开只读,否则按 ACL 逐用户授予访问级别;
/// 3. 返回构造好的共享。
fn build_share(backend: RemoteBackend, info: &ShareInfo) -> Result<Share, String> {
    let _ = (backend, info);
    // 伪代码阶段占位:返回未实现哨兵;真实现按上方分步注释执行。
    Err("伪代码:未实现".into())
}

/// 把 ACL 的 access 字符串翻译为库的 Access 枚举。
///
/// 参数:`access` 帧协议权限("readwrite" | "readonly")。
/// 返回值:库的 Access。
fn access_from_str(access: &str) -> Access {
    match access {
        "readonly" => Access::Read,
        _ => Access::ReadWrite,
    }
}
