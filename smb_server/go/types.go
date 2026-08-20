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
//	      ├─ file_ops.go 文件操作 RPC(复用 model + server 层 + core.Storage)
//	      ├─ gateway.go  Socket 帧协议:连接管理、心跳、鉴权
//	      └─ main.go     启动入口
//
// 本包全部函数体为"伪代码"(分步注释 + 编译期占位),但签名/结构体/帧定义
// 是最终设计,可直接作为 Go 侧与 Rust 侧并行开发的依据。
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
//	  - 结构化消息:UTF-8 JSON(字段名与 Rust 侧 serde 字段一一对应);
//	  - FILE_READ_RESP:  [4B 实际长度 u32] + 原始数据(数据长度以实际长度为准);
//	  - FILE_WRITE_REQ:  [8B 偏移 u64] + 原始数据(数据长度 = bodyLen-8);
//	  - 其余二进制消息以 8 字节定长偏移/长度头 + 数据体排列。
//
//	错误:业务失败统一回 ErrorEnvelope(JSON),msgType = MSG_ERR_RESP,
//	code 使用下方 Err* 哨兵常量(与 Rust 侧 SmbError 映射一一对应)。
package smbgateway

import "errors"

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
const (
	// MSG_HELLO 握手:body = HelloRequest(共享密钥挑战应答)。
	MSG_HELLO uint16 = 0x0001
	// MSG_HELLO_ACK 握手应答:body = HelloResponse(成功/失败)。
	MSG_HELLO_ACK uint16 = 0x0002
	// MSG_HEARTBEAT 心跳探测(单向,FlagHeartbeat)。
	MSG_HEARTBEAT uint16 = 0x0003

	// MSG_AUTH_QUERY_USER 查询用户凭据(NT hash):Rust→Go。
	MSG_AUTH_QUERY_USER uint16 = 0x0011
	// MSG_AUTH_QUERY_USER_RESP 用户凭据查询结果。
	MSG_AUTH_QUERY_USER_RESP uint16 = 0x0012
	// MSG_AUTH_QUERY_ACL 查询用户可见共享清单:Rust→Go。
	MSG_AUTH_QUERY_ACL uint16 = 0x0013
	// MSG_AUTH_QUERY_ACL_RESP 共享清单查询结果。
	MSG_AUTH_QUERY_ACL_RESP uint16 = 0x0014
	// MSG_AUTH_PUSH 变更推送(用户/共享/ACL upsert/delete):Go→Rust 单向。
	MSG_AUTH_PUSH uint16 = 0x0015
	// MSG_AUTH_SYNC_SNAPSHOT 全量快照请求(启动同步):Rust→Go。
	MSG_AUTH_SYNC_SNAPSHOT uint16 = 0x0016
	// MSG_AUTH_SYNC_SNAPSHOT_RESP 全量快照响应。
	MSG_AUTH_SYNC_SNAPSHOT_RESP uint16 = 0x0017

	// MSG_FILE_OPEN 打开/创建(文件或目录)。
	MSG_FILE_OPEN uint16 = 0x0101
	// MSG_FILE_OPEN_RESP 打开结果(远程句柄 ID 等)。
	MSG_FILE_OPEN_RESP uint16 = 0x0102
	// MSG_FILE_READ 按偏移读。
	MSG_FILE_READ uint16 = 0x0103
	// MSG_FILE_READ_RESP 读结果(实际数据)。
	MSG_FILE_READ_RESP uint16 = 0x0104
	// MSG_FILE_WRITE 按偏移写(写回缓存)。
	MSG_FILE_WRITE uint16 = 0x0105
	// MSG_FILE_WRITE_RESP 写结果(实际写入字节数)。
	MSG_FILE_WRITE_RESP uint16 = 0x0106
	// MSG_FILE_FLUSH 冲刷写回缓存。
	MSG_FILE_FLUSH uint16 = 0x0107
	// MSG_FILE_FLUSH_RESP 冲刷结果。
	MSG_FILE_FLUSH_RESP uint16 = 0x0108
	// MSG_FILE_STAT 查询文件元信息。
	MSG_FILE_STAT uint16 = 0x0109
	// MSG_FILE_STAT_RESP 元信息结果(FileInfo)。
	MSG_FILE_STAT_RESP uint16 = 0x010A
	// MSG_FILE_SET_TIMES 设置时间戳。
	MSG_FILE_SET_TIMES uint16 = 0x010B
	// MSG_FILE_SET_TIMES_RESP 设置结果。
	MSG_FILE_SET_TIMES_RESP uint16 = 0x010C
	// MSG_FILE_TRUNCATE 截断/扩展到指定长度。
	MSG_FILE_TRUNCATE uint16 = 0x010D
	// MSG_FILE_TRUNCATE_RESP 截断结果。
	MSG_FILE_TRUNCATE_RESP uint16 = 0x010E
	// MSG_FILE_LIST_DIR 列目录。
	MSG_FILE_LIST_DIR uint16 = 0x010F
	// MSG_FILE_LIST_DIR_RESP 目录条目列表。
	MSG_FILE_LIST_DIR_RESP uint16 = 0x0110
	// MSG_FILE_CLOSE 关闭句柄(触发写回上传)。
	MSG_FILE_CLOSE uint16 = 0x0111
	// MSG_FILE_CLOSE_RESP 关闭结果。
	MSG_FILE_CLOSE_RESP uint16 = 0x0112
	// MSG_FILE_UNLINK 删除路径(文件/空目录)。
	MSG_FILE_UNLINK uint16 = 0x0113
	// MSG_FILE_UNLINK_RESP 删除结果。
	MSG_FILE_UNLINK_RESP uint16 = 0x0114
	// MSG_FILE_RENAME 重命名/移动。
	MSG_FILE_RENAME uint16 = 0x0115
	// MSG_FILE_RENAME_RESP 重命名结果。
	MSG_FILE_RENAME_RESP uint16 = 0x0116

	// MSG_ERR_RESP 错误响应帧(ErrorEnvelope)。
	MSG_ERR_RESP uint16 = 0x8001
)

