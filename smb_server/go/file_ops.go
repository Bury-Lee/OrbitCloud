// file_ops.go —— 文件操作服务主体:共享上下文解析与 open 语义。
//
// 对应可行性报告 §3.1(文件操作可替换,核心目标):
//   - 把 SMB 操作翻译为对 OrbitCloud 后端的调用(复用 model + server 层 +
//     core.Storage);
//   - Open:校验用户 → 解析路径为 (bucket, folder 链) → 分配远程句柄 ID;
//   - 读/写/冲刷/元信息见 file_io.go;目录/删除/重命名见 file_dir.go;
//   - 路径解析与记录查询工具见 file_lookup.go;写回缓存见 write_cache.go。
//
// 对象键约定:对象 key = 文件记录主键 ID 字符串;桶名 = utils.BucketEncoder(桶ID);
// 桶根 = 虚拟根(FolderID=0,无根实例行)。
//
// 远程句柄(remoteHandle)是一次 SMB CREATE 的全部运行时状态:
// 归属(桶/文件夹/文件/连接)、写回缓存、删除标记、活动时间。
package smbgateway

import (
	"context" // 上下文(查询取消传播)
	"fmt"     // 格式化(对象 key/错误)       // 服务日志(打开/拒绝记录)
	"strings" // 字符串小写(缓存键规范化)
	"sync"    // sync.Map(共享上下文缓存)
	"time"    // 句柄活动时间

	"orbitcloud/core"   // 对象存储抽象与全局 DB
	"orbitcloud/model"  // 数据模型(File/Folder/Bucket/User)
	"orbitcloud/server" // server 层业务服务(GetBucket/CreateDir 等)
)

// FileOpsService 文件操作服务:实现全部文件类操作的落库/落对象逻辑。
// 由 gateway.go 的 OperateHandler 实现转发调用(操作码见 types.go)。
type FileOpsService struct {
	// st 对象存储抽象(core.Storage 单例:Put/Get/GetRange/Delete)。
	st core.ObjectStorage
	// handles 远程句柄表(与 gateway 共享,句柄生命周期统一管理)。
	handles *HandleRegistry
	// shareCache 共享上下文缓存:路径首段(共享名)→ ShareInfo。
	// Open 时登记;Unlink/Rename(无桶上下文的操作)按路径首段解析桶。
	shareCache sync.Map
}

// NewFileOpsService 构造文件操作服务。
// 参数:st 对象存储(core.Storage);handles 句柄表(由 gateway 注入)。
// 返回值:文件操作服务实例。
func NewFileOpsService(st core.ObjectStorage, handles *HandleRegistry) *FileOpsService {
	return &FileOpsService{st: st, handles: handles} // 持有存储与句柄表
}

// ============================================================================
// 共享上下文:路径首段(共享名 = 桶名)→ ShareInfo 的解析与缓存
// ============================================================================

// RegisterShare 登记共享上下文(同步快照/推送时调用;供无桶上下文操作解析)。
func (f *FileOpsService) RegisterShare(share ShareInfo) {
	f.shareCache.Store(strings.ToLower(share.ShareName), share) // 以小写共享名为键缓存
}

// UnregisterShare 移除共享上下文(共享删除时调用)。
func (f *FileOpsService) UnregisterShare(shareName string) {
	f.shareCache.Delete(strings.ToLower(shareName)) // 按小写键删除
}

// LookupShareByPath 按 SMB 路径首段解析共享上下文(open/unlink/rename 用)。
// 路径首段即共享名(共享根下第一级目录);未命中返回空 ShareInfo。
func (f *FileOpsService) LookupShareByPath(path string) ShareInfo {
	segs := splitSmbPath(path) // 拆路径段
	if len(segs) == 0 {        // 空路径(共享根)?
		return ShareInfo{} // 无首段可解析
	}
	if v, ok := f.shareCache.Load(strings.ToLower(segs[0])); ok { // 按首段查缓存(小写)
		return v.(ShareInfo) // 命中:返回共享上下文
	}
	return ShareInfo{} // 未命中:返回空(BucketID=0 表示未知)
}

