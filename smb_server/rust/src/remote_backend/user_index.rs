//! ============================================================================
//! remote_backend/user_index.rs —— 用户名 → users.id 索引(操作身份映射)
//! ============================================================================
//!
//! SMB 会话认证成功后,库层 handler 透传的是**用户名**(Identity::User.user);
//! 而 Go 侧身份审查以 **users.id**(UserID)为键。本索引在同步(快照/增量推送)
//! 时维护 username → user_id 映射,供 RemoteBackend 组装操作帧时把会话用户
//! 翻译为 userId 字段随请求携带。
//!
//! 一致性:与 Go 侧用户表同源(快照 + MSG_AUTH_PUSH upsert/delete 驱动),
//! 与内存用户表(NT hash 表)同步更新;用户新增/删除/重命名即时生效。

use std::collections::HashMap;
use std::sync::Arc;

use tokio::sync::RwLock;

/// 用户名 → users.id 索引(线程安全;共享给 RemoteBackend 与 sync 模块)。
#[derive(Clone, Default)]
pub struct UserIDIndex {
    inner: Arc<RwLock<HashMap<String, u64>>>,
}

impl UserIDIndex {
    /// 构造空索引。
    pub fn new() -> Self {
        Self::default()
    }

    /// 登记/更新用户映射(快照与推送 upsert 时调用)。
    pub async fn upsert(&self, username: &str, user_id: u64) {
        if user_id == 0 {
            return; // 无效 ID 不登记
        }
        self.inner.write().await.insert(username.to_string(), user_id);
    }

    /// 移除用户映射(推送 delete 时调用)。
    pub async fn remove(&self, username: &str) {
        self.inner.write().await.remove(username);
    }

    /// 按用户名查询 users.id(未命中 → 0;调用方按"未知身份"处理)。
    pub async fn get(&self, username: &str) -> u64 {
        self.inner.read().await.get(username).copied().unwrap_or(0)
    }
}
