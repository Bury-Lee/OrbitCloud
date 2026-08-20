// Package smbgateway —— SMB 网关(Go 服务端)伪代码级设计。
//
// ============================================================================
// 总体架构
// ============================================================================
//
//	Windows Explorer / Linux cifs
//	          │  445 (SMB 2.02/2.10/3.0/3.1.1)
//	          ▼
//	Rust SMB 网关(smb_server/rust,沿用 ixr-smb-server)
//	          │  出站私有 TCP 长连接(共享密钥鉴权 + 帧协议)
//	          ▼
//	Go 侧网关服务(smb_server/go,本包)—— 对接 OrbitCloud 后端
//	      ├─ auth.go     用户 / NT hash / ACL 查询与变更推送(动态认证)
//	      ├─ file_ops.go 文件操作(复用 model + server 层 + core.Storage)
//	      ├─ gateway.go  Socket 帧协议:连接管理、心跳、鉴权、请求池
//	      └─ wire.go     装配(由根 main.go 集成,与 HTTP 服务并行)
//
// 本包全部函数体为"伪代码"(分步注释 + 编译期占位),但签名/结构体/帧定义
// 是最终设计,可直接作为 Go 侧与 Rust 侧并行开发的依据。
//
// ============================================================================
// 控制面设计:总操作 JSON + 操作码路由(单次反序列化)
// ============================================================================
//
//	每个请求 body 只反序列化一次成 OperateRequest(总操作 JSON),
//	用操作码 Code 路由到具体操作(仿 HTTP 路由组):
//
//	    body = [4B jsonLen] + 总操作JSON(OperateRequest) + [流数据段(可选)]
//	                    │ 一次 json.Unmarshal
//	                    ▼
//	          OperateRequest.Route(handler) ── switch Code
//	                    │
//	                    ├─ 校验:Code ≠ 0 且对应指针非 nil(两者同时非 nil → 报错)
//	                    ├─ 调用 handler 对应方法(handler 由 Gateway 实现,
//	                    │    转发到 auth / files 业务服务)
//	                    └─ 返回 OperateResponse(总响应 JSON)+ 流数据段
//
//	约定:
//	  - Code 不能从 0 开始:0 = 未填写(校验层直接拒绝);
//	  - 操作参数(路径/意图/句柄/元数据)走 JSON;文件数据走流数据段
//	    (设计点 8:控制面 JSON、数据面 io.Reader 流式);
//	  - 流数据段仅 Read/Write 操作携带,其余操作 bodyLen == jsonLen。
//
//	帧消息类型因此精简为:
//	  MSG_OPERATE(请求)/ MSG_OPERATE_RESP(响应)承载全部控制面操作;
//	  MSG_AUTH_PUSH 保留为 Go→Rust 单向变更推送(无响应);
//	  握手/心跳仍为独立帧。
//
// ============================================================================
// 帧协议(与 smb_server/rust/types.rs 完全对应:字段语义/名称/顺序一致)
// ============================================================================
//
//	帧头 FrameHeader:固定 16 字节,大端序(big endian)
//	  offset  size  字段
//	  ──────  ────  ──────────────────────────────────────────────
//	  0       4     magic   = 0x4F435354("OCST")——快速丢弃非本协议连接
//	  4       1     version = 1
//	  5       1     flags   : 0x01=响应帧 | 0x02=需响应(请求) | 0x04=心跳
//	  6       2     msgType 消息类型(见下方常量表)
//	  8       4     seq     请求序列号(响应原样带回;0=单向通知,无响应)
//	  12      4     bodyLen payload 字节数(≤ 16 MiB)
//	  ──────  ────  ──────────────────────────────────────────────
//
//	消息体 body:
//	  - MSG_OPERATE / MSG_OPERATE_RESP:
//	    [4B jsonLen u32] + 总操作/总响应 JSON + [流数据段(仅 Read/Write)];
//	  - 其余消息(握手/心跳/推送):JSON 或空。
//
//	错误:业务失败统一进 OperateResponse.Err(ErrorEnvelope,code 用 Err* 哨兵)。
package smbgateway

import (
	"context"
	"errors"
)

