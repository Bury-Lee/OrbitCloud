// file_upload.go —— 桶内条目写入:上传文件 / 创建文件夹(均 mkdir -p 建父链)。
//
// 桶级权限由 api 层预检,本文件做可行性判断(桶状态、路径规范、Isable 链、
// 同名冲突、流式落盘);目标目录由 mkdir -p 内部解析,其可见性用
// checkAncestorsAccessTree(见 visibility.go)校验。
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"gorm.io/gorm"

	"orbitcloud/common"
	"orbitcloud/core"
	"orbitcloud/log"
	"orbitcloud/model"
	"orbitcloud/utils"
)

// UploadFileArg 上传文件入参。
type UploadFileArg struct {
	UserID   uint      // 操作者 users.id
	BucketID uint      // 目标桶 buckets.id
	DirPath  string    // 目标目录路径(mkdir -p;空串 = 桶根)
	Filename string    // 文件名(经 ValidateItemName 校验)
	Reader   io.Reader // 文件内容流
}

// UploadFile 上传文件到桶内目录,返回新建文件记录(*model.File)。
// 流程:规范化目录 + 校验条目名 → 桶可用性 → mkdir -p 解析目标目录并校验可见性 →
// 同名自动重命名 → 流式落盘采样 MD5 → 先落库拿主键 → 写对象 → 桶 UsedSpace 原子自增。
// 失败补偿:Put 失败删记录;元数据失败删记录并清理对象。
// 错误语义:路径/名非法或链长超限 → ErrInvalidInput;桶禁用 → ErrForbidden;
// 目标目录不可达 → ErrNotFound。允许 0 字节空文件。
func UploadFile(ctx context.Context, arg UploadFileArg) (*model.File, error) {
	userID, bucketID, dirPath, filename, r := arg.UserID, arg.BucketID, arg.DirPath, arg.Filename, arg.Reader
	// 入参校验(目录规范化 + 条目名)
	dir, err := common.NormalizeDirPath(dirPath)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(filename)
	if err := common.ValidateItemName(name); err != nil {
		return nil, err
	}
	if r == nil {
		return nil, ErrInvalidInput
	}

	// 桶对象状态(存在 + Status==1)
	bucket, err := CheckBucketUsable(ctx, CheckBucketUsableArg{BucketID: bucketID})
	if err != nil {
		return nil, err
	}

	// 解析目标目录(mkdir -p 建父链)
	folderID, err := common.ResolveDirPath(ctx, userID, bucketID, dir)
	if err != nil {
		return nil, err
	}
	// 目标目录可用性 + 条目级可见性
	if err := checkAncestorsAccessTree(ctx, userID, bucketID, folderID); err != nil {
		return nil, err
	}

	// 同名冲突自动重命名
	name, err = uniqueName(ctx, bucketID, folderID, name)
	if err != nil {
		return nil, err
	}

	// 流式接收:输入流先落临时文件(磁盘缓冲)
	tmp, err := os.CreateTemp("", "orbitcloud-upload-*")
	if err != nil {
		log.Errorf("upload: create temp file: %v", err)
		return nil, fmt.Errorf("upload: create temp file: %w", err)
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	size, err := io.Copy(tmp, r)
	if err != nil {
		return nil, fmt.Errorf("upload: buffer stream: %w", err)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("upload: seek temp file: %w", err)
	}
	// 采样 MD5;空文件照常落库+落对象
	md5Hex, _, err := utils.ComputeSampleMD5(tmp)
	if err != nil {
		return nil, fmt.Errorf("upload: compute sample md5: %w", err)
	}

	// 先落库拿主键(对象键 = 主键 ID,须 Create 后才有自增 ID)
	f := &model.File{
		BucketID:   bucketID,
		FolderID:   folderID,
		Name:       name,
		FileSize:   size,
		FileType:   mime.TypeByExtension(filepath.Ext(name)),
		MD5:        md5Hex,
		UploadedBy: userID,
	}
	if err := core.DB.WithContext(ctx).Create(f).Error; err != nil {
		if isUniqueViolation(err) { // 并发同名:重命名后重试一次
			if name, err2 := uniqueName(ctx, bucketID, folderID, name); err2 == nil && name != f.Name {
				f.Name = name
				f.FileType = mime.TypeByExtension(filepath.Ext(name))
				if err2 := core.DB.WithContext(ctx).Create(f).Error; err2 != nil {
					log.Errorf("upload: create file record (bucket %d): %v", bucketID, err2)
					return nil, fmt.Errorf("upload: create file record: %w", err2)
				}
			} else {
				return nil, ErrConflict
			}
		} else {
			log.Errorf("upload: create file record (bucket %d): %v", bucketID, err)
			return nil, fmt.Errorf("upload: create file record: %w", err)
		}
	}

	// 写对象存储(key = 记录主键 ID)
	objectKey := objectKeyForFile(f.ID)
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		_ = core.DB.Unscoped().Delete(&model.File{}, f.ID)
		return nil, fmt.Errorf("upload: seek temp file: %w", err)
	}
	if err := core.Storage.Put(ctx, utils.BucketEncoder(bucket.ID), objectKey, tmp, size); err != nil {
		log.Errorf("upload: storage put %s/%s (bucket %d): %v", utils.BucketEncoder(bucket.ID), objectKey, bucketID, err)
		_ = core.DB.Unscoped().Delete(&model.File{}, f.ID) // 补偿:不留空洞
		return nil, err
	}

	// 更新桶 UsedSpace(SQL 原子自增防并发覆盖)
	err = core.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.Model(&model.Bucket{}).Where("id = ?", bucketID).
			Update("used_space", gorm.Expr("used_space + ?", size)).Error
	})
	if err != nil {
		// 补偿:清理已写对象 + 硬删记录(不留孤儿)
		_ = core.Storage.Delete(ctx, utils.BucketEncoder(bucket.ID), objectKey)
		_ = core.DB.Unscoped().Delete(&model.File{}, f.ID)
		return nil, fmt.Errorf("upload: finalize record: %w", err)
	}

	log.Infof("upload: user %d bucket %d file %q -> id %d size %d", userID, bucketID, f.Name, f.ID, f.FileSize)
	return f, nil
}

