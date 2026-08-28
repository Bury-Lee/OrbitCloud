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
// 抢占任务锁 → 动态验证操作者 → 整桶删除对象存储(幂等)→ 续租确认持锁 →
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
		if err := core.DB.WithContext(ctx).Model(&model.DeleteTask{}).
			Where("id = ? AND lock_owner = ?", taskID, core.InstanceID()).
			Updates(map[string]any{"lock_owner": "", "lock_expires_at": nil}).Error; err != nil {
			log.Errorf("process delete task %d: release lock: %v", taskID, err)
		}
	}()

	// 动态验证操作者(禁用/降权/删号 → 暂停任务:status 回 0 + 释放锁,
	// 权限恢复后由启动扫描/cron 续跑重判,不会卡死)
	if err := checkDeleteOperator(ctx, task); err != nil {
		if uerr := core.DB.WithContext(ctx).Model(&model.DeleteTask{}).
			Where("id = ? AND lock_owner = ?", taskID, core.InstanceID()).
			Update("status", 0).Error; uerr != nil {
			log.Errorf("process delete task %d: pause (status back to 0): %v", taskID, uerr)
		}
		return err
	}

	// 取桶(定位对象存储桶名;桶不存在 → 删任务记录返回,幂等)
	bucket, err := GetBucket(ctx, GetBucketArg{ID: task.BucketID})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			if derr := core.DB.Unscoped().Delete(&model.DeleteTask{}, taskID).Error; derr != nil {
				log.Errorf("process delete task %d: delete task (bucket gone): %v", taskID, derr)
			}
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

	// 收尾(单事务硬删除):条目记录 + 桶 + 任务 + 级联删分享 + 归还用户用量
	if err := core.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 先取全部文件记录(用量归还按 UploadedBy 分组;须在删记录前查;
		// share_links 无 bucket 列,按条目 ID 匹配)。Unscoped 必须含回收站软删行
		// ——软删不归还用量,整桶清除时一并归还。
		var files []model.File
		if err := tx.Unscoped().Model(&model.File{}).Where("bucket_id = ?", task.BucketID).Find(&files).Error; err != nil {
			return err
		}
		fileIDs := make([]uint, 0, len(files))
		byUser := map[uint]int64{} // 用户用量归还汇总(按 UploadedBy 分组)
		for i := range files {
			fileIDs = append(fileIDs, files[i].ID)
			byUser[files[i].UploadedBy] += files[i].FileSize
		}
		var folderIDs []uint
		if err := tx.Unscoped().Model(&model.Folder{}).Where("bucket_id = ?", task.BucketID).Pluck("id", &folderIDs).Error; err != nil {
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
		// 用户用量归还(按文件归属者分组;整桶删除释放全部占用,钳制不为负)
		for uid, sz := range byUser {
			if err := returnUserUsedSpace(tx, uid, sz); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}

	log.Infof("delete task %d: bucket %d purged (objects + metadata deleted)", taskID, task.BucketID)
	return nil
}

// processDeleteDirTask 处理单个目录删除任务:删除发起时文件夹已置
// FolderDeleting(可见可读拒写),本任务深度优先**先删内部再删文件夹自身**:
//   - 每轮处理一个文件夹:软删其下全部活跃文件(进回收站)→ 活跃子文件夹压栈
//     (内部优先,本文件夹暂不删自身)→ 无活跃子文件夹且文件已清空(含删除期间
//     并发窗口新写入的兜底重试)才软删文件夹自身(置 FolderUnusable + 软删);
//   - 每批(每轮)动态验证操作者权限(禁用/降权/删号 → 暂停任务,记录保留待续跑);
//   - 任务级锁/续租/幂等与 processDeleteTask 相同。
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
		if err := core.DB.WithContext(ctx).Model(&model.DeleteTask{}).
			Where("id = ? AND lock_owner = ?", taskID, core.InstanceID()).
			Updates(map[string]any{"lock_owner": "", "lock_expires_at": nil}).Error; err != nil {
			log.Errorf("process delete dir task %d: release lock: %v", taskID, err)
		}
	}()

	// 源根文件夹(不存在 = 已删/已清 → 任务作废,删任务记录返回,幂等)
	if _, err := loadFolderByID(ctx, task.DirID); err != nil {
		if errors.Is(err, ErrNotFound) {
			if derr := core.DB.Unscoped().Delete(&model.DeleteTask{}, taskID).Error; derr != nil {
				log.Errorf("process delete dir task %d: delete task (source gone): %v", taskID, derr)
			}
			return nil
		}
		return fmt.Errorf("process delete dir task %d: load source folder: %w", taskID, err)
	}

	// 深度优先先删内部再删文件夹自身
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

		// 每批(每轮)动态验证操作者(禁用/降权/删号 → 暂停任务:status 回 0,
		// 权限恢复后由启动扫描/cron 续跑重判)
		if err := checkDeleteOperator(ctx, task); err != nil {
			if uerr := core.DB.WithContext(ctx).Model(&model.DeleteTask{}).
				Where("id = ? AND lock_owner = ?", taskID, core.InstanceID()).
				Update("status", 0).Error; uerr != nil {
				log.Errorf("process delete dir task %d: pause (status back to 0): %v", taskID, uerr)
			}
			return err
		}

		fid := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		// 1. 软删本文件夹下全部活跃文件(进回收站,保留对象/用量,可恢复)+ 级联分享
		files := []model.File{}
		if err := core.DB.WithContext(ctx).
			Where("bucket_id = ? AND folder_id = ?", task.BucketID, fid).Find(&files).Error; err != nil {
			return fmt.Errorf("process delete dir task %d: query files: %w", taskID, err)
		}
		if len(files) > 0 {
			if err := softDeleteFilesForTask(ctx, files); err != nil {
				return fmt.Errorf("process delete dir task %d: soft delete files of folder %d: %w", taskID, fid, err)
			}
		}

		// 2. 活跃子文件夹 → 内部优先:自身重新压栈沉底(等内部清空后最后删),
		//    子文件夹压栈(栈顶先处理)
		children, err := nextSubfolders(ctx, task.BucketID, fid)
		if err != nil {
			return err
		}
		if len(children) > 0 {
			stack = append(stack, fid) // 自身沉底:全部子文件夹清空后才轮到删自身
			for i := range children {
				stack = append(stack, children[i].ID)
			}
			continue
		}

		// 3. 无活跃子文件夹:确认文件已清空(兜底:置 Deleting 前已在上传中的
		//    并发请求可能残留活跃文件 → 重新压栈再处理一轮,直到清空)
		var remain int64
		if err := core.DB.WithContext(ctx).Model(&model.File{}).
			Where("bucket_id = ? AND folder_id = ?", task.BucketID, fid).Count(&remain).Error; err != nil {
			return fmt.Errorf("process delete dir task %d: count remaining files: %w", taskID, err)
		}
		if remain > 0 {
			stack = append(stack, fid)
			continue
		}

		// 4. 内部全部清空 → 软删文件夹自身(置 FolderUnusable + 软删进回收站 + 级联分享)
		if err := core.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&model.Folder{}).Where("id = ?", fid).
				Update("isable", model.FolderUnusable).Error; err != nil {
				return err
			}
			if err := tx.Delete(&model.Folder{}, fid).Error; err != nil {
				return err
			}
			return tx.Unscoped().
				Where("bucket_item_id = ? AND item_type = 'folder'", fid).
				Delete(&model.ShareLink{}).Error
		}); err != nil {
			return fmt.Errorf("process delete dir task %d: recycle folder %d: %w", taskID, fid, err)
		}
	}

	// 全部完成 → 硬删任务记录
	if err := core.DB.WithContext(ctx).Unscoped().Delete(&model.DeleteTask{}, taskID).Error; err != nil {
		return err
	}
	log.Infof("delete dir task %d: dir %d subtree purged (files soft-deleted, folder recycled)", taskID, task.DirID)
	return nil
}