// 协议魔数与版本(帧头校验,两侧一致)。
const (
	// Magic 帧魔数 "OCST"(OrbitCloud SMB Transport),用于识别本协议连接。
	Magic uint32 = 0x4F435354
	// Version 协议版本号,当前为 1。
	Version uint8 = 1
	// MaxBodyLen 单帧 body 上限(16 MiB),防内存放大攻击。
	MaxBodyLen uint32 = 16 << 20
)

// 帧头 flags 位标记(bitmask)。
const (
	// FlagResponse 响应帧标记:置位表示本帧是对某请求的响应。
	FlagResponse uint8 = 0x01
	// FlagNeedReply 请求需响应标记:置位表示对方须回响应帧。
	FlagNeedReply uint8 = 0x02
	// FlagHeartbeat 心跳标记:置位表示本帧为心跳探测(无 body)。
	FlagHeartbeat uint8 = 0x04
)

// 消息类型(msgType)常量表 —— 与 Rust 侧 MsgType 枚举完全一致。
// 控制面操作统一走 MSG_OPERATE/MSG_OPERATE_RESP,内部用 OperateCode 路由。
const (
	// MSG_HELLO 握手:body = HelloRequest(共享密钥挑战应答)。
	MSG_HELLO uint16 = 0x0001
	// MSG_HELLO_ACK 握手应答:body = HelloResponse(成功/失败)。
	MSG_HELLO_ACK uint16 = 0x0002
	// MSG_HEARTBEAT 心跳探测(单向,FlagHeartbeat)。
	MSG_HEARTBEAT uint16 = 0x0003

	// MSG_AUTH_PUSH 变更推送(用户/共享/ACL upsert/delete):Go→Rust 单向,
	// body = AclEntry(JSON);无响应。
	MSG_AUTH_PUSH uint16 = 0x0015

	// MSG_OPERATE 控制面总操作请求:body = [4B jsonLen] + OperateRequest
	// + [流数据段(可选)]。
	MSG_OPERATE uint16 = 0x0201
	// MSG_OPERATE_RESP 控制面总响应:body = [4B jsonLen] + OperateResponse
	// + [流数据段(可选,Read 数据)]。
	MSG_OPERATE_RESP uint16 = 0x0202

	// MSG_ERR_RESP 兜底错误帧(ErrorEnvelope;正常业务错误走
	// OperateResponse.Err,本帧仅用于协议级错误)。
	MSG_ERR_RESP uint16 = 0x8001
)

// 哨兵错误码(OperateResponse.Err.Code)—— 与 Rust 侧一致,一一映射到
// ixr-smb-server 的 SmbError(NTSTATUS 语义)。
const (
	ErrCodeOK           uint32 = 0  // 成功
	ErrCodeNotFound     uint32 = 1  // STATUS_OBJECT_NAME_NOT_FOUND(SmbError::NotFound)
	ErrCodeAccessDenied uint32 = 2  // STATUS_ACCESS_DENIED(SmbError::AccessDenied)
	ErrCodeExists       uint32 = 3  // STATUS_OBJECT_NAME_COLLISION(SmbError::Exists)
	ErrCodeNotEmpty     uint32 = 4  // STATUS_DIRECTORY_NOT_EMPTY(SmbError::NotEmpty)
	ErrCodeIsDirectory  uint32 = 5  // STATUS_FILE_IS_A_DIRECTORY(SmbError::IsDirectory)
	ErrCodeNotADir      uint32 = 6  // STATUS_NOT_A_DIRECTORY(SmbError::NotADirectory)
	ErrCodeIO           uint32 = 7  // STATUS_UNEXPECTED_IO_ERROR(SmbError::Io)
	ErrCodeNotImpl      uint32 = 8  // STATUS_NOT_IMPLEMENTED(伪代码占位)
	ErrCodeBadAuth      uint32 = 9  // STATUS_LOGON_FAILURE(握手/密钥失败)
	ErrCodeGatewayDown  uint32 = 10 // 网关不可达(连接断开/超时)
	ErrCodeTimeout      uint32 = 11 // 请求超时
	ErrCodeBadRequest   uint32 = 12 // 请求不合法(Code=0 / 指针与 Code 不匹配)
)

// errNotImplemented 伪代码占位哨兵:所有伪代码函数统一返回它,
// 保证 Go 侧在"设计阶段"即可编译通过(go build ./... 全绿)。
var errNotImplemented = errors.New("smb_server/go: 伪代码设计阶段,待真实现")

