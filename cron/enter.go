package cron

// Package cron 定时任务:周期清理过期数据(日志/刷新令牌/分享/下载任务),
// 并周期续跑删除/复制任务表(扫描任务表 → 委托 server.ProcessDeleteTask /
// server.ProcessCopyTask,单任务内任务级锁保证并发安全)。

import (
	"context"
	"fmt"
	"time"

	agilepool "github.com/Yiming1997/agilePool/v2"
	"gorm.io/gorm"

	"orbitcloud/config"
	"orbitcloud/core"
	"orbitcloud/log"
	"orbitcloud/model"
)

// Start 启动全部定时任务(由 main 调用;常驻协程,进程退出即终止)。
// 周期 1h,任务经 core.Pool(agilePool)提交,不裸起协程。
func Start(cfg *config.Config, db *gorm.DB) {
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			core.Pool.Submit(agilepool.TaskFunc(func() error {
				// 单个任务失败仅记日志,不影响其他任务
				if _, err := CleanExpiredLogs(db, cfg.Log.RetentionDays, 100); err != nil {
					log.Errorf("cron: clean expired logs: %v", err)
				}
				if _, err := CleanExpiredTokens(db, 100); err != nil {
					log.Errorf("cron: clean expired tokens: %v", err)
				}
				if _, err := CleanExpiredShares(db, 100); err != nil {
					log.Errorf("cron: clean expired shares: %v", err)
				}
				// 任务被清理后客户端恢复下载不硬断:由前端重新登记任务并续传
				if _, err := CleanExpiredDownloadTasks(db, 7, 100); err != nil {
					log.Errorf("cron: clean expired download tasks: %v", err)
				}
				if err := ResumeDeleteTasks(context.Background()); err != nil {
					log.Errorf("cron: resume delete tasks: %v", err)
				}
				if err := ResumeCopyTasks(context.Background()); err != nil {
					log.Errorf("cron: resume copy tasks: %v", err)
				}
				// 用户用量对账:以 files 表为权威修正 users.used_space 漂移
				// if _, err := ReconcileUserUsages(db); err != nil {
				// 	log.Errorf("cron: reconcile user usages: %v", err)
				// }
				return nil
			}))
		}
	}()
}

// CleanExpiredLogs 清理超过保留期的日志(operation_logs 与 logs 两表),分批直到清完。
// retentionDays <= 0 → 默认 30;必须 Unscoped() 物理删除(软删仍占表空间,防膨胀目的落空)。
func CleanExpiredLogs(db *gorm.DB, retentionDays, batchSize int) (int64, error) {
	if retentionDays <= 0 {
		retentionDays = 30
	}
	if batchSize <= 0 {
		batchSize = 100
	}
	cutoff := time.Now().AddDate(0, 0, -retentionDays)

	var total int64
	for {
		res := db.Unscoped().Where("created_at < ?", cutoff).Limit(batchSize).Delete(&model.OperationLog{})
		if res.Error != nil {
			return total, res.Error
		}
		total += res.RowsAffected
		resLog := db.Unscoped().Where("created_at < ?", cutoff).Limit(batchSize).Delete(&model.Log{})
		if resLog.Error != nil {
			return total, resLog.Error
		}
		total += resLog.RowsAffected
		if res.RowsAffected == 0 && resLog.RowsAffected == 0 {
			break
		}
		time.Sleep(5 * time.Second) // 每 5 秒清一批
	}
	log.Infof("cron: cleaned %d expired log records (retention %d days)", total, retentionDays)
	return total, nil
}

// CleanExpiredTokens 物理清理过期/已吊销的刷新令牌(refresh_tokens 白名单防膨胀)。
// 软删记录仍占唯一索引(token 哈希)与磁盘空间,必须 Unscoped() 真正删除。
func CleanExpiredTokens(db *gorm.DB, batchSize int) (int64, error) {
	if batchSize <= 0 {
		batchSize = 100
	}
	var total int64
	for {
		res := db.Unscoped().Where("expires_at < ? OR revoked_at IS NOT NULL", time.Now()).
			Limit(batchSize).Delete(&model.RefreshToken{})
		if res.Error != nil {
			return total, res.Error
		}
		if res.RowsAffected == 0 {
			break
		}
		total += res.RowsAffected
	}
	log.Infof("cron: cleaned %d expired/revoked refresh tokens", total)
	return total, nil
}

