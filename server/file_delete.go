// file_delete.go —— 桶内条目删除:文件/文件夹删除进入回收站(GORM 软删)。
//
// 回收站语义:
//   - 删除 = 置 gorm.Model.DeletedAt(deleted_at 列)= now,保留记录/对象/用量,
//     不归还空间(软删不归还用量,与 cron 对账口径一致);回收站清除(后续功能)
//     才物理删除并归还;
//   - 读取/列表/搜索路径全部经 GORM 软删自动过滤(deleted_at IS NULL),
//     回收站内条目不可见不可达;
//   - 文件夹删除另置 Isable=false,保证子树(未逐条标记的子孙)即刻不可达;
//   - 不再落 DeleteTask/不做物理清理(数据进回收站需保留);
//   - 级联:删除指向被删条目的分享链接(删除即不可再访问)。
//
// 条目权限由 api 层预检,本文件只做可行性(桶状态、条目归属、Isable 链);
// userID 仅用于操作日志。
package server

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"orbitcloud/core"
	"orbitcloud/log"
	"orbitcloud/model"
)

// DeleteFileArg 文件删除入参。
type DeleteFileArg struct {
	UserID   uint // 操作者 users.id(操作日志)
	BucketID uint // 所属桶
	FileID   uint // 文件 files.id
}

// DeleteFile 删除文件条目 → 进回收站:校验桶与条目 → 事务内软删记录
// (置 deleted_at=now,保留对象/用量)+ 级联清理指向该文件的分享。
// 错误语义:文件不存在/已进回收站/祖先目录已删 → ErrNotFound;桶禁用 → ErrForbidden。
func DeleteFile(ctx context.Context, arg DeleteFileArg) error {
	userID, bucketID, fileID := arg.UserID, arg.BucketID, arg.FileID
	// 桶对象状态(存在 + Status==1)
	if _, err := CheckBucketUsable(ctx, CheckBucketUsableArg{BucketID: bucketID}); err != nil {
		return err
	}

	// 查文件(软删过滤:回收站内再删 → ErrNotFound)
	file, err := loadFile(ctx, bucketID, fileID)
	if err != nil {
		return err
	}

	// 祖先链 Isable(目录已删 → 404)
	if err := checkAncestorsUsable(ctx, bucketID, file.FolderID); err != nil {
		return err
	}

	// 回收站:软删(置 deleted_at=now)+ 级联清理分享
	if err := core.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(&model.File{}, fileID).Error; err != nil {
			return err
		}
		// 级联:删除指向该文件的分享
		if err := tx.Unscoped().
			Where("bucket_item_id = ? AND item_type = 'file'", fileID).
			Delete(&model.ShareLink{}).Error; err != nil {
			return err
		}
		return nil
	}); err != nil {
		return err
	}

	log.Infof("delete file: user %d bucket %d file %d (%q) moved to recycle bin", userID, bucketID, fileID, file.Name)
	return nil
}

// DeleteDirArg 文件夹删除入参。
type DeleteDirArg struct {
	UserID   uint // 操作者 users.id(操作日志)
	BucketID uint // 所属桶
	DirID    uint // 文件夹 folders.id
}

// DeleteDir 删除文件夹 → 进回收站:置 Isable=false(子树即刻不可达)+ 软删
// (置 deleted_at=now)+ 级联清理指向该目录的分享。返回 taskID 恒 0(不再落
// DeleteTask/物理清理,回收站保留数据;调用方以 taskID==0 跳过任务提交)。
// 错误语义:目录不存在/已进回收站 → ErrNotFound;桶禁用 → ErrForbidden。
func DeleteDir(ctx context.Context, arg DeleteDirArg) (uint, error) {
	userID, bucketID, dirID := arg.UserID, arg.BucketID, arg.DirID
	// 桶对象状态(存在 + Status==1)
	if _, err := CheckBucketUsable(ctx, CheckBucketUsableArg{BucketID: bucketID}); err != nil {
		return 0, err
	}

	// 查目录(软删过滤:回收站内再删 → ErrNotFound)
	if _, err := loadFolder(ctx, bucketID, dirID); err != nil {
		return 0, err
	}

	// 回收站:Isable=false + 软删 + 级联清理分享
	if err := core.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 置 Isable=false:子树即刻不可达(读路径 404)
		if err := tx.Model(&model.Folder{}).
			Where("id = ?", dirID).Update("isable", false).Error; err != nil {
			return err
		}
		// 软删(置 deleted_at=now)
		if err := tx.Delete(&model.Folder{}, dirID).Error; err != nil {
			return err
		}
		// 级联:删除指向该目录的分享
		if err := tx.Unscoped().
			Where("bucket_item_id = ? AND item_type = 'folder'", dirID).
			Delete(&model.ShareLink{}).Error; err != nil {
			return err
		}
		return nil
	}); err != nil {
		return 0, fmt.Errorf("delete dir %d: recycle: %w", dirID, err)
	}

	log.Infof("delete dir: user %d bucket %d dir %d (isable=false) moved to recycle bin", userID, bucketID, dirID)
	return 0, nil
}
