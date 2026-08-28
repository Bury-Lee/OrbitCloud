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
//!      ▼
//!   BucketInstance(桶的运行时形态:后端 + 桶级元数据 + 库级共享)
//! ```

use std::collections::HashMap; // HashMap:注册表底层容器(共享名 → 实例)
use std::sync::Arc; // Arc:实例共享所有权(多任务并发持有)

use smb_server::{Access, ConfigHandle, Share}; // 库类型:访问级别/配置句柄/协议层共享

use crate::remote_backend::{GatewayClient, RemoteBackend, UserIDIndex}; // 网关客户端 + 远程后端 + 用户索引
use crate::types::ShareInfo; // Go 网关下发的共享定义

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

impl BucketInstance {
    /// 判定共享"定义"是否未变化(upsert 快速路径用)。
    ///
    /// 比较:共享名 / 桶 ID / 桶名 / 权限模式 / 状态。
    /// 不比较:配额与已用空间(桶级元数据,变化不应触发协议层重建);
    /// 也不比较 ACL——快照不再携带聚合 ACL,共享授权完全由认证时
    /// 按需拉取(CODE_AUTH_QUERY_ACL)维护,对账重建时合并保留旧授权。
    ///
    /// 背景:定期对账(apply_snapshot)会反复 upsert 全部共享;若每次都
    /// remove_share + add_share,库侧 remove_share 会关闭该共享全部活跃
    /// 树连接,导致周期踢掉所有 SMB 客户端。定义未变化时仅刷新元数据。
    pub fn same_definition(&self, info: &ShareInfo) -> bool {
        self.share_name == info.share_name // 共享名(= 表键)
            && self.bucket_id == info.bucket_id // 桶主键
            && self.bucket_name == info.bucket_name // 桶显示名
            && self.mode == info.mode // 共享级默认权限
            && self.status == info.status // 桶状态
    }
}

/// 桶实例注册表:动态 map,键 = 共享名(连接意图),值 = 桶实例。
/// 线程安全:内部 RwLock(读多写少,TREE_CONNECT 高频读)。
pub struct BucketRegistry {
    /// 实例表:共享名 → 桶实例(Arc 共享,后端/库共享可被多任务并发使用)。
    inner: tokio::sync::RwLock<HashMap<String, Arc<BucketInstance>>>, // 异步读写锁:读多写少
    /// 用户名 → users.id 索引(构造 RemoteBackend 时注入,操作身份映射)。
    users: UserIDIndex,
}

impl BucketRegistry {
    /// 取用户索引(共享引用;sync 模块维护,RemoteBackend 查询)。
    pub fn users_index(&self) -> UserIDIndex {
        self.users.clone()
    }

    /// 构造空注册表。
    pub fn new() -> Self {
        Self {
            inner: tokio::sync::RwLock::new(HashMap::new()), // 空 map + 锁
            users: UserIDIndex::new(), // 用户名 → users.id 索引(操作身份)
        }
    }

    /// 按连接意图(共享名)解析桶实例。
    ///
    /// 返回值:
    /// - Some(Arc<BucketInstance>):实例存在且 status=1;
    /// - None:未知共享或已禁用(协议层按 SHARE_UNAVAILABLE 处理)。
    pub async fn resolve(&self, share_name: &str) -> Option<Arc<BucketInstance>> {
        let map = self.inner.read().await; // 取读锁(高频读,读锁可并发)
        let hit = match map.get(share_name) { // 按共享名查表
            Some(inst) if inst.status == 1 => Some(inst.clone()), // 命中且状态正常:返回 Arc 副本
            _ => None, // 不存在或已禁用:返回 None(协议层拒绝连接)
        };
        debugf!("注册表:解析共享 {share_name} → {}", &["SMB"], if hit.is_some() { "hit" } else { "miss" }); // 记录连接意图解析结果(命中/未命中)
        hit
    }

