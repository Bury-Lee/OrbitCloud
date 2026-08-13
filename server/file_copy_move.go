// file_copy_move.go —— 桶内条目复制与移动:文件/文件夹复制(含跨桶)、
// 同桶 O(1) 移动重命名(剪切)。
//
// 源/目标桶权限由 api 层预检,本文件只做源对象状态与目标目录可行性校验;
// 目标目录由内部 mkdir -p 解析,其可见性用 checkAncestorsAccessTree 校验。
package server

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"orbitcloud/common"
	"orbitcloud/core"
	"orbitcloud/log"
	"orbitcloud/model"
	"orbitcloud/utils"
)

// CopyFileArg 文件复制入参。
type CopyFileArg struct {
	UserID       uint   // 操作者 users.id(目标记录 UploadedBy)
	SrcBucketID  uint   // 源桶
	SrcFileID    uint   // 源文件 files.id
	DstBucketID  uint   // 目标桶
	DstDirPath   string // 目标目录(空串 = 桶根;mkdir -p)
	DstFilename  string // 目标文件名(空 = 沿用源名)
}

// CopyFile 复制文件(可同桶/跨桶):校验源与目标桶 → 解析目标目录 →
// 同名自动重命名 → 建新记录 → 对象复制(Get 源 → Put 新 key)→ 目标桶 UsedSpace 原子自增。
// 复制 = 新记录+新对象,与源完全独立;失败补偿删新记录。
// 错误语义:源不存在/目标桶不存在 → ErrNotFound;桶禁用 → ErrForbidden。
func CopyFile(ctx context.Context, arg CopyFileArg) (*model.File, error) {
	userID, srcBucketID, srcFileID, dstBucketID, dstDirPath, dstFilename := arg.UserID, arg.SrcBucketID, arg.SrcFileID, arg.DstBucketID, arg.DstDirPath, arg.DstFilename
	// 校验源(桶可用 + 文件存在 + Isable)
	srcBucket, err := CheckBucketUsable(ctx, CheckBucketUsableArg{BucketID: srcBucketID})
	if err != nil {
		return nil, err
	}
	srcFile, err := loadFile(ctx, srcBucketID, srcFileID)
	if err != nil {
		return nil, err
	}
	if err := checkAncestorsUsable(ctx, srcBucketID, srcFile.FolderID); err != nil {
		return nil, err
	}

	// 校验目标桶(存在 + Status==1)
	dstBucket, err := GetBucket(ctx, GetBucketArg{ID: dstBucketID})
	if err != nil {
		return nil, err
	}
	if dstBucket.Status != 1 {
		return nil, ErrForbidden
	}

	// 目标目录规范化 + 目标名(缺省沿用源名)
	dstDir, err := common.NormalizeDirPath(dstDirPath)
	if err != nil {
		return nil, err
	}
	dstName := strings.TrimSpace(dstFilename)
	if dstName == "" {
		dstName = srcFile.Name
	} else if err := common.ValidateItemName(dstName); err != nil {
		return nil, err
	}

	// 解析目标目录(建父链)+ 同名冲突自动重命名
	dstFolderID, err := common.ResolveDirPath(ctx, userID, dstBucketID, dstDir)
	if err != nil {
		return nil, err
	}
	// 目标目录可用性与条目级可见性校验
	if err := checkAncestorsUsable(ctx, dstBucketID, dstFolderID); err != nil {
		return nil, err
	}
	// 目标目录条目级可见性(受限目标目录下不可写入)
	if err := checkAncestorsAccessTree(ctx, userID, dstBucketID, dstFolderID); err != nil {
		return nil, err
	}
	dstName, err = uniqueName(ctx, dstBucketID, dstFolderID, dstName)
	if err != nil {
		return nil, err
	}

	// 先建新记录拿主键(可见组继承源,防复制后泄露给全桶)
	f := &model.File{
		BucketID:        dstBucketID,
		FolderID:        dstFolderID,
		Name:            dstName,
		FileSize:        srcFile.FileSize,
		FileType:        srcFile.FileType,
		MD5:             srcFile.MD5,
		UploadedBy:      userID,
		VisibleToGroups: srcFile.VisibleToGroups,
	}
	if err := core.DB.WithContext(ctx).Create(f).Error; err != nil {
		return nil, fmt.Errorf("copy file: create record: %w", err)
	}

	// 对象复制(Get 源 → Put 新 key;失败补偿:删记录)
	rc, err := core.Storage.Get(ctx, utils.BucketEncoder(srcBucket.ID), objectKeyForFile(srcFile.ID))
	if err != nil {
		_ = core.DB.Unscoped().Delete(&model.File{}, f.ID)
		if errors.Is(err, core.ErrObjectNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	defer rc.Close()

	newKey := objectKeyForFile(f.ID)
	if err := core.Storage.Put(ctx, utils.BucketEncoder(dstBucket.ID), newKey, rc, srcFile.FileSize); err != nil {
		_ = core.DB.Unscoped().Delete(&model.File{}, f.ID)
		return nil, err
	}

	// 更新目标桶 UsedSpace;失败补偿:删新对象 + 删记录
	err = core.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.Model(&model.Bucket{}).Where("id = ?", dstBucketID).
			Update("used_space", gorm.Expr("used_space + ?", srcFile.FileSize)).Error
	})
	if err != nil {
		_ = core.Storage.Delete(ctx, utils.BucketEncoder(dstBucket.ID), newKey)
		_ = core.DB.Unscoped().Delete(&model.File{}, f.ID)
		return nil, fmt.Errorf("copy file: finalize record: %w", err)
	}

	log.Infof("copy: user %d file %d (bucket %d) -> bucket %d dir %q name %q new id %d", userID, srcFileID, srcBucketID, dstBucketID, dstDir, dstName, f.ID)
	return f, nil
}

