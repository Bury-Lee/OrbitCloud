// types.go —— 总操作 JSON 类型:操作码、请求/响应结构与参数/结果定义。
//
// 本文件承载"总操作 JSON"设计(见 doc.go)的全部结构:
//   - OperateCode 操作码常量表(与 Rust 侧 Code* 完全一致);
//   - OperateRequest 总操作请求(单次反序列化,Code 路由);
//   - OperateResponse 总操作响应(Code 回显 + 结果指针/Err);
//   - 各操作的参数(Args)与结果(Result)结构。
//
// 约定:操作参数/结果字段语义与 Rust 侧 `src/types.rs` 完全一致
// (字段名 / JSON tag / 顺序),修改需两侧同步。
package smbgateway

// ---------------------------------------------------------------------------
// 总操作 JSON:OperateCode
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// 总操作请求与响应
// ---------------------------------------------------------------------------

// OperateRequest 总操作 JSON:所有操作共用这一个结构,单次反序列化。
// 每个操作用指针字段承载参数;未填写的操作指针为 nil。
// 校验规则(见 operate.go Route):
//   - Code 必须在常量表中,且为 0 直接拒绝;
//   - Code 对应的指针必须非 nil,其余指针必须为 nil(防误填)。
type OperateRequest struct {
	// Code 操作码(CodeInvalid=0 视为未填写,直接拒绝)。
	Code OperateCode `json:"code"`
	// UserID 操作者身份标识(users.id;Rust 侧 SMB 会话认证成功时经
	// 凭据查询/快照取得;Go 侧经 SessionRegistry(map[UserID]Info,60s
	// 刷新)解析为真实用户,文件类操作以此身份审查/落库。
	// 认证类操作(QueryUser/QueryAcl/Snapshot)按自身 username 参数,
	// 可不携带;文件类操作必填,缺失或用户无效 → AccessDenied)。
	UserID uint `json:"userId,omitempty"`
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

// ---------------------------------------------------------------------------
// 操作参数(Args)—— 与 Rust 侧各 Args 结构字段/语义一致
// ---------------------------------------------------------------------------

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
	// BucketID 所属桶(共享上下文;由 Rust 侧 RemoteBackend 填充)。
	BucketID uint `json:"bucketId"`
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
	// BucketID 所属桶(共享上下文;由 Rust 侧 RemoteBackend 填充)。
	BucketID uint `json:"bucketId"`
	// Path SMB 相对路径。
	Path string `json:"path"`
}

// RenameArgs 重命名/移动参数(CodeFileRename)。
type RenameArgs struct {
	// BucketID 所属桶(共享上下文;由 Rust 侧 RemoteBackend 填充)。
	BucketID uint `json:"bucketId"`
	// FromPath 源 SMB 相对路径。
	FromPath string `json:"fromPath"`
	// ToPath 目标 SMB 相对路径(须同桶)。
	ToPath string `json:"toPath"`
	// ReplaceIfExists 目标已存在时允许覆盖(仅文件目标;目录目标一律拒绝)。
	// 由 Rust 侧 RemoteBackend 填充(true = 允许覆盖,见其 rename 实现说明)。
	ReplaceIfExists bool `json:"replaceIfExists"`
}

// ---------------------------------------------------------------------------
// 操作结果(Result)—— 与 Rust 侧各 Result 结构字段/语义一致
// ---------------------------------------------------------------------------

// AuthUserResult 查询用户凭据结果。
type AuthUserResult struct {
	// Found 用户是否存在(不存在 → false,认证方按 LOGON_FAILURE 处理)。
	Found bool `json:"found"`
	// Cred 用户凭据(Found=true 时有效)。
	Cred *UserCred `json:"cred,omitempty"`
}

// AuthAclResult 用户可见共享清单结果。
type AuthAclResult struct {
	// Shares 该用户可见的共享列表(经可见性 ACL 过滤)。
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
