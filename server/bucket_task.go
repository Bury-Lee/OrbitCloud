// bucket_task.go —— 存储桶/目录删除任务:任务分派与深度优先物理清理。
//
// DeleteTask{DirID=0 整桶 / >0 目录子树}均带任务级锁(乐观条件更新)+ 续租 + 幂等,
// 中断残留由启动扫描 / cron 续跑。
package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"orbitcloud/core"
	"orbitcloud/log"
	"orbitcloud/model"
	"orbitcloud/utils"
)

// ProcessDeleteTaskArg 删除任务处理入参。
type ProcessDeleteTaskArg struct {
	TaskID uint // 删除任务 delete_tasks.id
}

// ProcessDeleteTask 处理单个删除任务(按 DirID 分派桶/目录删除;任务不存在 → nil)。
// 供 cron 续跑扫描与 DeleteBucket/DeleteDir 同步触发共用。
func ProcessDeleteTask(ctx context.Context, arg ProcessDeleteTaskArg) error {
	taskID := arg.TaskID
	task := &model.DeleteTask{}
	if err := core.DB.WithContext(ctx).First(task, taskID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil // 任务不存在 → 幂等
		}
		return fmt.Errorf("process delete task %d: query task: %w", taskID, err)
	}
	if task.DirID > 0 {
		return processDeleteDirTask(ctx, taskID) // 目录删除
	}
	return processDeleteTask(ctx, taskID) // 桶删除
}