// ============================================================================
// FrameHeader 帧头(16 字节,大端序)
// ============================================================================

// FrameHeader SMB 网关私有 TCP 帧头。两侧必须按相同字节序(大端)编解码;
// 字段语义、名称、顺序与 Rust 侧 types.rs 的 FrameHeader 完全一致。
type FrameHeader struct {
	Magic   uint32 // 魔数 0x4F435354("OCST");不匹配立即断开连接
	Version uint8  // 协议版本(当前 1);不匹配拒绝握手
	Flags   uint8  // 位标记:FlagResponse / FlagNeedReply / FlagHeartbeat
	MsgType uint16 // 消息类型(见 MSG_* 常量表)
	Seq     uint32 // 请求序列号;响应原样带回;0 = 单向通知
	BodyLen uint32 // body 字节数(≤ MaxBodyLen)
}

// ============================================================================
// 握手(HELLO / HELLO_ACK)
// ============================================================================

// HelloRequest 客户端(Rust 侧)发起握手的请求体。
// 共享密钥双方预置(配置下发),不落盘明文;挑战应答防重放。
type HelloRequest struct {
	// ClientID Rust 网关实例标识(多实例隔离远程句柄表用,如 hostname+pid)。
	ClientID string `json:"clientId"`
	// Nonce 客户端随机 16 字节(hex);服务端以其为基础计算挑战。
	Nonce string `json:"nonce"`
	// ChallengeDigest 挑战应答摘要:
	// HMAC-SHA256(sharedKey, "HELLO:"+clientId+":"+nonce) 的 hex。
	ChallengeDigest string `json:"challengeDigest"`
}

// HelloResponse 握手应答体。
type HelloResponse struct {
	// OK 是否握手成功。
	OK bool `json:"ok"`
	// ServerNonce 服务端随机 16 字节(hex);客户端须回送验证(双向认证)。
	ServerNonce string `json:"serverNonce"`
	// Error 失败原因(OK=false 时填写,仅日志用)。
	Error string `json:"error"`
}

// ============================================================================
// 总操作 JSON:OperateCode + OperateRequest + Route 路由
// ============================================================================

// OperateCode 操作码:一个码对应一种操作。
// 约定:不能从 0 开始(0 = 根本没填写 JSON,校验层直接拒绝)。
type OperateCode int32

// 操作码常量表 —— 与 Rust 侧 OperateCode 完全一致。
const (
	// CodeInvalid 未填写(哨兵,禁止使用)。
	CodeInvalid OperateCode = 0
	// CodeAuthQueryUser 查询用户凭据(NT hash;AuthUserArgs)。
	CodeAuthQueryUser OperateCode = 1
	// CodeAuthQueryAcl 查询用户可见共享清单(AuthAclArgs)。
	CodeAuthQueryAcl OperateCode = 2
	// CodeAuthSnapshot 全量同步快照(SnapshotArgs,空)。
	CodeAuthSnapshot OperateCode = 3
	// CodeFileOpen 打开/创建文件或目录(OpenArgs)。
	CodeFileOpen OperateCode = 4
	// CodeFileRead 按偏移读(ReadArgs;响应带流数据段)。
	CodeFileRead OperateCode = 5
	// CodeFileWrite 按偏移写(WriteArgs;请求带流数据段)。
	CodeFileWrite OperateCode = 6
	// CodeFileFlush 冲刷写回缓存(FlushArgs)。
	CodeFileFlush OperateCode = 7
	// CodeFileStat 查询元信息(StatArgs)。
	CodeFileStat OperateCode = 8
	// CodeFileSetTimes 设置时间戳(SetTimesArgs)。
	CodeFileSetTimes OperateCode = 9
	// CodeFileTruncate 截断/扩展(TruncateArgs)。
	CodeFileTruncate OperateCode = 10
	// CodeFileListDir 列目录(ListDirArgs)。
	CodeFileListDir OperateCode = 11
	// CodeFileClose 关闭句柄(CloseArgs)。
	CodeFileClose OperateCode = 12
	// CodeFileUnlink 删除路径(UnlinkArgs)。
	CodeFileUnlink OperateCode = 13
	// CodeFileRename 重命名/移动(RenameArgs)。
	CodeFileRename OperateCode = 14
)

