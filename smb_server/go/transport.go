// transport.go —— 帧收发、请求分发与操作转发(请求处理链)。
//
// 职责:
//   - readFrame / writeFrame:与连接的帧级读写(超时刷新、编解码);
//   - dispatch:按消息类型分发(MSG_OPERATE 核心路径,控制面单次反序列化);
//   - handleRequest:请求准入池(设计点 6)——读循环不阻塞,
//     业务处理投协程池,池满 reject 回背压帧;
//   - Handle* 系列:Gateway 对 OperateHandler 接口的实现(操作码 → 业务服务
//     转发:auth 类转 AuthService,文件类转 FileOpsService)。
//
// 调用链(见 doc.go 连接模型):
//
//	handleConn(读循环)→ handleRequest → dispatch → req.Route(Gateway)
//	          → writeFrame(响应)
package smbgateway

import (
	"context"       // 上下文(请求取消传播)
	"encoding/json" // JSON 编解码(错误帧/总操作)
	"io"            // io.ReadFull 精确读满           // 服务日志
	"net"           // TCP 连接
	"time"          // 读截止时间(空闲超时)
)

// readFrame 从连接读一帧(帧头 + body)。
//
// 实现步骤:
//  1. 刷新读截止时间(空闲超时 = idleTimeout;心跳保活使连接常活);
//  2. io.ReadFull 读 16 字节帧头,big endian 解析 FrameHeader;
//  3. 校验 magic/version/bodyLen(≠ 魔数或 > MaxBodyLen → 错误,断开);
//  4. io.ReadFull 读 bodyLen 字节 body。
//
// 返回值:hdr 帧头;body 消息体;err 网络/协议错误。
func (g *Gateway) readFrame(conn net.Conn) (FrameHeader, []byte, error) {
	// 每读一帧都刷新截止时间:空闲超时 = idleTimeout(心跳保活使连接常活)。
	if err := conn.SetReadDeadline(time.Now().Add(g.idleTimeout)); err != nil { // 刷新读截止(90s 空闲超时)
		return FrameHeader{}, nil, err // 设置失败报错
	}
	// ---- 1. 读帧头 ----
	head := make([]byte, headerLen) // 16B 帧头缓冲
	if _, err := io.ReadFull(conn, head); err != nil { // 精确读满 16B(EOF/超时即错)
		return FrameHeader{}, nil, err // 读失败上抛
	}
	// ---- 2. 帧头校验(魔数/版本/长度上限) ----
	hdr, err := unmarshalFrameHeader(head) // 大端解析 + 硬契约校验
	if err != nil {
		return FrameHeader{}, nil, err // 非法帧:返回错误(上层断开)
	}
	// ---- 3. 读消息体 ----
	body := make([]byte, hdr.BodyLen) // 按帧头长度分配 body 缓冲
	if _, err := io.ReadFull(conn, body); err != nil { // 精确读满 body
		return FrameHeader{}, nil, err // 读失败上抛
	}
	return hdr, body, nil // 返回 (帧头, body)
}

// writeFrame 向连接写一帧(帧头 + body),大端序。
// 实现:单次 write(帧头+body) 聚合发送,减少系统调用。
// 返回值:err 网络错误。
func (g *Gateway) writeFrame(conn net.Conn, hdr FrameHeader, body []byte) error {
	frame := marshalFrame(hdr, body) // 组帧(回填魔数/版本/长度)
	_, err := conn.Write(frame) // 单次写完整帧
	return err // 返回写结果(失败由调用方处理)
}

