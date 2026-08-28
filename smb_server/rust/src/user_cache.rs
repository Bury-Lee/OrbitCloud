//! ============================================================================
//! user_cache.rs —�?用户快照缓存(大 key 库):认证按需拉取的核心
//! ============================================================================
//!
//! 取代"全量用户表对账":Rust 侧不再镜像全库用户,只缓存**实际被连接/认证**
//! 的用户快照。查询三级路径:
//!   1. 本地缓存命中且未过期(TTL 由 Moka 后台自动驱逐)→ 直接用;
//!   2. 未命中/已过期 → 向 Go 网关发 CODE_AUTH_QUERY_USER 按需拉取;
//!   3. 拉取结果回填缓存(含 users_index 身份映射),后续认证零 RPC。
//!
//! 一致性:
//!   - 变更即时性由保留的增量推送(MSG_AUTH_PUSH upsert/delete)保证——
//!     upsert 直接覆盖缓存,delete 立即失效(见 sync.rs apply_push);
//!   - TTL 只是"无变更时"的兜底刷新,不承担安全语义;
//!   - 用户不存在/禁用:不缓存 None(推送可随时纠正),认证方按
//!     LOGON_FAILURE 处理。

use std::collections::{HashMap, HashSet};
use std::sync::Arc;
use std::time::Duration;

use smb_server::ConfigHandle; // 协议层配置句柄(共享授权)
use smb_server::UserCreds; // ixr 凭据(nt_hash [u8;16],经库 re-export)

use crate::registry::{BucketRegistry, access_from_str}; // 桶实例注册表 + access 转换
use crate::remote_backend::UserIDIndex; // 用户名 → users.id 映射
use crate::remote_backend::GatewayClient; // Go 网关客户端(RPC)
use crate::types::*; // 帧协议契约(操作码/请求响应结构)

/// 缓存条目:用户快照(凭据 + 身份 ID)。
#[derive(Debug, Clone)]
pub struct CachedUser {
    /// ixr 认证凭据(NT hash)。
    pub creds: UserCreds,
    /// users.id(操作身份映射;0 = 未知)。
    pub user_id: u64,
}

/// 用户快照缓存(Moka TTL key 库)。
#[derive(Clone)]
pub struct UserCache {
    inner: moka::sync::Cache<String, CachedUser>,
    /// 用户 → 已授权共享名集合(ACL diff 基准;认证时按需刷新,
    /// 权限收紧(用户不再可见某桶)即时 revoke)。
    acl_table: Arc<std::sync::Mutex<HashMap<String, HashSet<String>>>>,
}

impl UserCache {
    /// 构造缓存(ttl 过期自动驱逐;capacity 上限兜底防误配膨胀)。
    pub fn new(ttl: Duration, capacity: u64) -> Self {
        Self {
            inner: moka::sync::Cache::builder()
                .time_to_live(ttl)
                .max_capacity(capacity.max(1))
                .build(),
            acl_table: Arc::new(std::sync::Mutex::new(HashMap::new())),
        }
    }

    /// 查询用户名对应的用户快照(本地缓存,不触发拉取)。
    pub fn peek(&self, username: &str) -> Option<CachedUser> {
        self.inner.get(username)
    }

    /// 按需确保用户可用(认证前置,三级查询):
    /// 命中缓存 → 直接返回;未命中 → RPC 拉取 → 回填缓存 + users_index。
    /// 返回 None = 用户不存在/禁用/RPC 失败(认证方按 LOGON_FAILURE 处理)。
    pub async fn ensure(
        &self,
        conn: &Arc<GatewayClient>,
        users_index: &UserIDIndex,
        username: &str,
    ) -> Option<CachedUser> {
        if let Some(hit) = self.peek(username) {
            return Some(hit);
        }
        match self.fetch(conn, username).await {
            Some(cached) => {
                self.inner.insert(username.to_string(), cached.clone());
                users_index.upsert(username, cached.user_id).await;
                Some(cached)
            }
            None => None,
        }
    }

    /// 失效缓存(用户删除/禁用推送时调用)。
    pub fn invalidate(&self, username: &str) {
        self.inner.invalidate(username);
    }

    /// 应用推送的用户权威变更(upsert/delete):
    /// 有效用户(status=1 且 hash 合法)→ 覆盖缓存;禁用/非法 → 失效缓存。
    /// 即时性通道,不等 TTL。
    pub fn apply_push_cred(&self, cred: &UserCred) {
        if cred.status != 1 || cred.nt_hash_hex.is_empty() {
            self.inner.invalidate(&cred.username);
            return;
        }
        match hex::decode(&cred.nt_hash_hex) {
            Ok(b) if b.len() == 16 => {
                let mut arr = [0u8; 16];
                arr.copy_from_slice(&b);
                self.inner.insert(
                    cred.username.clone(),
                    CachedUser {
                        creds: UserCreds { nt_hash: arr },
                        user_id: cred.id,
                    },
                );
            }
            _ => self.inner.invalidate(&cred.username),
        }
    }