    /// 新增/更新桶实例(建桶、桶元数据或 ACL 变更时由 sync 调用)。
    ///
    /// 参数:
    /// - `info`:Go 网关下发的共享定义(桶上下文 + ACL + 配额);
    /// - `conn`:到 Go 网关的连接(RemoteBackend 构造用);
    /// - `config`:库的 ConfigHandle(同步注册协议层共享)。
    ///
    /// 返回值:Ok(());Err(共享名非法/用户缺失/库注册失败)。
    pub async fn upsert(
        &self,
        info: &ShareInfo, // Go 网关下发的共享定义
        conn: &Arc<GatewayClient>, // 网关连接(构造后端用)
        config: &ConfigHandle, // 协议层配置句柄
    ) -> Result<(), String> {
        if info.share_name.trim().is_empty() { // 前置校验:共享名空白?
            return Err("registry: share_name is empty".into()); // 拒绝,避免空键污染注册表
        }

        // ---- 快速路径:定义未变化 → 仅刷新桶级元数据(配额/已用),不重建 ----
        // 定时对账(apply_snapshot)会反复 upsert 全部共享;若定义未变仍走
        // remove_share + add_share,库侧 remove_share 会关闭该共享全部活跃
        // 树连接(server.rs close_trees_for_share),导致周期踢掉所有客户端。
        {
            let map = self.inner.read().await; // 取读锁查现有实例
            if let Some(existing) = map.get(&info.share_name) { // 已有同名单实例?
                if existing.same_definition(info) { // 定义(名/桶/模式/状态)未变?
                    // 保留现有 ACL 底账:info.users 是"本次触发 upsert 的用户"
                    // (认证时 QUERY_ACL 单用户视角),覆盖会导致多用户授权在
                    // 重建时静默丢失(legacy 只剩最后触发者)。
                    let existing_users = existing.users.clone();
                    drop(map); // 释放读锁(下面取写锁覆盖实例)
                    let inst = Arc::new(BucketInstance { // 构造仅元数据刷新的新实例
                        share_name: info.share_name.clone(), // 共享名
                        bucket_id: info.bucket_id, // 桶主键
                        bucket_name: info.bucket_name.clone(), // 桶显示名
                        mode: info.mode.clone(), // 权限模式
                        users: existing_users, // 保留现有 ACL(多用户授权不丢)
                        quota: info.quota as u64, // 配额(可能已变)
                        used_space: info.used_space as u64, // 已用空间(可能已变)
                        status: info.status, // 桶状态
                        backend: RemoteBackend::new(Arc::clone(conn), info.clone(), self.users.clone()), // 后端(等价重建,含用户索引)
                        share: None, // 协议层共享由旧实例持有,此处不重建
                    });
                    let mut map = self.inner.write().await; // 取写锁
                    map.insert(info.share_name.clone(), inst); // 覆盖写入(元数据生效)
                    debugf!("注册表:共享 {} 元数据已刷新(定义未变)", &["SMB"], info.share_name); // 记录元数据刷新
                    return Ok(()); // 不触碰协议层:活跃连接保持不断
                }
            }
        }

        // 合并 ACL:保留旧实例已按需授权的用户(快照 ACL 为空,认证时经
        // QUERY_ACL 动态授权;重建不清空,否则已认证用户全部失去访问权),
        // 叠加快照携带的用户(协议兼容)。
        let legacy_acl: std::collections::HashMap<String, Access> = {
            let map = self.inner.read().await; // 取读锁查旧实例
            map.get(&info.share_name)
                .map(|e| e.users.clone()) // 旧实例 ACL(按需授权结果)
                .unwrap_or_default() // 无旧实例 = 空
        };
        let mut merged_users = legacy_acl; // 以旧授权为基底
        for u in &info.users { // 叠加快照用户
            merged_users.insert(u.username.clone(), access_from_str(&u.access));
        }

        let backend = RemoteBackend::new(Arc::clone(conn), info.clone(), self.users.clone()); // 构造远程后端(共享连接 + 桶上下文 + 用户索引)
        let share = build_share(backend, info)?; // 组装协议层共享(带后端)
        let inst = Arc::new(BucketInstance { // 构造桶实例(注册表内部形态)
            share_name: info.share_name.clone(), // 共享名(表键)
            bucket_id: info.bucket_id, // 桶主键
            bucket_name: info.bucket_name.clone(), // 桶显示名
            mode: info.mode.clone(), // 共享默认权限模式
            users: merged_users.clone(), // 合并后的 ACL(旧授权 + 快照)
            quota: info.quota as u64, // 容量配额
            used_space: info.used_space as u64, // 已用空间
            status: info.status, // 桶状态
            backend: RemoteBackend::new(Arc::clone(conn), info.clone(), self.users.clone()), // 后端(每实例独立持有,含用户索引)
            share: None, // 协议层共享(由下方 add_share 注册)
        });

        // 协议层注册:先移除旧实例(幂等 upsert;UnknownShare = 不存在,忽略),
        if let Err(e) = config.remove_share(&info.share_name).await { // 移除旧共享(清掉旧授权/句柄)
            if !format!("{:?}", e).contains("UnknownShare") { // 非"不存在"错误?
                return Err(format!("注册表:预移除共享 {} 失败:{:?}", info.share_name, e)); // 真实错误:终止
            }
        } // "不存在"错误直接忽略:首次建桶本就无旧实例
        config
            .add_share(share) // 注册新共享(要求用户已注册)
            .await
            .map_err(|e| format!("注册表:注册共享 {} 失败:{:?}", info.share_name, e))?; // 注册失败终止
        for (username, acc) in &merged_users { // 逐用户授予访问权限(合并后的 ACL)
            config
                .grant_share_user(&info.share_name, username, *acc) // 授权:用户名 → 共享
                .await
                .map_err(|e| {
                    format!( // 授权失败:报错并终止(附用户名/共享名)
                        "registry: grant {} on {}: {:?}",
                        username, info.share_name, e
                    )
                })?;
        }

        {
            let mut map = self.inner.write().await; // 取写锁
            map.insert(info.share_name.clone(), inst); // 写入/覆盖实例(保持"共享名→最新实例"唯一映射)
        }
        infof!(
            "注册表:共享 {} (桶 {}) 已上线,{} 个用户", // 记录 upsert 结果(便于排查)
            &["SMB"], // 前缀标签
            info.share_name,
            info.bucket_id,
            info.users.len()
        );
        Ok(())
    }