// CopyFolderArg 文件夹复制入参。
type CopyFolderArg struct {
	UserID      uint   // 操作者 users.id(目标记录 UploadedBy)
	SrcBucketID uint   // 源桶
	SrcFolderID uint   // 源文件夹 folders.id
	DstBucketID uint   // 目标桶
	DstDirPath  string // 目标父目录(空串 = 桶根;mkdir -p)
	DstName     string // 目标顶层目录名(空 = 沿用源目录名;Linux mv 语义改名复制)
}

// CopyFolder 复制文件夹(同桶/跨桶均可):校验源与目标桶 → 解析目标父目录 →
// 目标名冲突自动重命名 → 同步预建目标顶层目录(拿真实 ID)→ 落 CopyTask →
// 同步触发 ProcessCopyFolderTask 后台深度优先复制子树。
// 返回目标侧新建的顶层目录记录;子树由后台任务逐步完成。
func CopyFolder(ctx context.Context, arg CopyFolderArg) (*model.Folder, error) {
	userID, srcBucketID, srcFolderID, dstBucketID, dstDirPath, dstName := arg.UserID, arg.SrcBucketID, arg.SrcFolderID, arg.DstBucketID, arg.DstDirPath, arg.DstName
	// 校验源(桶可用 + 目录存在 + Isable)
	if _, err := CheckBucketUsable(ctx, CheckBucketUsableArg{BucketID: srcBucketID}); err != nil {
		return nil, err
	}
	srcFolder, err := loadFolder(ctx, srcBucketID, srcFolderID)
	if err != nil {
		return nil, err
	}
	if err := checkAncestorsUsable(ctx, srcBucketID, srcFolderID); err != nil {
		return nil, err
	}

	// 校验目标桶(存在 + Status==1)
	dstBucket, err := GetBucket(ctx, GetBucketArg{ID: dstBucketID})
	if err != nil {
		return nil, err
	}
	if dstBucket.Status != 1 {
		return nil, ErrForbidden
	}

	// 目标父目录规范化 + 解析(建父链)
	dstDir, err := common.NormalizeDirPath(dstDirPath)
	if err != nil {
		return nil, err
	}
	dstFolderID, err := common.ResolveDirPath(ctx, userID, dstBucketID, dstDir)
	if err != nil {
		return nil, err
	}
	// 目标父目录可用性与条目级可见性校验
	if err := checkAncestorsUsable(ctx, dstBucketID, dstFolderID); err != nil {
		return nil, err
	}
	// 目标父目录条目级可见性(受限目标目录下不可写入)
	if err := checkAncestorsAccessTree(ctx, userID, dstBucketID, dstFolderID); err != nil {
		return nil, err
	}

	// 目标根目录名冲突自动重命名(dstName 缺省沿用源目录名,Linux mv 语义)
	baseName := srcFolder.Name
	if s := strings.TrimSpace(dstName); s != "" {
		baseName = s
		if err := common.ValidateItemName(baseName); err != nil {
			return nil, err
		}
	}
	dstName2, err := uniqueName(ctx, dstBucketID, dstFolderID, baseName)
	if err != nil {
		return nil, err
	}
	dstName = dstName2

	// 同步预建目标顶层目录(拿真实 ID,任务续跑时直接复用;可见组继承源,防泄露给全桶)
	topFolder := &model.Folder{
		BucketID:        dstBucketID,
		ParentID:        dstFolderID,
		Name:            dstName,
		UploadedBy:      userID,
		Isable:          true,
		VisibleToGroups: srcFolder.VisibleToGroups,
	}
	if err := core.DB.WithContext(ctx).Create(topFolder).Error; err != nil {
		if isUniqueViolation(err) {
			return nil, ErrConflict // 并发抢占同名
		}
		return nil, fmt.Errorf("copy folder: create top folder: %w", err)
	}

	// 落复制任务(DstFolderID = 刚预建的顶层目录)
	task := &model.CopyTask{
		BucketID:       srcBucketID,
		SourceFolderID: srcFolderID,
		DstBucketID:    dstBucketID,
		DstFolderID:    topFolder.ID,
		DstDirPath:     dstDir,
		DstName:        dstName,
		UploadedBy:     userID,
		Status:         0,
	}
	if err := core.DB.WithContext(ctx).Create(task).Error; err != nil {
		_ = core.DB.Unscoped().Delete(&model.Folder{}, topFolder.ID) // 补偿:删预建目录
		return nil, fmt.Errorf("copy folder: create copy task: %w", err)
	}

	// 同步触发处理(尽力而为,失败由启动/cron 续跑)
	if err := ProcessCopyFolderTask(ctx, ProcessCopyFolderTaskArg{TaskID: task.ID}); err != nil {
		log.Errorf("copy folder: process task %d: %v", task.ID, err)
	}

	log.Infof("copy folder: user %d folder %d (bucket %d) -> bucket %d dir %q name %q task %d", userID, srcFolderID, srcBucketID, dstBucketID, dstDir, dstName, task.ID)
	return topFolder, nil
}

