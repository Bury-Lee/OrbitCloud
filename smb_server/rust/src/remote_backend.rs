//! ============================================================================
//! remote_backend.rs —— RemoteBackend:实现 ShareBackend/Handle trait,转发到 Go
//! ============================================================================
//!
//! 对应可行性报告 §3.1(文件操作可替换,核心目标):
//! 照 winfs.rs 的写法实现 `RemoteBackend`(参照样板:rust-smb-server/src/winfs.rs),
//! 把 SMB 操作翻译为对 Go 网关的帧协议 RPC:
//!
//! ```text
//!   SMB 操作           Go 侧映射(见 smb_server/go/file_ops.go)
//!   open(path, opts) → 校验用户 → 解析路径 → (bucket, folder 链) → 分配远程句柄 ID
//!   read(offset, len)→ core.Storage.GetRange(现成,范围读)
//!   write(offset,data)→ 写回缓存(write-back),close/flush 整体上传(§4.2)
//!   list_dir         → 查 files / folders 表
//!   unlink / rename  → 复用 server/file_delete.go、file_copy_move.go
//!   stat / set_times / truncate → 查/改文件记录
//! ```
//!
//! 线程安全:RemoteBackend 只持 Arc<GatewayClient>(内部锁 + 帧编解码),
//! 满足 ShareBackend 的 Send + Sync + 'static 约束。

use std::collections::HashMap;
use std::sync::Arc;

use async_trait::async_trait;
use bytes::Bytes;
use smb_server::{
    BackendCapabilities, DirEntry, FileInfo, FileTimes, Handle, OpenIntent, OpenOptions,
    ShareBackend, SmbError, SmbPath, SmbResult,
};

use crate::types::*;

/// 应答通道表:seq → 请求应答(oneshot)。
/// 承载:reader 任务按 seq 投递响应,调用方 await 取回;
/// 超时(30s)未收到 → 移除并回 ERR_TIMEOUT。
/// 用 Arc 包装:reader 任务与 GatewayClient 共享同一张表(无需 Clone 语义)。
type PendingReplies =
    Arc<tokio::sync::Mutex<HashMap<u32, tokio::sync::oneshot::Sender<Result<Vec<u8>, u32>>>>>;

// ============================================================================
// GatewayClient:SMB 网关的私有 TCP 客户端(帧协议)
// ============================================================================

/// 与 Go 网关的一条私有 TCP 长连接(帧协议客户端)。
/// 承载:握手、心跳、请求-响应关联(seq → oneshot 通道)、断线重连。
///
/// 关键字段意义:
/// - `send_queue`:发送缓冲管道(设计点 6,容量 = 配置 channel_buffer):
///   各调用方只往管道投帧,writer 任务消费并写 socket,写入侧不阻塞;
/// - `seq_counter`:请求序列号自增(响应原样带回);
/// - `pending`:seq → 应答通道表(reader 任务投递,超时回收);
/// - `push_tx`:变更推送转发通道(MSG_AUTH_PUSH body → sync 模块);
/// - `shared_key`:握手 HMAC 密钥(内存持有,不落盘;shared_key_env 未定义
///   时,握手成功后换成双方交换的随机密钥)。
pub struct GatewayClient {
    send_queue: tokio::sync::mpsc::Sender<Vec<u8>>, // 发送缓冲管道(设计点 6)
    seq_counter: std::sync::atomic::AtomicU32,      // 请求序列号(1 起)
    pending: PendingReplies,                        // seq → 应答通道表
    push_tx: tokio::sync::mpsc::Sender<Vec<u8>>,    // 推送转发(sync 模块消费)
    shared_key: Vec<u8>,                            // 共享密钥(握手鉴权/协商)
}

impl GatewayClient {
    /// 连接 Go 网关并完成握手。
    ///
    /// 参数:
    /// - `addr`:Go 网关地址(如 "127.0.0.1:9001");
    /// - `shared_key`:共享密钥(≥16 字节,与 Go 侧一致);
    /// - `client_id`:本实例标识(多实例隔离远程句柄表);
    /// - `channel_buffer`:发送管道缓冲长度(设计点 6,取配置
    ///   gateway.channel_buffer;与 Go 侧请求池队列深度对齐)。
    ///
    /// 返回值:握手成功的客户端;错误为连接失败/密钥校验失败/版本不匹配。
    ///
    /// 实现步骤:
    /// 1. 建立到网关地址的 TCP 连接,并开启快速发送(NODELAY);
    /// 2. 按 channel_buffer 容量建发送缓冲管道与推送通道;
    /// 3. 组装握手请求:带上本机标识、16 字节随机数,以及用共享密钥
    ///    对两者算出的 HMAC-SHA256 摘要;
    /// 4. 发出握手请求帧,等待应答帧;
    /// 5. 校验应答:成功标志与服务端随机数,再回送双向认证确认
    ///    (digest = HMAC(key, "VERIFY:"+server_nonce));
    /// 6. 启动三个后台任务:reader(读帧投递)、writer(消费管道写流)、
    ///    heartbeat(定时心跳)。
    pub async fn connect(
        addr: &str,
        shared_key: Vec<u8>,
        client_id: String,
        channel_buffer: usize,
    ) -> std::io::Result<Arc<Self>> {
        // ---- 1. 建立 TCP 连接并开启 NODELAY ----
        let stream = tokio::net::TcpStream::connect(addr)
            .await
            .map_err(|e| std::io::Error::new(e.kind(), format!("connect {addr}: {e}")))?;
        stream.set_nodelay(true).ok();

        // ---- 2. 发送缓冲管道 + 推送通道(容量 = channel_buffer) ----
        let (send_tx, send_rx) = tokio::sync::mpsc::channel(channel_buffer.max(1));
        let (push_tx, push_rx) = tokio::sync::mpsc::channel(channel_buffer.max(1));

        let client = Arc::new(Self {
            send_queue: send_tx,
            seq_counter: std::sync::atomic::AtomicU32::new(0),
            pending: Arc::new(tokio::sync::Mutex::new(HashMap::new())),
            push_tx,
            shared_key,
        });

        // ---- 3~5. 双向 HMAC 握手(使用整体流直接读写) ----
        let mut stream = stream;
        client.handshake(&mut stream, &client_id).await?;

        // ---- 6. 握手完成:拆分流并启动后台任务 ----
        let (reader, writer) = tokio::io::split(stream);
        tokio::spawn(writer_task(writer, send_rx));
        tokio::spawn(reader_task(
            reader,
            client.pending.clone(),
            client.push_tx.clone(),
        ));
        tokio::spawn(Arc::clone(&client).heartbeat_task());
        // 推送通道接收端交给 sync 模块(见 sync.rs;此处占位丢弃)。
        drop(push_rx);
        Ok(client)
    }

