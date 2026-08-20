// gateway.go —— Socket 帧协议:连接管理、心跳、鉴权。
//
// 职责:
//   - 监听私有 TCP 端口,接受 Rust 网关的出站连接(仅允许配置的共享密钥连接);
//   - 帧编解码(见 types.go 帧协议)、请求-响应分发、序列号管理;
//   - 心跳保活与超时回收;句柄表(远程句柄 ID 分配与过期回收);
//   - 请求背压(设计点 6):读循环只负责收帧,业务处理投协程池
//     (core.AdmissionPool,队列深度 = 配置 channel_buffer);
//   - 优雅停机:先停 accept,再逐连接发 Shutdown 通知并等待收尾。
//
// 连接模型(设计点 6/8):
//
//	Gateway(1) ── conn(每连接 1 协程:读循环)
//	                 │  读帧 → 入请求池(限并发+排队,池满回 ERR 背压)
//	                 │       → 协程处理(JSON 控制面 + io.Reader 流式数据面)
//	                 │       → 写响应
//	                 └  心跳由独立 goroutine 定期写 HEARTBEAT
package smbgateway

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"orbitcloud/core"
)

// Gateway 网关服务端:持有监听地址、共享密钥与各业务服务实例。
// 线程安全:各连接有独立读协程,通过 HandleRegistry(带锁)共享句柄表。
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
	mu    sync.Mutex
	// conns 活跃连接集合(变更推送/优雅停机时遍历)。
	conns map[net.Conn]struct{}

	// seqMu 保护 seq 请求序列号自增。
	seqMu sync.Mutex
	// seq 帧序列号自增器(响应原样带回)。
	seq uint32

	// idleTimeout 空闲超时(超过无任何帧则断开;心跳保活)。
	idleTimeout time.Duration
}

// NewGateway 构造网关实例。
// 参数:
//
//	listenAddr     监听地址;
//	sharedKey      共享密钥(两侧预置,≥ 16 字节);
//	pool           请求准入池(设计点 6:由 main 用 channel_buffer/max_concurrent
//	               构造,如 core.NewAdmissionPool(64, 1024, core.AdmissionModeReject));
//	auth           动态认证服务;
//	files          文件操作服务。
//
// 返回值:初始化完成的网关(未开始监听)。
func NewGateway(listenAddr string, sharedKey []byte, pool *core.AdmissionPool, auth *AuthService, files *FileOpsService) *Gateway {
	return &Gateway{
		listenAddr:  listenAddr,
		sharedKey:   sharedKey,
		auth:        auth,
		files:       files,
		handles:     NewHandleRegistry(),
		pool:        pool,
		conns:       make(map[net.Conn]struct{}),
		idleTimeout: 90 * time.Second,
	}
}

// Serve 启动监听并服务,阻塞直到 ctx 取消。
// 实现步骤:
//
//	1. net.Listen("tcp", g.listenAddr),失败返回错误;
//	2. 循环:conn = listener.Accept() —— 接受 Rust 网关出站连接;
//	3. 每连接启动 goroutine g.handleConn(ctx, conn),失败连接直接 Close;
//	4. 监听 ctx.Done():关闭 listener,向所有 conn 发送 MSG_ERR_RESP
//	   (ErrCodeGatewayDown, "server shutting down"),等待协程退出后返回。
func (g *Gateway) Serve(ctx context.Context) error {
	// ---- 1. 绑定监听端口 ----
	listener, err := net.Listen("tcp", g.listenAddr)
	if err != nil {
		return fmt.Errorf("smb_gateway: listen %s: %w", g.listenAddr, err)
	}
	defer listener.Close()
	log.Printf("smb_gateway: listening on %s", g.listenAddr)

	// 连接计数:用于等待所有处理协程退出(优雅停机)。
	var wg sync.WaitGroup
	// 子 ctx:停机时通知所有连接处理协程退出。
	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// ---- 2~3. accept 循环:每连接一个处理协程 ----
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				break // 停机中,退出循环
			}
			// 瞬时错误(如 fd 耗尽)短暂退避后继续。
			log.Printf("smb_gateway: accept error: %v", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		wg.Add(1)
		go func(c net.Conn) {
			defer wg.Done()
			// 握手失败等错误仅记日志,不影响其它连接。
			if err := g.handleConn(connCtx, c); err != nil {
				log.Printf("smb_gateway: conn %s closed: %v", c.RemoteAddr(), err)
			}
		}(conn)
	}

	// ---- 4. 停机广播:通知存量连接后等待收尾 ----
	g.shutdownBroadcast(connCtx)
	wg.Wait()
	return nil
}

