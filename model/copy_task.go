// copy_task.go —— 复制任务表(文件夹复制 / 单文件跨桶移动)。
//
// 语义:
//   - 文件夹复制(同桶/跨桶):不同步复制整棵子树,而是落 CopyTask →
//     后台深度优先逐文件夹复制(每次只查一层子文件夹,处理完一个再取下一个,
//     避免一次性把整棵子树载入,数据库压力可控);
//   - 单文件跨桶移动:落 CopyTask{SourceFileID} → 任务内先复制(不重命名,
//     目标已存在即跳过,幂等)再删源(两步串行,等价"复制任务+删除任务");
//     失败保留任务,由启动扫描 / cron 续跑,不会产生半成品或重复副本。
// 并发防护:与 DeleteTask 同模式——任务级锁字段(LockOwner/LockExpiresAt)
// 乐观抢占,防启动扫描与 cron 周期并发处理同一任务。
package model

import (
	"time"

	"gorm.io/gorm"
)

// CopyTask 复制任务(文件夹复制 / 单文件跨桶移动共用)。
type CopyTask struct {
	gorm.Model
	BucketID       uint   `gorm:"index;not null"`             // 源桶 buckets.id
	SourceFolderID uint   `gorm:"index;not null"`             // 源文件夹 folders.id(文件夹复制;文件移动任务为 0)
	SourceFileID   uint   `gorm:"index"`                      // 源文件 files.id(单文件跨桶移动;>0 时按文件任务分派)
	DstBucketID    uint   `gorm:"index;not null"`             // 目标桶 buckets.id
	DstFolderID    uint   `gorm:"index;not null"`             // 目标侧已预建的顶层目录 folders.id(任务处理时直接复用;文件移动任务为目标父目录 ID)
	DstDirPath     string `gorm:"type:varchar(512);not null"` // 目标父目录(路径;任务处理时解析为 folder_id)
	DstName        string `gorm:"type:varchar(255);not null"` // 目标根目录名(冲突自动重命名后的名字;文件移动任务为目标文件名)
	UploadedBy     uint   `gorm:"index;not null"`             // 操作者 users.id(目标侧新建目录/文件的创建者)
	// OperatorLevel 发起复制时的权限等级快照(处理时按批动态重查操作者
	// 最新权限,禁用/降权即中止任务;0 = 存量任务,按旧语义放行)。
	OperatorLevel int `gorm:"default:0"` // 发起时的权限等级快照(0 超管/1 管理员/3 普通)
	Status        int `gorm:"default:0"` // 0 待处理 / 1 处理中(完成即硬删任务记录)

	// LockOwner / LockExpiresAt:处理锁(条件更新抢占,带持有者条件释放)。
	LockOwner     string     `gorm:"type:varchar(64);index"` // 锁持有者(core.InstanceID,如 hostname-pid)
	LockExpiresAt *time.Time // 锁过期时间(进程崩溃后其他实例可抢占,防死锁)
}

// TableName copy_tasks。
func (CopyTask) TableName() string { return "copy_tasks" }