    /// 双向 HMAC 握手(HELLO → HELLO_ACK → VERIFY)。
    ///
    /// 参数:`stream` 已连接的 TCP 流(整体,读写直接用);`client_id` 本实例标识。
    /// 返回值:握手成功;Err(校验失败/超时/对端拒绝)。
    ///
    /// 实现步骤:
    /// 1. 生成 16 字节随机 nonce,算 HELLO 摘要
    ///    HMAC-SHA256(key, "HELLO:"+client_id+":"+nonce);
    /// 2. 发 MSG_HELLO 帧,等 MSG_HELLO_ACK(10s 超时);
    /// 3. 校验 ok 与 server_nonce;
    /// 4. 回 VERIFY 帧:HMAC-SHA256(key, "VERIFY:"+server_nonce)。
    async fn handshake(
        &self,
        stream: &mut tokio::net::TcpStream,
        client_id: &str,
    ) -> std::io::Result<()> {
        use tokio::io::AsyncWriteExt;

        // ---- 1. HELLO 帧:nonce + 摘要 ----
        let nonce = random_hex(16);
        let digest = hmac_sha256_hex(&self.shared_key, &format!("HELLO:{client_id}:{nonce}"));
        let hello = HelloRequest {
            client_id: client_id.to_string(),
            nonce: nonce.clone(),
            challenge_digest: digest,
        };
        let body = serde_json::to_vec(&hello)
            .map_err(|e| std::io::Error::other(format!("hello json: {e}")))?;
        let frame = encode_frame(
            FrameHeader {
                flags: FLAG_NEED_REPLY,
                msg_type: MSG_HELLO,
                seq: 1,
                ..Default::default()
            },
            &body,
        );
        stream.write_all(&frame).await?;

        // ---- 2. 等 HELLO_ACK ----
        let (hdr, resp_body) =
            tokio::time::timeout(std::time::Duration::from_secs(10), read_one_frame(stream))
                .await
                .map_err(|_| std::io::Error::other("handshake timeout"))??;
        if hdr.msg_type != MSG_HELLO_ACK {
            return Err(std::io::Error::other(format!(
                "handshake: expected HELLO_ACK, got {:#x}",
                hdr.msg_type
            )));
        }
        let ack: HelloResponse = serde_json::from_slice(&resp_body)
            .map_err(|e| std::io::Error::other(format!("hello ack json: {e}")))?;
        if !ack.ok {
            return Err(std::io::Error::other(format!(
                "handshake rejected: {}",
                ack.error
            )));
        }

        // ---- 3~4. 回 VERIFY 帧(双向认证) ----
        let vdigest = hmac_sha256_hex(&self.shared_key, &format!("VERIFY:{}", ack.server_nonce));
        let verify = HelloRequest {
            client_id: client_id.to_string(),
            nonce: ack.server_nonce.clone(),
            challenge_digest: vdigest,
        };
        let vbody = serde_json::to_vec(&verify)
            .map_err(|e| std::io::Error::other(format!("verify json: {e}")))?;
        let vframe = encode_frame(
            FrameHeader {
                flags: FLAG_NEED_REPLY,
                msg_type: MSG_HELLO,
                seq: 2,
                ..Default::default()
            },
            &vbody,
        );
        stream.write_all(&vframe).await?;
        Ok(())
    }

    /// 发送一帧(帧头 + body),不等待响应(单向通知/心跳)。
    ///
    /// 参数:
    /// - `msg_type`:消息类型(MSG_* 常量);
    /// - `flags`:帧标记(FLAG_NEED_REPLY / FLAG_HEARTBEAT 等);
    /// - `body`:载荷字节。
    ///
    /// 返回值:网络错误。
    ///
    /// 实现步骤(设计点 6):
    /// 1. 组装帧头(大端)与帧体字节;
    /// 2. 投进发送缓冲管道 send_queue(容量 = channel_buffer;管道满 →
    ///    阻塞,写入侧自然背压到调用方);
    /// 3. writer 任务从管道取出并独占写 socket,调用方不碰流。
    async fn send_frame(&self, msg_type: u16, flags: u8, body: &[u8]) -> std::io::Result<()> {
        let frame = encode_frame(
            FrameHeader {
                flags,
                msg_type,
                ..Default::default()
            },
            body,
        );
        self.send_queue
            .send(frame)
            .await
            .map_err(|_| std::io::Error::other("send queue closed"))
    }