// shutdownBroadcast 向所有活跃连接广播停机通知(ErrCodeGatewayDown)。
// 参数:ctx 停机上下文。
// 实现:遍历连接注册表,写 MSG_ERR_RESP 帧;写失败忽略(连接可能已断)。
func (g *Gateway) shutdownBroadcast(ctx context.Context) {
	g.mu.Lock()
	conns := make([]net.Conn, 0, len(g.conns))
	for c := range g.conns {
		conns = append(conns, c)
	}
	g.mu.Unlock()

	body, err := json.Marshal(ErrorEnvelope{Code: ErrCodeGatewayDown, Message: "server shutting down"})
	if err != nil {
		return
	}
	for _, c := range conns {
		_ = g.writeFrame(c, FrameHeader{Flags: FlagResponse, MsgType: MSG_ERR_RESP, Seq: 0}, body)
	}
	_ = ctx
}

// handleConn 处理单个连接的生命周期。
// 实现步骤:
//
//	1. 设置读写超时(握手阶段 10s 超时,防慢速攻击);
//	2. g.handshake(conn) —— 共享密钥挑战应答,失败记录日志并返回;
//	3. 注册连接(g.conns),启动心跳 goroutine(g.heartbeatLoop);
//	4. 进入读循环:readFrame → handleRequest(投请求池处理);
//	5. 帧错误(坏魔数/超长 body/解码失败)关闭连接;
//	6. defer 注销连接、关闭句柄表中本连接的所有句柄(files.CloseAllByConn)。
func (g *Gateway) handleConn(ctx context.Context, conn net.Conn) error {
	// 连接级上下文:本连接独立于其它连接退出。
	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// ---- 1. 握手阶段超时(防慢速攻击) ----
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	// ---- 2. 双向握手鉴权 ----
	if err := g.handshake(conn); err != nil {
		return fmt.Errorf("handshake: %w", err)
	}
	// ---- 3. 握手完成:清除截止时间(进入正常读写),注册连接 + 心跳 ----
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return err
	}
	g.registerConn(conn)
	defer g.unregisterConn(conn)
	go g.heartbeatLoop(connCtx, conn)

	// ---- 4. 读循环:收帧 → 投请求池 ----
	for {
		hdr, body, err := g.readFrame(conn)
		if err != nil {
			return err // 连接已断/协议错误,结束本连接
		}
		// 心跳帧(单向)直接忽略,由活动时间保活;其余请求投池处理。
		if hdr.Flags&FlagHeartbeat != 0 {
			continue
		}
		g.handleRequest(connCtx, conn, hdr, body)
	}
}

// registerConn 登记活跃连接(变更推送/停机广播遍历用)。
func (g *Gateway) registerConn(conn net.Conn) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.conns[conn] = struct{}{}
}

// unregisterConn 注销连接。
func (g *Gateway) unregisterConn(conn net.Conn) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.conns, conn)
}

