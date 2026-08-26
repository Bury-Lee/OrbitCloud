//! ============================================================================
//! types/frame.rs —— 帧协议:常量、帧头、握手结构与编解码
//! ============================================================================
//!
//! 与 Go 侧 `protocol.go` 完全对应(常量数值 / 帧头字段顺序 / 字节序一致):
//! - 帧常量:MAGIC / VERSION / MAX_BODY_LEN / FLAG_* / MSG_* / ERR_*;
//! - FrameHeader(16 字节,大端序)与 encode/decode;
//! - 握手结构(HelloRequest / HelloResponse);
//! - 控制面 body 布局编解码(encode_operate_body / split_operate_body)。
//!
//! 硬契约:任何改动须同步 Go 侧 protocol.go。

// 协议常量(与 Go 侧常量完全一致)。
pub const MAGIC: u32 = 0x4F435354; // 帧魔数 "OCST"(OCST 四个 ASCII 字母,用于快速识别协议)
pub const VERSION: u8 = 1; // 协议版本(升级不兼容时双方校验拒绝)
pub const MAX_BODY_LEN: u32 = 16 << 20; // 单帧 body 上限 16 MiB(防内存耗尽)

// 帧头 flags 位标记(与 Go 侧一致)。
pub const FLAG_RESPONSE: u8 = 0x01; // 响应帧
pub const FLAG_NEED_REPLY: u8 = 0x02; // 请求需响应
pub const FLAG_HEARTBEAT: u8 = 0x04; // 心跳探测

// ============================================================================
// 消息类型(msg_type)—— 与 Go 侧 MSG_* 常量数值一致
// ============================================================================

// 握手、心跳与推送。
pub const MSG_HELLO: u16 = 0x0001; // 握手请求(第二阶段 = 双向认证 VERIFY)
pub const MSG_HELLO_ACK: u16 = 0x0002; // 握手应答
pub const MSG_HEARTBEAT: u16 = 0x0003; // 心跳(单向)
pub const MSG_AUTH_PUSH: u16 = 0x0015; // 变更推送(Go→Rust 单向,body = AclEntry)

// 控制面统一走总操作帧,内部用 OperateCode 路由(见 operate.rs)。
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
pub const ERR_DISK_FULL: u32 = 13; // 磁盘满(桶配额超限,拒绝写入)

// ============================================================================
// FrameHeader 帧头(与 Go 侧 FrameHeader 字段/顺序一致)
// ============================================================================

/// 帧头固定长度(16 字节,大端序)。
pub const HEADER_LEN: usize = 16;

/// SMB 网关私有 TCP 帧头(16 字节,大端序)。
/// 承载:协议识别(magic/version)、帧类型(flags/msg_type)、
/// 请求-响应关联(seq)、载荷长度(body_len)。
#[derive(Debug, Clone, Copy, Default)] // 调试/拷贝/默认值派生
pub struct FrameHeader {
    pub magic: u32, // 魔数 "OCST";不匹配立即断开
    pub version: u8, // 协议版本(当前 1)
    pub flags: u8, // 位标记:FLAG_RESPONSE / FLAG_NEED_REPLY / FLAG_HEARTBEAT
    pub msg_type: u16, // 消息类型(见 MSG_* 常量)
    pub seq: u32, // 请求序列号;响应原样带回;0 = 单向通知
    pub body_len: u32, // body 字节数(≤ MAX_BODY_LEN)
}

// ============================================================================
// 握手
// ============================================================================

/// 客户端(本进程)发起握手的请求体。
/// 意义:双向 HMAC 挑战应答鉴权(共享密钥两端预置),防重放。
/// 第二阶段(双向认证)复用本结构:以服务端随机数计算摘要回发。
#[derive(Debug, serde::Serialize, serde::Deserialize)] // JSON 序列化/反序列化派生
#[serde(rename_all = "camelCase")] // 字段名驼峰(与 Go 侧 json tag 一致)
pub struct HelloRequest {
    pub client_id: String, // 网关实例标识(多实例隔离远程句柄表用)
    pub nonce: String, // 随机 16 字节(hex;阶段 1 客户端生成 / 阶段 2 服务端生成)
    pub challenge_digest: String, // HMAC-SHA256(key, "HELLO:"+client_id+":"+nonce) 或
                                  // HMAC-SHA256(key, "VERIFY:"+server_nonce) 的 hex
}

/// 握手应答体。
#[derive(Debug, serde::Serialize, serde::Deserialize)] // JSON 派生
#[serde(rename_all = "camelCase")] // 驼峰字段名
pub struct HelloResponse {
    pub ok: bool, // 是否握手成功
    #[serde(default)] // 缺失时默认空串
    pub server_nonce: String, // 服务端随机 16 字节(hex;双向认证;失败时可缺失)
    #[serde(default)] // 缺失时默认空串
    pub error: String, // 失败原因(仅日志)
}

// ============================================================================
// 帧编解码(传输层真实现;与 Go 侧 protocol.go 的编解码完全一致)
// ============================================================================