// OperateRequest 总操作 JSON:所有操作共用这一个结构,单次反序列化。
// 每个操作用指针字段承载参数;未填写的操作指针为 nil。
// 校验规则(见 Route):
//   - Code 必须在常量表中,且为 0 直接拒绝;
//   - Code 对应的指针必须非 nil,其余指针必须为 nil(防误填)。
type OperateRequest struct {
	// Code 操作码(CodeInvalid=0 视为未填写,直接拒绝)。
	Code OperateCode `json:"code"`
	// AuthUser Code=CodeAuthQueryUser 时有效。
	AuthUser *AuthUserArgs `json:"authUser,omitempty"`
	// AuthAcl Code=CodeAuthQueryAcl 时有效。
	AuthAcl *AuthAclArgs `json:"authAcl,omitempty"`
	// Snapshot Code=CodeAuthSnapshot 时有效(空参数)。
	Snapshot *SnapshotArgs `json:"snapshot,omitempty"`
	// Open Code=CodeFileOpen 时有效。
	Open *OpenArgs `json:"open,omitempty"`
	// Read Code=CodeFileRead 时有效。
	Read *ReadArgs `json:"read,omitempty"`
	// Write Code=CodeFileWrite 时有效。
	Write *WriteArgs `json:"write,omitempty"`
	// Flush Code=CodeFileFlush 时有效。
	Flush *FlushArgs `json:"flush,omitempty"`
	// Stat Code=CodeFileStat 时有效。
	Stat *StatArgs `json:"stat,omitempty"`
	// SetTimes Code=CodeFileSetTimes 时有效。
	SetTimes *SetTimesArgs `json:"setTimes,omitempty"`
	// Truncate Code=CodeFileTruncate 时有效。
	Truncate *TruncateArgs `json:"truncate,omitempty"`
	// ListDir Code=CodeFileListDir 时有效。
	ListDir *ListDirArgs `json:"listDir,omitempty"`
	// Close Code=CodeFileClose 时有效。
	Close *CloseArgs `json:"close,omitempty"`
	// Unlink Code=CodeFileUnlink 时有效。
	Unlink *UnlinkArgs `json:"unlink,omitempty"`
	// Rename Code=CodeFileRename 时有效。
	Rename *RenameArgs `json:"rename,omitempty"`
}

// OperateHandler 路由处理器(由 Gateway 实现,转发到 auth/files 业务服务)。
// 每个操作码对应一个方法,方法与操作参数/结果类型一一对应。
type OperateHandler interface {
	// HandleAuthQueryUser 查询用户凭据(CodeAuthQueryUser)。
	HandleAuthQueryUser(ctx context.Context, args *AuthUserArgs) (*AuthUserResult, error)
	// HandleAuthQueryAcl 查询用户可见共享清单(CodeAuthQueryAcl)。
	HandleAuthQueryAcl(ctx context.Context, args *AuthAclArgs) (*AuthAclResult, error)
	// HandleAuthSnapshot 全量同步快照(CodeAuthSnapshot)。
	HandleAuthSnapshot(ctx context.Context, args *SnapshotArgs) (*SnapshotResult, error)
	// HandleFileOpen 打开/创建(CodeFileOpen)。
	HandleFileOpen(ctx context.Context, args *OpenArgs) (*OpenResult, error)
	// HandleFileRead 按偏移读(CodeFileRead;流数据段由 gateway 组装)。
	HandleFileRead(ctx context.Context, args *ReadArgs) (*ReadResult, error)
	// HandleFileWrite 按偏移写(CodeFileWrite;流数据段由 gateway 拆出)。
	HandleFileWrite(ctx context.Context, args *WriteArgs, stream []byte) (*WriteResult, error)
	// HandleFileFlush 冲刷写回缓存(CodeFileFlush)。
	HandleFileFlush(ctx context.Context, args *FlushArgs) error
	// HandleFileStat 查询元信息(CodeFileStat)。
	HandleFileStat(ctx context.Context, args *StatArgs) (*StatResult, error)
	// HandleFileSetTimes 设置时间戳(CodeFileSetTimes)。
	HandleFileSetTimes(ctx context.Context, args *SetTimesArgs) error
	// HandleFileTruncate 截断/扩展(CodeFileTruncate)。
	HandleFileTruncate(ctx context.Context, args *TruncateArgs) error
	// HandleFileListDir 列目录(CodeFileListDir)。
	HandleFileListDir(ctx context.Context, args *ListDirArgs) (*ListDirResult, error)
	// HandleFileClose 关闭句柄(CodeFileClose)。
	HandleFileClose(ctx context.Context, args *CloseArgs) error
	// HandleFileUnlink 删除路径(CodeFileUnlink)。
	HandleFileUnlink(ctx context.Context, args *UnlinkArgs) error
	// HandleFileRename 重命名/移动(CodeFileRename)。
	HandleFileRename(ctx context.Context, args *RenameArgs) error
}