// 哨兵错误码(帧协议 ErrorEnvelope.Code)—— 与 Rust 侧一致,一一映射到
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

// HelloRequest 客户端( Rust 侧)发起握手的请求体。
// 共享密钥双方预置(配置下发),不落盘明文;挑战应答防重放。
type HelloRequest struct {
	// ClientID Rust 网关实例标识(多实例隔离远程句柄表用,如 hostname+pid)。
	ClientID string `json:"clientId"`
	// Nonce 客户端随机 16 字节(hex);服务端以其为基础计算挑战。
	Nonce string `json:"nonce"`
	// ChallengeDigest 挑战应答摘要:
	// HMAC-SHA256(sharedKey, "HELLO:" + clientId + ":" + nonce) 的 hex。
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
// 动态认证(用户 / NT hash / ACL 查询与变更推送)
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
	// Access 访问级别:"readwrite" | "readonly"(对应 ixr Access::ReadWrite/ReadOnly)。
	Access string `json:"access"`
}

// ShareInfo 一个 SMB 共享的完整定义(对应一个桶;拓扑一"每桶一共享")。
// 共享名 = 桶名;共享根 = 桶根(虚拟根,FolderID=0)。
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

// AuthQueryUserRequest 查询单个用户凭据(Rust 认证时实时回调用,级别 B 预留)。
type AuthQueryUserRequest struct {
	// Username 待查询用户名。
	Username string `json:"username"`
}

// AuthQueryUserResponse 用户凭据查询结果。
type AuthQueryUserResponse struct {
	// Found 用户是否存在(不存在 → false,认证方按 LOGON_FAILURE 处理)。
	Found bool `json:"found"`
	// Cred 用户凭据(Found=true 时有效)。
	Cred *UserCred `json:"cred,omitempty"`
}

// AuthQueryAclRequest 查询用户可见共享清单(级别 A 动态 ACL 用)。
type AuthQueryAclRequest struct {
	// Username 用户名。
	Username string `json:"username"`
}

// AuthQueryAclResponse 用户可见共享清单结果。
type AuthQueryAclResponse struct {
	// Shares 该用户可见的共享列表(经可见性 ACL 过滤,复用 server/visibility.go)。
	Shares []ShareInfo `json:"shares"`
}

// SyncSnapshotRequest 全量同步请求(Rust 启动/断线重连后拉取;无字段)。
type SyncSnapshotRequest struct{}

// SyncSnapshotResponse 全量同步响应:一次性下发全部用户与共享定义。
type SyncSnapshotResponse struct {
	// Users 全部有效用户(含 NT hash)。
	Users []UserCred `json:"users"`
	// Shares 全部共享(含各自 ACL)。
	Shares []ShareInfo `json:"shares"`
}

// ============================================================================
// 文件操作 RPC(open/read/write/list/unlink/rename/stat 等)
// ============================================================================

