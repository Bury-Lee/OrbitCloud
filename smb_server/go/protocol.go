// protocol.go —— 帧协议与共享契约类型(与 Rust 侧 types.rs 完全对应)。
//
// 本文件承载两侧一致的"协议契约"定义:
//   - 帧常量:魔数 / 版本 / flags / 消息类型 / 哨兵错误码;
//   - FrameHeader 帧头结构(16 字节,大端序)与编解码;
//   - 握手结构(HelloRequest / HelloResponse);
//   - 帧级与 body 级编解码函数(组帧 / 拆帧 / 控制面 body 布局);
//   - 动态认证共享类型(UserCred / ShareInfo / AclEntry / FileInfo);
//   - 业务哨兵错误(供 operate.go 错误码映射)。
//
// 硬契约(两侧必须一致,修改需同步 Rust 侧):
//   - Magic / Version / MaxBodyLen;
//   - 帧头字段顺序与字节序;
//   - 消息类型数值(MSG_*);
//   - 控制面 body 布局:[4B jsonLen] + JSON + [流数据段]。
package smbgateway

import (
	"encoding/binary" // 大端字节序编解码(帧头/长度前缀)
	"encoding/json"   // JSON 编解码(控制面/握手载荷)
	"errors"          // 协议错误构造
)

// ---------------------------------------------------------------------------
// 协议魔数与版本(帧头校验,两侧一致)
// ---------------------------------------------------------------------------

const (
	// Magic 帧魔数 "OCST"(OrbitCloud SMB Transport),用于识别本协议连接;
	// 不匹配立即断开(快速丢弃非本协议连接)。
	Magic uint32 = 0x4F435354 // "OCST" 四个 ASCII
	// Version 协议版本号,当前为 1(版本不匹配拒绝握手)。
	Version uint8 = 1
	// MaxBodyLen 单帧 body 上限(16 MiB),防内存放大攻击。
	MaxBodyLen uint32 = 16 << 20 // 16MiB
)

// ---------------------------------------------------------------------------
// 帧头 flags 位标记(bitmask)
// ---------------------------------------------------------------------------

const (
	// FlagResponse 响应帧标记:置位表示本帧是对某请求的响应。
	FlagResponse uint8 = 0x01
	// FlagNeedReply 请求需响应标记:置位表示对方须回响应帧。
	FlagNeedReply uint8 = 0x02
	// FlagHeartbeat 心跳标记:置位表示本帧为心跳探测(无 body,seq=0)。
	FlagHeartbeat uint8 = 0x04
)

// ---------------------------------------------------------------------------
// 消息类型(msgType)常量表 —— 与 Rust 侧 MsgType 枚举完全一致。
// 控制面操作统一走 MSG_OPERATE/MSG_OPERATE_RESP,内部用 OperateCode 路由。
// ---------------------------------------------------------------------------

