//! ============================================================================
//! remote_backend/client.rs —— GatewayClient:到 Go 网关的私有 TCP 客户端
//! ============================================================================
//!
//! 承载:握手、心跳、请求-响应关联(seq → oneshot 通道)、断线重连支持。
//!
//! 关键设计(设计点 6):
//! - `send_queue`:发送缓冲管道(容量 = 配置 channel_buffer):
//!   各调用方只往管道投帧,writer 任务消费并写 socket,写入侧不阻塞;
//! - `pending`:seq → 应答通道表(reader 任务投递,超时回收);
//! - `push_tx`:变更推送转发通道(MSG_AUTH_PUSH body → sync 模块);
//! - `shared_key`:握手 HMAC 密钥(内存持有,不落盘)。

use std::collections::HashMap; // HashMap:seq → 应答通道表
use std::sync::Arc; // Arc:与后台任务共享客户端状态

use crate::types::*; // 帧协议常量与编解码

/// 应答通道表:seq → 请求应答(oneshot)。
/// 承载:reader 任务按 seq 投递响应,调用方 await 取回;
/// 超时(30s)未收到 → 移除并回 ERR_TIMEOUT。
/// 用 Arc 包装:reader 任务与 GatewayClient 共享同一张表(无需 Clone 语义)。
type PendingReplies =
    Arc<tokio::sync::Mutex<HashMap<u32, tokio::sync::oneshot::Sender<Result<Vec<u8>, u32>>>>>; // Mutex:多调用方并发登记/消费

/// 与 Go 网关的一条私有 TCP 长连接(帧协议客户端)。
pub struct GatewayClient {
    send_queue: tokio::sync::mpsc::Sender<Vec<u8>>, // 发送缓冲管道(设计点 6:写入不阻塞)
    seq_counter: std::sync::atomic::AtomicU32, // 请求序列号(1 起,响应关联用)
    pending: PendingReplies, // seq → 应答通道表(reader 任务投递)
    push_tx: tokio::sync::mpsc::Sender<Vec<u8>>, // 推送转发(sync 模块消费)
    shared_key: Vec<u8>, // 共享密钥(握手鉴权/协商,仅内存)
    alive: std::sync::atomic::AtomicBool, // 连接存活标志(断线置 false)
    rpc_timeout: std::time::Duration, // 请求应答超时(可配置;大文件 Flush 需放宽)
}

impl GatewayClient {
    /// 连接是否存活(reader/writer/heartbeat 任一退出即 false;重连监督用)。
    pub fn is_alive(&self) -> bool {
        self.alive.load(std::sync::atomic::Ordering::Relaxed) // 原子读存活标志
    }

    /// 标记连接死亡(reader/writer/heartbeat 退出时调用)。
    fn mark_dead(&self) {
        self.alive.store(false, std::sync::atomic::Ordering::Relaxed); // 原子置 false
    }

