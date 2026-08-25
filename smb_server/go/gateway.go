// gateway.go —— 网关服务端:连接管理、心跳、鉴权与优雅停机。
//
// 职责(传输层与帧收发见 transport.go,操作转发见 handlers.go):
//   - 监听私有 TCP 端口,接受 Rust 网关的出站连接(仅允许配置的共享密钥连接);
//   - 连接生命周期:握手鉴权 → 注册 → 读循环 → 注销回收;
//   - 心跳保活与超时回收(空闲超时由 transport.readFrame 刷新判定);
//   - 优雅停机:先停 accept,再逐连接发 Shutdown 通知并等待收尾;
//   - 变更推送广播(MSG_AUTH_PUSH,单向)。
//
// 线程安全:各连接有独立读协程,通过 HandleRegistry(带锁)共享句柄表。
package smbgateway

import (
	"context"      // 上下文:停机/连接生命周期取消传播
	"crypto/hmac"  // HMAC 计算(握手摘要)
	"crypto/rand"  // 安全随机数(握手 nonce)
	"crypto/sha256" // SHA-256(握手摘要底层算法)
	"crypto/subtle" // 常量时间比较(防时序侧信道)
	"encoding/hex" // hex 编解码(摘要/nonce 字符串化)
	"encoding/json" // JSON 编解码(握手/推送载荷)
	"errors"       // 错误构造(握手失败)
	"fmt"          // 格式化错误/日志
	"orbitcloud/log"          // 服务日志
	"net"          // TCP 连接/监听
	"sync"         // 互斥锁(连接注册表/自增器保护)
	"time"         // 超时/心跳定时

	"orbitcloud/core" // 准入池(请求限流)
)

// Gateway 网关服务端:持有监听地址、共享密钥与各业务服务实例。
type Gateway struct {
	// listenAddr 私有 TCP 监听地址(如 127.0.0.1:9001,仅本机/内网可达)。
	listenAddr string
	// sharedKey 共享密钥(两端预置;用于握手 HMAC 挑战应答,不落盘明文)。
	sharedKey []byte
	// auth 动态认证服务(用户/NT hash/ACL 查询与变更推送)。
	auth *AuthService
	// files 文件操作服务(open/read/write/… 落地 OrbitCloud)。
	files *FileOpsService
	// handles 远程句柄表(句柄 ID 分配与生命周期管理)。
	handles *HandleRegistry
	// pool 请求准入池(设计点 6):限并发 + 排队缓冲(队列深度 = 配置
	// channel_buffer,与 Rust 侧管道缓冲对齐);池满按 reject 模式回
	// ErrTooManyRequests,由 dispatch 转成 ERR 帧背压。
	pool *core.AdmissionPool

	// mu 保护 conns 连接注册表。
	mu sync.Mutex
	// conns 活跃连接集合(变更推送/优雅停机时遍历)。
	conns map[net.Conn]struct{}

	// seqMu 保护 seq/connSeq 两个自增器。
	seqMu sync.Mutex
	// seq 帧请求序列号自增器(响应原样带回)。
	seq uint32
	// connSeq 连接 ID 自增器(断连时回收该连接全部句柄)。
	connSeq uint64

	// idleTimeout 空闲超时(超过无任何帧则断开;心跳保活)。
	idleTimeout time.Duration
}

// NewGateway 构造网关实例。
//
// 参数:
//
//	listenAddr     监听地址;
//	sharedKey      共享密钥(两侧预置,≥ 16 字节);
//	pool           请求准入池(设计点 6:由 main 用 channel_buffer/max_concurrent
//	               构造,如 core.NewAdmissionPool(64, 1024, core.AdmissionModeReject));
//	auth           动态认证服务;
//	files          文件操作服务。
//
// 返回值:初始化完成的网关(未开始监听,需调用 Serve)。
func NewGateway(listenAddr string, sharedKey []byte, pool *core.AdmissionPool, auth *AuthService, files *FileOpsService) *Gateway {
	return &Gateway{ // 字段逐一填充
		listenAddr:  listenAddr,  // 监听地址
		sharedKey:   sharedKey,   // 共享密钥
		auth:        auth,        // 认证服务
		files:       files,       // 文件操作服务
		handles:     NewHandleRegistry(), // 新建句柄注册表
		pool:        pool,        // 准入池
		conns:       make(map[net.Conn]struct{}), // 空连接集合
		idleTimeout: 90 * time.Second, // 空闲超时 90s
	}
}

// Auth 返回认证服务(变更推送常驻循环入口)。
func (g *Gateway) Auth() *AuthService { return g.auth } // 直接返回持有实例

