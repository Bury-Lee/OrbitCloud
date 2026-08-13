// delete_task.go —— 删除任务(桶删除 + 目录删除共用)。
//
// 用途:删除桶 / 删除目录时先把目标置为"不可用"(桶 Status=0;目录 Isable=false),
// 再落任务;后台/启动续跑分批物理清理:
//   - 桶删除:每轮现查 files(bucket_id = ?)取条目主键,先删对象 key 再删记录,"边删边查";
//   - 目录删除:深度优先逐文件夹——取某文件夹直接子目录的第一个往下递归,
//     没有更深的子目录为止,删除内部所有其文件,然后删除该目录,再到下一个;
//     避免一次全部删除给数据库压力太大;任务开始时目录 Isable 已置 false
//     (不可用不可达);
//   - 对象键不落快照,每轮现查(目标已禁用,不会再产生新条目);
//   - 并发防护:任务级数据库锁字段(LockOwner/LockExpiresAt)乐观抢占,
//     防启动扫描与 cron 周期并发处理同一任务。
//
// 中断场景不会产生"对象已删但元数据悬空"或"孤儿对象"。
package model

import (
	"time"

	"gorm.io/gorm"
)

// DeleteTask 删除任务。
type DeleteTask struct {
	gorm.Model
	BucketID uint `gorm:"index;not null"`  // 待删除桶 buckets.id
	DirID    uint `gorm:"index;default:0"` // 待删除目录 folders.id;0 = 桶删除
	Status   int  `gorm:"default:0;index"` // 0 待处理 / 1 处理中(完成即硬删任务记录)

	// LockOwner 锁持有者标识(进程实例唯一串,如 hostname-pid;空 = 无锁)。
	// 处理前条件更新抢占(乐观,不用 SELECT FOR UPDATE);完成释放带持有者条件。
	LockOwner     string     `gorm:"type:varchar(64);index"` // 锁持有者(core.InstanceID)
	LockExpiresAt *time.Time // 锁过期时间(进程崩溃后其他实例可抢占,防死锁)
}

// TableName delete_tasks。
func (DeleteTask) TableName() string { return "delete_tasks" }