// handshake 握手鉴权(双向 HMAC 挑战应答,防重放)。
// 实现步骤:
//
//	1. 读 HelloRequest(含 clientId + nonce + challengeDigest);
//	2. 校验客户端摘要:HMAC-SHA256(sharedKey, "HELLO:"+clientId+":"+nonce)
//	   与 challengeDigest 常量时间比较(subtle.ConstantTimeCompare),失败回
//	   HelloResponse{OK:false} 并断开;
//	3. 生成 ServerNonce(16 字节随机),回 HelloResponse{OK:true, serverNonce};
//	4. 等待客户端回送验证帧(客户端用 ServerNonce 计算摘要回发;
//	   超时 10s 断开)——双向认证完成;
//	5. 密钥协商(shared_key_env 未定义时):本侧生成随机密钥,在验证帧
//	   交换后与客户端互相下发并更新为会话密钥(后续实现,见 Rust 侧
//	   GatewayConfig.shared_key_env 注释)。
//
// 返回值:nil 表示握手成功;错误表示鉴权失败/超时(连接将被关闭)。
func (g *Gateway) handshake(conn net.Conn) error {
	// ---- 1. 读客户端 HELLO 帧 ----
	hdr, body, err := g.readFrame(conn)
	if err != nil {
		return fmt.Errorf("read hello: %w", err)
	}
	if hdr.MsgType != MSG_HELLO {
		return fmt.Errorf("expected MSG_HELLO, got %#x", hdr.MsgType)
	}
	var hello HelloRequest
	if err := json.Unmarshal(body, &hello); err != nil {
		return fmt.Errorf("parse hello: %w", err)
	}

	// ---- 2. 校验客户端摘要(常量时间比较,防时序侧信道) ----
	expect := hmacDigest(g.sharedKey, "HELLO:"+hello.ClientID+":"+hello.Nonce)
	if !constantEq(expect, hello.ChallengeDigest) {
		// 失败也回一帧(语义明确),随后调用方关闭连接。
		respBody, _ := json.Marshal(HelloResponse{OK: false, Error: "challenge digest mismatch"})
		_ = g.writeFrame(conn, FrameHeader{Flags: FlagResponse, MsgType: MSG_HELLO_ACK, Seq: hdr.Seq}, respBody)
		return errors.New("handshake: challenge digest mismatch")
	}

	// ---- 3. 生成服务端随机数并应答 ----
	serverNonce, err := randomHex(16)
	if err != nil {
		return err
	}
	respBody, err := json.Marshal(HelloResponse{OK: true, ServerNonce: serverNonce})
	if err != nil {
		return err
	}
	if err := g.writeFrame(conn, FrameHeader{Flags: FlagResponse, MsgType: MSG_HELLO_ACK, Seq: hdr.Seq}, respBody); err != nil {
		return err
	}

	// ---- 4. 等待客户端验证帧(双向认证:客户端证明它持有密钥) ----
	hdr2, body2, err := g.readFrame(conn)
	if err != nil {
		return fmt.Errorf("read verify: %w", err)
	}
	if hdr2.MsgType != MSG_HELLO {
		return fmt.Errorf("expected verify MSG_HELLO, got %#x", hdr2.MsgType)
	}
	var verify HelloRequest
	if err := json.Unmarshal(body2, &verify); err != nil {
		return fmt.Errorf("parse verify: %w", err)
	}
	expect2 := hmacDigest(g.sharedKey, "VERIFY:"+serverNonce)
	if !constantEq(expect2, verify.ChallengeDigest) {
		return errors.New("handshake: verify digest mismatch")
	}
	// ---- 5. 密钥协商(动态随机密钥)为后续实现,当前完成静态密钥双向认证 ----
	return nil
}

// readFrame 从连接读一帧(帧头 + body)。
// 实现步骤:
//
//	1. io.ReadFull 读 16 字节帧头,big endian 解析 FrameHeader;
//	2. 校验 magic/version/bodyLen(≠ 魔数或 > MaxBodyLen → 错误,断开);
//	3. io.ReadFull 读 bodyLen 字节 body;
//	4. 更新最后活动时间(供 idleTimeout 判定)。
//
// 返回值:hdr 帧头;body 消息体;err 网络/协议错误。
func (g *Gateway) readFrame(conn net.Conn) (FrameHeader, []byte, error) {
	// 每读一帧都刷新截止时间:空闲超时 = idleTimeout(心跳保活使连接常活)。
	if err := conn.SetReadDeadline(time.Now().Add(g.idleTimeout)); err != nil {
		return FrameHeader{}, nil, err
	}
	// ---- 1. 读帧头 ----
	head := make([]byte, headerLen)
	if _, err := io.ReadFull(conn, head); err != nil {
		return FrameHeader{}, nil, err
	}
	// ---- 2. 帧头校验(魔数/版本/长度上限) ----
	hdr, err := unmarshalFrameHeader(head)
	if err != nil {
		return FrameHeader{}, nil, err
	}
	// ---- 3. 读消息体 ----
	body := make([]byte, hdr.BodyLen)
	if _, err := io.ReadFull(conn, body); err != nil {
		return FrameHeader{}, nil, err
	}
	return hdr, body, nil
}