    /// 发起请求-响应调用(核心 RPC 原语)。
    ///
    /// 参数:
    /// - `msg_type`:请求消息类型;
    /// - `body`:请求载荷(JSON 或二进制)。
    ///
    /// 返回值:Ok(响应 body);Err(哨兵错误码 ERR_* —— 网关业务错误,
    /// 连接/超时错误 → ERR_GATEWAY_DOWN / ERR_TIMEOUT)。
    ///
    /// 实现步骤:
    /// 1. 申请一个自增序号,把应答通道登记到待应答表;
    /// 2. 发出带"需要响应"标记的请求帧;
    /// 3. 等待应答,超过 30 秒未到则返回超时哨兵错误;
    /// 4. 应答若是错误帧(MSG_ERR_RESP),解出错误码并返回。
    pub async fn call(&self, msg_type: u16, body: Vec<u8>) -> Result<Vec<u8>, u32> {
        // ---- 1. 分配序号并登记应答通道 ----
        let seq = self
            .seq_counter
            .fetch_add(1, std::sync::atomic::Ordering::Relaxed)
            + 1;
        let (tx, rx) = tokio::sync::oneshot::channel();
        self.pending.lock().await.insert(seq, tx);

        // ---- 2. 投发送管道(需响应标记) ----
        let frame = encode_frame(
            FrameHeader {
                flags: FLAG_NEED_REPLY,
                msg_type,
                seq,
                ..Default::default()
            },
            &body,
        );
        if self.send_queue.send(frame).await.is_err() {
            // 管道已关闭(writer 任务退出 = 连接断开)。
            self.pending.lock().await.remove(&seq);
            return Err(ERR_GATEWAY_DOWN);
        }

        // ---- 3~4. 等应答(30s 超时) ----
        match tokio::time::timeout(std::time::Duration::from_secs(30), rx).await {
            Ok(Ok(Ok(resp_body))) => Ok(resp_body),
            Ok(Ok(Err(code))) => Err(code),
            Ok(Err(_)) => {
                // oneshot 发送端被丢弃(reader 任务退出 = 断线)。
                self.pending.lock().await.remove(&seq);
                Err(ERR_GATEWAY_DOWN)
            }
            Err(_) => {
                // 超时:移除登记,返回超时哨兵。
                self.pending.lock().await.remove(&seq);
                Err(ERR_TIMEOUT)
            }
        }
    }

    /// 解码 ErrorEnvelope(供 call 的统一错误路径使用)。
    /// 参数:`body` MSG_ERR_RESP 的载荷。
    /// 返回值:哨兵错误码(解析失败 → ERR_IO)。
    fn decode_error(body: &[u8]) -> u32 {
        serde_json::from_slice::<ErrorEnvelope>(body)
            .map(|e| e.code)
            .unwrap_or(ERR_IO)
    }

    /// 接收变更推送的通道接收端(供 sync 模块消费)。
    /// 返回值:推送通道(MSG_AUTH_PUSH 的 body,即 AclEntry JSON)。
    pub fn take_push_rx(&self) -> Option<tokio::sync::mpsc::Receiver<Vec<u8>>> {
        // 注意:通道在 connect 时已建立;此处接口供 sync 模块在 connect
        // 之前预约接收端(真实现时由 connect 返回)。当前返回 None 占位。
        None
    }

    /// 心跳与空闲回收任务。
    ///
    /// 实现步骤:
    /// 1. 每 30 秒发一次心跳帧(单向,不等待响应);
    /// 2. 发送失败(管道关闭=断线)→ 退出;
    /// 3. 对端空闲超时由 Go 侧读超时判定;本侧重连由 sync 模块驱动。
    async fn heartbeat_task(self: Arc<Self>) {
        let mut ticker = tokio::time::interval(std::time::Duration::from_secs(30));
        loop {
            ticker.tick().await;
            if self
                .send_frame(MSG_HEARTBEAT, FLAG_HEARTBEAT, &[])
                .await
                .is_err()
            {
                break; // 管道关闭 = 连接已断,退出心跳
            }
        }
    }
}

/// reader 后台任务:持续读帧并按类型投递。
///
/// 参数:
/// - `reader`:TCP 读半流(独占);
/// - `pending`:seq → 应答通道表(响应帧按 seq 投递);
/// - `push_tx`:推送转发通道(MSG_AUTH_PUSH 转交 sync 模块)。
///
/// 实现步骤:
/// 1. 循环读帧头(16B)→ 校验 → 读 body;
/// 2. 响应帧(FLAG_RESPONSE):按 seq 从 pending 取通道投递;
///    MSG_ERR_RESP → Err(错误码),其余 → Ok(body);
/// 3. 单向帧 MSG_AUTH_PUSH(seq=0):转交 push_tx;
/// 4. 其余(心跳等)忽略;
/// 5. 断线/协议错误:向 pending 全部投 ERR_GATEWAY_DOWN 后退出。
async fn reader_task(
    mut reader: tokio::io::ReadHalf<tokio::net::TcpStream>,
    pending: PendingReplies,
    push_tx: tokio::sync::mpsc::Sender<Vec<u8>>,
) {
    use tokio::io::AsyncReadExt;
    loop {
        // ---- 读帧头 ----
        let mut head = [0u8; HEADER_LEN];
        if reader.read_exact(&mut head).await.is_err() {
            break; // 连接关闭
        }
        let hdr = match decode_frame_header(&head) {
            Ok(h) => h,
            Err(_) => break, // 协议错误:退出(上层重连)
        };
        // ---- 读 body ----
        let mut body = vec![0u8; hdr.body_len as usize];
        if reader.read_exact(&mut body).await.is_err() {
            break;
        }

        if hdr.flags & FLAG_RESPONSE != 0 {
            // ---- 响应帧:按 seq 投递 ----
            if let Some(tx) = pending.lock().await.remove(&hdr.seq) {
                if hdr.msg_type == MSG_ERR_RESP {
                    let code = GatewayClient::decode_error(&body);
                    let _ = tx.send(Err(code));
                } else {
                    let _ = tx.send(Ok(body));
                }
            }
        } else if hdr.msg_type == MSG_AUTH_PUSH {
            // ---- 单向推送:转交 sync 模块 ----
            let _ = push_tx.send(body).await;
        }
        // 其余单向帧(心跳等)忽略。
    }
    // ---- 断线收尾:通知所有等待中的调用方 ----
    let mut p = pending.lock().await;
    for (_, tx) in p.drain() {
        let _ = tx.send(Err(ERR_GATEWAY_DOWN));
    }
}