// dispatch 按消息类型分发请求,返回响应帧类型与响应 body。
// 设计点 6:本函数在协程池内执行(handleConn 读帧后先 pool.Acquire
// 再投协程调用,池满 reject → 回 ERR 背压帧,读循环不阻塞)。
// 控制面统一走 MSG_OPERATE(总操作 JSON,单次反序列化,见 operate.go)。
//
// 实现步骤:
//  1. switch hdr.MsgType:
//     - MSG_OPERATE → 核心路径:
//     a. 拆 body:[4B jsonLen BE] + 总操作 JSON + [流数据段];
//     b. 一次 json.Unmarshal → OperateRequest;
//     c. 调 req.Route(ctx, g, stream)(Gateway 实现 OperateHandler);
//     d. 序列化 OperateResponse(+ Read 的流数据段)→ 响应 body;
//     - 未知类型 → MSG_ERR_RESP + ErrorEnvelope{ErrCodeNotImpl};
//  2. 所有响应帧带 FlagResponse,seq = 请求 seq。
//
// 返回值:respType 响应消息类型;respBody 响应消息体;err 网络级错误。
func (g *Gateway) dispatch(ctx context.Context, hdr FrameHeader, body []byte) (uint16, []byte, error) {
	start := time.Now() // 计时起点(请求耗时统计)
	switch hdr.MsgType {
	case MSG_OPERATE: // 核心路径:总操作请求
		// ---- a. 拆控制面 body:JSON 段 + 流数据段 ----
		jsonBytes, stream, err := splitOperateBody(body) // 解 [4B jsonLen] 布局
		if err != nil { // 布局非法?
			smbErrorf("smb_gateway: 分发拆分错误 seq=%d:%v", hdr.Seq, err) // 记录布局错误
			// 布局非法 → 协议级错误帧。
			eb, _ := json.Marshal(ErrorEnvelope{Code: ErrCodeBadRequest, Message: err.Error()}) // 序列化坏请求错误
			return MSG_ERR_RESP, eb, nil // 回协议级错误帧
		}
		// ---- b. 单次反序列化总操作 JSON ----
		var req OperateRequest // 总请求结构
		if err := json.Unmarshal(jsonBytes, &req); err != nil { // 一次解析全部操作
			smbErrorf("smb_gateway: 分发 JSON 解析错误 seq=%d:%v", hdr.Seq, err) // 记录反序列化错误
			eb, _ := json.Marshal(ErrorEnvelope{Code: ErrCodeBadRequest, Message: err.Error()}) // 序列化坏请求错误
			return MSG_ERR_RESP, eb, nil // 回协议级错误帧
		}
		smbDebugf("smb_gateway: 请求 seq=%d 操作=%s 流=%d 字节", hdr.Seq, codeName(req.Code), len(stream)) // 记录请求(操作码/流段大小)
		// 文件类操作:先解析操作者身份(userId → 用户信息,ristretto 60s 缓存),
		// 注入 ctx 供 Handler 审查/落库;身份无效 → AccessDenied。
		if isFileOpCode(req.Code) {
			info, serr := resolveSession(ctx, req.UserID)
			if serr != nil {
				smbWarnf("smb_gateway: 文件操作身份无效 seq=%d code=%d userId=%d: %v",
					hdr.Seq, req.Code, req.UserID, serr)
				eb, _ := json.Marshal(ErrorEnvelope{Code: ErrCodeAccessDenied, Message: "invalid session user"})
				return MSG_ERR_RESP, eb, nil
			}
			ctx = withSession(ctx, info)
		}
		// ---- c. 路由并操作(业务错误进 OperateResponse.Err,不抛) ----
		resp, streamOut, err := req.Route(ctx, g, stream) // 按 Code 路由到 Handler
		if err != nil { // 路由层 Go error(网络/内部错误)
			smbErrorf("smb_gateway: 路由处理错误 seq=%d:%v", hdr.Seq, err) // 记录内部错误
			eb, _ := json.Marshal(ErrorEnvelope{Code: ErrCodeIO, Message: err.Error()}) // 序列化 IO 错误
			return MSG_ERR_RESP, eb, nil // 回协议级错误帧
		}
		if resp != nil && resp.Err != nil { // 业务错误?
			smbDebugf("smb_gateway: 处理 seq=%d 操作=%s 错误码=%d 消息=%s", hdr.Seq, codeName(req.Code), resp.Err.Code, resp.Err.Message) // 记录业务错误(排查用)
		} else {
			smbDebugf("smb_gateway: 响应 seq=%d 操作=%s 成功 输出流=%d 字节 耗时=%s", hdr.Seq, codeName(req.Code), len(streamOut), time.Since(start)) // 记录成功响应与耗时
		}
		// ---- d. 组装响应 body(JSON + Read 流数据段) ----
		respBody, err := marshalOperateBody(resp, streamOut) // [jsonLen]+JSON+流段
		if err != nil { // 序列化失败?
			smbErrorf("smb_gateway: 分发序列化错误 seq=%d:%v", hdr.Seq, err) // 记录序列化错误
			eb, _ := json.Marshal(ErrorEnvelope{Code: ErrCodeIO, Message: err.Error()}) // 序列化 IO 错误
			return MSG_ERR_RESP, eb, nil // 回协议级错误帧
		}
		return MSG_OPERATE_RESP, respBody, nil // 回总响应帧

	default: // 未知消息类型
		// ---- 未知消息类型:协议级错误帧 ----
		smbWarnf("smb_gateway: 分发未知消息类型=%#x seq=%d", hdr.MsgType, hdr.Seq) // 记录未知消息
		eb, _ := json.Marshal(ErrorEnvelope{Code: ErrCodeNotImpl, Message: "unknown msgType"}) // 序列化未实现错误
		return MSG_ERR_RESP, eb, nil // 回协议级错误帧
	}
}

