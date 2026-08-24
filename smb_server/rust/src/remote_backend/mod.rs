//! ============================================================================
//! remote_backend —— 到 Go 网关的帧协议客户端 + SMB 后端适配
//! ============================================================================
//!
//! 对应可行性报告 §3.1(文件操作可替换,核心目标):
//! 照 winfs.rs 的写法实现 `RemoteBackend`(参照样板:rust-smb-server/src/winfs.rs),
//! 把 SMB 操作翻译为对 Go 网关的帧协议 RPC:
//!
//! ```text
//!   SMB 操作           Go 侧映射(见 smb_server/go/file_ops.go 等)
//!   open(path, opts) → 校验用户 → 解析路径 → (bucket, folder 链) → 分配远程句柄 ID
//!   read(offset, len)→ core.Storage.GetRange(现成,范围读)
//!   write(offset,data)→ 写回缓存(write-back),close/flush 整体上传(§4.2)
//!   list_dir         → 查 files / folders 表
//!   unlink / rename  → 复用 server/file_delete.go、file_copy_move.go
//!   stat / set_times / truncate → 查/改文件记录
//! ```
//!
//! 模块索引:
//! - `client.rs`   GatewayClient:私有 TCP 长连接(握手/心跳/请求-响应关联/
//!                 后台 reader/writer/heartbeat 任务 + 密码学工具);
//! - `backend.rs`  RemoteBackend:实现库的 ShareBackend(打开/删除/重命名),
//!                 call_operate 控制面统一入口 + 错误码/意图映射工具;
//! - `handle.rs`   RemoteHandle:实现库的 Handle(读/写/冲刷/元信息/关闭),
//!                 一次 SMB CREATE 对应的远程句柄存根;
//! - `tests.rs`    mock Go 网关集成测试(握手 + OPERATE 全链路)。
//!
//! 线程安全:RemoteBackend 只持 Arc<GatewayClient>(内部锁 + 帧编解码),
//! 满足 ShareBackend 的 Send + Sync + 'static 约束。

mod backend; // RemoteBackend(SMB 后端适配)
mod client; // GatewayClient(帧协议客户端)
mod handle; // RemoteHandle(远程句柄存根)
mod user_index; // UserIDIndex(用户名 → users.id 索引)

#[cfg(test)]
mod tests; // mock 网关集成测试(仅测试编译)

// 统一 re-export:外部使用 `crate::remote_backend::{GatewayClient, RemoteBackend}`。
pub use backend::RemoteBackend; // SMB 后端(SMB 操作 → 帧协议 RPC)
pub use client::GatewayClient; // 网关客户端(私有 TCP 长连接)
pub use user_index::UserIDIndex; // 用户名 → users.id 索引(操作身份映射)