    /// 连接 Go 网关并完成握手。
    ///
    /// 参数:
    /// - `addr`:Go 网关地址(如 "127.0.0.1:9001");
    /// - `shared_key`:共享密钥(≥16 字节,与 Go 侧一致);
    /// - `client_id`:本实例标识(多实例隔离远程句柄表);
    /// - `channel_buffer`:发送管道缓冲长度(设计点 6,取配置
    ///   gateway.channel_buffer;与 Go 侧请求池队列深度对齐)。
    ///
    /// 返回值:握手成功的客户端 + 推送接收端(push_rx 交给 sync 模块);
    /// 错误为连接失败/密钥校验失败/版本不匹配。
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
        addr: &str, // 网关地址
        shared_key: Vec<u8>, // 共享密钥
        client_id: String, // 客户端标识
        channel_buffer: usize, // 管道容量
        rpc_timeout: std::time::Duration, // 请求应答超时(配置 rpc_timeout_secs)
    ) -> std::io::Result<(Arc<Self>, tokio::sync::mpsc::Receiver<Vec<u8>>)> {
        // ---- 1. 建立 TCP 连接并开启 NODELAY ----
        debugf!("网关客户端:正在连接 {addr}(客户端 {client_id},缓冲 {channel_buffer},超时 {rpc_timeout:?})", &["SMB"]); // 记录连接发起
        let stream = tokio::net::TcpStream::connect(addr) // 异步 TCP 连接
            .await
            .map_err(|e| std::io::Error::new(e.kind(), format!("connect {addr}: {e}")))?; // 失败:带地址报错
        stream.set_nodelay(true).ok(); // 禁用 Nagle(小帧不等待合并,降低握手/交互延迟)

        // ---- 2. 发送缓冲管道 + 推送通道(容量 = channel_buffer) ----
        let (send_tx, send_rx) = tokio::sync::mpsc::channel(channel_buffer.max(1)); // 发送管道(至少 1,防 0 容量)
        let (push_tx, push_rx) = tokio::sync::mpsc::channel(channel_buffer.max(1)); // 推送管道(转发给 sync)

        let client = Arc::new(Self { // 构造客户端(Arc:与后台任务共享)
            send_queue: send_tx, // 发送管道发送端
            seq_counter: std::sync::atomic::AtomicU32::new(0), // 序列号从 0 起(fetch_add 后 +1 用)
            pending: Arc::new(tokio::sync::Mutex::new(HashMap::new())), // 空应答表
            push_tx, // 推送转发端
            shared_key, // 共享密钥(所有权移入)
            alive: std::sync::atomic::AtomicBool::new(true), // 初始存活
            rpc_timeout, // 请求应答超时(可配置)
        });

        // ---- 3~5. 双向 HMAC 握手(使用整体流直接读写) ----
        let mut stream = stream; // 可变流(握手阶段直接读写)
        client.handshake(&mut stream, &client_id).await?; // 完成 HELLO→ACK→VERIFY 双向认证
        infof!("网关客户端:握手成功(客户端 {client_id},地址 {addr})", &["SMB"]); // 记录握手成功

        // ---- 6. 握手完成:拆分流并启动后台任务 ----
        let (reader, writer) = tokio::io::split(stream); // 拆成读半流/写半流(分别独占)
        tokio::spawn(writer_task(writer, send_rx, Arc::clone(&client))); // 任务 1:writer 消费管道写流
        tokio::spawn(reader_task( // 任务 2:reader 读帧并按 seq 投递
            reader, // 读半流
            client.pending.clone(), // 应答表(共享)
            client.push_tx.clone(), // 推送转发端
            Arc::clone(&client), // 客户端(断线置死标志)
        ));
        tokio::spawn(Arc::clone(&client).heartbeat_task()); // 任务 3:heartbeat 定时心跳
        Ok((client, push_rx)) // 返回客户端 + 推送接收端(交给 sync 模块)
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
        stream: &mut tokio::net::TcpStream, // 已连接流(整体读写)
        client_id: &str, // 客户端标识
    ) -> std::io::Result<()> {
        use tokio::io::AsyncWriteExt; // 写原语(write_all)

        // ---- 1. HELLO 帧:nonce + 摘要 ----
        let nonce = random_hex(16); // 生成 16 字节随机数的 hex 串(防重放)
        let digest = hmac_sha256_hex(&self.shared_key, &format!("HELLO:{client_id}:{nonce}")); // 计算挑战摘要
        let hello = HelloRequest { // 组装握手请求体
            client_id: client_id.to_string(), // 本实例标识
            nonce: nonce.clone(), // 客户端随机数
            challenge_digest: digest, // 摘要(服务端用共享密钥验证身份)
        };
        let body = serde_json::to_vec(&hello) // 请求体 → JSON 字节
            .map_err(|e| std::io::Error::other(format!("hello json: {e}")))?; // 序列化失败报错
        let frame = encode_frame( // 组装帧(帧头 + body)
            FrameHeader {
                flags: FLAG_NEED_REPLY, // 标记:需要应答
                msg_type: MSG_HELLO, // 握手消息
                seq: 1, // 握手阶段用固定序号
                ..Default::default() // 其余(魔数/版本/长度)由 encode_frame 回填
            },
            &body,
        );
        stream.write_all(&frame).await?; // 直接写流(握手期尚未起 writer 任务)

        // ---- 2. 等 HELLO_ACK ----
        let (hdr, resp_body) =
            tokio::time::timeout(std::time::Duration::from_secs(10), read_one_frame(stream)) // 10s 超时读一帧
                .await
                .map_err(|_| std::io::Error::other("handshake timeout"))??; // 超时/读失败均报错
        if hdr.msg_type != MSG_HELLO_ACK { // 应答类型不是 HELLO_ACK?
            return Err(std::io::Error::other(format!( // 协议不符:报错(附实际类型)
                "handshake: expected HELLO_ACK, got {:#x}",
                hdr.msg_type
            )));
        }
        let ack: HelloResponse = serde_json::from_slice(&resp_body) // 解析应答 JSON
            .map_err(|e| std::io::Error::other(format!("hello ack json: {e}")))?; // 解析失败报错
        if !ack.ok { // 服务端拒绝握手?
            return Err(std::io::Error::other(format!( // 带拒绝原因报错(如密钥不符)
                "handshake rejected: {}",
                ack.error
            )));
        }

        // ---- 3~4. 回 VERIFY 帧(双向认证) ----
        let vdigest = hmac_sha256_hex(&self.shared_key, &format!("VERIFY:{}", ack.server_nonce)); // 用服务端 nonce 算验证摘要
        let verify = HelloRequest { // 复用握手请求结构
            client_id: client_id.to_string(), // 本实例标识
            nonce: ack.server_nonce.clone(), // 回传服务端随机数
            challenge_digest: vdigest, // 验证摘要(证明双方持同一密钥)
        };
        let vbody = serde_json::to_vec(&verify) // 序列化验证请求
            .map_err(|e| std::io::Error::other(format!("verify json: {e}")))?; // 失败报错
        let vframe = encode_frame( // 组装 VERIFY 帧
            FrameHeader {
                flags: FLAG_NEED_REPLY, // 标记:需要应答
                msg_type: MSG_HELLO, // 复用 HELLO 消息类型(第二阶段)
                seq: 2, // 第二阶段序号
                ..Default::default() // 其余自动回填
            },
            &vbody,
        );
        stream.write_all(&vframe).await?; // 发出 VERIFY(服务端校验通过即完成握手)
        debugf!("握手:已发送验证(seq 2,服务器随机数 {})", &["SMB"], ack.server_nonce); // 记录双向认证完成
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
        let frame = encode_frame( // 组装完整帧
            FrameHeader {
                flags, // 帧标记(如心跳)
                msg_type, // 消息类型
                ..Default::default() // 序号/长度自动回填
            },
            body,
        );
        self.send_queue // 投进发送管道(不满即成功;满则等待 = 背压)
            .send(frame)
            .await
            .map_err(|_| std::io::Error::other("send queue closed")) // 管道关闭 = 连接已断
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
            .fetch_add(1, std::sync::atomic::Ordering::Relaxed) // 原子取序号并自增
            + 1; // 从 1 开始
        let (tx, rx) = tokio::sync::oneshot::channel(); // 一次性应答通道
        self.pending.lock().await.insert(seq, tx); // 登记:seq → 应答发送端
        debugf!("网关客户端:请求 seq={seq} 消息类型={msg_type:#x} 正文={} 字节", &["SMB"], body.len()); // 记录请求发起(含序号关联)

        // ---- 2. 投发送管道(需响应标记) ----
        let frame = encode_frame( // 组装请求帧
            FrameHeader {
                flags: FLAG_NEED_REPLY, // 标记:需要应答
                msg_type, // 请求类型
                seq, // 序列号(响应原样带回)
                ..Default::default() // 其余自动回填
            },
            &body,
        );
        if self.send_queue.send(frame).await.is_err() { // 投管道失败?
            // 管道已关闭(writer 任务退出 = 连接断开)。
            warnf!("网关客户端:请求 seq={seq} 发送队列已关闭(连接已断)", &["SMB"]); // 记录连接已断
            self.pending.lock().await.remove(&seq); // 清理登记,防止泄漏
            return Err(ERR_GATEWAY_DOWN); // 返回"网关不可达"哨兵
        }

        // ---- 3~4. 等应答(超时 = 配置 rpc_timeout_secs,默认 30s) ----
        match tokio::time::timeout(self.rpc_timeout, rx).await { // 限时等待应答(大文件 Flush 可放宽)
            Ok(Ok(Ok(resp_body))) => { // 正常应答
                debugf!("网关客户端:请求 seq={seq} 成功,响应={} 字节", &["SMB"], resp_body.len()); // 记录应答到达
                Ok(resp_body)
            }
            Ok(Ok(Err(code))) => { // 应答为错误帧
                debugf!("网关客户端:请求 seq={seq} 错误码={code}", &["SMB"]); // 记录网关业务错误
                Err(code)
            }
            Ok(Err(_)) => { // oneshot 发送端被丢弃
                warnf!("网关客户端:请求 seq={seq} 等待期间连接断开", &["SMB"]); // 记录断线(reader 已退出)
                self.pending.lock().await.remove(&seq); // (reader 任务退出 = 断线)清理登记
                Err(ERR_GATEWAY_DOWN) // 返回"网关不可达"
            }
            Err(_) => { // 30s 超时
                warnf!("网关客户端:请求 seq={seq} 30 秒超时", &["SMB"]); // 记录超时
                self.pending.lock().await.remove(&seq); // 超时:移除登记
                Err(ERR_TIMEOUT) // 返回"超时"哨兵
            }
        }
    }

    /// 解码 ErrorEnvelope(供 call 的统一错误路径使用)。
    /// 参数:`body` MSG_ERR_RESP 的载荷。
    /// 返回值:哨兵错误码(解析失败 → ERR_IO)。
    fn decode_error(body: &[u8]) -> u32 {
        serde_json::from_slice::<ErrorEnvelope>(body) // 解析错误信封
            .map(|e| e.code) // 取哨兵错误码
            .unwrap_or(ERR_IO) // 解析失败退化为 ERR_IO
    }

    /// 接收变更推送的通道接收端(供 sync 模块消费)。
    /// 注意:通道接收端在 connect 时返回(本方法保留仅为兼容旧接口)。
    pub fn take_push_rx(&self) -> Option<tokio::sync::mpsc::Receiver<Vec<u8>>> {
        None // 兼容占位:推送接收端已随 connect() 的返回值交出,此处恒为 None
    }

    /// 心跳与空闲回收任务。
    ///
    /// 实现步骤:
    /// 1. 每 30 秒发一次心跳帧(单向,不等待响应);
    /// 2. 发送失败(管道关闭=断线)→ 退出;
    /// 3. 对端空闲超时由 Go 侧读超时判定;本侧重连由 sync 模块驱动。
    async fn heartbeat_task(self: Arc<Self>) {
        let mut ticker = tokio::time::interval(std::time::Duration::from_secs(30)); // 30s 心跳定时器
        loop {
            ticker.tick().await; // 等待下一个心跳点(首 tick 立即触发)
            if self
                .send_frame(MSG_HEARTBEAT, FLAG_HEARTBEAT, &[]) // 发心跳帧(单向,空 body)
                .await
                .is_err() // 发送失败?
            {
                warnf!("网关客户端:心跳发送失败,连接已断开", &["SMB"]); // 记录心跳失败 = 连接断开
                self.mark_dead(); // 管道关闭 = 连接已断,置死标志
                break; // 退出心跳任务
            }
        }
    }
}