// checkDeleteOperator 动态验证删除任务操作者(每批刷新):
//   - OperatorID=0(存量任务,无快照)→ 按旧语义放行;
//   - 账号不存在(被删/软删)→ 暂停任务(记录保留,待续跑重判);
//   - 账号禁用(Status!=1)→ 暂停任务;
//
// 返回错误时调用方须把任务 status 回 0(暂停),由启动扫描/cron 续跑重判
// (权限恢复后自动继续);不回 0 则任务卡在"处理中"永不被重扫。
func checkDeleteOperator(ctx context.Context, task *model.DeleteTask) error {
	if task.OperatorID == 0 {
		return nil // 存量任务:无快照,按旧语义执行
	}
	u, err := GetUser(ctx, GetUserArg{ID: task.OperatorID})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			log.Warnf("delete task %d: operator %d gone, task paused", task.ID, task.OperatorID)
			return fmt.Errorf("delete task %d: operator %d gone, task paused", task.ID, task.OperatorID)
		}
		return fmt.Errorf("delete task %d: query operator: %w", task.ID, err)
	}
	if u.Status != 1 {
		log.Warnf("delete task %d: operator %d disabled, task paused", task.ID, task.OperatorID)
		return fmt.Errorf("delete task %d: operator %d disabled, task paused", task.ID, task.OperatorID)
	}
	//不考虑因等级变化而暂停任务,更新快照继续
	// if task.OperatorLevel > 0 && int(u.PermissionLevel) > task.OperatorLevel {
	// 	log.Warnf("delete task %d: operator %d demoted (level %d -> %d), task paused",
	// 		task.ID, task.OperatorID, task.OperatorLevel, u.PermissionLevel)
	// 	return fmt.Errorf("delete task %d: operator %d demoted, task paused", task.ID, task.OperatorID)
	// }
	return nil
}

// softDeleteFilesForTask 软删文件(进回收站,置 deleted_at,保留对象/用量)+
// 级联清理指向这些文件的分享。删除任务内部用:不做权限/存在性检查,
// 由任务创建时预检 + checkDeleteOperator 动态验证保证。
func softDeleteFilesForTask(ctx context.Context, files []model.File) error {
	if len(files) == 0 {
		return nil
	}
	ids := make([]uint, 0, len(files))
	for i := range files {
		ids = append(ids, files[i].ID)
	}
	return core.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.File{}).Where("id IN ?", ids).
			Update("deleted_at", time.Now()).Error; err != nil {
			return err
		}
		return tx.Unscoped().
			Where("bucket_item_id IN ? AND item_type = 'file'", ids).
			Delete(&model.ShareLink{}).Error
	})
}
