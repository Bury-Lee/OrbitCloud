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
)

// copyFrame 深度优先复制栈帧:源 folder → 目标侧父 folder。
type copyFrame struct {
	srcFolderID uint // 源文件夹 folders.id
	dstParentID uint // 目标侧父文件夹 folders.id(顶层 = 目标父目录解析出的 folderID)
	isTop       bool // 顶层:目标名用 task.DstName,不参与 uniqueName
}

// ProcessCopyTaskArg 复制任务处理入参。
type ProcessCopyTaskArg struct {
	TaskID uint // 复制任务 copy_tasks.id
}

// ProcessCopyTask 处理单个复制任务(启动/cron 续跑与 server 内同步触发共用)。
// 按任务字段分派:SourceFileID > 0 → 单文件跨桶移动(复制+删源);
// 否则 → 文件夹深度优先复制。
func ProcessCopyTask(ctx context.Context, arg ProcessCopyTaskArg) error {
	task := &model.CopyTask{}
	if err := core.DB.WithContext(ctx).First(task, arg.TaskID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil // 任务不存在 → 幂等
		}
		return fmt.Errorf("process copy task %d: query task: %w", arg.TaskID, err)
	}
	if task.SourceFileID > 0 {
		return processCopyFileTask(ctx, arg.TaskID) // 单文件跨桶移动
	}
	return processCopyFolderTask(ctx, arg.TaskID) // 文件夹复制
}