/// 把帧头编码为 16 字节大端字节串。
///
/// 参数:`hdr` 帧头结构(调用方填各字段)。
/// 返回值:16 字节帧头。
///
/// 布局:magic(4) + version(1) + flags(1) + msg_type(2) + seq(4) + body_len(4)。
pub fn encode_frame_header(hdr: &FrameHeader) -> [u8; HEADER_LEN] {
    let mut buf = [0u8; HEADER_LEN]; // 16B 输出缓冲(全 0 初始化)
    buf[0..4].copy_from_slice(&hdr.magic.to_be_bytes()); // [0..4] 魔数,大端写入
    buf[4] = hdr.version; // [4] 版本(1 字节)
    buf[5] = hdr.flags; // [5] 标记位(1 字节)
    buf[6..8].copy_from_slice(&hdr.msg_type.to_be_bytes()); // [6..8] 消息类型,大端
    buf[8..12].copy_from_slice(&hdr.seq.to_be_bytes()); // [8..12] 序列号,大端
    buf[12..16].copy_from_slice(&hdr.body_len.to_be_bytes()); // [12..16] body 长度,大端
    buf // 返回 16B 帧头
}

/// 从 16 字节大端字节串解析帧头。
///
/// 参数:`buf` 帧头字节(长度必须 ≥ HEADER_LEN)。
/// 返回值:帧头结构;Err(长度不足/魔数不匹配/版本不匹配/body 超限)。
pub fn decode_frame_header(buf: &[u8]) -> Result<FrameHeader, String> {
    if buf.len() < HEADER_LEN { // 不足 16B?
        return Err("frame: header too short".into()); // 无法构成合法帧头
    }
    let hdr = FrameHeader { // 按大端序逐字段解析(与编码镜像)
        magic: u32::from_be_bytes(buf[0..4].try_into().unwrap()), // [0..4] 魔数
        version: buf[4], // [4] 版本
        flags: buf[5], // [5] 标记位
        msg_type: u16::from_be_bytes(buf[6..8].try_into().unwrap()), // [6..8] 消息类型
        seq: u32::from_be_bytes(buf[8..12].try_into().unwrap()), // [8..12] 序列号
        body_len: u32::from_be_bytes(buf[12..16].try_into().unwrap()), // [12..16] 长度
    };
    if hdr.magic != MAGIC { // 魔数不符?
        return Err("frame: bad magic".into()); // 对端不是本协议(连错服务),拒绝
    }
    if hdr.version != VERSION { // 版本不符?
        return Err("frame: version mismatch".into()); // 协议不兼容,拒绝防误解码
    }
    if hdr.body_len > MAX_BODY_LEN { // 长度超限?
        return Err("frame: body too large".into()); // 防内存耗尽,拒绝
    }
    Ok(hdr) // 校验通过
}

/// 组一帧(帧头 + body),并回填 body_len。
///
/// 参数:`mut hdr` 帧头(调用方填 flags/msg_type/seq);`body` 消息体。
/// 返回值:完整帧字节串。
pub fn encode_frame(mut hdr: FrameHeader, body: &[u8]) -> Vec<u8> {
    hdr.magic = MAGIC; // 回填魔数(调用方无需填)
    hdr.version = VERSION; // 回填版本
    hdr.body_len = body.len() as u32; // 回填 body 长度
    let mut out = encode_frame_header(&hdr).to_vec(); // 先编码帧头
    out.extend_from_slice(body); // 再拼 body
    out // 完整帧 = 帧头 + body
}

/// 组装控制面 body:[4B json_len] + JSON + [流数据段]。
///
/// 参数:`v` 总操作/总响应 JSON 结构;`stream` 流数据段(仅 Read/Write 携带)。
/// 返回值:body 字节串;Err(序列化失败)。
pub fn encode_operate_body<T: serde::Serialize>(v: &T, stream: &[u8]) -> Result<Vec<u8>, String> {
    let j = serde_json::to_vec(v).map_err(|e| format!("operate body: {e}"))?; // JSON 序列化
    let mut out = Vec::with_capacity(4 + j.len() + stream.len()); // 预分配容量(避免多次扩容)
    out.extend_from_slice(&(j.len() as u32).to_be_bytes()); // 写 4B json 长度前缀(大端)
    out.extend_from_slice(&j); // 写 JSON 段
    out.extend_from_slice(stream); // 写流数据段(仅读写操作携带,其余为空)
    Ok(out) // body = [jsonLen] + JSON + 流段
}

/// 拆解控制面 body:解 [4B json_len] 并分离 JSON 与流数据段。
///
/// 参数:`body` 帧消息体(由 MSG_OPERATE/OPERATE_RESP 承载)。
/// 返回值:(json 段, 流数据段);Err(json_len 越界 = 协议错误)。
pub fn split_operate_body(body: &[u8]) -> Result<(&[u8], &[u8]), String> {
    if body.len() < 4 { // 连 4B 前缀都没有?
        return Err("frame: operate body too short".into()); // 协议错误
    }
    let json_len = u32::from_be_bytes(body[0..4].try_into().unwrap()) as usize; // 读 json 长度前缀
    if json_len > body.len() - 4 { // 长度越界?
        return Err("frame: operate json_len overflow".into()); // 对端损坏/恶意,拒绝
    }
    Ok((&body[4..4 + json_len], &body[4 + json_len..])) // 零拷贝拆出 (JSON 段, 流段)
}