    /// 删除/下线桶实例(删桶、桶禁用时由 sync 调用)。
    ///
    /// 返回:Ok(());Err(库移除失败——不存在视为成功)。
    pub async fn delete(&self, share_name: &str, config: &ConfigHandle) -> Result<(), String> {
        if let Err(e) = config.remove_share(share_name).await { // 协议层移除(库自动关闭该共享全部活跃树连接)
            if !format!("{:?}", e).contains("UnknownShare") { // 非"不存在"错误?
                return Err(format!("注册表:移除共享 {} 失败:{:?}", share_name, e)); // 真实错误:终止
            }
        } // "不存在"视为幂等成功:删不存在的共享不是错误
        {
            let mut map = self.inner.write().await; // 取写锁
            map.remove(share_name); // 从注册表移除实例(此后 TREE_CONNECT 该名 → 未知共享被拒)
        }
        infof!("注册表:共享 {} 已下线", &["SMB"], share_name); // 记录下线
        Ok(())
    }

    /// 授权用户访问共享(认证按需授权/周期刷新):协议层 ACL 写入 + 实例
    /// ACL 底账同步(重建 legacy 的来源,必须与协议层一致,否则多用户授权
    /// 在重建时会静默丢失)。
    pub async fn grant_user(
        &self,
        share_name: &str,
        username: &str,
        access: Access,
        config: &ConfigHandle,
    ) -> Result<(), String> {
        config
            .grant_share_user(share_name, username, access)
            .await
            .map_err(|e| format!("注册表:授权 {} 于 {} 失败:{e:?}", username, share_name))?;
        self.sync_instance_user(share_name, username, true).await; // 实例 ACL 同步写入
        Ok(())
    }

    /// 撤销用户对共享的访问(权限收紧/删号清理):协议层 ACL 移除 + 实例
    /// ACL 底账同步(与 grant_user 对称,防重建时已撤销授权"复活")。
    pub async fn revoke_user(
        &self,
        share_name: &str,
        username: &str,
        config: &ConfigHandle,
    ) -> Result<(), String> {
        config
            .revoke_share_user(share_name, username)
            .await
            .map_err(|e| format!("注册表:撤销 {} 于 {} 失败:{e:?}", username, share_name))?;
        self.sync_instance_user(share_name, username, false).await; // 实例 ACL 同步移除
        Ok(())
    }

    /// 同步实例 ACL 底账:grant=true 写入,grant=false 移除。
    /// Arc::get_mut 失败(实例被其他引用持有)时保守跳过——协议层 ACL 已生效,
    /// 底账在下次重建时以协议层为准收敛。
    async fn sync_instance_user(&self, share_name: &str, username: &str, grant: bool) {
        let mut map = self.inner.write().await;
        if let Some(inst) = map.get_mut(share_name) {
            if let Some(inner) = Arc::get_mut(inst) {
                if grant {
                    inner.users.insert(username.to_string(), Access::ReadWrite);
                } else {
                    inner.users.remove(username);
                }
            }
        }
    }