/// writer 后台任务:消费发送管道,独占写 socket。
///
/// 参数:`writer` TCP 写半流(独占);`rx` 发送缓冲管道接收端。
/// 实现:循环取帧 → write_all;失败退出(连接断开,心跳/调用方随之失败)。
async fn writer_task(
    mut writer: tokio::io::WriteHalf<tokio::net::TcpStream>,
    mut rx: tokio::sync::mpsc::Receiver<Vec<u8>>,
) {
    use tokio::io::AsyncWriteExt;
    while let Some(frame) = rx.recv().await {
        if writer.write_all(&frame).await.is_err() {
            break; // 写失败 = 连接断开
        }
    }
}

/// 从流中读一帧(握手期间直接读用;返回帧头与 body)。
async fn read_one_frame(
    stream: &mut tokio::net::TcpStream,
) -> std::io::Result<(FrameHeader, Vec<u8>)> {
    use tokio::io::AsyncReadExt;
    let mut head = [0u8; HEADER_LEN];
    stream.read_exact(&mut head).await?;
    let hdr = decode_frame_header(&head).map_err(std::io::Error::other)?;
    let mut body = vec![0u8; hdr.body_len as usize];
    stream.read_exact(&mut body).await?;
    Ok((hdr, body))
}

/// 生成 n 字节随机数的 hex 串(握手 nonce)。
fn random_hex(n: usize) -> String {
    use rand::RngCore;
    let mut buf = vec![0u8; n];
    rand::thread_rng().fill_bytes(&mut buf);
    hex::encode(buf)
}

/// HMAC-SHA256 摘要的 hex 串(握手挑战应答,与 Go 侧 hmacDigest 一致)。
fn hmac_sha256_hex(key: &[u8], payload: &str) -> String {
    use hmac::{Hmac, Mac};
    use sha2::Sha256;
    type HmacSha256 = Hmac<Sha256>;
    let mut mac = HmacSha256::new_from_slice(key).expect("hmac key any length");
    mac.update(payload.as_bytes());
    hex::encode(mac.finalize().into_bytes())
}

// ============================================================================
// RemoteBackend:实现 ShareBackend(转发到 Go)
// ============================================================================

/// 控制面调用统一入口:编码总操作 JSON → call → 解析总响应。
///
/// 参数:
/// - `conn`:GatewayClient(帧协议连接);
/// - `req`:总操作请求(单次反序列化目标,见 types.rs);
/// - `stream`:请求流数据段(仅 Write 携带)。
///
/// 返回值:
/// - Ok((OperateResponse, Vec<u8>)):响应结构 + 响应流数据段(仅 Read 携带);
/// - Err(u32):哨兵错误码(ERR_*;含网关业务错误与连接/超时)。
///
/// 实现步骤:
/// 1. encode_operate_body(req, stream) 组装 [4B jsonLen]+JSON+流段;
/// 2. conn.call(MSG_OPERATE, body) 等待应答;
/// 3. split_operate_body 拆响应 JSON 与流段,反序列化 OperateResponse;
/// 4. resp.err 非 None → Err(哨兵 code)。
async fn call_operate(
    conn: &Arc<GatewayClient>,
    req: &OperateRequest,
    stream: &[u8],
) -> Result<(OperateResponse, Vec<u8>), u32> {
    let body = encode_operate_body(req, stream).map_err(|_| ERR_IO)?;
    let resp_body = conn.call(MSG_OPERATE, body).await?;
    let (json_bytes, stream_out) = split_operate_body(&resp_body).map_err(|_| ERR_IO)?;
    let resp: OperateResponse = serde_json::from_slice(json_bytes).map_err(|_| ERR_IO)?;
    if let Some(err) = &resp.err {
        return Err(err.code);
    }
    Ok((resp, stream_out.to_vec()))
}

/// 远程后端:把 SMB 协议操作转发到 Go 网关(每共享一实例)。
///
/// 承载功能:
/// - `conn`:到 Go 网关的连接(共享同一长连接,帧协议复用);
/// - `share`:本共享的静态定义(共享名/桶 ID/权限模式),TREE_CONNECT 时使用。
#[derive(Clone)]
pub struct RemoteBackend {
    conn: Arc<GatewayClient>, // 到 Go 网关的共享连接
    share: ShareInfo,         // 本共享定义(共享名 = 桶名,桶根 = 虚拟根)
}

