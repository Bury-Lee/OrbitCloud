// copy_task.go —— 文件夹复制任务处理。
//
// CopyFolder 落 CopyTask → 后台 processCopyFolderTask 深度优先逐文件夹复制:
// 每帧先建目标 Folder,再复制其下文件(建新 File 记录 → Get 源对象 → Put 新对象),
// 子文件夹压栈续处理;失败保留任务/记录,续跑重试(幂等);
// 并发防护:任务级数据库锁(LockOwner/LockExpiresAt)乐观抢占。
package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"orbitcloud/common"
	"orbitcloud/core"
	"orbitcloud/log"
	"orbitcloud/model"
	"orbitcloud/utils"
)

// copyFrame 深度优先复制栈帧:源 folder → 目标侧父 folder。
type copyFrame struct {
	srcFolderID uint // 源文件夹 folders.id
	dstParentID uint // 目标侧父文件夹 folders.id(顶层 = 目标父目录解析出的 folderID)
	isTop       bool // 顶层:目标名用 task.DstName,不参与 uniqueName
}

// ProcessCopyFolderTaskArg 复制任务处理入参。
type ProcessCopyFolderTaskArg struct {
	TaskID uint // 复制任务 copy_tasks.id
}

// ProcessCopyFolderTask 处理单个文件夹复制任务(启动/cron 续跑与 server 内同步触发共用)。
func ProcessCopyFolderTask(ctx context.Context, arg ProcessCopyFolderTaskArg) error {
	return processCopyFolderTask(ctx, arg.TaskID)
}