// OpenRequest 打开/创建文件或目录(MSG_FILE_OPEN 请求体)。
// 字段语义与 ixr-smb-server 的 OpenOptions + OpenIntent 一一对应。
type OpenRequest struct {
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

// OpenResponse 打开结果(MSG_FILE_OPEN_RESP 响应体)。
type OpenResponse struct {
	// HandleID Go 网关分配的远程句柄 ID(全局唯一,后续 RPC 用)。
	HandleID uint64 `json:"handleId"`
	// IsDir 实际打开的是目录。
	IsDir bool `json:"isDir"`
	// EndOfFile 打开时的文件大小(新建 = 0;stat 语义预取)。
	EndOfFile uint64 `json:"endOfFile"`
	// Exists 打开时目标已存在(open_or_create 语义下客户端可据此判断新建/打开)。
	Exists bool `json:"exists"`
}

// ReadRequest 按偏移读(MSG_FILE_READ 请求体)。
type ReadRequest struct {
	// HandleID 远程句柄 ID。
	HandleID uint64 `json:"handleId"`
	// Offset 读取起始偏移(字节)。
	Offset uint64 `json:"offset"`
	// Length 期望读取长度(≤ 1 MiB);返回可少于请求(EOF)。
	Length uint32 `json:"length"`
}

// ReadResponse 读结果(MSG_FILE_READ_RESP;body = [4B 实际长度 u32] + 数据)。
type ReadResponse struct {
	// Data 实际读到的字节(长度 ≤ Length;0 字节 = EOF)。
	Data []byte `json:"data"`
}

// WriteRequest 按偏移写(MSG_FILE_WRITE;body = [8B 偏移 u64] + 数据)。
type WriteRequest struct {
	// HandleID 远程句柄 ID。
	HandleID uint64 `json:"handleId"`
	// Offset 写入偏移(字节)。
	Offset uint64 `json:"offset"`
	// Data 待写入数据。
	Data []byte `json:"data"`
}

// WriteResponse 写结果(MSG_FILE_WRITE_RESP)。
type WriteResponse struct {
	// Written 实际写入字节数(通常 = len(Data);异常时更少)。
	Written uint32 `json:"written"`
}

// FlushRequest 冲刷写回缓存(MSG_FILE_FLUSH;空结构)。
type FlushRequest struct {
	// HandleID 远程句柄 ID。
	HandleID uint64 `json:"handleId"`
}

// StatRequest 查询元信息(MSG_FILE_STAT)。
type StatRequest struct {
	// HandleID 远程句柄 ID。
	HandleID uint64 `json:"handleId"`
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

// StatResponse 元信息结果(MSG_FILE_STAT_RESP)。
type StatResponse struct {
	// Info 元信息。
	Info FileInfo `json:"info"`
}

// SetTimesRequest 设置时间戳(MSG_FILE_SET_TIMES;nil 字段 = 不改)。
type SetTimesRequest struct {
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

// TruncateRequest 截断/扩展到指定长度(MSG_FILE_TRUNCATE;SET_END_OF_FILE)。
type TruncateRequest struct {
	// HandleID 远程句柄 ID。
	HandleID uint64 `json:"handleId"`
	// Length 目标长度(字节)。
	Length uint64 `json:"length"`
}

// ListDirRequest 列目录(MSG_FILE_LIST_DIR)。
type ListDirRequest struct {
	// HandleID 目录句柄 ID。
	HandleID uint64 `json:"handleId"`
	// Pattern 通配符(后端可不实现,由 SMB 层后过滤;空 = 全部)。
	Pattern string `json:"pattern"`
}

// ListDirResponse 目录条目列表(MSG_FILE_LIST_DIR_RESP)。
type ListDirResponse struct {
	// Entries 目录条目(FileInfo 列表)。
	Entries []FileInfo `json:"entries"`
}

// CloseRequest 关闭句柄(MSG_FILE_CLOSE;触发写回缓存整体上传)。
type CloseRequest struct {
	// HandleID 远程句柄 ID。
	HandleID uint64 `json:"handleId"`
}

// UnlinkRequest 删除路径(MSG_FILE_UNLINK;目录须为空)。
type UnlinkRequest struct {
	// Path SMB 相对路径。
	Path string `json:"path"`
}

// RenameRequest 重命名/移动(MSG_FILE_RENAME;目标已存在必须拒绝)。
type RenameRequest struct {
	// FromPath 源 SMB 相对路径。
	FromPath string `json:"fromPath"`
	// ToPath 目标 SMB 相对路径(须同桶)。
	ToPath string `json:"toPath"`
}

// ErrorEnvelope 错误响应体(MSG_ERR_RESP;Code 用 Err* 哨兵常量)。
type ErrorEnvelope struct {
	// Code 哨兵错误码(ErrCode* 常量;与 Rust 侧映射一致)。
	Code uint32 `json:"code"`
	// Message 人类可读说明(仅日志,不面向 SMB 客户端)。
	Message string `json:"message"`
}