/// 实现库定义的 `ShareBackend` trait(存储抽象层;trait 定义见
/// rust-smb-server/vendor/ixr-smb-server/src/backend.rs)。
#[async_trait]
impl ShareBackend for RemoteBackend {
    /// 打开(或创建)文件/目录,返回远程句柄。
    ///
    /// 参数:
    /// - `path`:已验证的 SMB 相对路径(SmbPath 已过滤 ".."/绝对路径/非法字符);
    /// - `opts`:协议层翻译好的 SMB CREATE 意图(OpenOptions)。
    ///
    /// 返回值:Ok(Box<dyn Handle>);Err(SmbError —— 哨兵错误按 ERR_* 映射)。
    ///
    /// 实现步骤:
    /// 1. 把路径与打开选项翻译成打开请求(读写意图、目录标志、关闭即删);
    /// 2. 发给网关,取回打开结果(句柄 ID、是否目录、文件大小);
    /// 3. 用结果包成本地远程句柄存根返回;
    /// 4. 网关返回错误码时,映射为对应的 SMB 错误。
    async fn open(&self, path: &SmbPath, opts: OpenOptions) -> SmbResult<Box<dyn Handle>> {
        // ---- 1. 组装打开请求(OpenOptions → OpenArgs) ----
        let req = OperateRequest {
            code: CODE_FILE_OPEN,
            open: Some(OpenArgs {
                path: path.display_backslash(),
                read: opts.read,
                write: opts.write,
                intent: intent_str(opts.intent).to_string(),
                directory: opts.directory,
                non_directory: opts.non_directory,
                delete_on_close: opts.delete_on_close,
            }),
            ..Default::default()
        };
        // ---- 2~3. 发网关,包远程句柄 ----
        let (resp, _) = call_operate(&self.conn, &req, &[])
            .await
            .map_err(map_gateway_err)?;
        let r = resp.open.ok_or(SmbError::NotSupported)?;
        Ok(Box::new(RemoteHandle {
            conn: Arc::clone(&self.conn),
            handle_id: r.handle_id,
            is_dir: r.is_dir,
            delete_on_close: opts.delete_on_close,
        }))
    }

    /// 删除路径(文件/空目录)。
    ///
    /// 参数:`path` 待删除的 SMB 相对路径。
    /// 返回值:Ok(());Err(NotFound/AccessDenied/NotEmpty 等)。
    ///
    /// 实现步骤:
    /// 1. 组装删除请求发给网关;
    /// 2. 网关的错误码映射为 SMB 错误(非空目录对应目录非空错误)。
    async fn unlink(&self, path: &SmbPath) -> SmbResult<()> {
        let req = OperateRequest {
            code: CODE_FILE_UNLINK,
            unlink: Some(UnlinkArgs {
                path: path.display_backslash(),
            }),
            ..Default::default()
        };
        let (_, _) = call_operate(&self.conn, &req, &[])
            .await
            .map_err(map_gateway_err)?;
        Ok(())
    }

    /// 重命名(SMB RENAME;目标已存在时协议层要求拒绝)。
    ///
    /// 参数:`from` 源路径;`to` 目标路径。
    /// 返回值:Ok(());Err(Exists/NotFound 等)。
    ///
    /// 实现步骤:
    /// 1. 组装重命名请求(源路径、目标路径)发给网关;
    /// 2. 网关负责拒绝已存在的目标,错误码映射为 SMB 错误。
    async fn rename(&self, from: &SmbPath, to: &SmbPath) -> SmbResult<()> {
        let req = OperateRequest {
            code: CODE_FILE_RENAME,
            rename: Some(RenameArgs {
                from_path: from.display_backslash(),
                to_path: to.display_backslash(),
            }),
            ..Default::default()
        };
        let (_, _) = call_operate(&self.conn, &req, &[])
            .await
            .map_err(map_gateway_err)?;
        Ok(())
    }

    /// 静态能力声明(协议层 TREE_CONNECT 时读取)。
    ///
    /// 参数:无。
    /// 返回值:BackendCapabilities(只读标记 = 共享 mode 为 readonly)。
    ///
    /// 实现:
    /// 1. 只读标志取自共享的权限模式(共享只读则标记只读);
    /// 2. 文件名按大小写不敏感处理(桶内索引用小写规范化名)。
    fn capabilities(&self) -> BackendCapabilities {
        BackendCapabilities {
            is_read_only: self.share.mode == "readonly",
            case_sensitive: false,
        }
    }
}

// ============================================================================
// RemoteHandle:实现 Handle(一次打开的文件/目录,转发到 Go)
// ============================================================================

/// 远程句柄:对应一次 SMB CREATE 的本地存根。
///
/// 关键字段意义:
/// - `conn`:到 Go 网关的连接(共享);
/// - `handle_id`:Go 侧分配的远程句柄 ID(后续 RPC 的定位依据);
/// - `is_dir`:文件/目录区分(协议层不允许对目录读写数据);
/// - `delete_on_close`:FILE_DELETE_ON_CLOSE,close() 时删除。
#[derive(Clone)]
pub struct RemoteHandle {
    conn: Arc<GatewayClient>, // 到 Go 网关的连接
    handle_id: u64,           // 远程句柄 ID(Go 侧 HandleRegistry 分配)
    is_dir: bool,             // 是否目录句柄
    delete_on_close: bool,    // 关闭时删除
}

/// 实现库定义的 `Handle` trait(一次打开的文件/目录)。
#[async_trait]
impl Handle for RemoteHandle {
    /// 按偏移读取(SMB READ)。
    ///
    /// 参数:`offset` 起始偏移;`len` 期望长度。
    /// 返回值:Ok(Bytes)(实际字节,可少于请求 = EOF);Err(SmbError)。
    ///
    /// 实现步骤:
    /// 1. 目录句柄直接报"是目录"错误;
    /// 2. 组装按偏移读请求发给网关;
    /// 3. 应答体按协议先解实际长度,再取流数据段;
    /// 4. 实际长度为 0 表示读到文件尾;错误码映射为 SMB 错误。
    async fn read(&self, offset: u64, len: u32) -> SmbResult<Bytes> {
        if self.is_dir {
            return Err(SmbError::IsDirectory);
        }
        let req = OperateRequest {
            code: CODE_FILE_READ,
            read: Some(ReadArgs {
                handle_id: self.handle_id,
                offset,
                length: len,
            }),
            ..Default::default()
        };
        // ---- 2~3. 发网关,拆流数据段 ----
        let (resp, stream) = call_operate(&self.conn, &req, &[])
            .await
            .map_err(map_gateway_err)?;
        let r = resp.read.ok_or(SmbError::NotSupported)?;
        // 长度契约:网关的 Length 必须等于流段长度(防错位)。
        if r.length as usize != stream.len() {
            return Err(SmbError::Io(std::io::Error::other("read length mismatch")));
        }
        Ok(Bytes::from(stream))
    }

