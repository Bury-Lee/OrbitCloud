package cron

// Package cron 定时任务:周期清理过期数据(日志/刷新令牌/分享/下载任务),
// 并周期续跑删除/复制任务表(扫描任务表 → 委托 server.ProcessDeleteTask /
// server.ProcessCopyFolderTask,单任务内任务级锁保证并发安全)。

import (
	"context"
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

// CleanExpiredDownloadTasks 物理清理超过保留期(默认 7 天)的下载任务。
// 下载任务是断点续传的临时登记(断点权威在客户端本地),完成即 DELETE;
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