// CleanExpiredShares 清理已过期的分享链接(软删;过期即失效,保留无意义)。
func CleanExpiredShares(db *gorm.DB, batchSize int) (int64, error) {
	if batchSize <= 0 {
		batchSize = 100
	}
	var total int64
	for {
		res := db.Where("expires_at < ?", time.Now()).
			Limit(batchSize).Delete(&model.ShareLink{})
		if res.Error != nil {
			return total, res.Error
		}
		if res.RowsAffected == 0 {
			break
		}
		total += res.RowsAffected
	}
	log.Infof("cron: cleaned %d expired shares", total)
	return total, nil
}

// CleanExpiredDownloadTasks 物理清理超过保留期(默认 7 天)的下载任务。// 下载任务是断点续传的临时登记(断点权威在客户端本地),完成即 DELETE;
// 未完成但超期未恢复的遗留行保留无意义,物理删除防表膨胀。
// 被清理后客户端恢复下载不硬断:前端重新登记任务并续传。
func CleanExpiredDownloadTasks(db *gorm.DB, retentionDays, batchSize int) (int64, error) {
	if retentionDays <= 0 {
		retentionDays = 7
	}
	if batchSize <= 0 {
		batchSize = 100
	}
	cutoff := time.Now().AddDate(0, 0, -retentionDays)

	var total int64
	for {
		res := db.Unscoped().Where("created_at < ?", cutoff).
			Limit(batchSize).Delete(&model.DownloadTask{})
		if res.Error != nil {
			return total, res.Error
		}
		if res.RowsAffected == 0 {
			break
		}
		total += res.RowsAffected
	}
	log.Infof("cron: cleaned %d expired download tasks (retention %d days)", total, retentionDays)
	return total, nil
}

// 预备的函数
// ReconcileUserUsages 用户用量对账:以 files 表为权威口径
// (Unscoped 统计该用户全部文件记录,含回收站软删行——软删不归还用量,
// 与 users.used_space 记账语义一致),逐用户比对并修正漂移。
// 修正方式:直接 Update 覆盖(used_space 为冗余统计,对账即权威)。
// 已知局限:新增占用按"行为者"记账(上传/复制/写回增长),跨所有者写回
// (操作者 ≠ UploadedBy 扩展文件)时,对账口径(按 UploadedBy 求和)与记账存在
// 偏差,对账会向所有者口径收敛——属可接受的兜底行为(阻止无界漂移)。
// 返回:修正用户数;错误:任一步失败(记日志,不影响其他定时任务)。
func ReconcileUserUsages(db *gorm.DB) (int, error) {
	var users []model.User // 全部用户(仅取 id/用量)
	if err := db.Select("id", "used_space").Find(&users).Error; err != nil {
		return 0, fmt.Errorf("reconcile user usages: query users: %w", err)
	}
	fixed := 0 // 修正计数
	for i := range users {
		u := &users[i]     // 当前用户
		var expected int64 // 期望用量(该用户全部记录大小和)
		if err := db.Unscoped().Model(&model.File{}).
			Where("uploaded_by = ?", u.ID).
			Select("COALESCE(SUM(file_size), 0)").Scan(&expected).Error; err != nil {
			return fixed, fmt.Errorf("reconcile user usages: sum user %d: %w", u.ID, err)
		}
		if expected != u.UsedSpace { // 漂移?
			if err := db.Model(&model.User{}).
				Where("id = ?", u.ID).Update("used_space", expected).Error; err != nil {
				return fixed, fmt.Errorf("reconcile user usages: fix user %d: %w", u.ID, err)
			}
			fixed++ // 计数
			log.Infof("cron: reconcile user %d used_space %d -> %d", u.ID, u.UsedSpace, expected)
		}
	}
	return fixed, nil
}
