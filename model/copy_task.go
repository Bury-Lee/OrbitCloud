// copy_task.go —— 文件夹复制任务表。
//
// 语义:文件夹复制(同桶/跨桶)不同步复制整棵子树,而是落 CopyTask →
// 后台深度优先逐文件夹复制(每次只查一层子文件夹,处理完一个再取下一个,
// 避免一次性把整棵子树载入,数据库压力可控)。
// 并发防护:与 DeleteTask 同模式——任务级锁字段(LockOwner/LockExpiresAt)
// 乐观抢占,防启动扫描与 cron 周期并发处理同一任务。
package model

import (
	"time"

	"gorm.io/gorm"
)

// CopyTask 文件夹复制任务。
type CopyTask struct {
	gorm.Model
	BucketID       uint   `gorm:"index;not null"`             // 源桶 buckets.id
	SourceFolderID uint   `gorm:"index;not null"`             // 源文件夹 folders.id
	DstBucketID    uint   `gorm:"index;not null"`             // 目标桶 buckets.id
	DstFolderID    uint   `gorm:"index;not null"`             // 目标侧已预建的顶层目录 folders.id(任务处理时直接复用)
	DstDirPath     string `gorm:"type:varchar(512);not null"` // 目标父目录(路径;任务处理时解析为 folder_id)
	DstName        string `gorm:"type:varchar(255);not null"` // 目标根目录名(冲突自动重命名后的名字)
	UploadedBy     uint   `gorm:"index;not null"`             // 操作者 users.id(目标侧新建目录/文件的创建者)
	Status         int    `gorm:"default:0"`                  // 0 待处理 / 1 处理中(完成即硬删任务记录)

	// LockOwner / LockExpiresAt:处理锁(条件更新抢占,带持有者条件释放)。
	LockOwner     string     `gorm:"type:varchar(64);index"` // 锁持有者(core.InstanceID,如 hostname-pid)
	LockExpiresAt *time.Time // 锁过期时间(进程崩溃后其他实例可抢占,防死锁)
}

// TableName copy_tasks。
func (CopyTask) TableName() string { return "copy_tasks" }