// LookupShareByID 按桶 ID 解析共享上下文(句柄关闭等场景)。
// 实现:遍历缓存找 BucketID 匹配项(缓存规模小,线性可接受)。
func (f *FileOpsService) LookupShareByID(bucketID uint) ShareInfo {
	var found ShareInfo                      // 命中的共享(默认空)
	f.shareCache.Range(func(_, v any) bool { // 遍历缓存条目
		if s := v.(ShareInfo); s.BucketID == bucketID { // 桶 ID 匹配?
			found = s    // 记录命中
			return false // 停止遍历
		}
		return true // 继续遍历
	})
	return found // 返回命中结果(未命中 = 空)
}

// shareByID 按桶 ID 解析共享上下文(协议 OpenArgs.BucketID 直传路径)。
// 实现:查桶记录 → 组装共享上下文(操作者 = 桶属主)。
func (f *FileOpsService) shareByID(ctx context.Context, bucketID uint) ShareInfo {
	b, err := server.GetBucket(ctx, server.GetBucketArg{ID: bucketID}) // 按主键查桶
	if err != nil || b.Status != 1 {                                   // 查不到或桶禁用?
		return ShareInfo{} // 返回空上下文
	}
	return f.shareFromBucket(ctx, b) // 桶 → 共享上下文
}

// shareFromBucket 由桶记录组装共享上下文(操作者 = 桶属主)。
// 说明:ACL 列表仅含属主(可见性由 auth 侧兜底;此处主要提供桶上下文)。
func (f *FileOpsService) shareFromBucket(ctx context.Context, b *model.Bucket) ShareInfo {
	users := []ShareUserAccess{}                                                     // ACL 列表(默认空)
	if u, err := server.GetUser(ctx, server.GetUserArg{ID: b.OwnerID}); err == nil { // 查桶属主
		users = append(users, ShareUserAccess{Username: u.Username, Access: "readwrite"}) // 属主 = 读写
	}
	return ShareInfo{ // 组装共享上下文
		ShareName:  b.Name,      // 共享名 = 桶名
		BucketID:   b.ID,        // 桶主键
		BucketName: b.Name,      // 桶显示名
		Mode:       "readwrite", // 共享读写模式
		Users:      users,       // ACL(仅属主)
		Quota:      b.Quota,     // 配额
		UsedSpace:  b.UsedSpace, // 已用空间
		Status:     1,           // 桶正常
	}
}

// resolveShareByPath 解析 SMB 路径的共享上下文(权威:路径首段 = 共享名 = 桶名)。
// 先查本地共享缓存,未命中则按桶名查库(与 Sync 快照一致)。
func (f *FileOpsService) resolveShareByPath(ctx context.Context, path string) ShareInfo {
	if s := f.LookupShareByPath(path); s.BucketID != 0 { // 缓存命中?
		return s // 直接返回
	}
	segs := splitSmbPath(path) // 拆路径段
	if len(segs) == 0 {        // 空路径?
		return ShareInfo{} // 无法解析
	}
	b, err := server.GetBucketByName(ctx, server.GetBucketByNameArg{Name: segs[0]}) // 按桶名查库
	if err != nil || b.Status != 1 {                                                // 查不到或禁用?
		return ShareInfo{} // 返回空
	}
	return f.shareFromBucket(ctx, b) // 桶 → 共享上下文
}

// userIDOfShare 由共享上下文解析操作者用户 ID(取 ACL 首用户,空 → 0)。
// 用途:写回落库的 UploadedBy 字段与权限校验。
func (f *FileOpsService) userIDOfShare(ctx context.Context, share ShareInfo) uint {
	if len(share.Users) == 0 { // ACL 为空?
		return 0 // 无操作者
	}
	var u model.User // 查询目标行
	if err := core.DB.WithContext(ctx).
		Where("username = ?", share.Users[0].Username).First(&u).Error; err != nil { // 按首用户名查用户
		return 0 // 查不到返回 0
	}
	return u.ID // 返回用户主键
}

// ============================================================================
// 远程句柄
// ============================================================================