// handleRequest 把一帧请求投进协程池处理并回写响应(设计点 6)。
//
// 参数:conn 连接;hdr 帧头;body 消息体。
// 实现步骤:
//  1. pool.Acquire(ctx):并发满 + 队列满(reject 模式)→ 回
//     ErrorEnvelope{ErrCodeTimeout} 背压帧,不阻塞读循环;
//  2. 协程内调用 dispatch(hdr, body) 得到响应帧类型与 body;
//  3. pool.Release() 释放令牌;
//  4. writeFrame 回写响应(seq 原样带回)。
func (g *Gateway) handleRequest(ctx context.Context, conn net.Conn, hdr FrameHeader, body []byte) {
	// ---- 1. 请求池准入(读循环不阻塞,背压自然传导) ----
	if g.pool != nil { // 启用了准入池?
		if err := g.pool.Acquire(ctx); err != nil { // 申请令牌(并发满+队列满会失败)
			// 池满(reject 模式):回背压错误帧。
			smbWarnf("smb_gateway: 请求池已满,拒绝 seq=%d", hdr.Seq) // 记录池满拒绝
			eb, _ := json.Marshal(ErrorEnvelope{Code: ErrCodeTimeout, Message: "request pool full"}) // 序列化池满错误
			_ = g.writeFrame(conn, FrameHeader{Flags: FlagResponse, MsgType: MSG_ERR_RESP, Seq: hdr.Seq}, eb) // 回背压帧(seq 带回)
			return // 丢弃本请求(客户端会收到超时错误)
		}
		defer g.pool.Release() // 处理完释放令牌
	}

	// ---- 2~4. 协程内分发并回写 ----
	respType, respBody, err := g.dispatch(ctx, hdr, body) // 分发处理(单次反序列化 + 路由)
	if err != nil {
		return // 分发层网络错误:忽略(连接由读循环负责关闭)
	}
	_ = g.writeFrame(conn, FrameHeader{Flags: FlagResponse, MsgType: respType, Seq: hdr.Seq}, respBody) // 回写响应帧(seq 原样带回)
}

// ============================================================================
// OperateHandler 实现:Gateway 按操作码转发到 auth / files 业务服务
// ============================================================================
// 说明:转发逻辑为真实现;业务错误由 operate.go wrapErrResp 映射为
// 哨兵错误码(ErrCode*),进入 OperateResponse.Err,不抛 Go error。
//
// auth 类操作转发到 AuthService(见 auth.go);文件类操作转发到
// FileOpsService(见 file_ops.go / file_io.go / file_dir.go)。

// HandleAuthQueryUser 查询用户凭据(CodeAuthQueryUser)。
// 转发:auth.QueryUser(按用户名查 users 表 + nt_hash 列)。
func (g *Gateway) HandleAuthQueryUser(ctx context.Context, args *AuthUserArgs) (*AuthUserResult, error) {
	cred, err := g.auth.QueryUser(ctx, args.Username) // 查用户凭据(含 NT hash)
	if err != nil {
		return nil, err // 数据库错误上抛
	}
	if cred == nil { // 未找到/已禁用?
		return &AuthUserResult{Found: false}, nil // 返回 found=false(登录失败)
	}
	return &AuthUserResult{Found: true, Cred: cred}, nil // 返回凭据
}

// HandleAuthQueryAcl 查询用户可见共享清单(CodeAuthQueryAcl)。
// 转发:auth.QuerySharesForUser(经可见性 ACL 过滤的桶 → 共享)。
func (g *Gateway) HandleAuthQueryAcl(ctx context.Context, args *AuthAclArgs) (*AuthAclResult, error) {
	shares, err := g.auth.QuerySharesForUser(ctx, args.Username) // 查可见共享
	if err != nil {
		return nil, err // 错误上抛
	}
	if shares == nil { // 结果为 nil?
		shares = []ShareInfo{} // 空清单显式返回,勿回 nil(JSON 为 null 会造成歧义)
	}
	return &AuthAclResult{Shares: shares}, nil // 返回清单
}

// HandleAuthSnapshot 全量同步快照(CodeAuthSnapshot)。
// 转发:auth.Snapshot(全部用户 + 全部桶/共享)。
func (g *Gateway) HandleAuthSnapshot(ctx context.Context, args *SnapshotArgs) (*SnapshotResult, error) {
	return g.auth.Snapshot(ctx) // 直接委托快照
}