// Shutdown 优雅关闭入口:由调用方取消 Serve 的 ctx 驱动(等待连接收尾);
// 本方法仅同步等待已在 Serve 内完成的收尾(无独立状态,直接返回)。
func (g *Gateway) Shutdown(ctx context.Context) error {
	_ = ctx // 当前无独立状态:收尾已由 Serve 的 connCtx 取消驱动
	return nil
}

// Serve 启动监听并服务,阻塞直到 ctx 取消。
//
// 实现步骤:
//  1. net.Listen("tcp", g.listenAddr),失败返回错误;
//  2. 循环:conn = listener.Accept() —— 接受 Rust 网关出站连接;
//  3. 每连接启动 goroutine g.handleConn(ctx, conn),失败连接直接 Close;
//  4. 监听 ctx.Done():关闭 listener,向所有 conn 发送 MSG_ERR_RESP
//     (ErrCodeGatewayDown, "server shutting down"),等待协程退出后返回。
func (g *Gateway) Serve(ctx context.Context) error {
	// ---- 1. 绑定监听端口 ----
	listener, err := net.Listen("tcp", g.listenAddr) // 监听私有 TCP 端口
	if err != nil {
		return fmt.Errorf("smb_gateway: listen %s: %w", g.listenAddr, err) // 绑定失败:带地址报错
	}
	defer listener.Close() // 退出时关闭监听器
	log.Debugf("smb_gateway: 正在监听 %s", g.listenAddr) // 记录监听地址

	// 连接计数:用于等待所有处理协程退出(优雅停机)。
	var wg sync.WaitGroup // 等待组:统计每个连接处理协程
	// 子 ctx:停机时通知所有连接处理协程退出。
	connCtx, cancel := context.WithCancel(ctx) // 从父 ctx 派生连接级上下文
	defer cancel() // 函数退出时取消所有连接协程

	// ---- 2~3. accept 循环:每连接一个处理协程 ----
	for {
		conn, err := listener.Accept() // 接受 Rust 网关出站连接
		if err != nil {
			if ctx.Err() != nil {
				break // 停机中,退出循环
			}
			// 瞬时错误(如 fd 耗尽)短暂退避后继续。
			log.Errorf("smb_gateway: 接受连接出错:%v", err) // 记录瞬时错误
			time.Sleep(100 * time.Millisecond) // 退避 100ms 防忙循环
			continue // 继续 accept
		}
		wg.Add(1) // 连接协程计数 +1
		go func(c net.Conn) { // 每连接一个独立处理协程
			defer wg.Done() // 退出时计数 -1
			// 握手失败等错误仅记日志,不影响其它连接。
			if err := g.handleConn(connCtx, c); err != nil { // 处理连接完整生命周期
				log.Warnf("smb_gateway: 连接 %s 关闭:%v", c.RemoteAddr(), err) // 记录连接结束原因
			}
		}(conn)
	}

	// ---- 4. 停机广播:通知存量连接后等待收尾 ----
	g.shutdownBroadcast(connCtx) // 向所有活跃连接广播停机帧
	wg.Wait() // 等待全部连接协程退出
	return nil
}

// shutdownBroadcast 向所有活跃连接广播停机通知(ErrCodeGatewayDown)。
// 参数:ctx 停机上下文。
// 实现:遍历连接注册表,写 MSG_ERR_RESP 帧;写失败忽略(连接可能已断)。
func (g *Gateway) shutdownBroadcast(ctx context.Context) {
	g.mu.Lock() // 锁连接注册表
	conns := make([]net.Conn, 0, len(g.conns)) // 预分配快照切片
	for c := range g.conns { // 拷贝全部连接
		conns = append(conns, c)
	}
	g.mu.Unlock() // 尽快解锁(快照外无锁写网络)

	body, err := json.Marshal(ErrorEnvelope{Code: ErrCodeGatewayDown, Message: "server shutting down"}) // 序列化停机错误帧
	if err != nil {
		return // 序列化失败(理论不可能):放弃广播
	}
	for _, c := range conns { // 逐个连接发送
		_ = g.writeFrame(c, FrameHeader{Flags: FlagResponse, MsgType: MSG_ERR_RESP, Seq: 0}, body) // 写停机帧(失败忽略:连接可能已断)
	}
	_ = ctx // 保留参数签名(当前不依赖 ctx)
}