// remoteHandle 远程句柄:一次 SMB CREATE 对应的全部状态。
// 由 HandleRegistry(registry.go)统一分配/查找/注销,线程安全由自身 mu 保证。
type remoteHandle struct {
	// handleID 句柄 ID(gateway.HandleRegistry 分配,全局唯一)。
	handleID uint64
	// share 所属共享(桶上下文:BucketID/BucketName/Mode)。
	share ShareInfo
	// smbPath 打开的 SMB 相对路径(共享根下,组件以 "/" 分隔)。
	smbPath string
	// isDir 是否目录句柄。
	isDir bool
	// bucketID 桶主键 ID(对象存储桶名 = utils.BucketEncoder(bucketID))。
	bucketID uint
	// folderID 文件所在文件夹 ID(0 = 桶根)。
	folderID uint
	// fileID 文件记录主键 ID(= 对象 key;目录句柄为 0)。
	fileID uint
	// objectKey 对象存储 key(= fileID 字符串;目录句柄为空)。
	objectKey string
	// size 当前已知文件大小(写回后更新;Stat 时取 max(记录, 本值))。
	size int64
	// truncateAt 对象旧数据可信上界(-1 = 无截断;Truncate 后对象中
	// [truncateAt, ∞) 的旧数据作废,冲刷拼装时零填充——截断语义的
	// 权威状态在句柄,不再寄生在写缓存)。
	truncateAt int64
	// writeCache 写回缓存(S3 随机写约束;见 write_cache.go WriteBackCache)。
	writeCache *WriteBackCache
	// deleteOnClose FILE_DELETE_ON_CLOSE 语义(close 时删除)。
	deleteOnClose bool
	// lastActive 最后活动时间(句柄过期回收依据;当前以 Close 为主)。
	lastActive time.Time
	// connID 所属连接(断连时批量回收本连接全部句柄)。
	connID uint64
	// userID 打开者 users.id(写回落库/权限用)。
	userID uint
	// session 打开者身份(可见性过滤等审查用;随身份缓存 60s 过期)。
	session *SessionInfo
	// mu 本句柄操作串行化(写回缓存合并与上传需原子性)。
	mu sync.Mutex
}

// flushSpec 取冲刷状态快照(调用方须持 h.mu;内部按 h.mu → cache.mu
// 固定顺序取锁,与 ApplyWrite/FlushToStorage 同序,无死锁无数据竞争)。
// dirty = 有写段 或 有截断待固化(Truncate 未落盘也算脏)。
func (h *remoteHandle) flushSpec() writeSpec {
	spec := writeSpec{size: h.size, truncateAt: h.truncateAt}
	if h.writeCache != nil {
		h.writeCache.mu.Lock()
		spec.segments = h.writeCache.snapshotLocked()
		spec.dirty = h.writeCache.dirty
		h.writeCache.mu.Unlock()
	}
	if h.truncateAt >= 0 {
		spec.dirty = true // 截断待固化(即使无写段)
	}
	return spec
}

// ============================================================================
// Open:打开/创建文件或目录(CodeFileOpen)
// ============================================================================

