//! ============================================================================
//! types —— 与 Go 侧 `smb_server/go/protocol.go + types.go` 一一对应的契约层
//! ============================================================================
//!
//! **帧协议、操作码、结构体字段语义/名称/顺序与 Go 侧完全一致**(验收标准 3)。
//!
//! 控制面统一"总操作 JSON + 操作码路由"(单次反序列化):
//! - `OperateRequest`:一次反序列化,Code 路由到具体操作(仿 HTTP 路由组);
//! - `OperateResponse`:同样式总响应(Code 回显 + 结果指针/Err);
//! - 文件数据走流数据段(设计点 8:控制面 JSON、数据面流);
//! - 字段经 `#[serde(rename_all = "camelCase")]` 与 Go 侧 json tag 完全一致。
//!
//! 模块索引:
//! - `frame.rs`    帧协议:常量(MAGIC/MSG_*/ERR_*)、FrameHeader、握手结构、
//!                 帧编解码(大端 16 字节帧头 + 控制面 body 布局);
//! - `operate.rs`  总操作 JSON:OperateCode 常量表、OperateRequest/Response、
//!                 各操作参数(Args)与结果(Result)结构;
//! - `identity.rs` 动态认证与文件元信息共享类型(UserCred/ShareInfo/AclEntry/FileInfo);
//! - `tests.rs`    契约测试(常量与 Go 侧对齐 + 编解码往返)。
//!
//! 使用方式:`use crate::types::*;`(本模块统一 re-export 全部公开项)。
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
//! 帧协议速览(与 Go 侧完全一致,大端序,帧头 16 字节)
//! ============================================================================
//!
//! ```text
//! offset  size  字段
//! ------  ----  --------------------------------------------------
//! 0       4     magic   = 0x4F435354("OCST")
//! 4       1     version = 1
//! 5       1     flags   : 0x01=响应帧 | 0x02=需响应 | 0x04=心跳
//! 6       2     msg_type(见 frame.rs MSG_*)
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

pub mod frame; // 帧协议(常量/帧头/编解码)
pub mod identity; // 动态认证与文件元信息共享类型
pub mod operate; // 总操作 JSON(操作码/请求/响应)

#[cfg(test)]
mod tests; // 契约单元测试(仅测试编译)

// 统一 re-export:外部(`crate::types::*`)一次性取用全部契约项。
pub use frame::*; // 帧层公开项(常量/帧头/握手/编解码)
pub use identity::*; // 身份/共享/元信息类型
pub use operate::*; // 总操作结构