// handleConn 处理单个连接的生命周期。
//
// 实现步骤:
//  1. 设置读写超时(握手阶段 10s 超时,防慢速攻击);
//  2. g.handshake(conn) —— 共享密钥挑战应答,失败记录日志并返回;
//  3. 注册连接(g.conns),启动心跳 goroutine(g.heartbeatLoop);
//  4. 进入读循环:readFrame → handleRequest(投请求池处理);
//  5. 帧错误(坏魔数/超长 body/解码失败)关闭连接;
//  6. defer 注销连接、关闭句柄表中本连接的所有句柄(files.CloseAllByConn)。
func (g *Gateway) handleConn(ctx context.Context, conn net.Conn) error {
	// 连接级上下文:本连接独立于其它连接退出;携带连接 ID 供句柄归属。
	connID := g.nextConnID() // 分配本连接唯一 ID
	ctx = context.WithValue(ctx, connIDKey{}, connID) // 把连接 ID 塞入上下文(句柄归属判定)
	connCtx, cancel := context.WithCancel(ctx) // 本连接子上下文(停机时取消)
	defer cancel() // 连接结束时取消心跳等子协程
	log.Debugf("smb_gateway: 连接 %s 建立(连接ID=%d)", conn.RemoteAddr(), connID) // 记录新连接建立

	// ---- 1. 握手阶段超时(防慢速攻击) ----
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil { // 10s 握手超时
		return err // 设置失败直接报错
	}
	// ---- 2. 双向握手鉴权 ----
	if err := g.handshake(conn); err != nil { // 共享密钥挑战应答
		return fmt.Errorf("handshake: %w", err) // 鉴权失败:上层记录后关闭
	}
	log.Debugf("smb_gateway: 连接 %s 握手成功(连接ID=%d)", conn.RemoteAddr(), connID) // 记录握手通过
	// ---- 3. 握手完成:清除截止时间(进入正常读写),注册连接 + 心跳 ----
	if err := conn.SetDeadline(time.Time{}); err != nil { // 清除握手超时(零值 = 无截止)
		return err
	}
	g.registerConn(conn) // 登记活跃连接(推送/停机广播遍历用)
	defer g.unregisterConn(conn) // 退出时注销
	defer g.files.CloseAllByConn(connID) // 退出时回收本连接全部远程句柄
	go g.heartbeatLoop(connCtx, conn) // 启动心跳协程(30s 单向心跳)

	// ---- 4. 读循环:收帧 → 投请求池(见 transport.go handleRequest) ----
	for {
		hdr, body, err := g.readFrame(conn) // 读一帧(内部刷新空闲超时)
		if err != nil {
			return err // 连接已断/协议错误,结束本连接
		}
		// 心跳帧(单向)直接忽略,由活动时间保活;其余请求投池处理。
		if hdr.Flags&FlagHeartbeat != 0 { // 心跳帧?
			continue // 忽略(心跳本身就是活动,保活由读截止刷新实现)
		}
		g.handleRequest(connCtx, conn, hdr, body) // 投请求池处理(异步,不阻塞读循环)
	}
}

// connIDKey context 键:连接 ID。
type connIDKey struct{} // 空结构体类型作为上下文键(防碰撞)

// nextConnID 分配连接 ID(1 起;每个连接唯一,断连时回收该连接全部句柄)。
func (g *Gateway) nextConnID() uint64 {
	g.seqMu.Lock() // 锁自增器
	defer g.seqMu.Unlock() // 解锁
	g.connSeq++ // 自增
	if g.connSeq == 0 { // 溢出回绕到 0?
		g.connSeq = 1 // 跳过 0(0 保留为哨兵)
	}
	return g.connSeq // 返回新 ID
}

// connIDFrom 从上下文取连接 ID(无则 0)。
func connIDFrom(ctx context.Context) uint64 {
	if id, ok := ctx.Value(connIDKey{}).(uint64); ok { // 从上下文取值并断言类型
		return id // 有连接 ID
	}
	return 0 // 缺失返回 0(哨兵)
}

// registerConn 登记活跃连接(变更推送/停机广播遍历用)。
func (g *Gateway) registerConn(conn net.Conn) {
	g.mu.Lock() // 锁注册表
	defer g.mu.Unlock() // 解锁
	g.conns[conn] = struct{}{} // 以连接对象为键登记(空结构体占位)
}

// unregisterConn 注销连接(连接结束时调用)。
func (g *Gateway) unregisterConn(conn net.Conn) {
	g.mu.Lock() // 锁注册表
	defer g.mu.Unlock() // 解锁
	delete(g.conns, conn) // 移除该连接
}