// processCopyFileTask 处理单文件跨桶移动任务(两步串行,等价"复制任务+删除任务"):
//  1. 复制源文件到目标(不重命名;目标已存在 = 上轮已复制 → 跳过,幂等);
//  2. 删除源文件(移动语义;失败保留任务,续跑时复制跳过、重试删源)。
//
// 任务级锁/续租/幂等与文件夹任务一致;完成后硬删任务记录。
func processCopyFileTask(ctx context.Context, taskID uint) error {
	// 取任务;不存在 → 返回 nil(幂等)
	task := &model.CopyTask{}
	if err := core.DB.WithContext(ctx).First(task, taskID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return fmt.Errorf("process copy file task %d: query task: %w", taskID, err)
	}

	// 抢占处理权(任务级数据库锁,乐观条件更新,防并发处理同一任务)
	now := time.Now()
	res := core.DB.WithContext(ctx).Model(&model.CopyTask{}).
		Where("id = ? AND (lock_owner = '' OR lock_expires_at < ?)", taskID, now).
		Updates(map[string]any{
			"status":          1,
			"lock_owner":      core.InstanceID(),
			"lock_expires_at": now.Add(5 * time.Minute),
		})
	if res.Error != nil {
		return fmt.Errorf("process copy file task %d: acquire lock: %w", taskID, res.Error)
	}
	if res.RowsAffected == 0 {
		return nil // 其他进程处理中,跳过
	}

	// 后台任务不随 HTTP 请求取消;defer 释放锁(仅当仍持有,防误清新持有者)
	ctx = context.WithoutCancel(ctx)
	defer func() {
		if err := core.DB.WithContext(ctx).Model(&model.CopyTask{}).
			Where("id = ? AND lock_owner = ?", taskID, core.InstanceID()).
			Updates(map[string]any{"lock_owner": "", "lock_expires_at": nil}).Error; err != nil {
			log.Errorf("process copy file task %d: release lock: %v", taskID, err)
		}
	}()

	// 源文件存在性(已删 → 目标侧已复制过,移动完成,删任务)
	if _, err := loadFile(ctx, task.BucketID, task.SourceFileID); err != nil {
		if errors.Is(err, ErrNotFound) {
			if derr := core.DB.Unscoped().Delete(&model.CopyTask{}, taskID).Error; derr != nil {
				log.Errorf("process copy file task %d: delete task (source gone): %v", taskID, derr)
			}
			return nil
		}
		return fmt.Errorf("process copy file task %d: load source: %w", taskID, err)
	}

	// 第一步:复制(不重命名;目标已存在 = 上轮已复制 → 跳过,续跑幂等)
	if _, err := CopyFileNoRename(ctx, CopyFileArg{
		UserID:      task.UploadedBy,
		SrcBucketID: task.BucketID,
		SrcFileID:   task.SourceFileID,
		DstBucketID: task.DstBucketID,
		DstFilename: task.DstName,
		DstFolderID: task.DstFolderID,
	}); err != nil && !errors.Is(err, ErrConflict) {
		// 配额拒绝:放弃任务(保留已复制部分,用户可自行清理),防每轮无限重试卡死
		if errors.Is(err, ErrQuotaExceeded) {
			if derr := core.DB.Unscoped().Delete(&model.CopyTask{}, taskID).Error; derr != nil {
				log.Errorf("process copy file task %d: delete task (quota aborted): %v", taskID, derr)
			}
			log.Errorf("process copy file task %d: quota exceeded, task aborted", taskID)
			return fmt.Errorf("process copy file task %d: copy: %w", taskID, err)
		}
		return fmt.Errorf("process copy file task %d: copy: %w", taskID, err)
	}

	// 第二步:删除源(跨桶移动 = 复制 + 删源串行;失败保留任务续跑重试删源)
	if err := DeleteFile(ctx, DeleteFileArg{UserID: task.UploadedBy, BucketID: task.BucketID, FileID: task.SourceFileID}); err != nil {
		return fmt.Errorf("process copy file task %d: delete source: %w", taskID, err)
	}

	// 全部完成 → 硬删任务记录
	if err := core.DB.Unscoped().Delete(&model.CopyTask{}, taskID).Error; err != nil {
		return fmt.Errorf("process copy file task %d: delete task: %w", taskID, err)
	}
	log.Infof("copy task %d: file %d moved (bucket %d -> %d)", taskID, task.SourceFileID, task.BucketID, task.DstBucketID)
	return nil
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
		if err := core.DB.WithContext(ctx).Model(&model.CopyTask{}).
			Where("id = ? AND lock_owner = ?", taskID, core.InstanceID()).
			Updates(map[string]any{"lock_owner": "", "lock_expires_at": nil}).Error; err != nil {
			log.Errorf("process copy task %d: release lock: %v", taskID, err)
		}
	}()

	// 目标父目录解析(建父链,拿 folderID)
	dstBaseID, err := common.ResolveDirPath(ctx, task.UploadedBy, task.DstBucketID, task.DstDirPath)
	if err != nil {
		return fmt.Errorf("process copy task %d: resolve dst dir: %w", taskID, err)
	}

	// 4. 源根文件夹(不存在 → 任务作废:删任务记录返回)
	srcRoot, err := loadFolderByID(ctx, task.SourceFolderID)
	if err != nil {
		if derr := core.DB.Unscoped().Delete(&model.CopyTask{}, taskID).Error; derr != nil {
			log.Errorf("process copy task %d: delete task (source folder gone): %v", taskID, derr)
		}
		return fmt.Errorf("process copy task %d: load source folder: %w", taskID, err)
	}
	_ = srcRoot // 仅用于校验存在

	// 源侧可见性复核:复制任务在后台执行,api 层仅预检顶层源文件夹,
	// 子条目须在此逐条复核(防 ACL 变更/绕过导致受限内容被复制)。
	// 用户/组成员数据加载一次,链路复用。
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

		// 每批刷新操作者最新权限(动态验证,载入内存):任务可能持续很久,
		// 执行期间操作者被禁用/降权/删号 → 立即中止,不再复制任何内容
		operator, err := GetUser(ctx, GetUserArg{ID: task.UploadedBy})
		if err != nil {
			// 账号已删(软删/不存在)→ 中止;其他错误保留任务续跑重试
			if errors.Is(err, ErrNotFound) {
				log.Warnf("process copy task %d: operator %d gone; abort task", taskID, task.UploadedBy)
				if derr := core.DB.Unscoped().Delete(&model.CopyTask{}, taskID).Error; derr != nil {
					log.Errorf("process copy task %d: delete task (operator gone): %v", taskID, derr)
				}
				return nil
			}
			return fmt.Errorf("process copy task %d: refresh operator: %w", taskID, err)
		}
		if operator.Status != 1 {
			log.Warnf("process copy task %d: operator %d disabled; abort task", taskID, task.UploadedBy)
			if derr := core.DB.Unscoped().Delete(&model.CopyTask{}, taskID).Error; derr != nil {
				log.Errorf("process copy task %d: delete task (operator disabled): %v", taskID, derr)
			}
			return nil
		}
		// 权限降级(等级数值变大 = 权限变低):中止,防降权后继续搬运数据
		if task.OperatorLevel > 0 && int(operator.PermissionLevel) > task.OperatorLevel {
			log.Warnf("process copy task %d: operator %d demoted (level %d -> %d); abort task",
				taskID, task.UploadedBy, task.OperatorLevel, operator.PermissionLevel)
			if derr := core.DB.Unscoped().Delete(&model.CopyTask{}, taskID).Error; derr != nil {
				log.Errorf("process copy task %d: delete task (operator demoted): %v", taskID, derr)
			}
			return nil
		}
		var operatorGroups []uint

		fr := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		// 顶层源文件夹可见性复核(api 层已预检,此处兜底:任务执行期间
		// 发起者失去访问权 → 终止任务,避免继续复制受限内容)
		if fr.isTop {
			if err := checkAncestorsAccessTree(ctx, task.UploadedBy, task.BucketID, fr.srcFolderID); err != nil {
				log.Warnf("process copy task %d: operator lost access to source folder %d: %v; abort task", taskID, fr.srcFolderID, err)
				if derr := core.DB.Unscoped().Delete(&model.CopyTask{}, taskID).Error; derr != nil {
					log.Errorf("process copy task %d: delete task (access revoked): %v", taskID, derr)
				}
				return nil
			}
		}

		// 目标侧可写性(正在删除的目标目录/父链拒绝写入)
		if err := checkAncestorsWritable(ctx, task.DstBucketID, fr.dstParentID); err != nil {
			return fmt.Errorf("process copy task %d: dst dir not writable: %w", taskID, err)
		}

		// 目标侧目录:顶层直接复用 CopyFolder 预建的任务目标(DstFolderID);
		// 子层:已存在(上轮已建)则复用继续复制,否则创建(不重命名)。
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
			// 目标子目录已存在(上轮已建,内容可能未完成)→ 复用继续(幂等续跑,
			// 不可跳过:目录创建与内容复制之间可能中断,跳过会导致内容永久缺失)
			if existingID, ferr := findFolderByParent(ctx, task.DstBucketID, fr.dstParentID, srcF.Name); ferr == nil {
				newFolder, err = loadFolderByID(ctx, existingID)
				if err != nil {
					return fmt.Errorf("process copy task %d: load reused folder %d: %w", taskID, existingID, err)
				}
			} else if !errors.Is(ferr, ErrNotFound) {
				return fmt.Errorf("process copy task %d: check dst folder: %w", taskID, ferr)
			} else {
				newFolder = &model.Folder{
					BucketID:   task.DstBucketID,
					ParentID:   fr.dstParentID,
					Name:       srcF.Name,
					UploadedBy: task.UploadedBy,
					Isable:     model.FolderUsable,
					// 可见组继承源目录(防复制后泄露给全桶)
					VisibleToGroups: srcF.VisibleToGroups,
				}
				if err := core.DB.WithContext(ctx).Create(newFolder).Error; err != nil {
					if isUniqueViolation(err) {
						// 并发窗口(预检后仍被抢占):下轮复用已存在目录
						return fmt.Errorf("process copy task %d: dst folder conflict (retry later): %w", taskID, err)
					}
					return fmt.Errorf("process copy task %d: create dst folder: %w", taskID, err)
				}
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
				// 受限文件跳过(源侧文件级 ACL 复核;已复制的历史副本不受影响,
				// 目标侧副本继承源可见组,语义不变)
				if err := checkItemAccessTree(ctx, operator, &operatorGroups, sf.VisibleToGroups, sf.UploadedBy); err != nil {
					log.Warnf("process copy task %d: skip restricted file %d: %v", taskID, sf.ID, err)
					continue
				}
				// 复制(不重命名,目标已存在 → ErrConflict):
				// 上轮已完整复制的文件直接跳过,续跑幂等,不再产生 "xxx (1)" 副本;
				// 复制内部建记录/对象复制/used_space 原子入账/失败补偿与 CopyFile 一致。
				nf, cerr := CopyFileNoRename(ctx, CopyFileArg{
					UserID:      task.UploadedBy,
					SrcBucketID: task.BucketID,
					SrcFileID:   sf.ID,
					DstBucketID: task.DstBucketID,
					DstFilename: sf.Name,
					DstFolderID: newFolder.ID, // 目录 ID 直传(免路径重解析)
				})
				if cerr != nil {
					if errors.Is(cerr, ErrConflict) {
						continue // 目标已存在 = 上轮已复制,跳过(幂等)
					}
					// 配额拒绝:放弃任务(保留已复制部分,用户可自行清理),
					// 防每轮无限重试卡死在同一个文件
					if errors.Is(cerr, ErrQuotaExceeded) {
						if derr := core.DB.Unscoped().Delete(&model.CopyTask{}, taskID).Error; derr != nil {
							log.Errorf("process copy task %d: delete task (quota aborted): %v", taskID, derr)
						}
						log.Errorf("process copy task %d: copy file %d: quota exceeded, task aborted", taskID, sf.ID)
						return fmt.Errorf("process copy task %d: copy file %d: %w", taskID, sf.ID, cerr)
					}
					return fmt.Errorf("process copy task %d: copy file %d: %w", taskID, sf.ID, cerr)
				}
				_ = nf
			}
			lastFileID = srcFiles[len(srcFiles)-1].ID
			if len(srcFiles) < batch {
				break // 不足一批 = 已取完(避免最后一轮多余查询)
			}
		}

		// 子文件夹压栈(先压后弹 = 深度优先);受限子文件夹连同子树整棵跳过
		children, err := nextSubfolders(ctx, task.BucketID, fr.srcFolderID)
		if err != nil {
			return err
		}
		for i := range children {
			if err := checkAncestorsAccessTree(ctx, task.UploadedBy, task.BucketID, children[i].ID); err != nil {
				log.Warnf("process copy task %d: skip restricted subfolder %d: %v", taskID, children[i].ID, err)
				continue
			}
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