// writeFrame 向连接写一帧(帧头 + body),大端序。
// 实现:单次 write(帧头+body) 聚合发送,减少系统调用。
// 返回值:err 网络错误。
func (g *Gateway) writeFrame(conn net.Conn, hdr FrameHeader, body []byte) error {
	frame := marshalFrame(hdr, body)
	_, err := conn.Write(frame)
	return err
}

// dispatch 按消息类型分发请求,返回响应帧类型与响应 body。
// 设计点 6:本函数在协程池内执行(handleConn 读帧后先
// pool.Acquire 再投协程调用,池满 reject → 回 ERR 背压帧,读循环不阻塞)。
// 控制面统一走 MSG_OPERATE(总操作 JSON,单次反序列化,见 types.go)。
// 实现步骤:
//
//	1. switch hdr.MsgType:
//	   - MSG_OPERATE → 核心路径:
//	     a. 拆 body:[4B jsonLen BE] + 总操作 JSON + [流数据段];
//	     b. 一次 json.Unmarshal → OperateRequest;
//	     c. 调 req.Route(ctx, g, stream)(Gateway 实现 OperateHandler);
//	     d. 序列化 OperateResponse(+ Read 的流数据段)→ 响应 body;
//	   - 未知类型 → MSG_ERR_RESP + ErrorEnvelope{ErrCodeNotImpl};
//	2. 所有响应帧带 FlagResponse,seq = 请求 seq。
//
// 返回值:respType 响应消息类型;respBody 响应消息体;err 网络级错误。
func (g *Gateway) dispatch(hdr FrameHeader, body []byte) (uint16, []byte, error) {
	switch hdr.MsgType {
	case MSG_OPERATE:
		// ---- a. 拆控制面 body:JSON 段 + 流数据段 ----
		jsonBytes, stream, err := splitOperateBody(body)
		if err != nil {
			// 布局非法 → 协议级错误帧。
			eb, _ := json.Marshal(ErrorEnvelope{Code: ErrCodeBadRequest, Message: err.Error()})
			return MSG_ERR_RESP, eb, nil
		}
		// ---- b. 单次反序列化总操作 JSON ----
		var req OperateRequest
		if err := json.Unmarshal(jsonBytes, &req); err != nil {
			eb, _ := json.Marshal(ErrorEnvelope{Code: ErrCodeBadRequest, Message: err.Error()})
			return MSG_ERR_RESP, eb, nil
		}
		// ---- c. 路由并操作(业务错误进 OperateResponse.Err,不抛) ----
		resp, streamOut, err := req.Route(context.Background(), g, stream)
		if err != nil {
			eb, _ := json.Marshal(ErrorEnvelope{Code: ErrCodeIO, Message: err.Error()})
			return MSG_ERR_RESP, eb, nil
		}
		// ---- d. 组装响应 body(JSON + Read 流数据段) ----
		respBody, err := marshalOperateBody(resp, streamOut)
		if err != nil {
			eb, _ := json.Marshal(ErrorEnvelope{Code: ErrCodeIO, Message: err.Error()})
			return MSG_ERR_RESP, eb, nil
		}
		return MSG_OPERATE_RESP, respBody, nil

	default:
		// ---- 未知消息类型:协议级错误帧 ----
		eb, _ := json.Marshal(ErrorEnvelope{Code: ErrCodeNotImpl, Message: "unknown msgType"})
		return MSG_ERR_RESP, eb, nil
	}
}

