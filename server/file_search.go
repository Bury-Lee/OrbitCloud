// file_search.go —— 文件/文件夹前缀搜索。
//
// 按 name_lower LIKE 'prefix%' 在指定目录（或桶根）内进行前缀匹配，
// 单层不递归，支持可见性过滤与 offset 分页。不含总计数。

package server

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"orbitcloud/common"
	"orbitcloud/core"
	"orbitcloud/model"
)

// FileSearchArg 文件前缀搜索入参。
type FileSearchArg struct {
	UserID   uint
	BucketID uint
	Path     string // 目标目录路径（优先级高于 FolderID）
	FolderID uint   // 目标目录 ID（Path 为空时生效，0 = 桶根）
	Key      string // 搜索前缀
	Page     int
	PageSize int
}

// FolderSearchArg 文件夹前缀搜索入参。
type FolderSearchArg struct {
	UserID   uint
	BucketID uint
	Path     string // 目标目录路径（优先级高于 FolderID）
	FolderID uint   // 目标目录 ID（Path 为空时生效，0 = 桶根）
	Key      string // 搜索前缀
	Page     int
	PageSize int
}

// visibilityFilter 将可见性谓词包装为 GORM Scope 函数。
func visibilityFilter(visSQL string, visArgs []any) func(*gorm.DB) *gorm.DB {
	if visSQL == "" {
		return func(db *gorm.DB) *gorm.DB { return db }
	}
	return func(db *gorm.DB) *gorm.DB {
		return db.Where(visSQL, visArgs...)
	}
}

// FileSearch 按文件名前缀搜索文件（单层，不递归子树，无 COUNT）。
func FileSearch(ctx context.Context, arg FileSearchArg) (files []model.File, err error) {
	userID, bucketID, dirPath, folderID, key, page, pageSize :=
		arg.UserID, arg.BucketID, arg.Path, arg.FolderID, arg.Key, arg.Page, arg.PageSize

	if _, err := CheckBucketUsable(ctx, CheckBucketUsableArg{BucketID: bucketID}); err != nil {
		return nil, err
	}

	targetFolderID := folderID
	if dirPath != "" {
		dir, err := common.NormalizeDirPath(dirPath)
		if err != nil {
			return nil, err
		}
		targetFolderID, err = common.ResolveDirPathStrict(ctx, bucketID, dir)
		if err != nil {
			return nil, err
		}
	}

	if targetFolderID != 0 {
		if err := checkAncestorsAccessTree(ctx, userID, bucketID, targetFolderID); err != nil {
			return nil, err
		}
	}

	visSQL, visArgs, err := listVisibilityPredicate(ctx, userID)
	if err != nil {
		return nil, err
	}

	prefix := strings.ToLower(key) + "%"

	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 500 {
		pageSize = 500
	}

	if err := core.DB.WithContext(ctx).Model(&model.File{}).
		Where("bucket_id = ? AND folder_id = ? AND name_lower LIKE ?",
			bucketID, targetFolderID, prefix).
		Scopes(visibilityFilter(visSQL, visArgs)).
		Order("created_at DESC").
		Limit(pageSize).Offset((page - 1) * pageSize).
		Find(&files).Error; err != nil {
		return nil, fmt.Errorf("file search: %w", err)
	}
	if files == nil {
		files = []model.File{}
	}
	return files, nil
}

// FolderSearch 按文件夹名前缀搜索文件夹（单层，不递归子树，无 COUNT）。
func FolderSearch(ctx context.Context, arg FolderSearchArg) (folders []model.Folder, err error) {
	userID, bucketID, dirPath, folderID, key, page, pageSize :=
		arg.UserID, arg.BucketID, arg.Path, arg.FolderID, arg.Key, arg.Page, arg.PageSize

	if _, err := CheckBucketUsable(ctx, CheckBucketUsableArg{BucketID: bucketID}); err != nil {
		return nil, err
	}

	targetFolderID := folderID
	if dirPath != "" {
		dir, err := common.NormalizeDirPath(dirPath)
		if err != nil {
			return nil, err
		}
		targetFolderID, err = common.ResolveDirPathStrict(ctx, bucketID, dir)
		if err != nil {
			return nil, err
		}
	}

	if targetFolderID != 0 {
		if err := checkAncestorsAccessTree(ctx, userID, bucketID, targetFolderID); err != nil {
			return nil, err
		}
	}

	visSQL, visArgs, err := listVisibilityPredicate(ctx, userID)
	if err != nil {
		return nil, err
	}

	prefix := strings.ToLower(key) + "%"

	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 500 {
		pageSize = 500
	}

	if err := core.DB.WithContext(ctx).Model(&model.Folder{}).
		Where("bucket_id = ? AND parent_id = ? AND name_lower LIKE ? AND isable = true",
			bucketID, targetFolderID, prefix).
		Scopes(visibilityFilter(visSQL, visArgs)).
		Order("created_at DESC").
		Limit(pageSize).Offset((page - 1) * pageSize).
		Find(&folders).Error; err != nil {
		return nil, fmt.Errorf("folder search: %w", err)
	}
	if folders == nil {
		folders = []model.Folder{}
	}
	return folders, nil
}