// MoveFileArg 文件移动入参。
type MoveFileArg struct {
	UserID      uint   // 操作者 users.id
	SrcBucketID uint   // 源桶
	SrcFileID   uint   // 源文件 files.id
	DstBucketID uint   // 目标桶(缺省同桶)
	DstDirPath  string // 目标目录(空串 = 桶根;mkdir -p)
	DstName     string // 新文件名(空 = 沿用源名)
}

// MoveFile 移动/重命名文件(剪切):同桶 = 单条元数据更新(FolderID + Name);
// 跨桶 = 复制 + 删源(失败则整体失败,源保留)。
// 错误语义:源不存在 → ErrNotFound;同目录同名(大小写不敏感)无操作 → ErrInvalidInput。
func MoveFile(ctx context.Context, arg MoveFileArg) (*model.File, error) {
	userID, srcBucketID, srcFileID, dstBucketID, dstDirPath, dstName := arg.UserID, arg.SrcBucketID, arg.SrcFileID, arg.DstBucketID, arg.DstDirPath, arg.DstName
	// 校验源(桶可用 + 文件存在 + Isable)
	if _, err := CheckBucketUsable(ctx, CheckBucketUsableArg{BucketID: srcBucketID}); err != nil {
		return nil, err
	}
	file, err := loadFile(ctx, srcBucketID, srcFileID)
	if err != nil {
		return nil, err
	}
	if err := checkAncestorsUsable(ctx, srcBucketID, file.FolderID); err != nil {
		return nil, err
	}

	// 目标目录规范化 + 新名(缺省沿用源文件名)
	dstDir, err := common.NormalizeDirPath(dstDirPath)
	if err != nil {
		return nil, err
	}
	newName := strings.TrimSpace(dstName)
	if newName == "" {
		newName = file.Name
	} else if err := common.ValidateItemName(newName); err != nil {
		return nil, err
	}

	// 同桶:直接改 (FolderID, Name)
	if dstBucketID == srcBucketID {
		f, err := moveFileSameBucket(ctx, userID, srcBucketID, file, dstDir, newName)
		if err != nil {
			return nil, err
		}
		log.Infof("move: user %d file %d moved/renamed in bucket %d -> dir %q name %q (id %d)", userID, srcFileID, srcBucketID, dstDir, newName, f.ID)
		return f, nil
	}

	// 跨桶:剪切 = 复制 + 删源
	f, err := CopyFile(ctx, CopyFileArg{
		UserID: userID, SrcBucketID: srcBucketID, SrcFileID: srcFileID,
		DstBucketID: dstBucketID, DstDirPath: dstDirPath, DstFilename: dstName,
	})
	if err != nil {
		return nil, err // 源保留,不产生半成品
	}
	if err := DeleteFile(ctx, DeleteFileArg{UserID: userID, BucketID: srcBucketID, FileID: srcFileID}); err != nil {
		return nil, err // 目标已有副本,源未删,调用方可重试
	}
	log.Infof("move: user %d file %d -> bucket %d dir %q name %q (cross-bucket, new id %d)", userID, srcFileID, dstBucketID, dstDir, newName, f.ID)
	return f, nil
}