// processDeleteTask 处理单个桶删除任务(后台/续跑调用,幂等):
// 抢占任务锁 → 整桶删除对象存储(幂等)→ 续租确认持锁 →
// 单事务硬删全部文件/文件夹记录、桶元数据与任务记录。
// 一个网盘桶 = 一个对象存储 bucket,清空对象与删元数据解耦。
func processDeleteTask(ctx context.Context, taskID uint) error {
	// 取任务;不存在 → 返回 nil(幂等)
	task := &model.DeleteTask{}
	if err := core.DB.WithContext(ctx).First(task, taskID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return fmt.Errorf("process delete task %d: query task: %w", taskID, err)
	}

	// 抢占处理权(数据库锁字段,乐观条件更新,防并发处理同一任务)
	now := time.Now()
	res := core.DB.WithContext(ctx).Model(&model.DeleteTask{}).
		Where("id = ? AND (lock_owner = '' OR lock_expires_at < ?)", taskID, now).
		Updates(map[string]any{
			"status":          1,
			"lock_owner":      core.InstanceID(),
			"lock_expires_at": now.Add(5 * time.Minute),
		})
	if res.Error != nil {
		return fmt.Errorf("process delete task %d: acquire lock: %w", taskID, res.Error)
	}
	if res.RowsAffected == 0 {
		return nil // 其他进程处理中,跳过
	}

	// 删除任务为后台性质,不随 HTTP 请求取消(客户端断开不应中断删除进度或锁释放)
	ctx = context.WithoutCancel(ctx)

	// 取得锁后 defer 释放(仅当仍持有锁;完成路径任务记录已删,Update 影响 0 行无害)
	defer func() {
		_ = core.DB.WithContext(ctx).Model(&model.DeleteTask{}).
			Where("id = ? AND lock_owner = ?", taskID, core.InstanceID()).
			Updates(map[string]any{"lock_owner": "", "lock_expires_at": nil}).Error
	}()

	// 取桶(定位对象存储桶名;桶不存在 → 删任务记录返回,幂等)
	bucket, err := GetBucket(ctx, GetBucketArg{ID: task.BucketID})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			_ = core.DB.Unscoped().Delete(&model.DeleteTask{}, taskID).Error
			return nil
		}
		return err
	}

	// 整桶删除对象存储(BucketEncoder 映射,幂等;失败返回错误,任务保留待续跑重试,
	// 已删部分不影响;对象全清后才进收尾事务,避免"元数据已删而对象残留")
	if err := core.Storage.DeleteBucket(ctx, utils.BucketEncoder(bucket.ID)); err != nil {
		return fmt.Errorf("process delete task %d: delete object bucket: %w", taskID, err)
	}

	// 续租并确认仍持锁(耗时操作防锁过期;锁被抢占则放弃本轮)
	r := core.DB.WithContext(ctx).Model(&model.DeleteTask{}).
		Where("id = ? AND lock_owner = ?", taskID, core.InstanceID()).
		Update("lock_expires_at", time.Now().Add(5*time.Minute))
	if r.Error != nil {
		return fmt.Errorf("process delete task %d: renew lock: %w", taskID, r.Error)
	}
	if r.RowsAffected == 0 {
		return nil // 锁被抢占,放弃本轮
	}

	// 收尾(单事务硬删除):条目记录 + 桶 + 任务 + 级联删分享
	if err := core.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 先取全部条目 ID(share_links 无 bucket 列,按条目 ID 匹配;须在删记录前查)
		var fileIDs, folderIDs []uint
		if err := tx.Model(&model.File{}).Where("bucket_id = ?", task.BucketID).Pluck("id", &fileIDs).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.Folder{}).Where("bucket_id = ?", task.BucketID).Pluck("id", &folderIDs).Error; err != nil {
			return err
		}
		// 级联:分享随桶删除一并物理清除
		shareCond := "bucket_item_id IN ? AND item_type = 'file'"
		shareArgs := []any{fileIDs}
		if len(folderIDs) > 0 {
			if len(fileIDs) > 0 {
				shareCond = "(bucket_item_id IN ? AND item_type = 'file') OR (bucket_item_id IN ? AND item_type = 'folder')"
				shareArgs = []any{fileIDs, folderIDs}
			} else {
				shareCond = "bucket_item_id IN ? AND item_type = 'folder'"
				shareArgs = []any{folderIDs}
			}
		}
		if err := tx.Unscoped().Where(shareCond, shareArgs...).Delete(&model.ShareLink{}).Error; err != nil {
			return err
		}
		if err := tx.Unscoped().Where("bucket_id = ?", task.BucketID).Delete(&model.File{}).Error; err != nil {
			return err
		}
		if err := tx.Unscoped().Where("bucket_id = ?", task.BucketID).Delete(&model.Folder{}).Error; err != nil {
			return err
		}
		if err := tx.Unscoped().Delete(&model.Bucket{}, task.BucketID).Error; err != nil {
			return err
		}
		if err := tx.Unscoped().Delete(&model.DeleteTask{}, taskID).Error; err != nil {
			return err
		}
		return nil
	}); err != nil {
		return err
	}

	log.Infof("delete task %d: bucket %d purged (objects + metadata deleted)", taskID, task.BucketID)
	return nil
}