// Route 路由并操作(仿 HTTP 路由组;在 gateway 收到 MSG_OPERATE 后调用,
// 整个 body 已在外部只反序列化过一次)。
// 参数:ctx 上下文;handler 路由处理器(Gateway 实现);stream 流数据段
// (仅 Write 请求携带,其余为 nil)。
// 返回值:resp 总响应 JSON(Code 回显,失败时 Err 非 nil);streamOut 流
// 数据段(仅 Read 响应携带);err 路由/校验级错误。
// 伪代码步骤:
//
//	1. Code 为 0 → ErrCodeBadRequest(视为根本没填写 JSON);
//	2. 校验唯一性:Code 对应指针必须非 nil,且其余指针必须全 nil,
//	   否则 → ErrCodeBadRequest(防误填,拒绝静默路由);
//	3. switch Code → 调 handler 对应方法:
//	   - CodeAuthQueryUser  → handler.HandleAuthQueryUser
//	   - CodeAuthQueryAcl   → handler.HandleAuthQueryAcl
//	   - CodeAuthSnapshot   → handler.HandleAuthSnapshot
//	   - CodeFileOpen…Rename → handler 对应 HandleFile*
//	   - 未知 Code → ErrCodeNotImpl;
//	4. 业务错误 → 填 resp.Err(哨兵 code),不抛 Go error;
//	5. 组装 resp{Code 回显, 结果指针} 返回。
func (r *OperateRequest) Route(ctx context.Context, handler OperateHandler, stream []byte) (resp *OperateResponse, streamOut []byte, err error) {
	_ = ctx
	_ = handler
	_ = stream
	return nil, nil, errNotImplemented
}

// ============================================================================
// 总响应 JSON:OperateResponse
// ============================================================================

// OperateResponse 总响应 JSON:与请求同构,操作结果用指针字段承载;
// 失败时 Err 非 nil(哨兵 code),结果字段保持 nil。
type OperateResponse struct {
	// Code 回显请求操作码(与请求一致;校验失败时为 0)。
	Code OperateCode `json:"code"`
	// Err 业务错误(成功为 nil)。
	Err *ErrorEnvelope `json:"err,omitempty"`
	// AuthUser Code=CodeAuthQueryUser 时有效。
	AuthUser *AuthUserResult `json:"authUser,omitempty"`
	// AuthAcl Code=CodeAuthQueryAcl 时有效。
	AuthAcl *AuthAclResult `json:"authAcl,omitempty"`
	// Snapshot Code=CodeAuthSnapshot 时有效。
	Snapshot *SnapshotResult `json:"snapshot,omitempty"`
	// Open Code=CodeFileOpen 时有效。
	Open *OpenResult `json:"open,omitempty"`
	// Read Code=CodeFileRead 时有效(数据走流数据段)。
	Read *ReadResult `json:"read,omitempty"`
	// Write Code=CodeFileWrite 时有效。
	Write *WriteResult `json:"write,omitempty"`
	// Stat Code=CodeFileStat 时有效。
	Stat *StatResult `json:"stat,omitempty"`
	// ListDir Code=CodeFileListDir 时有效。
	ListDir *ListDirResult `json:"listDir,omitempty"`
	// 无结果操作(Flush/SetTimes/Truncate/Close/Unlink/Rename):仅 Code+Err。
}