// Open 打开/创建文件或目录(CodeFileOpen)。
// 参数:ctx 上下文;args 打开请求(路径/读写/意图);share 所属共享(桶上下文);
// connID 连接 ID(句柄归属,断连回收用)。
// 返回值:打开结果(句柄 ID 等);哨兵错误按 ErrCode* 语义。
//
// 语义要点:
//   - 共享根(路径为空)一律按目录打开(客户端打开共享根是基本操作);
//   - Directory intent=open 时绝不隐式创建(Windows Move-Item 探测语义);
//   - 文件分支按 Intent 处理创建/覆盖/截断,写 intent 自动建父链;
//   - 目标已存在目录:显式 non_directory → IsDirectory,否则按目录打开。
func (f *FileOpsService) Open(ctx context.Context, args *OpenArgs, share ShareInfo, connID uint64) (*OpenResult, error) {
	// ---- 0. 解析操作者身份(文件操作必填;用户无效/禁用 → 拒绝) ----
	// 身份由 dispatch 经操作帧 userId → SessionRegistry 解析注入(ctx);
	// 缺失/无效 → ErrForbidden(防冒充)。
	info := sessionFrom(ctx)
	if info == nil {
		smbWarnf("smb_gateway: 打开被拒:缺少操作者身份 路径=%q", args.Path)
		return nil, server.ErrForbidden
	}
	// ---- 1. 解析共享上下文(优先级:路径缓存 → 路径查库 → BucketID 直传) ----
	if share.BucketID == 0 { // 调用方未给共享?
		share = f.LookupShareByPath(args.Path) // 按路径首段查缓存
	}
	if share.BucketID == 0 { // 缓存未命中?
		share = f.resolveShareByPath(ctx, args.Path) // 按桶名查库
	}
	if share.BucketID == 0 && args.BucketID > 0 { // 仍无且请求带桶 ID?
		share = f.shareByID(ctx, args.BucketID) // 按桶主键查库
	}
	if share.BucketID == 0 { // 三层都解析不到?
		smbWarnf("smb_gateway: 打开失败:路径 %q 缺少共享上下文", args.Path)                  // 记录缺桶上下文
		return nil, fmt.Errorf("open: missing share context for %q", args.Path) // 缺桶上下文:拒绝
	}
	// ---- 1.5 桶可用性 + 桶权限(同 HTTP 桶级校验:状态 + 权限等级/属主) ----
	bucket, err := server.CheckBucketUsable(ctx, server.CheckBucketUsableArg{BucketID: share.BucketID})
	if err != nil {
		return nil, err
	}
	if err := checkBucketAccess(ctx, info, bucket); err != nil {
		return nil, err
	}
	userID := info.UserID // 操作者(落库/权限判定用;取代桶属主推断)

	path := cleanSmbPath(args.Path) // 规范化路径(去首尾斜杠/折叠)
	// ---- 2. 共享根(路径为空 = "\"):虚拟根目录句柄 ----
	// folderID=0,无记录行;无论 directory 标志如何一律按目录打开。
	if path == "" { // 打开共享根?
		h := &remoteHandle{ // 构造虚拟根句柄
			share:      share,          // 桶上下文
			smbPath:    "",             // 根路径
			isDir:      true,           // 视为目录
			bucketID:   share.BucketID, // 桶
			folderID:   0,              // 虚拟根(无记录行)
			userID:     userID,         // 操作者
			session:    info,           // 身份(可见性过滤用)
			connID:     connID,         // 连接归属
			truncateAt: -1,             // 无截断(默认)
			lastActive: time.Now(),     // 活动时间
		}
		return &OpenResult{HandleID: f.handles.Alloc(h), IsDir: true, Exists: true}, nil // 登记并返回句柄
	}
	// ---- 3. 只读共享拒绝写 intent ----
	if args.Write && share.Mode == "readonly" { // 请求写且共享只读?
		smbWarnf("smb_gateway: 打开被拒:只读共享 %q 写意图 路径=%q", share.ShareName, args.Path) // 记录只读拒绝
		return nil, server.ErrForbidden                                             // 拒绝访问
	}

	if args.Directory { // 目录打开分支
		// ---- 4a. 目录分支 ----
		// intent=open:目标不存在 → NotFound(绝不隐式创建,Windows Move-Item
		// 会用 dir=true+open 探测目标,创建会破坏语义)。存在性按父链正确解析。
		if args.Intent == "open" { // 只打开不创建?
			parentID, err := f.resolveDir(ctx, share.BucketID, path, false, userID) // 解析父目录链
			if err != nil {
				return nil, server.ErrNotFound // 父链解析失败 = 不存在
			}
			dirID, derr := f.lookupFolder(ctx, share.BucketID, parentID, lastSegment(path), "isable <> 0") // 目标目录存在?
			if derr != nil {
				return nil, server.ErrNotFound // 不存在:绝不隐式创建
			}
			// 目标目录自身与祖先链可见性(同 HTTP precheckFolderRead)
			if err := checkAncestorsVisible(ctx, info, share.BucketID, dirID); err != nil {
				return nil, err
			}
			folder, gerr := server.GetFolder(ctx, server.GetFolderArg{BucketID: share.BucketID, FolderID: dirID})
			if gerr != nil {
				return nil, gerr
			}
			if err := checkItemVisible(info, folder.VisibleToGroups); err != nil {
				return nil, err
			}
			// 打开成功:构造目录句柄
			h := &remoteHandle{
				share:      share,
				smbPath:    path,
				isDir:      true,
				bucketID:   share.BucketID,
				folderID:   dirID,
				userID:     userID,
				session:    info,
				connID:     connID,
				truncateAt: -1, // 无截断(默认)
				lastActive: time.Now(),
			}
			return &OpenResult{HandleID: f.handles.Alloc(h), IsDir: true, Exists: true}, nil
		}
		// create / open_or_create / overwrite:mkdir -p(已存在幂等)。
		dir, err := server.CreateDir(ctx, server.CreateDirArg{ // 调 server 层建目录(幂等)
			UserID:   userID,         // 操作者(真实身份,修复 UserID=0 问题)
			BucketID: share.BucketID, // 桶
			DirPath:  "/" + path,     // 目录路径
		})
		if err != nil {
			return nil, err // 建目录失败上抛
		}
		// 目标目录自身与祖先链可见性(同 HTTP precheckFolderRead)
		if err := checkAncestorsVisible(ctx, info, share.BucketID, dir.ID); err != nil {
			return nil, err
		}
		h := &remoteHandle{ // 构造目录句柄
			share:      share,          // 桶上下文
			smbPath:    path,           // 目录路径
			isDir:      true,           // 目录句柄
			bucketID:   share.BucketID, // 桶
			folderID:   dir.ID,         // 目录记录主键
			userID:     userID,         // 操作者
			session:    info,           // 身份
			connID:     connID,         // 连接归属
			truncateAt: -1,             // 无截断(默认)
			lastActive: time.Now(),     // 活动时间
		}
		return &OpenResult{HandleID: f.handles.Alloc(h), IsDir: true, Exists: true}, nil // 登记返回
	}

	// ---- 4b. 文件分支 ----
	// 解析父目录链(缺段 → NotFound;写 intent 时自动建父链,以真实操作者建)。
	folderID, err := f.resolveDir(ctx, share.BucketID, path, args.Write, userID) // 父链解析(写时自动建)
	if err != nil {
		return nil, err // 父链失败上抛
	}
	name := lastSegment(path) // 末级组件 = 文件名
	// 目标已存在目录:
	//   - 显式 non_directory → FILE_IS_A_DIRECTORY(Windows Move-Item 探测用);
	//   - 否则按目录打开(兼容资源管理器打开文件夹)。
	if _, derr := f.lookupFolder(ctx, share.BucketID, folderID, name, "isable <> 0"); derr == nil { // 目标已是目录?
		if args.NonDirectory { // 显式按文件打开?
			return nil, errIsDirectory // 报"是目录"
		}
		dir, cerr := server.CreateDir(ctx, server.CreateDirArg{ // 幂等建目录(拿记录 ID)
			UserID:   userID,         // 操作者
			BucketID: share.BucketID, // 桶
			DirPath:  "/" + path,     // 路径
		})
		if cerr != nil {
			return nil, cerr // 失败上抛
		}
		// 目录可见性(同 HTTP precheckFolderRead)
		if err := checkAncestorsVisible(ctx, info, share.BucketID, dir.ID); err != nil {
			return nil, err
		}
		dh := &remoteHandle{ // 按目录打开
			share:      share,          // 桶上下文
			smbPath:    path,           // 路径
			isDir:      true,           // 目录句柄
			bucketID:   share.BucketID, // 桶
			folderID:   dir.ID,         // 目录记录
			userID:     userID,         // 操作者
			session:    info,           // 身份
			connID:     connID,         // 连接归属
			truncateAt: -1,             // 无截断(默认)
			lastActive: time.Now(),     // 活动时间
		}
		return &OpenResult{HandleID: f.handles.Alloc(dh), IsDir: true, Exists: true}, nil // 登记返回
	}
	fileID, size, exists, err := f.lookupFile(ctx, share.BucketID, folderID, name) // 查文件记录
	if err != nil {
		return nil, err // 查询失败上抛
	}

	// ---- 4c. 可见性校验(同 HTTP precheckFileRead:祖先链 + 条目可见组) ----
	// 读/改已存在文件与新建写 intent 均需目标父链可见;
	// 已存在文件打开还需条目本身可见(受限文件禁止经 SMB 读取/覆盖)。
	if err := checkAncestorsVisible(ctx, info, share.BucketID, folderID); err != nil {
		return nil, err
	}
	if exists {
		fl, lerr := f.loadFileRecord(ctx, share.BucketID, folderID, name)
		if lerr != nil {
			return nil, lerr
		}
		if err := checkItemVisible(info, fl.VisibleToGroups); err != nil {
			return nil, err
		}
	}

	// ---- 5. 按 Intent 处理创建处置 ----
	created := false     // 本次是否新建了文件记录(变更日志用)
	truncAt := int64(-1) // 截断点(默认无截断;覆盖打开已存在文件 → 0:旧数据整体作废)
	switch args.Intent { // 创建处置语义分发
	case "open": // 只打开
		if !exists { // 文件不存在?
			return nil, server.ErrNotFound // 报未找到
		}
	case "create": // 只创建
		if exists { // 已存在?
			return nil, server.ErrConflict // 报冲突
		}
		fileID, err = f.createEmptyFile(ctx, userID, share.BucketID, folderID, name) // 建空文件记录
		if err != nil {
			return nil, err // 失败上抛
		}
		created = true // 标记新建(变更日志)
	case "open_or_create": // 打开或创建
		if !exists { // 不存在?
			fileID, err = f.createEmptyFile(ctx, userID, share.BucketID, folderID, name) // 创建
			if err != nil {
				return nil, err // 失败上抛
			}
			created = true // 标记新建(变更日志)
		}
	case "overwrite_or_create", "truncate": // 覆盖或截断
		if args.Intent == "truncate" && !exists { // truncate 且不存在?
			return nil, server.ErrNotFound // 截断必须目标存在
		}
		if !exists { // 不存在 → 创建空文件
			fileID, err = f.createEmptyFile(ctx, userID, share.BucketID, folderID, name) // 建记录
			if err != nil {
				return nil, err // 失败上抛
			}
			created = true // 标记新建(变更日志)
			size = 0       // 新文件大小 0
		} else { // 已存在 → 清空(对称记账:保留记录与对象,冲刷时按新旧差值记账)
			// 不再清零记录/删对象:冲刷快照 truncateAt=0 使旧数据整体作废(零填充),
			// syncFileMeta 按 delta = 新大小 - 旧大小 对称入账(归还旧占用、计入新占用),
			// 配额审查亦按真实增量(此前按"新大小-0"虚增旧占用);打开后未冲刷即崩溃/
			// 断连时记录与对象保持旧内容,不丢数据(此前打开即删对象、清零记录)。
			size = 0    // 句柄大小清零(逻辑 EOF)
			truncAt = 0 // 截断点:旧对象数据作废(冲刷时零填充,flushSpec 视为脏)
		}
	default: // 未知意图
		return nil, fmt.Errorf("open: unknown intent %q", args.Intent) // 协议不兼容:报错
	}

	// ---- 6. 组装句柄并登记(写句柄附带写回缓存) ----
	if created { // 新建文件(create / open_or_create / overwrite_or_create 触发)
		smbInfof("smb_gateway: 文件变更 新建 user=%d(%s) 文件=%d", userID, info.Username, fileID) // 每次文件变更仅记录一次(Info 级:操作者+被操作文件)
	}
	h := &remoteHandle{ // 构造文件句柄
		share:         share,                     // 桶上下文
		smbPath:       path,                      // 路径
		bucketID:      share.BucketID,            // 桶
		folderID:      folderID,                  // 父目录
		fileID:        fileID,                    // 文件记录
		objectKey:     fmt.Sprintf("%d", fileID), // 对象 key = 记录 ID 字符串
		size:          size,                      // 已知大小
		truncateAt:    truncAt,                   // 截断点(覆盖打开已存在文件 = 0)
		deleteOnClose: args.DeleteOnClose,        // 关闭即删标记
		userID:        userID,                    // 操作者
		session:       info,                      // 身份(可见性过滤用)
		connID:        connID,                    // 连接归属
		lastActive:    time.Now(),                // 活动时间
	}
	if args.Write { // 请求写权限?
		h.writeCache = NewWriteBackCache() // 附带写回缓存(随机写合并)
	}
	smbDebugf("smb_gateway: 打开成功 共享=%q 路径=%q 意图=%s → 句柄=%d 文件=%d 目录=%v 大小=%d 已存在=%v", share.ShareName, path, args.Intent, h.handleID, fileID, h.isDir, size, exists) // 记录打开成功(共享/路径/意图/句柄)
	return &OpenResult{                                                                                                                                              // 返回打开结果
		HandleID:  f.handles.Alloc(h), // 登记句柄并取 ID
		EndOfFile: uint64(size),       // 文件大小(stat 预取)
		Exists:    exists,             // 是否已存在
	}, nil
}