// handshake 握手鉴权(双向 HMAC 挑战应答,防重放)。
//
// 实现步骤:
//  1. 读 HelloRequest(含 clientId + nonce + challengeDigest);
//  2. 校验客户端摘要:HMAC-SHA256(sharedKey, "HELLO:"+clientId+":"+nonce)
//     与 challengeDigest 常量时间比较(subtle.ConstantTimeCompare),失败回
//     HelloResponse{OK:false} 并断开;
//  3. 生成 ServerNonce(16 字节随机),回 HelloResponse{OK:true, serverNonce};
//  4. 等待客户端回送验证帧(客户端用 ServerNonce 计算摘要回发;
//     超时 10s 断开)——双向认证完成;
//  5. 密钥协商(shared_key_env 未定义时):本侧生成随机密钥,在验证帧
//     交换后与客户端互相下发并更新为会话密钥(后续实现,见 Rust 侧
//     GatewayConfig.shared_key_env 注释)。
//
// 返回值:nil 表示握手成功;错误表示鉴权失败/超时(连接将被关闭)。
func (g *Gateway) handshake(conn net.Conn) error {
	// ---- 1. 读客户端 HELLO 帧 ----
	hdr, body, err := g.readFrame(conn) // 读握手首帧
	if err != nil {
		return fmt.Errorf("read hello: %w", err) // 读取失败:带上下文报错
	}
	if hdr.MsgType != MSG_HELLO { // 首帧必须是 HELLO?
		return fmt.Errorf("expected MSG_HELLO, got %#x", hdr.MsgType) // 类型不符:拒绝
	}
	var hello HelloRequest // 解析目标结构
	if err := json.Unmarshal(body, &hello); err != nil { // 反序列化请求体
		return fmt.Errorf("parse hello: %w", err) // 解析失败:拒绝
	}

	// ---- 2. 校验客户端摘要(常量时间比较,防时序侧信道) ----
	expect := hmacDigest(g.sharedKey, "HELLO:"+hello.ClientID+":"+hello.Nonce) // 用共享密钥重算期望摘要
	if !constantEq(expect, hello.ChallengeDigest) { // 常量时间比较
		log.Errorf("smb_gateway: 握手摘要不匹配:期望=%s 实得=%s 客户端=%q 随机数=%q", expect, hello.ChallengeDigest, hello.ClientID, hello.Nonce) // 记录不匹配详情(排查用)
		// 失败也回一帧(语义明确),随后调用方关闭连接。
		respBody, _ := json.Marshal(HelloResponse{OK: false, Error: "challenge digest mismatch"}) // 序列化失败应答
		_ = g.writeFrame(conn, FrameHeader{Flags: FlagResponse, MsgType: MSG_HELLO_ACK, Seq: hdr.Seq}, respBody) // 回显序号回失败帧
		return errors.New("handshake: challenge digest mismatch") // 返回鉴权失败
	}

	// ---- 3. 生成服务端随机数并应答 ----
	serverNonce, err := randomHex(16) // 16 字节安全随机数(hex)
	if err != nil {
		return err // 随机源失败:直接报错
	}
	respBody, err := json.Marshal(HelloResponse{OK: true, ServerNonce: serverNonce}) // 序列化成功应答(带服务端 nonce)
	if err != nil {
		return err // 序列化失败报错
	}
	if err := g.writeFrame(conn, FrameHeader{Flags: FlagResponse, MsgType: MSG_HELLO_ACK, Seq: hdr.Seq}, respBody); err != nil { // 回 HELLO_ACK
		return err // 写失败:连接已断
	}

	// ---- 4. 等待客户端验证帧(双向认证:客户端证明它持有密钥) ----
	hdr2, body2, err := g.readFrame(conn) // 读客户端验证帧
	if err != nil {
		return fmt.Errorf("read verify: %w", err) // 读取失败/超时:拒绝
	}
	if hdr2.MsgType != MSG_HELLO { // 验证帧复用 HELLO 类型?
		return fmt.Errorf("expected verify MSG_HELLO, got %#x", hdr2.MsgType) // 类型不符:拒绝
	}
	var verify HelloRequest // 解析目标结构
	if err := json.Unmarshal(body2, &verify); err != nil { // 反序列化验证体
		return fmt.Errorf("parse verify: %w", err) // 解析失败:拒绝
	}
	expect2 := hmacDigest(g.sharedKey, "VERIFY:"+serverNonce) // 用服务端 nonce 重算期望摘要
	if !constantEq(expect2, verify.ChallengeDigest) { // 常量时间比较
		return errors.New("handshake: verify digest mismatch") // 验证失败:证明客户端无密钥
	}
	// ---- 5. 密钥协商(动态随机密钥)为后续实现,当前完成静态密钥双向认证 ----
	return nil // 双向认证通过
}