// ErrorEnvelope 错误响应体(Err 字段;Code 用 Err* 哨兵常量)。
type ErrorEnvelope struct {
	// Code 哨兵错误码(ErrCode* 常量;与 Rust 侧映射一致)。
	Code uint32 `json:"code"`
	// Message 人类可读说明(仅日志,不面向 SMB 客户端)。
	Message string `json:"message"`
}

// ============================================================================
// 操作参数(Args)—— 与旧协议各 Request 结构对应,字段语义不变
// ============================================================================

// AuthUserArgs 查询用户凭据参数(CodeAuthQueryUser)。
type AuthUserArgs struct {
	// Username 待查询用户名。
	Username string `json:"username"`
}

// AuthAclArgs 查询用户可见共享清单参数(CodeAuthQueryAcl)。
type AuthAclArgs struct {
	// Username 用户名。
	Username string `json:"username"`
}

// SnapshotArgs 全量同步快照参数(CodeAuthSnapshot;空结构,占位)。
type SnapshotArgs struct{}

// OpenArgs 打开/创建文件或目录参数(CodeFileOpen)。
// 字段语义与 ixr-smb-server 的 OpenOptions + OpenIntent 一一对应。
type OpenArgs struct {
	// Path SMB 相对路径(共享根下,组件以 "/" 分隔,不含 ".." 等;协议层已验证)。
	Path string `json:"path"`
	// Read 请求读权限。
	Read bool `json:"read"`
	// Write 请求写权限。
	Write bool `json:"write"`
	// Intent 创建处置语义(与 OpenIntent 对应):
	// "open" | "create" | "open_or_create" | "overwrite_or_create" | "truncate"。
	Intent string `json:"intent"`
	// Directory 按目录打开(FILE_DIRECTORY_FILE)。
	Directory bool `json:"directory"`
	// NonDirectory 按普通文件打开,目标为目录则报错(FILE_NON_DIRECTORY_FILE)。
	NonDirectory bool `json:"nonDirectory"`
	// DeleteOnClose 关闭句柄时删除(FILE_DELETE_ON_CLOSE)。
	DeleteOnClose bool `json:"deleteOnClose"`
}

// ReadArgs 按偏移读参数(CodeFileRead)。
type ReadArgs struct {
	// HandleID 远程句柄 ID。
	HandleID uint64 `json:"handleId"`
	// Offset 读取起始偏移(字节)。
	Offset uint64 `json:"offset"`
	// Length 期望读取长度(≤ 1 MiB);返回可少于请求(EOF)。
	Length uint32 `json:"length"`
}

// WriteArgs 按偏移写参数(CodeFileWrite;数据在流数据段)。
type WriteArgs struct {
	// HandleID 远程句柄 ID。
	HandleID uint64 `json:"handleId"`
	// Offset 写入偏移(字节)。
	Offset uint64 `json:"offset"`
}

// FlushArgs 冲刷写回缓存参数(CodeFileFlush)。
type FlushArgs struct {
	// HandleID 远程句柄 ID。
	HandleID uint64 `json:"handleId"`
}

// StatArgs 查询元信息参数(CodeFileStat)。
type StatArgs struct {
	// HandleID 远程句柄 ID。
	HandleID uint64 `json:"handleId"`
}

// SetTimesArgs 设置时间戳参数(CodeFileSetTimes;nil 字段 = 不改)。
type SetTimesArgs struct {
	// HandleID 远程句柄 ID。
	HandleID uint64 `json:"handleId"`
	// CreationTime 创建时间 FILETIME(nil = 不改)。
	CreationTime *uint64 `json:"creationTime,omitempty"`
	// LastAccessTime 最后访问时间 FILETIME(nil = 不改)。
	LastAccessTime *uint64 `json:"lastAccessTime,omitempty"`
	// LastWriteTime 最后写入时间 FILETIME(nil = 不改)。
	LastWriteTime *uint64 `json:"lastWriteTime,omitempty"`
	// ChangeTime 变更时间 FILETIME(nil = 不改;后端可忽略)。
	ChangeTime *uint64 `json:"changeTime,omitempty"`
}

// TruncateArgs 截断/扩展参数(CodeFileTruncate;SET_END_OF_FILE)。
type TruncateArgs struct {
	// HandleID 远程句柄 ID。
	HandleID uint64 `json:"handleId"`
	// Length 目标长度(字节)。
	Length uint64 `json:"length"`
}