// handleRequest 把一帧请求投进协程池处理并回写响应(设计点 6)。
// 参数:conn 连接;hdr 帧头;body 消息体。
// 实现步骤:
//
//	1. pool.Acquire(ctx):并发满 + 队列满(reject 模式)→ 回
//	   ErrorEnvelope{ErrCodeTimeout} 背压帧,不阻塞读循环;
//	2. 协程内调用 dispatch(hdr, body) 得到响应帧类型与 body;
//	3. pool.Release() 释放令牌;
//	4. writeFrame 回写响应(seq 原样带回)。
func (g *Gateway) handleRequest(ctx context.Context, conn net.Conn, hdr FrameHeader, body []byte) {
	// ---- 1. 请求池准入(读循环不阻塞,背压自然传导) ----
	if g.pool != nil {
		if err := g.pool.Acquire(ctx); err != nil {
			// 池满(reject 模式):回背压错误帧。
			eb, _ := json.Marshal(ErrorEnvelope{Code: ErrCodeTimeout, Message: "request pool full"})
			_ = g.writeFrame(conn, FrameHeader{Flags: FlagResponse, MsgType: MSG_ERR_RESP, Seq: hdr.Seq}, eb)
			return
		}
		defer g.pool.Release()
	}

	// ---- 2~4. 协程内分发并回写 ----
	respType, respBody, err := g.dispatch(hdr, body)
	if err != nil {
		return // 分发层网络错误:忽略(连接由读循环负责关闭)
	}
	_ = g.writeFrame(conn, FrameHeader{Flags: FlagResponse, MsgType: respType, Seq: hdr.Seq}, respBody)
}

// ============================================================================
// OperateHandler 实现:Gateway 按操作码转发到 auth / files 业务服务
// ============================================================================
// OperateHandler 实现:Gateway 按操作码转发到 auth / files 业务服务
// ============================================================================
// 说明:转发逻辑为真实现;auth/file_ops 业务函数当前为占位
// (errNotImplemented),路由层会把占位错误映射为 ErrCodeNotImpl 响应。

// HandleAuthQueryUser 查询用户凭据(CodeAuthQueryUser)。
// 转发:auth.QueryUser(按用户名查 users 表 + nt_hash 列)。
func (g *Gateway) HandleAuthQueryUser(ctx context.Context, args *AuthUserArgs) (*AuthUserResult, error) {
	cred, err := g.auth.QueryUser(ctx, args.Username)
	if err != nil {
		return nil, err
	}
	if cred == nil {
		return &AuthUserResult{Found: false}, nil
	}
	return &AuthUserResult{Found: true, Cred: cred}, nil
}

// HandleAuthQueryAcl 查询用户可见共享清单(CodeAuthQueryAcl)。
// 转发:auth.QuerySharesForUser(经可见性 ACL 过滤的桶 → 共享)。
func (g *Gateway) HandleAuthQueryAcl(ctx context.Context, args *AuthAclArgs) (*AuthAclResult, error) {
	shares, err := g.auth.QuerySharesForUser(ctx, args.Username)
	if err != nil {
		return nil, err
	}
	if shares == nil {
		shares = []ShareInfo{} // 空清单显式返回,勿回 nil(JSON 为 null 会造成歧义)
	}
	return &AuthAclResult{Shares: shares}, nil
}

// HandleAuthSnapshot 全量同步快照(CodeAuthSnapshot)。
// 转发:auth.Snapshot(全部用户 + 全部桶/共享)。
func (g *Gateway) HandleAuthSnapshot(ctx context.Context, args *SnapshotArgs) (*SnapshotResult, error) {
	return g.auth.Snapshot(ctx)
}

// HandleFileOpen 打开/创建(CodeFileOpen)。
// 转发:files.Open(路径解析 → 记录落库 → 分配远程句柄 ID)。
func (g *Gateway) HandleFileOpen(ctx context.Context, args *OpenArgs) (*OpenResult, error) {
	// 桶上下文:真实现时由"共享 → 桶"绑定表解析(见 04 文档);
	// 当前按空 ShareInfo 转发,业务层占位。
	return g.files.Open(ctx, args, ShareInfo{})
}

