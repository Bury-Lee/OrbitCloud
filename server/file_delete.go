// file_delete.go —— 桶内条目删除:文件硬删(对象 + 记录)/
// 文件夹逻辑删除(Isable=false + 后台任务)。
//
// 条目权限由 api 层预检,本文件只做可行性(桶状态、条目归属、Isable 链);
// userID 仅用于操作日志。
package server

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"orbitcloud/core"
	"orbitcloud/log"
	"orbitcloud/model"
	"orbitcloud/utils"
)

// DeleteFileArg 文件删除入参。
type DeleteFileArg struct {
	UserID   uint // 操作者 users.id(操作日志)
	BucketID uint // 所属桶
	FileID   uint // 文件 files.id
}

// DeleteFile 删除文件条目:校验桶与条目 → 删对象(幂等)→ 事务硬删记录、
// 归还桶 UsedSpace、级联清理分享。
// 错误语义:文件不存在/祖先目录已删 → ErrNotFound;桶禁用 → ErrForbidden。
func DeleteFile(ctx context.Context, arg DeleteFileArg) error {
	userID, bucketID, fileID := arg.UserID, arg.BucketID, arg.FileID
	// 桶对象状态(存在 + Status==1)
	bucket, err := CheckBucketUsable(ctx, CheckBucketUsableArg{BucketID: bucketID})
	if err != nil {
		return err
	}

	// 查文件
	file, err := loadFile(ctx, bucketID, fileID)
	if err != nil {
		return err
	}

	// 祖先链 Isable(目录已删 → 404)
	if err := checkAncestorsUsable(ctx, bucketID, file.FolderID); err != nil {
		return err
	}

	// 删对象(幂等;其他错误记日志继续,避免"删不掉"死锁)
	if err := core.Storage.Delete(ctx, utils.BucketEncoder(bucket.ID), objectKeyForFile(file.ID)); err != nil && !errors.Is(err, core.ErrObjectNotFound) {
		log.Errorf("delete file %d: delete object: %v", fileID, err)
	}

	// 事务:硬删记录 + 归还桶 UsedSpace(钳制不为负)+ 级联清理分享
	if err := core.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Unscoped().Delete(&model.File{}, fileID).Error; err != nil {
			return err
		}
		// 级联:删除指向该文件的分享
		if err := tx.Unscoped().
			Where("bucket_item_id = ? AND item_type = 'file'", fileID).
			Delete(&model.ShareLink{}).Error; err != nil {
			return err
		}
		var b model.Bucket
		if err := tx.First(&b, bucketID).Error; err == nil {
			used := b.UsedSpace - file.FileSize
			if used < 0 {
				used = 0
			}
			if err := tx.Model(&model.Bucket{}).Where("id = ?", bucketID).Update("used_space", used).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}

	log.Infof("delete file: user %d bucket %d file %d (%q) deleted", userID, bucketID, fileID, file.Name)
	return nil
}

// DeleteDirArg 文件夹删除入参。
type DeleteDirArg struct {
	UserID   uint // 操作者 users.id(操作日志)
	BucketID uint // 所属桶
	DirID    uint // 文件夹 folders.id
}

// DeleteDir 删除文件夹:置 Isable=false(立即不可达)→ 落 DeleteTask{DirID} →
// 返回任务 ID;任务执行(物理清理)由 api 层经全局协程池提交,不在此同步执行。
// 中断残留由启动扫描 / cron 续跑。错误语义:目录不存在 → ErrNotFound;桶禁用 → ErrForbidden。
func DeleteDir(ctx context.Context, arg DeleteDirArg) (uint, error) {
	userID, bucketID, dirID := arg.UserID, arg.BucketID, arg.DirID
	// 桶对象状态(存在 + Status==1)
	if _, err := CheckBucketUsable(ctx, CheckBucketUsableArg{BucketID: bucketID}); err != nil {
		return 0, err
	}

	// 查目录
	if _, err := loadFolder(ctx, bucketID, dirID); err != nil {
		return 0, err
	}

	// 置 Isable=false:子树即刻不可达(读路径 404)
	if err := core.DB.WithContext(ctx).Model(&model.Folder{}).
		Where("id = ?", dirID).Update("isable", false).Error; err != nil {
		return 0, fmt.Errorf("delete dir %d: disable: %w", dirID, err)
	}

	// 落删除任务(DirID>0 目录删除,DirID=0 桶删除)
	task := &model.DeleteTask{BucketID: bucketID, DirID: dirID, Status: 0}
	if err := core.DB.WithContext(ctx).Create(task).Error; err != nil {
		return 0, fmt.Errorf("delete dir %d: create delete task: %w", dirID, err)
	}

	log.Infof("delete dir: user %d bucket %d dir %d (isable=false, task %d queued)", userID, bucketID, dirID, task.ID)
	return task.ID, nil
}
