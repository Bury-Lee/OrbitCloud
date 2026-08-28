// file_dir.go —— 目录与变更操作:列目录 / 关闭 / 删除 / 重命名 / 断连回收。
//
// 对应 CodeFileListDir / CodeFileClose / CodeFileUnlink / CodeFileRename:
//   - ListDir:查 files / folders 表组装 FileInfo 列表;
//   - Close:注销句柄 + 写回缓存整体上传 + delete_on_close;
//   - Unlink:目录递归删除(SMB 语义,深度优先清空后删自身);
//   - Rename:复用 server 层 MoveFolder / MoveFile(目标已存在必须拒绝);
//   - CloseAllByConn:断连时回收该连接的全部句柄。
package smbgateway

import (
	"context" // 上下文
	"strings" // 错误文本匹配(416 判定)
	"time"    // 句柄活动时间

	"orbitcloud/core"   // 对象存储/全局 DB
	"orbitcloud/model"  // 数据模型
	"orbitcloud/server" // server 层服务(DeleteFile/DeleteDir/MoveFolder/MoveFile)
	"orbitcloud/utils"  // 桶名编码
)

// ListDir 列目录(CodeFileListDir)。
// 参数:ctx 上下文;handleID 目录句柄 ID;pattern 通配符(可忽略,由 SMB 层过滤)。
// 返回值:目录条目列表;错误按哨兵语义。
// 实现:按 (bucket, folder) 查子文件夹与文件,均按 name_lower 排序;
// 条目级可见性与 HTTP 列表同口径过滤(VisibleToGroups 受限条目对非组成员隐藏)。
func (f *FileOpsService) ListDir(ctx context.Context, handleID uint64, pattern string) ([]FileInfo, error) {
	h := f.handles.Get(handleID) // 定位句柄
	if h == nil {                // 不存在?
		return nil, server.ErrNotFound // 报未找到
	}
	if !h.isDir { // 非目录句柄?
		return nil, errNotADirectory // 不能列目录
	}
	h.lastActive = time.Now() // 刷新活动时间
	_ = pattern               // 通配符由 SMB 协议层后过滤(后端返回全量)

	// 身份(打开时绑定;60s 缓存内的权限/组成员数据)
	info := h.session
	if info == nil { // 防御:老句柄无身份 → 实时解析
		var serr error
		info, serr = resolveSession(ctx, h.userID)
		if serr != nil {
			return nil, serr
		}
	}
	// 目录祖先链可见性(同 HTTP precheckFolderRead;目录已删/受限 → 拒绝)
	if err := checkAncestorsVisible(ctx, info, h.bucketID, h.folderID); err != nil {
		return nil, err
	}

	var folders []model.Folder // 子文件夹集合
	if err := core.DB.WithContext(ctx).
		Where("bucket_id = ? AND parent_id = ? AND isable <> 0", h.bucketID, h.folderID). // 本目录下启用子文件夹
		Order("name_lower ASC").Find(&folders).Error; err != nil {                        // 按小写名排序
		return nil, err // 查询失败上抛
	}
	var files []model.File // 文件集合
	if err := core.DB.WithContext(ctx).
		Where("bucket_id = ? AND folder_id = ?", h.bucketID, h.folderID). // 本目录下文件
		Order("name_lower ASC").Find(&files).Error; err != nil {          // 按小写名排序
		return nil, err // 查询失败上抛
	}

	out := make([]FileInfo, 0, len(folders)+len(files)) // 预分配输出
	for i := range folders {                            // 文件夹条目
		fd := &folders[i] // 取行
		// 条目级可见性(受限子目录对非组成员隐藏,同 HTTP 列表过滤)
		if err := checkItemVisible(info, fd.VisibleToGroups); err != nil {
			continue
		}
		out = append(out, FileInfo{ // 组装条目
			Name:           fd.Name,                      // 目录名
			EndOfFile:      0,                            // 目录无大小
			AllocationSize: 0,                            // 无分配
			CreationTime:   timeToFiletime(fd.CreatedAt), // 创建时间
			LastAccessTime: timeToFiletime(fd.UpdatedAt), // 最后访问
			LastWriteTime:  timeToFiletime(fd.UpdatedAt), // 最后写入
			ChangeTime:     timeToFiletime(fd.UpdatedAt), // 变更时间
			IsDirectory:    true,                         // 是目录
			FileIndex:      uint64(fd.ID),                // 索引
		})
	}
	for i := range files { // 文件条目
		fl := &files[i] // 取行
		// 条目级可见性(受限文件对非组成员隐藏,同 HTTP 列表过滤)
		if err := checkItemVisible(info, fl.VisibleToGroups); err != nil {
			continue
		}
		us := uint64(fl.FileSize)   // 大小转无符号
		out = append(out, FileInfo{ // 组装条目
			Name:           fl.Name,                      // 文件名
			EndOfFile:      us,                           // 大小
			AllocationSize: us,                           // 分配(简化 = 大小)
			CreationTime:   timeToFiletime(fl.CreatedAt), // 创建时间
			LastAccessTime: timeToFiletime(fl.UpdatedAt), // 最后访问
			LastWriteTime:  timeToFiletime(fl.UpdatedAt), // 最后写入
			ChangeTime:     timeToFiletime(fl.UpdatedAt), // 变更时间
			IsDirectory:    false,                        // 是文件
			FileIndex:      uint64(fl.ID),                // 索引
		})
	}
	smbDebugf("smb_gateway: 列目录 句柄=%d 条目=%d(过滤后)", h.handleID, len(out)) // 记录列目录结果(句柄/条目数)
	return out, nil                                                      // 返回条目列表
}