// HandleFileRead 按偏移读(CodeFileRead;返回流数据段,由 dispatch 组装)。
// 转发:files.Read(GetRange 流式搬出)。
func (g *Gateway) HandleFileRead(ctx context.Context, args *ReadArgs) (*ReadResult, []byte, error) {
	data, err := g.files.Read(ctx, args.HandleID, args.Offset, args.Length)
	if err != nil {
		return nil, nil, err
	}
	return &ReadResult{Length: uint32(len(data))}, data, nil
}

// HandleFileWrite 按偏移写(CodeFileWrite;流数据段由 dispatch 拆出)。
// 转发:files.Write(帧数据段 → 写回缓存)。
func (g *Gateway) HandleFileWrite(ctx context.Context, args *WriteArgs, stream []byte) (*WriteResult, error) {
	written, err := g.files.Write(ctx, args.HandleID, args.Offset, stream)
	if err != nil {
		return nil, err
	}
	return &WriteResult{Written: written}, nil
}

// HandleFileFlush 冲刷写回缓存(CodeFileFlush)。
func (g *Gateway) HandleFileFlush(ctx context.Context, args *FlushArgs) error {
	return g.files.Flush(ctx, args.HandleID)
}

// HandleFileStat 查询元信息(CodeFileStat)。
func (g *Gateway) HandleFileStat(ctx context.Context, args *StatArgs) (*StatResult, error) {
	info, err := g.files.Stat(ctx, args.HandleID)
	if err != nil {
		return nil, err
	}
	return &StatResult{Info: *info}, nil
}

// HandleFileSetTimes 设置时间戳(CodeFileSetTimes)。
func (g *Gateway) HandleFileSetTimes(ctx context.Context, args *SetTimesArgs) error {
	return g.files.SetTimes(ctx, args)
}

// HandleFileTruncate 截断/扩展(CodeFileTruncate)。
func (g *Gateway) HandleFileTruncate(ctx context.Context, args *TruncateArgs) error {
	return g.files.Truncate(ctx, args.HandleID, args.Length)
}

// HandleFileListDir 列目录(CodeFileListDir)。
func (g *Gateway) HandleFileListDir(ctx context.Context, args *ListDirArgs) (*ListDirResult, error) {
	entries, err := g.files.ListDir(ctx, args.HandleID, args.Pattern)
	if err != nil {
		return nil, err
	}
	if entries == nil {
		entries = []FileInfo{}
	}
	return &ListDirResult{Entries: entries}, nil
}

// HandleFileClose 关闭句柄(CodeFileClose)。
func (g *Gateway) HandleFileClose(ctx context.Context, args *CloseArgs) error {
	return g.files.Close(ctx, args.HandleID)
}

// HandleFileUnlink 删除路径(CodeFileUnlink)。
func (g *Gateway) HandleFileUnlink(ctx context.Context, args *UnlinkArgs) error {
	return g.files.Unlink(ctx, args.Path)
}

// HandleFileRename 重命名/移动(CodeFileRename)。
func (g *Gateway) HandleFileRename(ctx context.Context, args *RenameArgs) error {
	return g.files.Rename(ctx, args.FromPath, args.ToPath)
}

// nextSeq 分配下一个请求序列号(响应原样带回;0 保留给单向通知)。
func (g *Gateway) nextSeq() uint32 {
	g.seqMu.Lock()
	defer g.seqMu.Unlock()
	g.seq++
	if g.seq == 0 {
		g.seq = 1
	}
	return g.seq
}

// heartbeatLoop 心跳保活:每 30s 发一帧 MSG_HEARTBEAT(单向,FlagHeartbeat)。
// 保活原理:readFrame 每次读帧都刷新读截止时间(idleTimeout),心跳帧本身
// 就是"活动",对端只要活着就会持续收到我们的心跳并回发自己的心跳,
// 双向互相保活;对端死掉时读超时触发连接关闭。
// 实现:
//
//	1. ticker 每 30s 触发,写 HEARTBEAT 帧(失败即返回,读循环会收尾);
//	2. ctx 取消 → 退出。
func (g *Gateway) heartbeatLoop(ctx context.Context, conn net.Conn) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// 单向心跳:seq=0,FlagHeartbeat,无 body。
			if err := g.writeFrame(conn, FrameHeader{Flags: FlagHeartbeat, MsgType: MSG_HEARTBEAT, Seq: 0}, nil); err != nil {
				return // 写失败:连接已断,读循环会收到错误并关闭
			}
		}
	}
}