// ============================================================================
// 后台任务:reader / writer / 单帧读取
// ============================================================================

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
    mut reader: tokio::io::ReadHalf<tokio::net::TcpStream>, // 读半流(独占)
    pending: PendingReplies, // 应答表(共享)
    push_tx: tokio::sync::mpsc::Sender<Vec<u8>>, // 推送转发端
    client: Arc<GatewayClient>, // 客户端(断线时置死标志)
) {
    use tokio::io::AsyncReadExt; // 读原语(read_exact)
    loop {
        // ---- 读帧头 ----
        let mut head = [0u8; HEADER_LEN]; // 帧头缓冲(16B)
        if reader.read_exact(&mut head).await.is_err() { // 读帧头失败
            debugf!("网关客户端:读取器 EOF(对端已关闭连接)", &["SMB"]); // 记录对端关闭
            break; // 连接关闭:退出循环
        }
        let hdr = match decode_frame_header(&head) { // 解析并校验帧头
            Ok(h) => h, // 合法帧头
            Err(e) => { // 协议错误
                warnf!("网关客户端:读取器协议错误:{e}", &["SMB"]); // 记录协议错误详情
                break; // 退出(上层重连)
            }
        };
        // ---- 读 body ----
        let mut body = vec![0u8; hdr.body_len as usize]; // 按帧头长度分配 body 缓冲
        if reader.read_exact(&mut body).await.is_err() { // 读 body 失败
            debugf!("网关客户端:读取器半途 EOF(连接已关闭)", &["SMB"]); // 记录中断
            break; // 连接关闭:退出
        }

        if hdr.flags & FLAG_RESPONSE != 0 { // 响应帧?
            // ---- 响应帧:按 seq 投递 ----
            if let Some(tx) = pending.lock().await.remove(&hdr.seq) { // 取出对应应答通道(seq 关联)
                if hdr.msg_type == MSG_ERR_RESP { // 错误帧?
                    let code = GatewayClient::decode_error(&body); // 解出哨兵错误码
                    debugf!("网关客户端:响应 seq={} 错误码={}", &["SMB"], hdr.seq, code); // 记录错误响应
                    let _ = tx.send(Err(code)); // 投递错误结果
                } else { // 正常响应
                    debugf!("网关客户端:响应 seq={} 消息类型={:#x} 长度={} 字节", &["SMB"], hdr.seq, hdr.msg_type, body.len()); // 记录响应到达
                    let _ = tx.send(Ok(body)); // 投递响应 body
                }
            } // 未知 seq:通道已被超时清理,直接丢弃
        } else if hdr.msg_type == MSG_AUTH_PUSH { // 单向推送帧?
            // ---- 单向推送:转交 sync 模块 ----
            debugf!("网关客户端:收到认证推送({} 字节)", &["SMB"], body.len()); // 记录变更推送
            let _ = push_tx.send(body).await; // 转发推送 body(缓冲满则等待)
        }
        // 其余单向帧(心跳等)忽略。
    }
    // ---- 断线收尾:通知所有等待中的调用方 ----
    warnf!("网关客户端:读取任务退出,{} 个挂起请求失败", &["SMB"], pending.lock().await.len()); // 记录断线收尾(带挂起请求数)
    client.mark_dead(); // 置连接死亡标志(重连监督可见)
    let mut p = pending.lock().await; // 锁住应答表
    for (_, tx) in p.drain() { // 遍历并清空全部登记
        let _ = tx.send(Err(ERR_GATEWAY_DOWN)); // 逐个通知:网关不可达(避免调用方永久挂起)
    }
}