// ListDirArgs 列目录参数(CodeFileListDir)。
type ListDirArgs struct {
	// HandleID 目录句柄 ID。
	HandleID uint64 `json:"handleId"`
	// Pattern 通配符(后端可不实现,由 SMB 层后过滤;空 = 全部)。
	Pattern string `json:"pattern"`
}

// CloseArgs 关闭句柄参数(CodeFileClose;触发写回缓存整体上传)。
type CloseArgs struct {
	// HandleID 远程句柄 ID。
	HandleID uint64 `json:"handleId"`
}

// UnlinkArgs 删除路径参数(CodeFileUnlink;目录须为空)。
type UnlinkArgs struct {
	// Path SMB 相对路径。
	Path string `json:"path"`
}

// RenameArgs 重命名/移动参数(CodeFileRename;目标已存在必须拒绝)。
type RenameArgs struct {
	// FromPath 源 SMB 相对路径。
	FromPath string `json:"fromPath"`
	// ToPath 目标 SMB 相对路径(须同桶)。
	ToPath string `json:"toPath"`
}

// ============================================================================
// 操作结果(Result)—— 与旧协议各 Response 结构对应
// ============================================================================

// AuthUserResult 查询用户凭据结果。
type AuthUserResult struct {
	// Found 用户是否存在(不存在 → false,认证方按 LOGON_FAILURE 处理)。
	Found bool `json:"found"`
	// Cred 用户凭据(Found=true 时有效)。
	Cred *UserCred `json:"cred,omitempty"`
}

// AuthAclResult 用户可见共享清单结果。
type AuthAclResult struct {
	// Shares 该用户可见的共享列表(经可见性 ACL 过滤,复用 server/visibility.go)。
	Shares []ShareInfo `json:"shares"`
}

// SnapshotResult 全量同步快照结果:一次性下发全部用户与共享定义。
type SnapshotResult struct {
	// Users 全部有效用户(含 NT hash)。
	Users []UserCred `json:"users"`
	// Shares 全部共享(含各自 ACL)。
	Shares []ShareInfo `json:"shares"`
}

// OpenResult 打开结果。
type OpenResult struct {
	// HandleID Go 网关分配的远程句柄 ID(全局唯一,后续 RPC 用)。
	HandleID uint64 `json:"handleId"`
	// IsDir 实际打开的是目录。
	IsDir bool `json:"isDir"`
	// EndOfFile 打开时的文件大小(新建 = 0;stat 语义预取)。
	EndOfFile uint64 `json:"endOfFile"`
	// Exists 打开时目标已存在(open_or_create 语义下客户端可据此判断新建/打开)。
	Exists bool `json:"exists"`
}

// ReadResult 读结果(数据走流数据段,本结构仅承载元信息)。
type ReadResult struct {
	// Length 流数据段实际字节数(0 = EOF)。
	Length uint32 `json:"length"`
}

// WriteResult 写结果。
type WriteResult struct {
	// Written 实际写入字节数(通常 = 流数据段长度;异常时更少)。
	Written uint32 `json:"written"`
}

// StatResult 元信息结果。
type StatResult struct {
	// Info 元信息。
	Info FileInfo `json:"info"`
}

// ListDirResult 目录条目列表结果。
type ListDirResult struct {
	// Entries 目录条目(FileInfo 列表)。
	Entries []FileInfo `json:"entries"`
}

// ============================================================================
// 动态认证共享类型(与 Rust 侧镜像)
// ============================================================================

// UserCred 用户凭据快照(驱动 Rust 侧用户表;对应 model.User)。
// NT hash 约束:Go 侧 users 表存储的是 bcrypt,不可逆推 NT hash;
// 必须新增 users.nt_hash 列,在密码设置/修改时由明文计算(NT hash =
// MD4(UTF-16LE(密码))),仅经本网关下发(见 auth.go SetUserNTHash)。
type UserCred struct {
	// Username 用户名(对应 model.User.Username,全局唯一)。
	Username string `json:"username"`
	// NtHashHex NT hash 十六进制(32 字符);供 NTLMv2 挑战-响应校验。
	NtHashHex string `json:"ntHashHex"`
	// PermissionLevel 权限级别(0=超管,数值越小权限越高)。
	PermissionLevel int8 `json:"permissionLevel"`
	// Status 账号状态(1 正常 / 0 禁用;禁用账号不下发/立即失效)。
	Status int `json:"status"`
}

