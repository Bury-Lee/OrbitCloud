// resume.go —— 删除/复制任务表续跑扫描。
//
// 归属:任务扫描(启动时 + cron 周期)落在 cron 包;单任务的物理处理在 server 包
// (API 同步触发与续跑共用),本包通过 server.ProcessDeleteTask /
// server.ProcessCopyFolderTask 驱动。
//
// 并发安全:单任务内部有任务级锁(LockOwner/LockExpiresAt,持有者 = core.InstanceID)
// 乐观抢占,多调用方(启动扫描/cron 周期/API 同步触发)并发时仅一方取得处理权。
package cron

import (
	"context"
	"fmt"

	"orbitcloud/core"
	"orbitcloud/log"
	"orbitcloud/model"
	"orbitcloud/server"
)

// ResumeDeleteTasks 续跑全部未完成删除任务(启动扫描 + cron 周期调用)。
// 按任务字段分派(server.ProcessDeleteTask 内部):DirID>0 → 目录删除;DirID=0 → 桶删除。
// 返回首个错误(记日志后继续扫描其余任务;失败的下一轮周期重试)。
func ResumeDeleteTasks(ctx context.Context) error {
	tasks := []model.DeleteTask{}
	if err := core.DB.WithContext(ctx).Where("status <> 2").Find(&tasks).Error; err != nil {
		return fmt.Errorf("resume delete tasks: query tasks: %w", err)
	}
	var firstErr error
	for _, t := range tasks {
		if err := server.ProcessDeleteTask(ctx, server.ProcessDeleteTaskArg{TaskID: t.ID}); err != nil {
			log.Errorf("resume delete task %d: %v", t.ID, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	log.Infof("resume delete tasks: scanned %d pending tasks (err %v)", len(tasks), firstErr)
	return firstErr
}

// ResumeCopyTasks 续跑全部未完成复制任务(启动扫描 + cron 周期调用)。
// 委托 server.ProcessCopyFolderTask(深度优先逐文件夹,任务级锁幂等)。
func ResumeCopyTasks(ctx context.Context) error {
	tasks := []model.CopyTask{}
	if err := core.DB.WithContext(ctx).Where("status <> 2").Find(&tasks).Error; err != nil {
		return fmt.Errorf("resume copy tasks: query tasks: %w", err)
	}
	var firstErr error
	for _, t := range tasks {
		if err := server.ProcessCopyFolderTask(ctx, server.ProcessCopyFolderTaskArg{TaskID: t.ID}); err != nil {
			log.Errorf("resume copy task %d: %v", t.ID, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	log.Infof("resume copy tasks: scanned %d pending tasks (err %v)", len(tasks), firstErr)
	return firstErr
}