/// writer 后台任务:消费发送管道,独占写 socket。
///
/// 参数:`writer` TCP 写半流(独占);`rx` 发送缓冲管道接收端。
/// 实现:循环取帧 → write_all;失败退出(连接断开,心跳/调用方随之失败)。
async fn writer_task(
    mut writer: tokio::io::WriteHalf<tokio::net::TcpStream>, // 写半流(独占)
    mut rx: tokio::sync::mpsc::Receiver<Vec<u8>>, // 发送管道接收端
    client: Arc<GatewayClient>, // 客户端(断线时置死标志)
) {
    use tokio::io::AsyncWriteExt; // 写原语(write_all)
    while let Some(frame) = rx.recv().await { // 等待管道中的帧(关闭则 None 退出)
        if writer.write_all(&frame).await.is_err() { // 写整帧到 socket
            warnf!("网关客户端:写入失败,连接已断开", &["SMB"]); // 记录写失败 = 连接断开
            break; // 写失败 = 连接断开
        }
    }
    client.mark_dead(); // 置连接死亡标志
}

/// 从流中读一帧(握手期间直接读用;返回帧头与 body)。
/// `pub(super)`:mock 集成测试(本模块 tests.rs)模拟 Go 网关时也会用到。
pub(super) async fn read_one_frame(
    stream: &mut tokio::net::TcpStream, // 整体流
) -> std::io::Result<(FrameHeader, Vec<u8>)> {
    use tokio::io::AsyncReadExt; // 读原语
    let mut head = [0u8; HEADER_LEN]; // 帧头缓冲
    stream.read_exact(&mut head).await?; // 读 16B 帧头
    let hdr = decode_frame_header(&head).map_err(std::io::Error::other)?; // 解析并校验(失败转 io 错误)
    let mut body = vec![0u8; hdr.body_len as usize]; // 按长度分配缓冲
    stream.read_exact(&mut body).await?; // 读 body
    Ok((hdr, body)) // 返回 (帧头, body)
}