// CreateDirArg 创建目录入参。
type CreateDirArg struct {
	UserID   uint   // 操作者 users.id
	BucketID uint   // 目标桶 buckets.id
	DirPath  string // 目录路径(mkdir -p:父链自动创建;"/" = 桶根,拒绝创建)
}

// CreateDir 创建文件夹(mkdir -p 语义:父链自动创建,已存在同名文件夹幂等成功),
// 返回新建文件夹记录(*model.Folder)。
// 错误语义:路径非法/链长超限 → ErrInvalidInput;桶禁用 → ErrForbidden;
// 目标段被同名文件/文件夹占用 → ErrConflict;父目录不可达 → ErrNotFound。
func CreateDir(ctx context.Context, arg CreateDirArg) (*model.Folder, error) {
	userID, bucketID, dirPath := arg.UserID, arg.BucketID, arg.DirPath
	// 规范化(桶根 "/" 为虚拟根,拒绝创建)
	dir, err := common.NormalizeDirPath(dirPath)
	if err != nil {
		return nil, err
	}
	if dir == "/" {
		return nil, ErrInvalidInput // 桶根无法"创建"
	}

	// 桶对象状态(存在 + Status==1)
	if _, err := CheckBucketUsable(ctx, CheckBucketUsableArg{BucketID: bucketID}); err != nil {
		return nil, err
	}

	// 解析父链(建父链,拿父 folder_id)
	segs := strings.Split(dir, "/")
	parentDir := ""
	if len(segs) > 1 {
		parentDir = strings.Join(segs[:len(segs)-1], "/")
	}
	parentID, err := common.ResolveDirPath(ctx, userID, bucketID, parentDir)
	if err != nil {
		return nil, err
	}
	// 父链可用性 + 条目级可见性
	if err := checkAncestorsAccessTree(ctx, userID, bucketID, parentID); err != nil {
		return nil, err
	}
	name := segs[len(segs)-1]
	// 目录段名校验(与文件名同一套校验,拒绝 Windows 不可访问的名)
	if err := common.ValidateItemName(name); err != nil {
		return nil, err
	}

	// 已存在同名文件夹 → 幂等返回(mkdir -p 语义)
	var exist model.Folder
	err = core.DB.WithContext(ctx).
		Where("bucket_id = ? AND parent_id = ? AND name_lower = ?",
			bucketID, parentID, strings.ToLower(name)).First(&exist).Error
	if err == nil {
		if !exist.Isable {
			// 同名目录已逻辑删除:拒绝复用,按不存在处理
			return nil, ErrNotFound
		}
		return &exist, nil // 幂等:已存在同名文件夹,直接返回
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("create dir: query: %w", err)
	}

	// 同名互斥(文件/文件夹同目录互斥,双表检查)
	exists, err := nameExists(ctx, bucketID, parentID, name)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, ErrConflict
	}

	// 创建目录节点(无实体对象)
	folder := &model.Folder{
		BucketID:   bucketID,
		ParentID:   parentID,
		Name:       name,
		UploadedBy: userID,
		Isable:     true,
	}
	if err := core.DB.WithContext(ctx).Create(folder).Error; err != nil {
		if isUniqueViolation(err) {
			return nil, ErrConflict // 并发下被占
		}
		return nil, fmt.Errorf("create dir: create record: %w", err)
	}

	log.Infof("mkdir: user %d bucket %d dir %q -> id %d", userID, bucketID, common.JoinItemPath(parentDir, name), folder.ID)
	return folder, nil
}