// nextSeq 分配下一个请求序列号(响应原样带回;0 保留给单向通知)。
func (g *Gateway) nextSeq() uint32 {
	g.seqMu.Lock() // 锁自增器
	defer g.seqMu.Unlock() // 解锁
	g.seq++ // 自增
	if g.seq == 0 { // 溢出回绕?
		g.seq = 1 // 跳过 0(单向通知哨兵)
	}
	return g.seq // 返回新序号
}

// heartbeatLoop 心跳保活:每 30s 发一帧 MSG_HEARTBEAT(单向,FlagHeartbeat)。
// 保活原理:readFrame 每次读帧都刷新读截止时间(idleTimeout),心跳帧本身
// 就是"活动",对端只要活着就会持续收到我们的心跳并回发自己的心跳,
// 双向互相保活;对端死掉时读超时触发连接关闭。
//
// 实现:
//  1. ticker 每 30s 触发,写 HEARTBEAT 帧(失败即返回,读循环会收尾);
//  2. ctx 取消 → 退出。
func (g *Gateway) heartbeatLoop(ctx context.Context, conn net.Conn) {
	ticker := time.NewTicker(30 * time.Second) // 30s 心跳定时器
	defer ticker.Stop() // 退出时停止定时器
	for {
		select {
		case <-ctx.Done(): // 上下文取消(停机/连接结束)?
			return // 退出心跳
		case <-ticker.C: // 心跳周期到
			// 单向心跳:seq=0,FlagHeartbeat,无 body。
			if err := g.writeFrame(conn, FrameHeader{Flags: FlagHeartbeat, MsgType: MSG_HEARTBEAT, Seq: 0}, nil); err != nil { // 发心跳帧
				return // 写失败:连接已断,读循环会收到错误并关闭
			}
		}
	}
}

// pushToAll 向所有活跃连接推送变更通知(MSG_AUTH_PUSH,单向)。
//
// 实现:
//  1. 序列化 AclEntry 为 JSON;
//  2. 遍历 g.conns,逐个 writeFrame(发送失败仅记日志,不断连接);
//  3. 无活跃连接时丢弃(下次全量快照兜底)。
func (g *Gateway) pushToAll(entry AclEntry) {
	body, err := json.Marshal(entry) // 序列化变更条目
	if err != nil {
		log.Errorf("smb_gateway: 推送序列化失败:%v", err) // 序列化失败:记日志丢弃
		return
	}
	// 先快照连接列表(避免持锁写网络)。
	g.mu.Lock() // 锁注册表
	conns := make([]net.Conn, 0, len(g.conns)) // 预分配快照切片
	for c := range g.conns { // 拷贝连接列表
		conns = append(conns, c)
	}
	g.mu.Unlock() // 尽快解锁
	log.Debugf("smb_gateway: 推送 操作=%s 类型=%s 广播到 %d 个连接", entry.Op, entry.Kind, len(conns)) // 记录推送广播(操作/类型/连接数)

	for _, c := range conns { // 逐个连接推送
		// 单向推送:seq=0,不需要响应。
		if err := g.writeFrame(c, FrameHeader{Flags: 0, MsgType: MSG_AUTH_PUSH, Seq: 0}, body); err != nil { // 发推送帧
			log.Warnf("smb_gateway: 推送到 %s 失败:%v", c.RemoteAddr(), err) // 单连接失败仅记日志(不断连接)
		}
	}
}

// hmacDigest 计算握手摘要:HMAC-SHA256(key, payload) 的 hex。
// 与 Rust 侧 hmac_sha256_hex 完全一致(握手挑战应答用)。
func hmacDigest(key []byte, payload string) string {
	mac := hmac.New(sha256.New, key) // 以密钥初始化 HMAC-SHA256
	mac.Write([]byte(payload)) // 吸收载荷
	return hex.EncodeToString(mac.Sum(nil)) // 输出摘要 hex
}

// constantEq 常量时间比较(防时序侧信道;内部握手用)。
func constantEq(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 // 逐字节同时比较,耗时与内容无关
}

// randomHex 生成 n 字节随机数的 hex(握手 nonce)。
func randomHex(n int) (string, error) {
	buf := make([]byte, n) // 随机字节缓冲
	if _, err := rand.Read(buf); err != nil { // 读安全随机源
		return "", err // 随机源失败:报错
	}
	return hex.EncodeToString(buf), nil // hex 编码返回
}