// processDeleteDirTask 处理单个目录删除任务:目录已被 DeleteDir 置 Isable=false,
// 本任务深度优先逐文件夹物理清理——对每个 folder 先删其下文件(对象+记录)再删自身,
// 避免一次性全删给数据库压力过大;任务级锁/续租/幂等与 processDeleteTask 相同。
func processDeleteDirTask(ctx context.Context, taskID uint) error {
	// 取任务;不存在 → 返回 nil(幂等)
	task := &model.DeleteTask{}
	if err := core.DB.WithContext(ctx).First(task, taskID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return fmt.Errorf("process delete dir task %d: query task: %w", taskID, err)
	}
	if task.DirID == 0 {
		return processDeleteTask(ctx, taskID) // 防御:转交桶删除
	}

	// 抢占处理权(同 processDeleteTask)
	now := time.Now()
	res := core.DB.WithContext(ctx).Model(&model.DeleteTask{}).
		Where("id = ? AND (lock_owner = '' OR lock_expires_at < ?)", taskID, now).
		Updates(map[string]any{
			"status":          1,
			"lock_owner":      core.InstanceID(),
			"lock_expires_at": now.Add(5 * time.Minute),
		})
	if res.Error != nil {
		return fmt.Errorf("process delete dir task %d: acquire lock: %w", taskID, res.Error)
	}
	if res.RowsAffected == 0 {
		return nil // 其他进程处理中,跳过
	}

	ctx = context.WithoutCancel(ctx)
	defer func() {
		_ = core.DB.WithContext(ctx).Model(&model.DeleteTask{}).
			Where("id = ? AND lock_owner = ?", taskID, core.InstanceID()).
			Updates(map[string]any{"lock_owner": "", "lock_expires_at": nil}).Error
	}()

	// 深度优先逐文件夹删除
	stack := []uint{task.DirID}
	for len(stack) > 0 {
		// 每轮续租并确认仍持有锁
		r := core.DB.WithContext(ctx).Model(&model.DeleteTask{}).
			Where("id = ? AND lock_owner = ?", taskID, core.InstanceID()).
			Update("lock_expires_at", time.Now().Add(5*time.Minute))
		if r.Error != nil {
			return fmt.Errorf("process delete dir task %d: renew lock: %w", taskID, r.Error)
		}
		if r.RowsAffected == 0 {
			return nil // 锁已被抢占,放弃本轮
		}

		fid := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		// 取直接子文件夹压栈(先压后弹 = 深度优先)
		children, err := nextSubfolders(ctx, task.BucketID, fid)
		if err != nil {
			return err
		}
		for i := range children {
			stack = append(stack, children[i].ID)
		}

		// 先删对象,全部成功才硬删记录
		files := []model.File{}
		if err := core.DB.WithContext(ctx).Where("bucket_id = ? AND folder_id = ?", task.BucketID, fid).Find(&files).Error; err != nil {
			return fmt.Errorf("process delete dir task %d: query files: %w", taskID, err)
		}
		allOK := true
		var folderSize int64 // 本文件夹文件总大小
		for _, f := range files {
			err := core.Storage.Delete(ctx, utils.BucketEncoder(task.BucketID), objectKeyForFile(f.ID))
			if err != nil && !errors.Is(err, core.ErrObjectNotFound) {
				log.Errorf("process delete dir task %d: delete object %s: %v", taskID, objectKeyForFile(f.ID), err)
				allOK = false
			}
			folderSize += f.FileSize
		}
		if !allOK {
			return fmt.Errorf("process delete dir task %d: object delete failed, records retained", taskID)
		}

		// 事务:删文件记录 + 级联删分享 + 删文件夹自身 + 归还桶 UsedSpace
		if err := core.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if len(files) > 0 {
				ids := make([]uint, 0, len(files))
				for _, f := range files {
					ids = append(ids, f.ID)
				}
				if err := tx.Unscoped().Where("id IN ?", ids).Delete(&model.File{}).Error; err != nil {
					return err
				}
				// 级联:删除指向这些文件的分享
				if err := tx.Unscoped().
					Where("bucket_item_id IN ? AND item_type = 'file'", ids).
					Delete(&model.ShareLink{}).Error; err != nil {
					return err
				}
			}
			if err := tx.Unscoped().Delete(&model.Folder{}, fid).Error; err != nil {
				return err
			}
			// 级联:删除指向该目录的分享
			if err := tx.Unscoped().
				Where("bucket_item_id = ? AND item_type = 'folder'", fid).
				Delete(&model.ShareLink{}).Error; err != nil {
				return err
			}
			var b model.Bucket
			if err := tx.First(&b, task.BucketID).Error; err == nil {
				used := b.UsedSpace - folderSize
				if used < 0 {
					used = 0
				}
				if err := tx.Model(&model.Bucket{}).Where("id = ?", task.BucketID).Update("used_space", used).Error; err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return fmt.Errorf("process delete dir task %d: delete folder %d: %w", taskID, fid, err)
		}
	}

	// 全部完成 → 硬删任务记录
	if err := core.DB.WithContext(ctx).Unscoped().Delete(&model.DeleteTask{}, taskID).Error; err != nil {
		return err
	}
	log.Infof("delete dir task %d: dir %d subtree purged", taskID, task.DirID)
	return nil
}
