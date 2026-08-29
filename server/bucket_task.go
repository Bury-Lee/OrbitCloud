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

// deleteBatchSize 删除任务单批游标拉取条数(文件/子文件夹)。
const deleteBatchSize = 100

// dirDeleteFrame 目录删除遍历帧(迭代栈元素)。
// 游标字段记录本帧批处理进度(id 单调推进,拉空 = 该资源清空,不重不漏);
// residue 标记本帧子树内存在被可见组跳过(残留)的条目。
type dirDeleteFrame struct {
	id              uint  // 文件夹 ID
	parent          int   // 父帧栈索引; -1 = 任务根
	pendingChildren int   // 尚未完成处理的直接子文件夹数
	residue         bool  // 子树内存在跳过残留
	fileCursor      int64 // 文件批次游标(0 = 从首条开始)
	subCursor       int64 // 子文件夹批次游标(0 = 从首条开始)
}

// processDeleteDirTask 处理单个目录删除任务:删除发起时文件夹已置
// FolderDeleting(可见可读拒写),本任务深度优先**先删内部再删文件夹自身**:
//   - 游标批次:每个文件夹的文件/子文件夹按 id > cursor 分批拉取处理
//     (每批 deleteBatchSize 条),拉空即该资源清空——游标单调推进,已处理
//     或已跳过的条目不会在后续批重复出现;删除期间整棵子树拒写,无并发窗口;
//   - 可见组匹配:压栈/软删前用操作者快照(OperatorID + 组成员)做条目级
//     可见性判定,不可见的文件/子文件夹整体跳过并标记残留(记录/对象/用量
//     保留),残留沿祖先链向上传播——含残留的文件夹不删自身,任务根恢复
//     FolderUsable(目录树保持可达,不产生孤儿);
//   - 每批(每帧)动态验证操作者权限(禁用/降权/删号 → 暂停任务,记录保留待续跑);
//   - 任务级锁/续租/幂等与 processDeleteTask 相同。
func processDeleteDirTask(ctx context.Context, taskID uint) error {
	// 取任务;不存在 → 返回 nil(幂等)
	task := &model.DeleteTask{}
	var err error
	if err = core.DB.WithContext(ctx).First(task, taskID).Error; err != nil {
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

	// 深度优先 + 游标批次删除(见函数头注释)。操作者快照每帧刷新:
	// OperatorID=0(存量任务)时按非管理员、非任何组成员判定(受限条目跳过)。
	stack := []dirDeleteFrame{{id: task.DirID, parent: -1}}
	idx := 0
	for idx >= 0 {
		// 每帧处理前:续租并确认仍持有锁
		r := core.DB.WithContext(ctx).Model(&model.DeleteTask{}).
			Where("id = ? AND lock_owner = ?", taskID, core.InstanceID()).
			Update("lock_expires_at", time.Now().Add(5*time.Minute))
		if r.Error != nil {
			return fmt.Errorf("process delete dir task %d: renew lock: %w", taskID, r.Error)
		}
		if r.RowsAffected == 0 {
			return nil // 锁已被抢占,放弃本轮
		}

		// 动态验证操作者(禁用/删号 → 暂停任务:status 回 0,权限恢复后由
		// 启动扫描/cron 续跑重判),并加载可见性匹配用快照
		if err := checkDeleteOperator(ctx, task); err != nil {
			if uerr := core.DB.WithContext(ctx).Model(&model.DeleteTask{}).
				Where("id = ? AND lock_owner = ?", taskID, core.InstanceID()).
				Update("status", 0).Error; uerr != nil {
				log.Errorf("process delete dir task %d: pause (status back to 0): %v", taskID, uerr)
			}
			return err
		}
		var opUser *model.User
		var opGroups []uint
		if task.OperatorID > 0 {
			opUser, err = GetUser(ctx, GetUserArg{ID: task.OperatorID})
			if err != nil {
				return fmt.Errorf("process delete dir task %d: load operator: %w", taskID, err)
			}
			opGroups, err = UserGroupIDs(ctx, UserGroupIDsArg{UserID: task.OperatorID})
			if err != nil {
				return fmt.Errorf("process delete dir task %d: load operator groups: %w", taskID, err)
			}
		}

		// 1. 文件批次(游标拉空为止):可见 → 软删进回收站(级联分享);
		//    不可见 → 跳过(残留,不删记录/对象/用量)
		cursor := stack[idx].fileCursor
		residue := stack[idx].residue
		for {
			files, err := loadFilesBatch(ctx, task.BucketID, stack[idx].id, cursor, deleteBatchSize)
			if err != nil {
				return err
			}
			if len(files) == 0 {
				break // 拉空 = 清空
			}
			visible := make([]model.File, 0, len(files))
			for i := range files {
				if ItemVisibleRule(0, files[i].VisibleToGroups, files[i].UploadedBy, opUser, opGroups) != nil {
					residue = true // 不可见 → 跳过并标记残留
					continue
				}
				visible = append(visible, files[i])
			}
			if len(visible) > 0 {
				if err := softDeleteFilesForTask(ctx, visible); err != nil {
					return fmt.Errorf("process delete dir task %d: soft delete files of folder %d: %w", taskID, stack[idx].id, err)
				}
			}
			cursor = int64(files[len(files)-1].ID)
		}

		// 2. 子文件夹批次(游标拉空为止):可见 → 压栈(内部优先);
		//    不可见 → 跳过(残留,不深入)
		subCursor := stack[idx].subCursor
		pending := 0
		for {
			subs, err := loadSubfoldersBatch(ctx, task.BucketID, stack[idx].id, subCursor, deleteBatchSize)
			if err != nil {
				return err
			}
			if len(subs) == 0 {
				break // 拉空 = 全部子文件夹已处理
			}
			children := make([]dirDeleteFrame, 0, len(subs))
			for i := range subs {
				if ItemVisibleRule(0, subs[i].VisibleToGroups, subs[i].UploadedBy, opUser, opGroups) != nil {
					residue = true // 不可见子文件夹 → 跳过整棵子树并标记残留
					continue
				}
				children = append(children, dirDeleteFrame{id: subs[i].ID, parent: idx})
				pending++
			}
			stack = append(stack, children...)
			subCursor = int64(subs[len(subs)-1].ID)
		}
		stack[idx].fileCursor = cursor
		stack[idx].subCursor = subCursor
		stack[idx].residue = residue
		stack[idx].pendingChildren += pending

		// 3. 子文件夹未处理完 → 转最深层(深度优先)
		if stack[idx].pendingChildren > 0 {
			idx = len(stack) - 1
			continue
		}

		// 4. 本帧完成:有残留 → 不删自身(任务根 FolderDeleting → FolderUsable,
		//    中间帧从未置 FolderDeleting,保持可用——残留条目不产生孤儿);
		//    无残留 → 软删自身进回收站(置 FolderUnusable + 软删 + 级联分享)
		frameDone := stack[idx]
		if frameDone.residue {
			if frameDone.parent < 0 {
				if err := core.DB.WithContext(ctx).Model(&model.Folder{}).
					Where("id = ? AND isable = ?", frameDone.id, model.FolderDeleting).
					Update("isable", model.FolderUsable).Error; err != nil {
					return fmt.Errorf("process delete dir task %d: restore folder %d usable: %w", taskID, frameDone.id, err)
				}
				log.Warnf("delete dir task %d: dir %d has invisible residue, restored usable", taskID, frameDone.id)
			}
		} else {
			if err := recycleFolderForTask(ctx, frameDone.id); err != nil {
				return fmt.Errorf("process delete dir task %d: recycle folder %d: %w", taskID, frameDone.id, err)
			}
		}
		// 出栈 + 残留向上传播
		parentIdx := frameDone.parent
		if parentIdx >= 0 {
			parent := &stack[parentIdx]
			parent.pendingChildren--
			if frameDone.residue {
				parent.residue = true
			}
		}
		stack = stack[:len(stack)-1]
		idx = parentIdx
	}

	// 全部完成 → 硬删任务记录
	if err := core.DB.WithContext(ctx).Unscoped().Delete(&model.DeleteTask{}, taskID).Error; err != nil {
		return err
	}
	log.Infof("delete dir task %d: dir %d subtree purged (files soft-deleted, folder recycled)", taskID, task.DirID)
	return nil
}

// loadFilesBatch 游标拉取某文件夹下的活跃文件批(单批 limit 条,id 升序;
// 返回空切片 = 拉空)。软删行(deleted_at 非空)由 GORM 自动过滤;id 游标
// 单调推进保证每批不重不漏,并发窗口新写入(id 更大)会在后续批出现,拉空即清空。
func loadFilesBatch(ctx context.Context, bucketID, folderID uint, cursor int64, limit int) ([]model.File, error) {
	files := make([]model.File, 0, limit)
	q := core.DB.WithContext(ctx).Where("bucket_id = ? AND folder_id = ?", bucketID, folderID)
	if cursor > 0 {
		q = q.Where("id > ?", cursor)
	}
	if err := q.Order("id ASC").Limit(limit).Find(&files).Error; err != nil {
		return nil, fmt.Errorf("load files batch folder %d: %w", folderID, err)
	}
	return files, nil
}

// loadSubfoldersBatch 游标拉取某文件夹的直接子文件夹批(单批 limit 条,id 升序;
// 返回空切片 = 拉空)。语义同 loadFilesBatch。
func loadSubfoldersBatch(ctx context.Context, bucketID, folderID uint, cursor int64, limit int) ([]model.Folder, error) {
	subs := make([]model.Folder, 0, limit)
	q := core.DB.WithContext(ctx).Where("bucket_id = ? AND parent_id = ?", bucketID, folderID)
	if cursor > 0 {
		q = q.Where("id > ?", cursor)
	}
	if err := q.Order("id ASC").Limit(limit).Find(&subs).Error; err != nil {
		return nil, fmt.Errorf("load subfolders batch folder %d: %w", folderID, err)
	}
	return subs, nil
}

// recycleFolderForTask 软删文件夹自身(置 FolderUnusable + 软删进回收站 +
// 级联清理指向该文件夹的分享)。删除任务内部用。
func recycleFolderForTask(ctx context.Context, fid uint) error {
	return core.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
	})
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