// moveFileSameBucket 同桶移动/重命名文件:单条元数据更新(FolderID + Name,对象 key = 主键不变)。
func moveFileSameBucket(ctx context.Context, userID, bucketID uint, file *model.File, dstDir, newName string) (*model.File, error) {
	// 解析目标目录(建父链,拿 dstFolderID)
	dstFolderID, err := common.ResolveDirPath(ctx, userID, bucketID, dstDir)
	if err != nil {
		return nil, err
	}
	// 目标目录可用性与条目级可见性校验
	if err := checkAncestorsUsable(ctx, bucketID, dstFolderID); err != nil {
		return nil, err
	}
	// 目标目录条目级可见性(受限目标目录下不可写入)
	if err := checkAncestorsAccessTree(ctx, userID, bucketID, dstFolderID); err != nil {
		return nil, err
	}

	// 位置未变化 → 无操作(folderID + 大小写不敏感名比较)
	if dstFolderID == file.FolderID && strings.EqualFold(newName, file.Name) {
		return nil, ErrInvalidInput
	}

	// 同名冲突自动重命名
	cand := newName
	exists, err := nameExists(ctx, bucketID, dstFolderID, cand)
	if err != nil {
		return nil, err
	}
	if exists {
		cand, err = uniqueName(ctx, bucketID, dstFolderID, cand)
		if err != nil {
			return nil, err
		}
	}

	// 单条元数据更新(对象 key = 主键不变)
	file.FolderID = dstFolderID
	file.Name = cand
	if err := core.DB.WithContext(ctx).Save(file).Error; err != nil {
		if isUniqueViolation(err) {
			return nil, ErrConflict
		}
		return nil, fmt.Errorf("move file %d: %w", file.ID, err)
	}
	return file, nil
}

// MoveFolderArg 文件夹移动入参。
type MoveFolderArg struct {
	UserID      uint   // 操作者 users.id
	SrcBucketID uint   // 源桶
	SrcFolderID uint   // 源文件夹 folders.id
	DstBucketID uint   // 目标桶(缺省同桶;跨桶 MVP 拒绝)
	DstDirPath  string // 目标目录(空串 = 桶根;mkdir -p)
	DstName     string // 新目录名(空 = 沿用源名)
}

