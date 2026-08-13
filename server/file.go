// file.go —— 桶内条目基础层:对象键 / 桶与权限校验 / 条目加载 / 命名冲突 /
// 祖先链校验等公共助手(上传/下载/列表/删除/复制/移动等业务见各子文件)。
//
// 模型(files/folders 双表):文件夹 = 目录树节点(ParentID=0 为桶根,Isable=false
// 表示已删除),文件 = 条目(FolderID 指向所属文件夹)。路径为派生值不落库,
// 读取只向上查祖先链或向下查一层;目录移动 = 单行更新 parent_id,
// 删除/复制走后台任务(DeleteTask/CopyTask)。
//
// 对象键:S3 key = 记录主键 ID 转字符串,不依赖文件名/内容/路径;文件夹无实体对象。
package server

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"gorm.io/gorm"

	"orbitcloud/core"
	"orbitcloud/model"
)

// 条目类型常量:file = files 表,folder = folders 表。
const (
	ItemKindFile   = "file"
	ItemKindFolder = "folder"
)

// objectKeyForFile 对象键:文件记录主键 ID 转字符串(主键全局唯一,
// 不依赖文件名/内容/桶);文件夹无实体对象,不调用。
func objectKeyForFile(fileID uint) string {
	return strconv.FormatUint(uint64(fileID), 10)
}

// CheckBucketUsableArg 桶可用性校验入参。
type CheckBucketUsableArg struct {
	BucketID uint // 桶 buckets.id
}

// CheckBucketUsable 校验桶可用性:桶存在(ErrNotFound 透传)且状态正常(Status==1,否则 ErrForbidden)。
func CheckBucketUsable(ctx context.Context, arg CheckBucketUsableArg) (*model.Bucket, error) {
	bucket, err := GetBucket(ctx, GetBucketArg{ID: arg.BucketID})
	if err != nil {
		return nil, err
	}
	if bucket.Status != 1 {
		return nil, ErrForbidden // 桶禁用
	}
	return bucket, nil
}

// GetFileArg 文件加载入参。
type GetFileArg struct {
	BucketID uint // 所属桶 buckets.id
	FileID   uint // 文件 files.id
}

// GetFile 按 ID 加载文件(可行性:存在 + 属于该桶)。api 层预检取数据用。
func GetFile(ctx context.Context, arg GetFileArg) (*model.File, error) {
	return loadFile(ctx, arg.BucketID, arg.FileID)
}

// GetFolderArg 文件夹加载入参。
type GetFolderArg struct {
	BucketID uint // 所属桶 buckets.id
	FolderID uint // 文件夹 folders.id
}

// GetFolder 按 ID 加载文件夹(可行性:存在 + 属于该桶)。api 层祖先链 ACL 取数据用。
func GetFolder(ctx context.Context, arg GetFolderArg) (*model.Folder, error) {
	return loadFolder(ctx, arg.BucketID, arg.FolderID)
}

// loadFile 按 ID 加载文件(校验属于该桶)。
func loadFile(ctx context.Context, bucketID, fileID uint) (*model.File, error) {
	var f model.File
	err := core.DB.WithContext(ctx).Where("id = ? AND bucket_id = ?", fileID, bucketID).First(&f).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load file %d: %w", fileID, err)
	}
	return &f, nil
}

// loadFolder 按 ID 加载文件夹(校验属于该桶)。
// 注:folderID=0 不合法(桶根虚拟无实例),调用方先特判。
func loadFolder(ctx context.Context, bucketID, folderID uint) (*model.Folder, error) {
	var f model.Folder
	err := core.DB.WithContext(ctx).Where("id = ? AND bucket_id = ?", folderID, bucketID).First(&f).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load folder %d: %w", folderID, err)
	}
	return &f, nil
}

// nameExists 检查 (bucketID, folderID, name) 是否已存在(同时查 files 与 folders 两表)。
func nameExists(ctx context.Context, bucketID, folderID uint, name string) (bool, error) {
	lower := strings.ToLower(name)
	var n1, n2 int64
	if err := core.DB.WithContext(ctx).Model(&model.File{}).
		Where("bucket_id = ? AND folder_id = ? AND name_lower = ?", bucketID, folderID, lower).
		Count(&n1).Error; err != nil {
		return false, fmt.Errorf("name exists check (files): %w", err)
	}
	if err := core.DB.WithContext(ctx).Model(&model.Folder{}).
		Where("bucket_id = ? AND parent_id = ? AND name_lower = ?", bucketID, folderID, lower).
		Count(&n2).Error; err != nil {
		return false, fmt.Errorf("name exists check (folders): %w", err)
	}
	return n1 > 0 || n2 > 0, nil
}

// uniqueName 自动重命名:重名时生成 name (1)、name (2)… 返回第一个不冲突的名字,
// 上限 1000 次保护。
func uniqueName(ctx context.Context, bucketID, folderID uint, name string) (string, error) {
	exists, err := nameExists(ctx, bucketID, folderID, name)
	if err != nil {
		return "", err
	}
	if !exists {
		return name, nil
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for n := 1; n <= 1000; n++ { // 上限保护,正常不可能耗尽
		cand := fmt.Sprintf("%s (%d)%s", base, n, ext)
		exists, err := nameExists(ctx, bucketID, folderID, cand)
		if err != nil {
			return "", err
		}
		if !exists {
			return cand, nil
		}
	}
	return "", ErrConflict
}

// checkAncestorsUsable 校验目标 folder 及其祖先链全部可用(Isable=true):
// 沿 parent 链逐级向上查至桶根,任一不可用 → ErrNotFound。
// 纯对象状态校验,用户维度权限见 api.permAncestorsAccess。
func checkAncestorsUsable(ctx context.Context, bucketID, folderID uint) error {
	cur := folderID
	for cur != 0 {
		f, err := loadFolder(ctx, bucketID, cur)
		if err != nil {
			return err
		}
		if !f.Isable {
			return ErrNotFound // 目录已删 → 不可达(404 防探测)
		}
		cur = f.ParentID
	}
	return nil // 到桶根(0)为止,通过;folderID=0(桶根)直接通过
}

// CheckAncestorsUsableArg 祖先链可用性校验入参。
type CheckAncestorsUsableArg struct {
	BucketID uint // 所属桶
	FolderID uint // 目标文件夹(0 = 桶根,直接通过)
}

// CheckAncestorsUsable 导出版(api 层预检共用)。
func CheckAncestorsUsable(ctx context.Context, arg CheckAncestorsUsableArg) error {
	return checkAncestorsUsable(ctx, arg.BucketID, arg.FolderID)
}

// nextSubfolders 取某文件夹的直接子文件夹(仅一层,供后台删除/复制任务深度优先遍历)。
func nextSubfolders(ctx context.Context, bucketID, folderID uint) ([]model.Folder, error) {
	var children []model.Folder
	if err := core.DB.WithContext(ctx).
		Where("bucket_id = ? AND parent_id = ?", bucketID, folderID).
		Order("id ASC").Find(&children).Error; err != nil {
		return nil, fmt.Errorf("next subfolders %d: %w", folderID, err)
	}
	if children == nil {
		children = []model.Folder{} // 空切片而非 nil,保证遍历终止
	}
	return children, nil
}

// 用户维度访问判定已迁移至 api/perm.go(permUserActive + canAccess)。