// Close 关闭句柄(CodeFileClose)。
// 参数:ctx 上下文;handleID 远程句柄 ID。
// 返回值:错误按哨兵语义。
// 实现:
//  1. 注销句柄(幂等:句柄已注销返回 nil);
//  2. 非目录且带写回缓存:整体上传对象存储,同步记录与桶用量;
//  3. delete_on_close:上传后删除路径。
func (f *FileOpsService) Close(ctx context.Context, handleID uint64) error {
	h := f.handles.Get(handleID) // 定位句柄
	if h == nil {                // 已注销?
		return nil // 幂等:句柄已注销
	}
	f.handles.Delete(handleID) // 先注销(后续操作不再可寻址)

	var flushErr error                   // 冲刷错误记录(delete_on_close 时优先返回删除错误)
	if !h.isDir && h.writeCache != nil { // 文件句柄且有写回缓存?
		h.mu.Lock()           // 锁句柄
		spec := h.flushSpec() // 冲刷快照(逻辑 EOF/截断点/写段,一次性取出)
		if spec.dirty {       // 有变化?
			// 配额审查(写入前):与 Flush/提前冲刷一致,超限拒绝(STATUS_DISK_FULL);
			// 此前 Close 直接冲刷绕过审查,已超额用户可经此持续增长,此处补齐。
			flushErr = f.checkWriteQuota(ctx, h)
			if flushErr == nil { // 审查通过?
				flushErr = h.writeCache.FlushToStorage(ctx, f.st, utils.BucketEncoder(h.bucketID), h.objectKey, spec) // 整体上传(快照拼装)
				if flushErr == nil {
					h.truncateAt = -1 // 截断已固化
				}
			} else {
				smbErrorf("smb_gateway: 关闭句柄=%d 配额审查拒绝:%v", h.handleID, flushErr) // 记录拒绝原因
				// 清理新建文件的 0 字节残留(审查拒绝 = 本次内容未落盘;记录为新建
				// 且未写入过时删记录防幽灵文件;覆盖已有文件时旧数据仍在,保留记录)
				var rec model.File
				if err := core.DB.WithContext(ctx).First(&rec, h.fileID).Error; err == nil && rec.FileSize == 0 {
					if derr := core.DB.WithContext(ctx).Unscoped().Delete(&model.File{}, h.fileID).Error; derr != nil {
						smbErrorf("smb_gateway: 关闭句柄=%d 清理幽灵记录 %d 失败:%v", h.handleID, h.fileID, derr)
					}
					if derr := f.st.Delete(ctx, utils.BucketEncoder(h.bucketID), h.objectKey); derr != nil { // 幂等
						smbErrorf("smb_gateway: 关闭句柄=%d 清理幽灵对象 %s/%s 失败:%v", h.handleID, utils.BucketEncoder(h.bucketID), h.objectKey, derr)
					}
				}
			}
		}
		h.mu.Unlock()        // 解锁
		if flushErr == nil { // 上传成功?
			flushErr = f.syncFileMeta(ctx, h) // 同步记录大小与桶用量
		}
	}
	if h.deleteOnClose { // 关闭即删?
		if err := f.Unlink(ctx, &UnlinkArgs{BucketID: h.bucketID, Path: h.smbPath}); err != nil { // 删除路径
			smbDebugf("smb_gateway: 关闭句柄=%d 关闭即删失败:%v", h.handleID, err) // 记录关闭即删失败
			return err                                                   // 删除失败优先上报
		}
		smbDebugf("smb_gateway: 关闭句柄=%d 关闭即删已删除 路径=%q", h.handleID, h.smbPath) // 记录关闭即删成功
		return nil
	}
	smbDebugf("smb_gateway: 关闭句柄=%d 冲刷错误=%v", h.handleID, flushErr) // 记录句柄关闭(含冲刷结果)
	return flushErr                                                 // 返回冲刷结果(无缓存时为 nil)
}