// MoveFolder 移动/重命名文件夹(剪切):同桶 = 单条 UPDATE parent_id + name(O(1),
// 子树零改动);跨桶 MVP 拒绝。禁止移动到自身子树(防套娃)。
// 错误语义:源不存在 → ErrNotFound;无变化/跨桶/套娃 → ErrInvalidInput。
func MoveFolder(ctx context.Context, arg MoveFolderArg) (*model.Folder, error) {
	userID, srcBucketID, srcFolderID, dstBucketID, dstDirPath, dstName := arg.UserID, arg.SrcBucketID, arg.SrcFolderID, arg.DstBucketID, arg.DstDirPath, arg.DstName
	// 校验源(桶可用 + 目录存在 + Isable)
	if _, err := CheckBucketUsable(ctx, CheckBucketUsableArg{BucketID: srcBucketID}); err != nil {
		return nil, err
	}
	folder, err := loadFolder(ctx, srcBucketID, srcFolderID)
	if err != nil {
		return nil, err
	}
	if err := checkAncestorsUsable(ctx, srcBucketID, srcFolderID); err != nil {
		return nil, err
	}

	// 跨桶:MVP 拒绝
	if dstBucketID != srcBucketID {
		return nil, ErrInvalidInput // 跨桶移动文件夹 MVP 不支持
	}

	// 目标目录规范化 + 新名(缺省沿用源目录名)
	dstDir, err := common.NormalizeDirPath(dstDirPath)
	if err != nil {
		return nil, err
	}
	newName := strings.TrimSpace(dstName)
	if newName == "" {
		newName = folder.Name
	} else if err := common.ValidateItemName(newName); err != nil {
		return nil, err
	}

	// 同桶 O(1) 移动
	f, err := moveFolderSameBucket(ctx, userID, srcBucketID, folder, dstDir, newName)
	if err != nil {
		return nil, err
	}
	log.Infof("move: user %d folder %d moved/renamed in bucket %d -> dir %q name %q (id %d)", userID, srcFolderID, srcBucketID, dstDir, newName, f.ID)
	return f, nil
}

// moveFolderSameBucket 同桶移动/重命名文件夹:单事务 UPDATE parent_id + name(O(1),子树零改动)。
func moveFolderSameBucket(ctx context.Context, userID, bucketID uint, folder *model.Folder, dstDir, newName string) (*model.Folder, error) {
	// 解析目标目录(建父链,拿 dstFolderID)
	dstFolderID, err := common.ResolveDirPath(ctx, userID, bucketID, dstDir)
	if err != nil {
		return nil, err
	}
	// 目标目录可用性(移动到自身子树由下方防套娃兜底)
	if err := checkAncestorsUsable(ctx, bucketID, dstFolderID); err != nil {
		return nil, err
	}
	// 目标目录条目级可见性(受限目标目录下不可写入)
	if err := checkAncestorsAccessTree(ctx, userID, bucketID, dstFolderID); err != nil {
		return nil, err
	}

	// 位置未变化 → 无操作(folderID + 大小写不敏感名比较)
	if dstFolderID == folder.ParentID && strings.EqualFold(newName, folder.Name) {
		return nil, ErrInvalidInput
	}

	// 同名冲突自动重命名
	cand := newName
	exists, err := nameExists(ctx, bucketID, dstFolderID, cand)
	if err != nil {
		return nil, err
	}
	if exists {
		cand, err = uniqueName(ctx, bucketID, dstFolderID, cand)
		if err != nil {
			return nil, err
		}
	}

	// 防套娃:沿目标父链向上遇到自身 → ErrInvalidInput
	cur := dstFolderID
	for cur != 0 {
		if cur == folder.ID {
			return nil, ErrInvalidInput
		}
		p, err := loadFolder(ctx, bucketID, cur)
		if err != nil {
			return nil, err
		}
		cur = p.ParentID
	}

	// 核心:单条更新,子树零改动,路径读取时按新链派生
	if err := core.DB.WithContext(ctx).Model(&model.Folder{}).Where("id = ?", folder.ID).
		Updates(map[string]any{
			"parent_id":  dstFolderID,
			"name":       cand,
			"name_lower": strings.ToLower(cand),
		}).Error; err != nil {
		if isUniqueViolation(err) {
			return nil, ErrConflict
		}
		return nil, fmt.Errorf("move folder %d: %w", folder.ID, err)
	}
	folder.ParentID = dstFolderID
	folder.Name = cand
	return folder, nil
}