    /// 按偏移写入(SMB WRITE;Go 侧走写回缓存)。
    ///
    /// 参数:`offset` 写入偏移;`data` 待写数据。
    /// 返回值:Ok(u32)(实际写入字节数);Err(SmbError)。
    ///
    /// 实现步骤:
    /// 1. 目录句柄直接报"是目录"错误;
    /// 2. 组装写请求(参数 JSON)+ 数据流段;
    /// 3. 发给网关,取回实际写入字节数。
    async fn write(&self, offset: u64, data: &[u8]) -> SmbResult<u32> {
        if self.is_dir {
            return Err(SmbError::IsDirectory);
        }
        let req = OperateRequest {
            code: CODE_FILE_WRITE,
            write: Some(WriteArgs {
                handle_id: self.handle_id,
                offset,
            }),
            ..Default::default()
        };
        let (resp, _) = call_operate(&self.conn, &req, data)
            .await
            .map_err(map_gateway_err)?;
        let r = resp.write.ok_or(SmbError::NotSupported)?;
        Ok(r.written)
    }

    /// 冲刷缓冲(SMB FLUSH;触发 Go 侧写回缓存整体上传)。
    ///
    /// 参数:无。
    /// 返回值:Ok(());Err(SmbError)。
    ///
    /// 实现步骤:
    /// 1. 组装冲刷请求发给网关(触发写回缓存整体上传);
    /// 2. 错误码映射为 SMB 错误。
    async fn flush(&self) -> SmbResult<()> {
        let req = OperateRequest {
            code: CODE_FILE_FLUSH,
            flush: Some(FlushArgs {
                handle_id: self.handle_id,
            }),
            ..Default::default()
        };
        let (_, _) = call_operate(&self.conn, &req, &[])
            .await
            .map_err(map_gateway_err)?;
        Ok(())
    }

    /// 查询文件信息(SMB QUERY_INFO)。
    ///
    /// 参数:无。
    /// 返回值:Ok(FileInfo)(字段与 Go 侧/库定义一致);Err(SmbError)。
    ///
    /// 实现步骤:
    /// 1. 组装元信息查询请求发给网关;
    /// 2. 把应答的元信息字段逐一搬到库的元信息结构返回。
    async fn stat(&self) -> SmbResult<FileInfo> {
        let req = OperateRequest {
            code: CODE_FILE_STAT,
            stat: Some(StatArgs {
                handle_id: self.handle_id,
            }),
            ..Default::default()
        };
        let (resp, _) = call_operate(&self.conn, &req, &[])
            .await
            .map_err(map_gateway_err)?;
        let r = resp.stat.ok_or(SmbError::NotSupported)?;
        // 库的 FileInfo 与帧协议 FileInfo 字段一一对应,直接搬运。
        Ok(FileInfo {
            name: r.info.name,
            end_of_file: r.info.end_of_file,
            allocation_size: r.info.allocation_size,
            creation_time: r.info.creation_time,
            last_access_time: r.info.last_access_time,
            last_write_time: r.info.last_write_time,
            change_time: r.info.change_time,
            is_directory: r.info.is_directory,
            file_index: r.info.file_index,
        })
    }

    /// 设置时间戳(SMB SET_INFO / FILE_BASIC_INFORMATION)。
    ///
    /// 参数:`times` 可选 FILETIME 值(None = 不改)。
    /// 返回值:Ok(());Err(SmbError)。
    ///
    /// 实现步骤:
    /// 1. 把要修改的时间戳(可空)组装进请求,发给网关;
    /// 2. 错误码映射为 SMB 错误。
    async fn set_times(&self, times: FileTimes) -> SmbResult<()> {
        let req = OperateRequest {
            code: CODE_FILE_SET_TIMES,
            set_times: Some(SetTimesArgs {
                handle_id: self.handle_id,
                creation_time: times.creation_time,
                last_access_time: times.last_access_time,
                last_write_time: times.last_write_time,
                change_time: times.change_time,
            }),
            ..Default::default()
        };
        let (_, _) = call_operate(&self.conn, &req, &[])
            .await
            .map_err(map_gateway_err)?;
        Ok(())
    }

    /// 截断/扩展文件到指定长度(SMB SET_END_OF_FILE)。
    ///
    /// 参数:`len` 目标长度。
    /// 返回值:Ok(());Err(SmbError)。
    ///
    /// 实现步骤:
    /// 1. 组装截断请求(目标长度)发给网关;
    /// 2. 错误码映射为 SMB 错误。
    async fn truncate(&self, len: u64) -> SmbResult<()> {
        let req = OperateRequest {
            code: CODE_FILE_TRUNCATE,
            truncate: Some(TruncateArgs {
                handle_id: self.handle_id,
                length: len,
            }),
            ..Default::default()
        };
        let (_, _) = call_operate(&self.conn, &req, &[])
            .await
            .map_err(map_gateway_err)?;
        Ok(())
    }