// Unlink 删除路径(CodeFileUnlink;目录须为空)。
// 参数:ctx 上下文;args 删除请求(路径 + 可选桶 ID)。
// 返回值:错误按哨兵语义。
// 实现:解析共享 → 解析父目录 → 目录分支复用 server.DeleteDir(进回收站)/
// 文件分支复用 server.DeleteFile(软删进回收站,记录/对象/用量保留)。
func (f *FileOpsService) Unlink(ctx context.Context, args *UnlinkArgs) error {
	// 操作者身份(同 Open:操作帧 userId 解析;缺失/无效 → 拒绝)
	info := sessionFrom(ctx)
	if info == nil {
		return server.ErrForbidden
	}
	share := f.LookupShareByPath(args.Path) // 按路径首段查共享缓存
	if share.BucketID == 0 {                // 未命中?
		share = f.resolveShareByPath(ctx, args.Path) // 按桶名查库
	}
	if share.BucketID == 0 && args.BucketID > 0 { // 仍无且带桶 ID?
		share = f.shareByID(ctx, args.BucketID) // 按桶主键查库
	}
	if share.BucketID == 0 { // 解析不到?
		return server.ErrNotFound // 报未找到
	}
	// 桶可用 + 桶权限(同 HTTP 桶级校验)
	bucket, err := server.CheckBucketUsable(ctx, server.CheckBucketUsableArg{BucketID: share.BucketID})
	if err != nil {
		return err
	}
	if err := checkBucketAccess(ctx, info, bucket); err != nil {
		return err
	}
	path := cleanSmbPath(args.Path) // 归一化路径
	segs := splitSmbPath(path)      // 拆段
	if len(segs) == 0 {             // 空路径(删共享根)?
		return server.ErrInvalidInput // 非法输入
	}
	userID := info.UserID // 操作者(落库/权限用)

	// 解析到父目录(最后一段是目标)。
	folderID, err := f.resolveDir(ctx, share.BucketID, path, false, userID) // 父链解析(不建)
	if err != nil {
		return err // 父链失败上抛
	}
	name := lastSegment(path) // 目标名

	// 目录还是文件?
	fid, ferr := f.lookupFolder(ctx, share.BucketID, folderID, name, "isable <> 0") // 查目标是否为目录
	if ferr == nil {                                                                // 是目录
		// 目录自身与祖先链可见性(同 HTTP precheckFolderRead)
		if err := checkAncestorsVisible(ctx, info, share.BucketID, fid); err != nil {
			return err
		}
		// 目录:复用 server 层删除(落删除任务 + 置"正在删除"拒写,后台先删内部
		// 再删文件夹自身,内部条目软删进回收站),与 Web 端同一删除语义。
		taskID, err := server.DeleteDir(ctx, server.DeleteDirArg{
			UserID: userID, BucketID: share.BucketID, DirID: fid,
		})
		if err != nil {
			smbErrorf("smb_gateway: 删除目录失败 路径=%q 文件夹=%d:%v", path, fid, err) // 记录目录删除失败
			return err                                                       // 失败上抛
		}
		// 防御:返回 taskID>0(如未来回收站清除复用该通道)才同步处理
		if taskID > 0 {
			if err := server.ProcessDeleteTask(ctx, server.ProcessDeleteTaskArg{TaskID: taskID}); err != nil {
				// 物理清理失败:任务保留(启动/cron 续跑),SMB 返回错误,客户端可重试
				smbErrorf("smb_gateway: 删除目录失败 路径=%q 文件夹=%d 任务=%d:%v", path, fid, taskID, err)
				return err
			}
		}
		smbDebugf("smb_gateway: 删除目录成功 路径=%q 文件夹=%d", path, fid)                         // 记录目录删除成功
		smbInfof("smb_gateway: 文件变更 删除目录 user=%d(%s) 目录=%d", userID, info.Username, fid) // 每次文件变更仅记录一次(Info 级:操作者+被操作文件)
		return nil
	}
	// 文件:复用 server 删除逻辑(先做条目级可见性校验,同 HTTP precheckFileRead)。
	fileID, _, exists, err := f.lookupFile(ctx, share.BucketID, folderID, name) // 查文件记录
	if err != nil || !exists {                                                  // 查错或不存在?
		return server.ErrNotFound // 报未找到
	}
	if err := checkAncestorsVisible(ctx, info, share.BucketID, folderID); err != nil {
		return err
	}
	fl, lerr := f.loadFileRecord(ctx, share.BucketID, folderID, name)
	if lerr != nil {
		return server.ErrNotFound
	}
	if err := checkItemVisible(info, fl.VisibleToGroups); err != nil {
		return err // 受限文件:非组成员不可删
	}
	if err := server.DeleteFile(ctx, server.DeleteFileArg{ // 调 server 层删除
		UserID:   userID,         // 操作者(真实身份)
		BucketID: share.BucketID, // 桶
		FileID:   fileID,         // 文件
	}); err != nil { // 删除失败
		return err // 上抛
	}
	smbDebugf("smb_gateway: 删除文件成功 路径=%q 文件=%d", path, fileID)                          // 记录文件删除成功
	smbInfof("smb_gateway: 文件变更 删除文件 user=%d(%s) 文件=%d", userID, info.Username, fileID) // 每次文件变更仅记录一次(Info 级:操作者+被操作文件)
	return nil
}