// ShareUserAccess 某用户对某共享的访问权限条目(驱动 ACL 热更新)。
type ShareUserAccess struct {
	// Username 用户名。
	Username string `json:"username"`
	// Access 访问级别:"readwrite" | "readonly"(对应 ixr Access::ReadWrite/Read)。
	Access string `json:"access"`
}

// ShareInfo 一个 SMB 共享的完整定义(对应一个桶;拓扑一"每桶一共享")。
// 共享名 = 桶名;共享根 = 桶根(虚拟根,FolderID=0)。
// 桶级元数据(配额/已用/状态)随推送下发,Rust 侧维护桶实例表(registry.rs)。
type ShareInfo struct {
	// ShareName SMB 共享名(客户端 \\host\<ShareName> 访问;= 桶显示名)。
	ShareName string `json:"shareName"`
	// BucketID 桶主键 ID(对象存储桶名 = utils.BucketEncoder(BucketID))。
	BucketID uint `json:"bucketId"`
	// BucketName 桶显示名(model.Bucket.Name,与 ShareName 相同)。
	BucketName string `json:"bucketName"`
	// Mode 共享级默认权限:"readwrite" | "readonly"。
	Mode string `json:"mode"`
	// Users 该共享的 ACL 用户清单(空 = 仅 Mode 生效)。
	Users []ShareUserAccess `json:"users"`
	// Quota 桶容量配额(字节;0 = 不限;SMB FS_SIZE_INFORMATION 容量上报)。
	Quota int64 `json:"quota"`
	// UsedSpace 桶已用空间(字节;冗余统计,上报剩余容量)。
	UsedSpace int64 `json:"usedSpace"`
	// Status 桶状态(1 正常 / 0 禁用;禁用 = 实例下线,踢出活跃连接)。
	Status int `json:"status"`
}

// AclEntry 变更推送条目(增量;Go→Rust,MSG_AUTH_PUSH)。
// 语义:Op=upsert 表示新增/更新;Op=delete 表示删除。Kind 指明载荷。
type AclEntry struct {
	// Op 操作类型:"upsert" | "delete"。
	Op string `json:"op"`
	// Kind 载荷类型:"user" | "share" | "acl"。
	Kind string `json:"kind"`
	// User Kind=user 时有效:用户凭据(delete 时仅 Username 有意义)。
	User *UserCred `json:"user,omitempty"`
	// Share Kind=share 时有效:共享完整定义(delete 时仅 ShareName 有意义)。
	Share *ShareInfo `json:"share,omitempty"`
	// Acl Kind=acl 时有效:单个用户-共享授权变更。
	Acl *ShareUserAccess `json:"acl,omitempty"`
	// ShareName Kind=acl 时有效:授权变更所在的共享名。
	ShareName string `json:"shareName"`
}

// FileInfo 文件/目录元信息 —— 与 ixr-smb-server FileInfo 字段一一对应
// (字段名/顺序/语义一致,时间均为 FILETIME:1601-01-01 起 100ns 刻度)。
type FileInfo struct {
	// Name 显示名(末级组件;共享根 = 共享名)。
	Name string `json:"name"`
	// EndOfFile 文件大小(字节)。
	EndOfFile uint64 `json:"endOfFile"`
	// AllocationSize 分配大小(简化实现 = EndOfFile)。
	AllocationSize uint64 `json:"allocationSize"`
	// CreationTime 创建时间 FILETIME。
	CreationTime uint64 `json:"creationTime"`
	// LastAccessTime 最后访问时间 FILETIME。
	LastAccessTime uint64 `json:"lastAccessTime"`
	// LastWriteTime 最后写入时间 FILETIME。
	LastWriteTime uint64 `json:"lastWriteTime"`
	// ChangeTime 变更时间 FILETIME(后端不维护时退化 = LastWriteTime)。
	ChangeTime uint64 `json:"changeTime"`
	// IsDirectory 是否目录。
	IsDirectory bool `json:"isDirectory"`
	// FileIndex 唯一文件索引(无则 0,协议层用 FileId 替代)。
	FileIndex uint64 `json:"fileIndex"`
}