// processCopyFolderTask 处理单个文件夹复制任务(内部实现,幂等)。
func processCopyFolderTask(ctx context.Context, taskID uint) error {
	const batch = 100 // 单文件夹内文件批处理上限(id 游标分批,防一次取全)

	// 1. 取任务;不存在 → 返回 nil(幂等)
	task := &model.CopyTask{}
	if err := core.DB.WithContext(ctx).First(task, taskID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return fmt.Errorf("process copy task %d: query task: %w", taskID, err)
	}

	// 2. 抢占处理权(任务级数据库锁,乐观条件更新,防并发处理同一任务)
	now := time.Now()
	res := core.DB.WithContext(ctx).Model(&model.CopyTask{}).
		Where("id = ? AND (lock_owner = '' OR lock_expires_at < ?)", taskID, now).
		Updates(map[string]any{
			"status":          1,
			"lock_owner":      core.InstanceID(),
			"lock_expires_at": now.Add(5 * time.Minute),
		})
	if res.Error != nil {
		return fmt.Errorf("process copy task %d: acquire lock: %w", taskID, res.Error)
	}
	if res.RowsAffected == 0 {
		return nil // 其他进程处理中,跳过
	}

	// 后台任务不随 HTTP 请求取消;defer 释放锁(仅当仍持有,防误清新持有者)
	ctx = context.WithoutCancel(ctx)
	defer func() {
		_ = core.DB.WithContext(ctx).Model(&model.CopyTask{}).
			Where("id = ? AND lock_owner = ?", taskID, core.InstanceID()).
			Updates(map[string]any{"lock_owner": "", "lock_expires_at": nil}).Error
	}()

	// 目标父目录解析(建父链,拿 folderID)
	dstBaseID, err := common.ResolveDirPath(ctx, task.UploadedBy, task.DstBucketID, task.DstDirPath)
	if err != nil {
		return fmt.Errorf("process copy task %d: resolve dst dir: %w", taskID, err)
	}

	// 4. 源根文件夹(不存在 → 任务作废:删任务记录返回)
	srcRoot, err := loadFolderByID(ctx, task.SourceFolderID)
	if err != nil {
		_ = core.DB.Unscoped().Delete(&model.CopyTask{}, taskID).Error
		return fmt.Errorf("process copy task %d: load source folder: %w", taskID, err)
	}
	_ = srcRoot // 仅用于校验存在

	// 深度优先逐文件夹复制(每轮只查一层,避免一次全取压力过大)
	stack := []copyFrame{{srcFolderID: task.SourceFolderID, dstParentID: dstBaseID, isTop: true}}
	for len(stack) > 0 {
		// 每轮开始前续租并确认仍持有锁(锁被抢占 → 放弃本轮,下轮续跑)
		r := core.DB.WithContext(ctx).Model(&model.CopyTask{}).
			Where("id = ? AND lock_owner = ?", taskID, core.InstanceID()).
			Update("lock_expires_at", time.Now().Add(5*time.Minute))
		if r.Error != nil {
			return fmt.Errorf("process copy task %d: renew lock: %w", taskID, r.Error)
		}
		if r.RowsAffected == 0 {
			return nil // 锁已被抢占,放弃本轮
		}

		fr := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		// 目标侧目录:顶层直接复用 CopyFolder 预建的任务目标(DstFolderID);
		// 子层:以源文件夹名 + uniqueName 兜底创建
		var newFolder *model.Folder
		if fr.isTop {
			newFolder, err = loadFolderByID(ctx, task.DstFolderID)
			if err != nil {
				return fmt.Errorf("process copy task %d: load dst top folder %d: %w", taskID, task.DstFolderID, err)
			}
		} else {
			srcF, err := loadFolderByID(ctx, fr.srcFolderID)
			if err != nil {
				return fmt.Errorf("process copy task %d: load source folder %d: %w", taskID, fr.srcFolderID, err)
			}
			dstName, err := uniqueName(ctx, task.DstBucketID, fr.dstParentID, srcF.Name)
			if err != nil {
				return err
			}
			newFolder = &model.Folder{
				BucketID:   task.DstBucketID,
				ParentID:   fr.dstParentID,
				Name:       dstName,
				UploadedBy: task.UploadedBy,
				Isable:     true,
				// 可见组继承源目录(防复制后泄露给全桶)
				VisibleToGroups: srcF.VisibleToGroups,
			}
			if err := core.DB.WithContext(ctx).Create(newFolder).Error; err != nil {
				if isUniqueViolation(err) {
					// 并发窗口(uniqueName 预检后仍被抢占):保留任务,下轮换新名重试
					return fmt.Errorf("process copy task %d: dst folder conflict (retry later): %w", taskID, err)
				}
				return fmt.Errorf("process copy task %d: create dst folder: %w", taskID, err)
			}
		}

		// 复制该源文件夹下全部文件(先建新记录拿主键 → Get 源对象 → Put 新对象);
		// 按 id 游标分批取,used_space 逐文件原子入账
		var lastFileID uint
		for {
			var srcFiles []model.File
			if err := core.DB.WithContext(ctx).
				Where("bucket_id = ? AND folder_id = ? AND id > ?", task.BucketID, fr.srcFolderID, lastFileID).
				Order("id ASC").Limit(batch).Find(&srcFiles).Error; err != nil {
				return fmt.Errorf("process copy task %d: query source files: %w", taskID, err)
			}
			if len(srcFiles) == 0 {
				break // 已取空 → 本轮文件夹文件复制完成
			}
			for i := range srcFiles {
				sf := &srcFiles[i]
				// 目标文件名冲突自动重命名
				dstFileName, err := uniqueName(ctx, task.DstBucketID, newFolder.ID, sf.Name)
				if err != nil {
					return err
				}
				nf := &model.File{
					BucketID:   task.DstBucketID,
					FolderID:   newFolder.ID,
					Name:       dstFileName,
					FileSize:   sf.FileSize,
					FileType:   sf.FileType,
					MD5:        sf.MD5,
					UploadedBy: task.UploadedBy,
					// 可见组继承源文件(防复制后泄露给全桶)
					VisibleToGroups: sf.VisibleToGroups,
				}
				if err := core.DB.WithContext(ctx).Create(nf).Error; err != nil {
					if isUniqueViolation(err) {
						continue // 并发同名:跳过该文件,下一轮续跑重试
					}
					return fmt.Errorf("process copy task %d: create dst file: %w", taskID, err)
				}
				// 对象复制(Get 源 → Put 新 key;失败补偿删新记录,任务保留续跑)
				rc, err := core.Storage.Get(ctx, utils.BucketEncoder(task.BucketID), objectKeyForFile(sf.ID))
				if err != nil {
					_ = core.DB.Unscoped().Delete(&model.File{}, nf.ID)
					if errors.Is(err, core.ErrObjectNotFound) {
						log.Errorf("process copy task %d: source object %d missing, skip", taskID, sf.ID)
						continue // 源对象缺失:跳过该文件
					}
					return fmt.Errorf("process copy task %d: get source object: %w", taskID, err)
				}
				err = core.Storage.Put(ctx, utils.BucketEncoder(task.DstBucketID), objectKeyForFile(nf.ID), rc, sf.FileSize)
				_ = rc.Close()
				if err != nil {
					_ = core.DB.Unscoped().Delete(&model.File{}, nf.ID) // 补偿:不留空洞
					return fmt.Errorf("process copy task %d: put dst object: %w", taskID, err)
				}
				// used_space 原子自增;失败补偿删对象+删记录,任务保留续跑
				if err := core.DB.WithContext(ctx).Model(&model.Bucket{}).Where("id = ?", task.DstBucketID).
					Update("used_space", gorm.Expr("used_space + ?", sf.FileSize)).Error; err != nil {
					_ = core.Storage.Delete(ctx, utils.BucketEncoder(task.DstBucketID), objectKeyForFile(nf.ID))
					_ = core.DB.Unscoped().Delete(&model.File{}, nf.ID)
					return fmt.Errorf("process copy task %d: update used space: %w", taskID, err)
				}
			}
			lastFileID = srcFiles[len(srcFiles)-1].ID
			if len(srcFiles) < batch {
				break // 不足一批 = 已取完(避免最后一轮多余查询)
			}
		}

		// 子文件夹压栈(先压后弹 = 深度优先)
		children, err := nextSubfolders(ctx, task.BucketID, fr.srcFolderID)
		if err != nil {
			return err
		}
		for i := range children {
			stack = append(stack, copyFrame{srcFolderID: children[i].ID, dstParentID: newFolder.ID})
		}
	}

	// 全部完成 → 硬删任务记录
	if err := core.DB.Unscoped().Delete(&model.CopyTask{}, taskID).Error; err != nil {
		return fmt.Errorf("process copy task %d: delete task: %w", taskID, err)
	}
	log.Infof("copy task %d: folder %d copied to bucket %d dir %q (dst %q)", taskID, task.SourceFolderID, task.DstBucketID, task.DstDirPath, task.DstName)
	return nil
}