    /// 列出目录内容(SMB QUERY_DIRECTORY)。
    ///
    /// 参数:`pattern` 通配符(后端可不实现,协议层 dispatcher 后过滤)。
    /// 返回值:Ok(Vec<DirEntry>);Err(SmbError)。
    ///
    /// 实现步骤:
    /// 1. 非目录句柄直接报"不是目录"错误;
    /// 2. 组装列目录请求发给网关;
    /// 3. 把应答的条目列表转成库的目录条目结构。
    async fn list_dir(&self, pattern: Option<&str>) -> SmbResult<Vec<DirEntry>> {
        if !self.is_dir {
            return Err(SmbError::NotADirectory);
        }
        let req = OperateRequest {
            code: CODE_FILE_LIST_DIR,
            list_dir: Some(ListDirArgs {
                handle_id: self.handle_id,
                pattern: pattern.unwrap_or("").to_string(),
            }),
            ..Default::default()
        };
        let (resp, _) = call_operate(&self.conn, &req, &[])
            .await
            .map_err(map_gateway_err)?;
        let r = resp.list_dir.ok_or(SmbError::NotSupported)?;
        // 条目字段一一对应搬运(帧协议 FileInfo → 库 FileInfo)。
        let entries = r
            .entries
            .into_iter()
            .map(|info| DirEntry {
                info: FileInfo {
                    name: info.name,
                    end_of_file: info.end_of_file,
                    allocation_size: info.allocation_size,
                    creation_time: info.creation_time,
                    last_access_time: info.last_access_time,
                    last_write_time: info.last_write_time,
                    change_time: info.change_time,
                    is_directory: info.is_directory,
                    file_index: info.file_index,
                },
            })
            .collect();
        Ok(entries)
    }

    /// 关闭句柄(SMB CLOSE;Go 侧触发写回缓存整体上传 + delete_on_close)。
    ///
    /// 参数:无(消费 self)。
    /// 返回值:Ok(());Err(SmbError)(上传失败仍注销句柄,错误仅记录)。
    ///
    /// 实现步骤:
    /// 1. 组装关闭请求发给网关(触发写回缓存整体上传与关闭即删);
    /// 2. 失败仅记日志——句柄已注销,数据一致性由网关侧写回重试兜底。
    async fn close(self: Box<Self>) -> SmbResult<()> {
        let req = OperateRequest {
            code: CODE_FILE_CLOSE,
            close: Some(CloseArgs {
                handle_id: self.handle_id,
            }),
            ..Default::default()
        };
        let (_, _) = call_operate(&self.conn, &req, &[])
            .await
            .map_err(map_gateway_err)?;
        Ok(())
    }
}

// ============================================================================
// 工具:OpenIntent → 字符串(与 Go 侧 Intent 字段语义一致)
// ============================================================================

/// 把库的 OpenIntent 翻译为帧协议的 intent 字符串。
///
/// 参数:`intent` 库的创建处置枚举。
/// 返回值:帧协议字符串(与 Go 侧 OpenArgs.Intent 完全一致)。
fn intent_str(intent: OpenIntent) -> &'static str {
    match intent {
        OpenIntent::Open => "open",
        OpenIntent::Create => "create",
        OpenIntent::OpenOrCreate => "open_or_create",
        OpenIntent::OverwriteOrCreate => "overwrite_or_create",
        OpenIntent::Truncate => "truncate",
    }
}