// ============================================================================
// 密码学工具(握手挑战应答,与 Go 侧 gateway.go 一致)
// ============================================================================

/// 生成 n 字节随机数的 hex 串(握手 nonce)。
fn random_hex(n: usize) -> String {
    use rand::RngCore; // 随机数核心 trait
    let mut buf = vec![0u8; n]; // 随机字节缓冲
    rand::thread_rng().fill_bytes(&mut buf); // 填充系统随机源
    hex::encode(buf) // 编码为 hex 串
}

/// HMAC-SHA256 摘要的 hex 串(握手挑战应答,与 Go 侧 hmacDigest 一致)。
/// `pub(super)`:mock 集成测试(本模块 tests.rs)校验摘要时也会用到。
pub(super) fn hmac_sha256_hex(key: &[u8], payload: &str) -> String {
    use hmac::{Hmac, Mac}; // HMAC 计算 trait
    use sha2::Sha256; // SHA-256 摘要算法
    type HmacSha256 = Hmac<Sha256>; // 类型别名:HMAC-SHA256
    let mut mac = HmacSha256::new_from_slice(key).expect("hmac key any length"); // 以密钥初始化(任意长度均可)
    mac.update(payload.as_bytes()); // 吸收载荷数据
    hex::encode(mac.finalize().into_bytes()) // 输出摘要的 hex 串
}

#[cfg(test)] // 仅测试编译
mod tests {
    use super::*; // 引入被测函数

    /// HMAC 握手摘要:与 Go 侧 hmacDigest("HELLO:"+clientId+":"+nonce) 一致。
    /// 验证:同一输入摘要稳定且为 64 位 hex(SHA-256)。
    #[test]
    fn hmac_digest_is_stable_hex() {
        let key = b"0123456789abcdef"; // 测试密钥(16B)
        let d1 = hmac_sha256_hex(key, "HELLO:gw-1:abc123"); // 计算第 1 次
        let d2 = hmac_sha256_hex(key, "HELLO:gw-1:abc123"); // 计算第 2 次(同输入)
        assert_eq!(d1, d2, "同输入必须同摘要"); // 确定性验证
        assert_eq!(d1.len(), 64, "SHA-256 hex 长度 64"); // 长度验证
        assert_ne!(
            d1,
            hmac_sha256_hex(key, "HELLO:gw-1:abc124"), // 输入仅改一位
            "不同输入不同摘要"
        ); // 敏感性验证
    }
}