// Rename 重命名/移动(CodeFileRename;目标已存在时按 ReplaceIfExists 覆盖文件目标)。
// 参数:ctx 上下文;args 重命名请求(源/目标路径,须同桶)。
// 返回值:错误按哨兵语义。
// 实现:解析共享 → 解析源/目标父目录 → 目标处理(文件:按覆盖标志删除;
// 目录:一律拒绝)→ 目录分支 MoveFolder / 文件分支 MoveFile。
func (f *FileOpsService) Rename(ctx context.Context, args *RenameArgs) error {
	// 操作者身份(同 Open:操作帧 userId 解析;缺失/无效 → 拒绝)
	info := sessionFrom(ctx)
	if info == nil {
		return server.ErrForbidden
	}
	share := f.LookupShareByPath(args.FromPath) // 按源路径首段查缓存
	if share.BucketID == 0 {                    // 未命中?
		share = f.resolveShareByPath(ctx, args.FromPath) // 按桶名查库
	}
	if share.BucketID == 0 && args.BucketID > 0 { // 仍无且带桶 ID?
		share = f.shareByID(ctx, args.BucketID) // 按桶主键查库
	}
	if share.BucketID == 0 { // 解析不到?
		return server.ErrNotFound // 报未找到
	}
	// 桶可用 + 桶权限(同 HTTP 桶级校验)
	bucket, err := server.CheckBucketUsable(ctx, server.CheckBucketUsableArg{BucketID: share.BucketID})
	if err != nil {
		return err
	}
	if err := checkBucketAccess(ctx, info, bucket); err != nil {
		return err
	}
	from := cleanSmbPath(args.FromPath)         // 归一化源路径
	to := cleanSmbPath(args.ToPath)             // 归一化目标路径
	fromSegs := splitSmbPath(from)              // 源段
	toSegs := splitSmbPath(to)                  // 目标段
	if len(fromSegs) == 0 || len(toSegs) == 0 { // 任一为空(操作共享根)?
		return server.ErrInvalidInput // 非法输入
	}
	userID := info.UserID // 操作者(落库/权限用)

	fromFolderID, err := f.resolveDir(ctx, share.BucketID, from, false, userID) // 源父目录
	if err != nil {
		return err // 失败上抛
	}
	toFolderID, err := f.resolveDir(ctx, share.BucketID, to, false, userID) // 目标父目录
	if err != nil {
		return err // 失败上抛
	}
	fromName := lastSegment(from) // 源名
	toName := lastSegment(to)     // 目标名

	// 源条目可见性(同 HTTP precheckFileRead:源可读才可移动/重命名)
	if err := checkAncestorsVisible(ctx, info, share.BucketID, fromFolderID); err != nil {
		return err
	}
	if fid, ferr := f.lookupFolder(ctx, share.BucketID, fromFolderID, fromName, "isable <> 0"); ferr == nil { // 源是目录?
		folder, gerr := server.GetFolder(ctx, server.GetFolderArg{BucketID: share.BucketID, FolderID: fid})
		if gerr != nil {
			return gerr
		}
		if err := checkItemVisible(info, folder.VisibleToGroups); err != nil {
			return err
		}
	} else {
		fl, lerr := f.loadFileRecord(ctx, share.BucketID, fromFolderID, fromName)
		if lerr != nil {
			return server.ErrNotFound
		}
		if err := checkItemVisible(info, fl.VisibleToGroups); err != nil {
			return err
		}
	}
	// 目标父目录可见性(同 HTTP 写路径:受限目标目录下不可写入)
	if err := checkAncestorsVisible(ctx, info, share.BucketID, toFolderID); err != nil {
		return err
	}

	// 目标处理:目录目标一律拒绝(递归覆盖不可靠,安全兜底);
	// 文件目标按 ReplaceIfExists 覆盖(删除后移动,Windows/WPS 保存覆盖依赖)。
	if _, ferr := f.lookupFolder(ctx, share.BucketID, toFolderID, toName, "isable <> 0"); ferr == nil { // 目标已是目录?
		return server.ErrConflict // 拒绝(目录覆盖不做)
	}
	targetFileID, _, targetExists, _ := f.lookupFile(ctx, share.BucketID, toFolderID, toName) // 查目标文件
	if targetExists {                                                                         // 目标文件已存在?
		if !args.ReplaceIfExists { // 未声明覆盖?
			return server.ErrConflict // 拒绝(冲突)
		}
		if err := server.DeleteFile(ctx, server.DeleteFileArg{ // 覆盖:先删目标文件(对象+记录+归还空间)
			UserID:   userID,         // 操作者
			BucketID: share.BucketID, // 桶
			FileID:   targetFileID,   // 目标文件记录
		}); err != nil { // 删除失败?
			return err // 上抛(不执行移动,避免半覆盖)
		}
	}

	// 目录还是文件?
	if fid, ferr := f.lookupFolder(ctx, share.BucketID, fromFolderID, fromName, "isable <> 0"); ferr == nil { // 源是目录?
		if _, err := server.MoveFolder(ctx, server.MoveFolderArg{ // 移动目录树
			UserID:      userID,                              // 操作者(真实身份)
			SrcBucketID: share.BucketID,                      // 源桶
			SrcFolderID: fid,                                 // 源目录
			DstBucketID: share.BucketID,                      // 目标桶(须同桶)
			DstDirPath:  joinSmbPath(toSegs[:len(toSegs)-1]), // 目标父路径
			DstName:     toName,                              // 新目录名
		}); err != nil { // 移动失败
			return err // 上抛
		}
		smbDebugf("smb_gateway: 重命名成功 %q → %q(目录=%d)", from, to, fid)                                       // 记录目录重命名成功
		smbInfof("smb_gateway: 文件变更 重命名目录 user=%d(%s) 目录=%d %q → %q", userID, info.Username, fid, from, to) // 每次文件变更仅记录一次(Info 级:操作者+被操作文件)
		return nil
	}
	fileID, _, exists, err := f.lookupFile(ctx, share.BucketID, fromFolderID, fromName) // 查源文件
	if err != nil || !exists {                                                          // 查错或不存在?
		return server.ErrNotFound // 报未找到
	}
	if _, err := server.MoveFile(ctx, server.MoveFileArg{ // 移动文件
		UserID:      userID,                              // 操作者(真实身份)
		SrcBucketID: share.BucketID,                      // 源桶
		SrcFileID:   fileID,                              // 源文件
		DstBucketID: share.BucketID,                      // 目标桶
		DstDirPath:  joinSmbPath(toSegs[:len(toSegs)-1]), // 目标父路径
		DstName:     toName,                              // 新文件名
	}); err != nil { // 移动失败
		return err // 上抛
	}
	smbDebugf("smb_gateway: 重命名成功 %q → %q(文件=%d)", from, to, fileID)                                       // 记录文件重命名成功
	smbInfof("smb_gateway: 文件变更 重命名文件 user=%d(%s) 文件=%d %q → %q", userID, info.Username, fileID, from, to) // 每次文件变更仅记录一次(Info 级:操作者+被操作文件)
	return nil
}