// HandleFileOpen 打开/创建(CodeFileOpen)。
// 转发:files.Open(路径解析 → 记录落库 → 分配远程句柄 ID)。
// 桶上下文:由"共享 → 桶"绑定表解析;当前按路径首段查共享缓存
// (见 file_ops.go LookupShareByPath),未命中由 Open 内部兜底解析。
func (g *Gateway) HandleFileOpen(ctx context.Context, args *OpenArgs) (*OpenResult, error) {
	share := g.files.LookupShareByPath(args.Path) // 按路径首段解析共享(桶上下文)
	return g.files.Open(ctx, args, share, connIDFrom(ctx)) // 打开文件并带连接 ID 归属句柄
}

// HandleFileRead 按偏移读(CodeFileRead;返回流数据段,由 dispatch 组装)。
// 转发:files.Read(GetRange 流式搬出,缓存优先命中)。
func (g *Gateway) HandleFileRead(ctx context.Context, args *ReadArgs) (*ReadResult, []byte, error) {
	data, err := g.files.Read(ctx, args.HandleID, args.Offset, args.Length) // 按句柄偏移读
	if err != nil {
		return nil, nil, err // 错误上抛
	}
	return &ReadResult{Length: uint32(len(data))}, data, nil // 长度 + 流数据段
}

// HandleFileWrite 按偏移写(CodeFileWrite;流数据段由 dispatch 拆出)。
// 转发:files.Write(帧数据段 → 写回缓存,见 write_cache.go)。
func (g *Gateway) HandleFileWrite(ctx context.Context, args *WriteArgs, stream []byte) (*WriteResult, error) {
	written, err := g.files.Write(ctx, args.HandleID, args.Offset, stream) // 写入(缓存/直写)
	if err != nil {
		return nil, err // 错误上抛
	}
	return &WriteResult{Written: written}, nil // 实际写入字节数
}

// HandleFileFlush 冲刷写回缓存(CodeFileFlush)。
// 转发:files.Flush(缓存整体上传对象存储 + 同步元数据)。
func (g *Gateway) HandleFileFlush(ctx context.Context, args *FlushArgs) error {
	return g.files.Flush(ctx, args.HandleID) // 触发写回上传
}

// HandleFileStat 查询元信息(CodeFileStat)。
// 转发:files.Stat(查文件/文件夹/桶记录 → FileInfo)。
func (g *Gateway) HandleFileStat(ctx context.Context, args *StatArgs) (*StatResult, error) {
	info, err := g.files.Stat(ctx, args.HandleID) // 查句柄元信息
	if err != nil {
		return nil, err // 错误上抛
	}
	return &StatResult{Info: *info}, nil // 返回元信息
}

// HandleFileSetTimes 设置时间戳(CodeFileSetTimes)。
// 转发:files.SetTimes(nil 字段 = 不改)。
func (g *Gateway) HandleFileSetTimes(ctx context.Context, args *SetTimesArgs) error {
	return g.files.SetTimes(ctx, args) // 委托设置时间戳
}

// HandleFileTruncate 截断/扩展(CodeFileTruncate;SET_END_OF_FILE)。
// 转发:files.Truncate(更新句柄大小 + 写回缓存截断标记)。
func (g *Gateway) HandleFileTruncate(ctx context.Context, args *TruncateArgs) error {
	return g.files.Truncate(ctx, args.HandleID, args.Length) // 委托截断
}

// HandleFileListDir 列目录(CodeFileListDir)。
// 转发:files.ListDir(查 files/folders 表 → FileInfo 列表)。
func (g *Gateway) HandleFileListDir(ctx context.Context, args *ListDirArgs) (*ListDirResult, error) {
	entries, err := g.files.ListDir(ctx, args.HandleID, args.Pattern) // 查目录条目
	if err != nil {
		return nil, err // 错误上抛
	}
	if entries == nil { // 空目录返回 nil?
		entries = []FileInfo{} // 规范化为空切片(JSON 不为 null)
	}
	return &ListDirResult{Entries: entries}, nil // 返回条目
}

// HandleFileClose 关闭句柄(CodeFileClose)。
// 转发:files.Close(注销句柄 + 写回缓存整体上传 + delete_on_close)。
func (g *Gateway) HandleFileClose(ctx context.Context, args *CloseArgs) error {
	return g.files.Close(ctx, args.HandleID) // 委托关闭
}

// HandleFileUnlink 删除路径(CodeFileUnlink)。
// 转发:files.Unlink(目录须为空,递归删除目录树)。
func (g *Gateway) HandleFileUnlink(ctx context.Context, args *UnlinkArgs) error {
	return g.files.Unlink(ctx, args) // 委托删除
}

// HandleFileRename 重命名/移动(CodeFileRename)。
// 转发:files.Rename(目标已存在必须拒绝)。
func (g *Gateway) HandleFileRename(ctx context.Context, args *RenameArgs) error {
	return g.files.Rename(ctx, args) // 委托重命名
}