    /// 按需刷新用户共享授权(认证成功后调用):
    /// RPC CODE_AUTH_QUERY_ACL 拉该用户可见共享清单 → 逐共享 upsert 实例
    /// (幂等)+ grant 授权;与上次授权集合 diff,不再可见的立即 revoke
    /// (权限收紧即时生效,不等对账周期)。
    pub async fn ensure_user_acl(
        &self,
        conn: &Arc<GatewayClient>,
        registry: &BucketRegistry,
        handle: &ConfigHandle,
        username: &str,
    ) {
        let shares = match self.fetch_acl(conn, username).await {
            Some(s) => s,
            None => return, // 拉取失败:保持现状,下次认证再试
        };
        let mut granted: HashSet<String> = HashSet::new();
        for s in &shares {
            granted.insert(s.share_name.to_ascii_lowercase());
            // 桶实例确保存在(共享清单对账已注册,此处幂等兜底)
            if let Err(e) = registry.upsert(s, conn, handle).await {
                warnf!("缓存:用户 {username} 授权 {} upsert 失败:{e}", &["SMB"], s.share_name);
                continue;
            }
            if let Err(e) = handle
                .grant_share_user(&s.share_name, username, access_from_str(&s.mode))
                .await
            {
                warnf!("缓存:用户 {username} 授权 {} 失败:{e:?}", &["SMB"], s.share_name);
            }
        }
        // diff:上轮已授权但本轮不再可见 → revoke(权限收紧即时)
        // 注意:先取出待撤销名单、释放锁再 await(guard 非 Send)
        let to_revoke: Vec<String> = {
            let mut table = self.acl_table.lock().unwrap();
            let old = table.get(username);
            let revoke = old
                .map(|o| o.difference(&granted).cloned().collect())
                .unwrap_or_default();
            table.insert(username.to_string(), granted);
            revoke
        };
        for name in to_revoke {
            if let Err(e) = handle.revoke_share_user(&name, username).await {
                warnf!("缓存:用户 {username} 撤销 {name} 失败:{e:?}", &["SMB"]);
            }
        }
    }

    /// RPC 拉取用户可见共享清单(CODE_AUTH_QUERY_ACL)。
    async fn fetch_acl(
        &self,
        conn: &Arc<GatewayClient>,
        username: &str,
    ) -> Option<Vec<ShareInfo>> {
        let req = OperateRequest {
            code: CODE_AUTH_QUERY_ACL, // 查询用户可见共享清单(按需)
            auth_acl: Some(AuthAclArgs {
                username: username.to_string(),
            }),
            ..Default::default()
        };
        let body = match encode_operate_body(&req, &[]) {
            Ok(b) => b,
            Err(e) => {
                errorf!("缓存:编码查询 ACL 请求失败 {username}: {e}", &["SMB"]);
                return None;
            }
        };
        let resp_body = match conn.call(MSG_OPERATE, body).await {
            Ok(b) => b,
            Err(code) => {
                warnf!("缓存:查询用户 {username} ACL RPC 失败(code {code})", &["SMB"]);
                return None;
            }
        };
        let (json_bytes, _) = match split_operate_body(&resp_body) {
            Ok(parts) => parts,
            Err(e) => {
                errorf!("缓存:拆查询 ACL 响应失败 {username}: {e}", &["SMB"]);
                return None;
            }
        };
        let resp: OperateResponse = match serde_json::from_slice(json_bytes) {
            Ok(r) => r,
            Err(e) => {
                errorf!("缓存:解析查询 ACL 响应失败 {username}: {e}", &["SMB"]);
                return None;
            }
        };
        match resp.auth_acl {
            Some(r) => Some(r.shares),
            None => {
                errorf!("缓存:查询 ACL 响应缺 auth_acl 段({username})", &["SMB"]);
                None
            }
        }
    }

    /// RPC 拉取单个用户快照(CODE_AUTH_QUERY_USER)。
    async fn fetch(&self, conn: &Arc<GatewayClient>, username: &str) -> Option<CachedUser> {
        let req = OperateRequest {
            code: CODE_AUTH_QUERY_USER, // 查询用户凭据(按需)
            auth_user: Some(AuthUserArgs {
                username: username.to_string(),
            }),
            ..Default::default()
        };
        let body = match encode_operate_body(&req, &[]) {
            Ok(b) => b,
            Err(e) => {
                errorf!("缓存:编码查询用户请求失败 {username}: {e}", &["SMB"]);
                return None;
            }
        };
        let resp_body = match conn.call(MSG_OPERATE, body).await {
            Ok(b) => b,
            Err(code) => {
                warnf!("缓存:查询用户 {username} RPC 失败(code {code})", &["SMB"]);
                return None;
            }
        };
        let (json_bytes, _) = match split_operate_body(&resp_body) {
            Ok(parts) => parts,
            Err(e) => {
                errorf!("缓存:拆查询用户响应失败 {username}: {e}", &["SMB"]);
                return None;
            }
        };
        let resp: OperateResponse = match serde_json::from_slice(json_bytes) {
            Ok(r) => r,
            Err(e) => {
                errorf!("缓存:解析查询用户响应失败 {username}: {e}", &["SMB"]);
                return None;
            }
        };
        let result = match resp.auth_user {
            Some(r) => r,
            None => {
                errorf!("缓存:查询用户响应缺 auth_user 段({username})", &["SMB"]);
                return None;
            }
        };
        if !result.found {
            return None; // 不存在/禁用:不缓存,认证方 LOGON_FAILURE
        }
        let cred = result.cred?;
        // nt_hash_hex(32 字符)→ [u8; 16](转换失败视为凭据损坏,不缓存)
        let hash_bytes = match hex::decode(&cred.nt_hash_hex) {
            Ok(b) if b.len() == 16 => {
                let mut arr = [0u8; 16];
                arr.copy_from_slice(&b);
                arr
            }
            _ => {
                warnf!("缓存:用户 {username} NT hash 格式非法,拒绝缓存", &["SMB"]);
                return None;
            }
        };
        debugf!(
            "缓存:按需拉取用户 {username} 成功(id={}, 权限={})",
            &["SMB"],
            cred.id,
            cred.permission_level
        );
        Some(CachedUser {
            creds: UserCreds { nt_hash: hash_bytes },
            user_id: cred.id,
        })
    }
}