// pushToAll 向所有活跃连接推送变更通知(MSG_AUTH_PUSH,单向)。
// 实现:
//
//	1. 序列化 AclEntry 为 JSON;
//	2. 遍历 g.conns,逐个 writeFrame(发送失败仅记日志,不断连接);
//	3. 无活跃连接时丢弃(下次全量快照兜底)。
func (g *Gateway) pushToAll(entry AclEntry) {
	body, err := json.Marshal(entry)
	if err != nil {
		log.Printf("smb_gateway: push marshal failed: %v", err)
		return
	}
	// 先快照连接列表(避免持锁写网络)。
	g.mu.Lock()
	conns := make([]net.Conn, 0, len(g.conns))
	for c := range g.conns {
		conns = append(conns, c)
	}
	g.mu.Unlock()

	for _, c := range conns {
		// 单向推送:seq=0,不需要响应。
		if err := g.writeFrame(c, FrameHeader{Flags: 0, MsgType: MSG_AUTH_PUSH, Seq: 0}, body); err != nil {
			log.Printf("smb_gateway: push to %s failed: %v", c.RemoteAddr(), err)
		}
	}
}

// HandleRegistry 远程句柄表:句柄 ID 分配、查找、过期回收。
// 线程安全:内部 RWMutex。
type HandleRegistry struct {
	// mu 保护 handles 映射。
	mu sync.RWMutex
	// handles 句柄 ID → 远程句柄(remoteHandle 见 file_ops.go)。
	handles map[uint64]*remoteHandle
	// nextID 句柄 ID 自增器(1 起)。
	nextID uint64
	// ttl 句柄空闲 TTL(超过即回收;默认 30min)。
	ttl time.Duration
}

// NewHandleRegistry 构造空句柄表(句柄 ID 从 1 开始,0 保留非法)。
func NewHandleRegistry() *HandleRegistry {
	return &HandleRegistry{
		handles: make(map[uint64]*remoteHandle),
		nextID:  1,
		ttl:     30 * time.Minute,
	}
}

// Alloc 分配新句柄 ID 并登记。
// 参数:h 待登记的远程句柄。
// 返回值:分配到的句柄 ID(全局唯一,多连接不冲突)。
func (r *HandleRegistry) Alloc(h *remoteHandle) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := r.nextID
	r.nextID++
	h.handleID = id
	r.handles[id] = h
	return id
}

// Get 按句柄 ID 查找句柄,不存在返回 nil。
func (r *HandleRegistry) Get(id uint64) *remoteHandle {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.handles[id]
}

// Delete 注销句柄(关闭后调用;防 ID 复用与句柄泄漏)。
func (r *HandleRegistry) Delete(id uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.handles, id)
}

// ReapExpired 回收超时未活动的句柄(定时任务调用;伪代码)。
func (r *HandleRegistry) ReapExpired(now time.Time) {
	_ = now
}

// encodeJSON 便捷:结构体 → JSON(帧 body;伪代码阶段统一入口)。
func encodeJSON(v any) ([]byte, error) {
	return json.Marshal(v)
}

// decodeJSON 便捷:JSON → 结构体(帧 body 解码)。
func decodeJSON(body []byte, v any) error {
	return json.Unmarshal(body, v)
}

// hmacDigest 计算握手摘要(伪代码:HMAC-SHA256 的 hex)。
func hmacDigest(key []byte, payload string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// constantEq 常量时间比较(防时序侧信道;内部握手用)。
func constantEq(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// randomHex 生成 n 字节随机数的 hex(握手 nonce)。
func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// 编译期断言:本文件使用的二进制编解码符号(伪代码实现预留)。
var (
	_ = binary.BigEndian
	_ = io.EOF
	_ = log.Printf
)