const (
	// MSG_HELLO 握手:body = HelloRequest(共享密钥挑战应答;第二阶段为 VERIFY 验证)。
	MSG_HELLO uint16 = 0x0001
	// MSG_HELLO_ACK 握手应答:body = HelloResponse(成功/失败)。
	MSG_HELLO_ACK uint16 = 0x0002
	// MSG_HEARTBEAT 心跳探测(单向,FlagHeartbeat,无 body)。
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

// ---------------------------------------------------------------------------
// 哨兵错误码(OperateResponse.Err.Code)—— 与 Rust 侧一致,一一映射到
// ixr-smb-server 的 SmbError(NTSTATUS 语义)。
// ---------------------------------------------------------------------------

const (
	ErrCodeOK           uint32 = 0  // 成功
	ErrCodeNotFound     uint32 = 1  // STATUS_OBJECT_NAME_NOT_FOUND(SmbError::NotFound)
	ErrCodeAccessDenied uint32 = 2  // STATUS_ACCESS_DENIED(SmbError::AccessDenied)
	ErrCodeExists       uint32 = 3  // STATUS_OBJECT_NAME_COLLISION(SmbError::Exists)
	ErrCodeNotEmpty     uint32 = 4  // STATUS_DIRECTORY_NOT_EMPTY(SmbError::NotEmpty)
	ErrCodeIsDirectory  uint32 = 5  // STATUS_FILE_IS_A_DIRECTORY(SmbError::IsDirectory)
	ErrCodeNotADir      uint32 = 6  // STATUS_NOT_A_DIRECTORY(SmbError::NotADirectory)
	ErrCodeIO           uint32 = 7  // STATUS_UNEXPECTED_IO_ERROR(SmbError::Io)
	ErrCodeNotImpl      uint32 = 8  // STATUS_NOT_IMPLEMENTED(占位未实现)
	ErrCodeBadAuth      uint32 = 9  // STATUS_LOGON_FAILURE(握手/密钥失败)
	ErrCodeGatewayDown  uint32 = 10 // 网关不可达(连接断开/超时)
	ErrCodeTimeout      uint32 = 11 // 请求超时
	ErrCodeBadRequest   uint32 = 12 // 请求不合法(Code=0 / 指针与 Code 不匹配)
	ErrCodeDiskFull     uint32 = 13 // STATUS_DISK_FULL(桶配额超限,拒绝写入)
)

// ---------------------------------------------------------------------------
// 业务哨兵错误(与 server 层/common 错误并列,供 operate.go 错误码映射)
// ---------------------------------------------------------------------------

// errNotImplemented 占位哨兵:未真实现的功能统一返回它,
// 由 wrapErrResp 映射为 ErrCodeNotImpl。
var errNotImplemented = errors.New("smb_server/go: 伪代码设计阶段,待真实现") // 全局占位错误

var (
	// errIsDirectory 目标是目录(文件操作语义失败 → ErrCodeIsDirectory)。
	errIsDirectory = errors.New("is a directory") // 当文件打开目录
	// errNotADirectory 目标不是目录(目录操作语义失败 → ErrCodeNotADir)。
	errNotADirectory = errors.New("not a directory") // 当目录打开文件
	// errNotEmpty 目录非空(删除失败 → ErrCodeNotEmpty)。
	errNotEmpty = errors.New("directory not empty") // 删非空目录
)

// ---------------------------------------------------------------------------
// FrameHeader 帧头(16 字节,大端序)
// ---------------------------------------------------------------------------

// headerLen 帧头固定长度(16 字节,大端序)。
const headerLen = 16

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

// marshalFrameHeader 把帧头编码为 16 字节大端字节串。
// 参数:hdr 帧头结构。
// 返回值:16 字节帧头。
// 布局:magic(4) + version(1) + flags(1) + msgType(2) + seq(4) + bodyLen(4)。
func marshalFrameHeader(hdr FrameHeader) []byte {
	buf := make([]byte, headerLen) // 16B 输出缓冲
	binary.BigEndian.PutUint32(buf[0:4], hdr.Magic) // [0:4] 魔数大端
	buf[4] = hdr.Version // [4] 版本
	buf[5] = hdr.Flags // [5] 标记
	binary.BigEndian.PutUint16(buf[6:8], hdr.MsgType) // [6:8] 消息类型大端
	binary.BigEndian.PutUint32(buf[8:12], hdr.Seq) // [8:12] 序号大端
	binary.BigEndian.PutUint32(buf[12:16], hdr.BodyLen) // [12:16] 长度大端
	return buf // 返回帧头字节
}

// unmarshalFrameHeader 从 16 字节大端字节串解析帧头。
// 参数:buf 帧头字节(长度必须 ≥ headerLen)。
// 返回值:帧头结构;err 长度不足或魔数/版本不匹配。
// 硬契约校验:魔数与版本必须匹配,body 长度不得超过上限(非法帧即断开)。
func unmarshalFrameHeader(buf []byte) (FrameHeader, error) {
	if len(buf) < headerLen { // 不足 16B?
		return FrameHeader{}, errors.New("frame: header too short") // 无法构成合法帧头
	}
	hdr := FrameHeader{ // 按大端逐字段解析
		Magic:   binary.BigEndian.Uint32(buf[0:4]), // [0:4] 魔数
		Version: buf[4], // [4] 版本
		Flags:   buf[5], // [5] 标记
		MsgType: binary.BigEndian.Uint16(buf[6:8]), // [6:8] 类型
		Seq:     binary.BigEndian.Uint32(buf[8:12]), // [8:12] 序号
		BodyLen: binary.BigEndian.Uint32(buf[12:16]), // [12:16] 长度
	}
	if hdr.Magic != Magic { // 魔数不符?
		return FrameHeader{}, errors.New("frame: bad magic") // 非本协议连接,拒绝
	}
	if hdr.Version != Version { // 版本不符?
		return FrameHeader{}, errors.New("frame: version mismatch") // 协议不兼容,拒绝
	}
	if hdr.BodyLen > MaxBodyLen { // 长度超限?
		return FrameHeader{}, errors.New("frame: body too large") // 防内存放大,拒绝
	}
	return hdr, nil // 校验通过
}

// marshalFrame 组一帧(帧头 + body),并回填 BodyLen。
// 参数:hdr 帧头(调用方填 Flags/MsgType/Seq);body 消息体。
// 返回值:完整帧字节串(帧头 BodyLen 已按 len(body) 回填)。
func marshalFrame(hdr FrameHeader, body []byte) []byte {
	hdr.Magic = Magic // 回填魔数
	hdr.Version = Version // 回填版本
	hdr.BodyLen = uint32(len(body)) // 回填 body 长度
	out := marshalFrameHeader(hdr) // 编码帧头
	return append(out, body...) // 拼接 body 返回完整帧
}

// ---------------------------------------------------------------------------
// 握手(HELLO / HELLO_ACK)
// ---------------------------------------------------------------------------

// HelloRequest 客户端(Rust 侧)发起握手的请求体。
// 共享密钥双方预置(配置下发),不落盘明文;挑战应答防重放。
// 第二阶段(双向认证)复用本结构:客户端以服务端随机数计算摘要回发。
type HelloRequest struct {
	// ClientID Rust 网关实例标识(多实例隔离远程句柄表用,如 hostname+pid)。
	ClientID string `json:"clientId"`
	// Nonce 客户端随机 16 字节(hex);服务端以其为基础计算挑战。
	Nonce string `json:"nonce"`
	// ChallengeDigest 挑战应答摘要:
	// 第一阶段 = HMAC-SHA256(sharedKey, "HELLO:"+clientId+":"+nonce) 的 hex;
	// 第二阶段 = HMAC-SHA256(sharedKey, "VERIFY:"+serverNonce) 的 hex。
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

// ---------------------------------------------------------------------------
// 控制面 body 布局:[4B jsonLen] + JSON + [流数据段]
// ---------------------------------------------------------------------------

// marshalOperateBody 组装控制面 body:[4B jsonLen] + JSON + [流数据段]。
// 参数:v 总操作 JSON 或总响应 JSON;stream 流数据段(仅 Read/Write 携带)。
// 返回值:body 字节串;err 序列化失败。
func marshalOperateBody(v any, stream []byte) ([]byte, error) {
	j, err := json.Marshal(v) // 序列化总操作/总响应 JSON
	if err != nil {
		return nil, err // 序列化失败上抛
	}
	out := make([]byte, 4+len(j)) // 预分配:4B 前缀 + JSON 长度
	binary.BigEndian.PutUint32(out[0:4], uint32(len(j))) // 写 json 长度前缀(大端)
	copy(out[4:], j) // 写 JSON 段
	return append(out, stream...), nil // 拼接流数据段返回
}

// splitOperateBody 拆解控制面 body:解 [4B jsonLen] 并分离 JSON 与流数据段。
// 参数:body 帧消息体(由 MSG_OPERATE/OPERATE_RESP 承载)。
// 返回值:jsonBytes JSON 段;stream 流数据段(可为空);err 布局非法。
// 硬契约:jsonLen 必须 ≤ body 长度,否则判协议错误。
func splitOperateBody(body []byte) (jsonBytes, stream []byte, err error) {
	if len(body) < 4 { // 连 4B 前缀都没有?
		return nil, nil, errors.New("frame: operate body too short") // 协议错误
	}
	jsonLen := int(binary.BigEndian.Uint32(body[0:4])) // 读 json 长度前缀
	if jsonLen > len(body)-4 { // 长度越界?
		return nil, nil, errors.New("frame: operate jsonLen overflow") // 对端损坏/恶意,拒绝
	}
	return body[4 : 4+jsonLen], body[4+jsonLen:], nil // 零拷贝拆出 (JSON 段, 流段)
}

// ---------------------------------------------------------------------------
// 动态认证共享类型(与 Rust 侧镜像)
// ---------------------------------------------------------------------------

// UserCred 用户凭据快照(驱动 Rust 侧用户表;对应 model.User)。
// NT hash 约束:Go 侧 users 表存储的是 bcrypt,不可逆推 NT hash;
// 必须新增 users.nt_hash 列,在密码设置/修改时由明文计算(NT hash =
// MD4(UTF-16LE(密码))),仅经本网关下发(见 auth.go SetUserNTHash)。
type UserCred struct {
	// ID 用户主键 users.id(会话身份标识;认证成功后 Rust 侧会话绑定该
	// ID,文件操作帧随请求携带,Go 侧据此解析身份)。
	ID uint `json:"id"`
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
// 语义:Op=upsert 表示新增/更新;Op=delete 表示删除;Kind 指明载荷。
// 额外约定:Op="notify" + Kind="dir" 表示文件/文件夹表有变更
// (Rust 侧据此完成挂起的 CHANGE_NOTIFY,触发客户端重枚举)。
type AclEntry struct {
	// Op 操作类型:"upsert" | "delete" | "notify"。
	Op string `json:"op"`
	// Kind 载荷类型:"user" | "share" | "acl" | "dir"。
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