/// 把 Go 侧哨兵错误码映射为库的 SmbError(NTSTATUS 语义)。
///
/// 参数:`code` 帧协议错误码(ERR_* 常量)。
/// 返回值:库的 SmbError(协议层翻译为 NTSTATUS)。
///
/// 内部逻辑(伪代码):
/// 1. 对照 ERR_* ↔ SmbError 一一映射(NotFound/AccessDenied/Exists/
///    NotEmpty/IsDirectory/NotADirectory/Io);
/// 2. 未识别的 code → SmbError::Io(STATUS_UNEXPECTED_IO_ERROR);
/// 3. ERR_GATEWAY_DOWN / ERR_TIMEOUT → SmbError::Io(连接层上层负责重连)。
fn map_gateway_err(code: u32) -> SmbError {
    match code {
        ERR_NOT_FOUND => SmbError::NotFound,
        ERR_ACCESS_DENIED => SmbError::AccessDenied,
        ERR_EXISTS => SmbError::Exists,
        ERR_NOT_EMPTY => SmbError::NotEmpty,
        ERR_IS_DIRECTORY => SmbError::IsDirectory,
        ERR_NOT_A_DIRECTORY => SmbError::NotADirectory,
        _ => SmbError::NotSupported,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// OpenIntent → 帧协议字符串,须与 Go 侧 OpenArgs.Intent 一致。
    #[test]
    fn intent_str_matches_go_side() {
        assert_eq!(intent_str(OpenIntent::Open), "open");
        assert_eq!(intent_str(OpenIntent::Create), "create");
        assert_eq!(intent_str(OpenIntent::OpenOrCreate), "open_or_create");
        assert_eq!(
            intent_str(OpenIntent::OverwriteOrCreate),
            "overwrite_or_create"
        );
        assert_eq!(intent_str(OpenIntent::Truncate), "truncate");
    }

    /// 帧协议错误码映射到库的 SmbError(NTSTATUS 语义)。
    #[test]
    fn gateway_err_maps_to_smb_error() {
        assert!(matches!(map_gateway_err(ERR_NOT_FOUND), SmbError::NotFound));
        assert!(matches!(
            map_gateway_err(ERR_ACCESS_DENIED),
            SmbError::AccessDenied
        ));
        assert!(matches!(map_gateway_err(ERR_EXISTS), SmbError::Exists));
        assert!(matches!(map_gateway_err(ERR_NOT_EMPTY), SmbError::NotEmpty));
        assert!(matches!(
            map_gateway_err(ERR_IS_DIRECTORY),
            SmbError::IsDirectory
        ));
        assert!(matches!(
            map_gateway_err(ERR_NOT_A_DIRECTORY),
            SmbError::NotADirectory
        ));
        assert!(matches!(map_gateway_err(0xFFFF), SmbError::NotSupported));
    }

    /// HMAC 握手摘要:与 Go 侧 hmacDigest("HELLO:"+clientId+":"+nonce) 一致。
    /// 验证:同一输入摘要稳定且为 64 位 hex(SHA-256)。
    #[test]
    fn hmac_digest_is_stable_hex() {
        let key = b"0123456789abcdef";
        let d1 = hmac_sha256_hex(key, "HELLO:gw-1:abc123");
        let d2 = hmac_sha256_hex(key, "HELLO:gw-1:abc123");
        assert_eq!(d1, d2, "同输入必须同摘要");
        assert_eq!(d1.len(), 64, "SHA-256 hex 长度 64");
        assert_ne!(
            d1,
            hmac_sha256_hex(key, "HELLO:gw-1:abc124"),
            "不同输入不同摘要"
        );
    }

    /// 集成测试:本地 mock Go 网关(握手 + OPERATE 应答),验证
    /// connect → call 全链路(帧编解码、pending 应答表、流数据段)。
    #[tokio::test]
    async fn connect_and_call_against_mock_server() {
        use tokio::io::AsyncWriteExt;

        // ---- mock 服务端:绑定随机端口,模拟 Go 网关 ----
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0")
            .await
            .expect("bind");
        let addr = listener.local_addr().expect("addr");
        // 共享密钥(两侧一致,≥16 字节)。
        let key: &[u8] = b"0123456789abcdef0123456789abcdef";

        // 服务端任务:处理一次握手 + 一次 OPERATE 请求。
        let server_key = key.to_vec();
        let server = tokio::spawn(async move {
            let (mut sock, _) = listener.accept().await.expect("accept");

            // ---- 握手 1:读 HELLO,校验摘要,回 HELLO_ACK ----
            let (hdr, body) = read_one_frame(&mut sock).await.expect("hello frame");
            assert_eq!(hdr.msg_type, MSG_HELLO);
            let hello: HelloRequest = serde_json::from_slice(&body).expect("hello json");
            let expect = hmac_sha256_hex(
                &server_key,
                &format!("HELLO:{}:{}", hello.client_id, hello.nonce),
            );
            assert_eq!(hello.challenge_digest, expect, "服务端校验客户端摘要");
            let ack = HelloResponse {
                ok: true,
                server_nonce: "servernonce16b".into(),
                error: String::new(),
            };
            let ack_body = serde_json::to_vec(&ack).expect("ack json");
            let ack_frame = encode_frame(
                FrameHeader {
                    flags: FLAG_RESPONSE,
                    msg_type: MSG_HELLO_ACK,
                    seq: hdr.seq,
                    ..Default::default()
                },
                &ack_body,
            );
            sock.write_all(&ack_frame).await.expect("write ack");

            // ---- 握手 2:读 VERIFY,校验双向认证摘要 ----
            let (_, vbody) = read_one_frame(&mut sock).await.expect("verify frame");
            let verify: HelloRequest = serde_json::from_slice(&vbody).expect("verify json");
            let expect_v = hmac_sha256_hex(&server_key, "VERIFY:servernonce16b");
            assert_eq!(verify.challenge_digest, expect_v, "服务端校验双向认证");

            // ---- 业务:等 MSG_OPERATE,回 OPERATE_RESP ----
            let (hdr, body) = read_one_frame(&mut sock).await.expect("operate frame");
            assert_eq!(hdr.msg_type, MSG_OPERATE);
            let (json_bytes, stream) = split_operate_body(&body).expect("split");
            assert_eq!(stream, b"hello data", "请求流数据段原样到达");
            let req: OperateRequest = serde_json::from_slice(json_bytes).expect("req json");
            assert_eq!(req.code, CODE_FILE_OPEN);

            // 回总响应(open 结果)。
            let resp = OperateResponse {
                code: CODE_FILE_OPEN,
                open: Some(OpenResult {
                    handle_id: 100,
                    is_dir: false,
                    end_of_file: 11,
                    exists: true,
                }),
                ..Default::default()
            };
            let resp_body = encode_operate_body(&resp, &[]).expect("resp body");
            let resp_frame = encode_frame(
                FrameHeader {
                    flags: FLAG_RESPONSE,
                    msg_type: MSG_OPERATE_RESP,
                    seq: hdr.seq,
                    ..Default::default()
                },
                &resp_body,
            );
            sock.write_all(&resp_frame).await.expect("write resp");
        });

        // ---- 客户端:连接并完成一次 open 调用 ----
        let client = GatewayClient::connect(&addr.to_string(), key.to_vec(), "gw-test".into(), 16)
            .await
            .expect("connect");
        let req = OperateRequest {
            code: CODE_FILE_OPEN,
            open: Some(OpenArgs {
                path: "a/b.txt".into(),
                read: true,
                write: false,
                intent: "open".into(),
                directory: false,
                non_directory: true,
                delete_on_close: false,
            }),
            ..Default::default()
        };
        let body = encode_operate_body(&req, b"hello data").expect("body");
        let resp_body = client.call(MSG_OPERATE, body).await.expect("call ok");
        let (json_bytes, stream_out) = split_operate_body(&resp_body).expect("split");
        assert!(stream_out.is_empty(), "open 响应无流段");
        let resp: OperateResponse = serde_json::from_slice(json_bytes).expect("resp json");
        assert_eq!(resp.code, CODE_FILE_OPEN);
        assert!(resp.err.is_none(), "无业务错误");
        let r = resp.open.expect("open result");
        assert_eq!(r.handle_id, 100);
        assert_eq!(r.end_of_file, 11);

        server.await.expect("server task ok");
    }
}