// CloseAllByConn 断开连接时回收该连接的全部句柄(handleConn defer 调用)。
// 参数:connID 连接 ID(0 = 无效,直接返回)。
func (f *FileOpsService) CloseAllByConn(connID uint64) {
	if connID == 0 { // 无效连接 ID?
		return // 直接返回
	}
	closed := 0                         // 关闭计数(汇总日志)
	for _, h := range f.handles.All() { // 遍历全部句柄
		if h.connID == connID { // 属于该连接?
			if cerr := f.Close(context.Background(), h.handleID); cerr != nil { // 逐个关闭(触发上传/删除)
				smbDebugf("smb_gateway: 断连回收 句柄=%d 关闭失败:%v", h.handleID, cerr) // 断连场景尽力而为,记录
			}
			closed++ // 计数
		}
	}
	smbDebugf("smb_gateway: 连接 %d 关闭,回收 %d 个句柄", connID, closed) // 记录断连句柄回收汇总
}

// isRangeNotSatisfiable 判断对象存储范围读越界错误(S3 416)。
// 实现:按错误文本特征匹配(S3/minio 各实现的常见表述)。
func isRangeNotSatisfiable(err error) bool {
	msg := strings.ToLower(err.Error())                         // 错误文本转小写
	return strings.Contains(msg, "range is not satisfiable") || // 完整表述 1
		strings.Contains(msg, "range not satisfiable") || // 完整表述 2
		strings.Contains(msg, "satisfiable") || // 模糊匹配
		strings.Contains(msg, "416") || // 状态码
		strings.Contains(msg, "invalidrange") // minio 变体
}