    /// 全量快照替换(启动/重连/定期对账时由 sync 调用)。
    ///
    /// 参数:
    /// - `snap`:Go 网关下发的全量快照(全部桶定义);
    /// - `conn`:到 Go 网关的连接(RemoteBackend 构造用);
    /// - `config`:库的 ConfigHandle。
    ///
    /// 返回值:Ok(());Err(任一实例注册失败)。
    pub async fn apply_snapshot(
        &self,
        snap: &crate::types::SnapshotResult, // 全量快照(users + shares)
        conn: &Arc<GatewayClient>, // 网关连接
        config: &ConfigHandle, // 配置句柄
    ) -> Result<(), String> {
        let current: Vec<String> = { // 留底:当前注册表中的全部共享名(用于差集比对)
            let map = self.inner.read().await; // 取读锁
            map.keys().cloned().collect() // 拷贝所有键
        };
        let mut incoming: Vec<String> = Vec::new(); // 记录快照中正常桶的共享名(差集比对用)
        for info in &snap.shares { // 遍历快照共享
            if info.status != 1 { // 桶被禁用?
                continue; // 跳过:不注册 = 实例下线(连接意图被拒)
            }
            incoming.push(info.share_name.clone()); // 记入"应存在"集合
            if let Err(e) = self.upsert(info, conn, config).await { // 逐条 upsert(新增或覆盖更新)
                warnf!("注册表:应用快照 upsert {} 失败:{}(仅告警)", &["SMB"], info.share_name, e); // 单条失败仅告警,不中断整体快照
            } // 未生效条目留待下次对账/增量推送再收敛
        }
        for name in &current { // 差集处理:旧实例中快照没有的
            if !incoming.contains(name) { // 不在"应存在"集合?
                if let Err(e) = self.delete(name, config).await { // 一律下线(桶被删除/禁用)
                    warnf!("注册表:应用快照删除 {} 失败:{}(仅告警)", &["SMB"], name, e); // 下线失败仅告警
                }
            }
        }
        // 快照应用成功不再记录 INFO(周期性对账刷屏);异常已由上方 warnf/调用方 errorf 记录
        Ok(())
    }

    /// 列出全部实例(对账/调试用)。
    ///
    /// 返回值:实例列表(Arc 快照,顺序无关)。
    pub async fn all(&self) -> Vec<Arc<BucketInstance>> {
        let map = self.inner.read().await; // 取读锁
        map.values().cloned().collect() // 拷贝全部实例 Arc
    }

    /// 当前实例数量。
    pub async fn len(&self) -> usize {
        let map = self.inner.read().await; // 取读锁
        map.len() // 返回条目数
    }

    /// 是否为空。
    pub async fn is_empty(&self) -> bool {
        self.len().await == 0 // 数量为 0 即为空
    }
}

/// 默认构造实现(规范:与 new 配套)。
impl Default for BucketRegistry {
    fn default() -> Self {
        Self::new() // 委托 new()
    }
}

/// 把一个 ShareInfo 组装为库的 Share(带 RemoteBackend)。
///
/// 权限模型:默认 AuthenticatedOnly(仅授权用户可访问),ACL 用户由
/// registry.upsert 逐用户 grant_share_user(access 按 info.mode/用户 access)。
/// 注意:不能使用 .public(),否则与显式用户 ACL 冲突(库拒绝 Public+users 混用)。
///
/// 返回值:
/// - Ok(Share):构建成功的库共享;
/// - Err(String):共享名非法等。
fn build_share(backend: RemoteBackend, info: &ShareInfo) -> Result<Share, String> {
    let _ = info; // 占位:当前未使用 info(保留参数以支持后续权限模式扩展)
    Ok(Share::new(&info.share_name, backend)) // 以共享名 + 远程后端构建协议层共享
}

/// 把 ACL 的 access 字符串翻译为库的 Access 枚举。
///
/// 参数:`access` 帧协议权限("readwrite" | "readonly")。
/// 返回值:库的 Access。
pub fn access_from_str(access: &str) -> Access {
    match access {
        "readonly" => Access::Read, // 只读 → 库 Read 权限
        _ => Access::ReadWrite, // 其余(含 "readwrite")→ 库读写权限
    }
}
