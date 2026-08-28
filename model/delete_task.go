// delete_task.go —— 删除任务(桶删除 + 目录删除共用)。
//
// 用途:删除桶 / 删除目录时落任务,后台/启动续跑分批清理:
//   - 桶删除:先置桶禁用(Status=0),再落任务;处理时删对象 + 硬删记录,单事务收尾;
//   - 目录删除:落任务时目录置 FolderDeleting(可见可读,拒绝新写入),
//     后台深度优先**先删内部再删文件夹自身**:每轮软删文件夹下全部文件
//     (进回收站),子文件夹压栈内部优先,无活跃子文件夹且文件清空后才软删
//     文件夹自身(置 FolderUnusable + 软删);中断时子树结构完整,可续跑/恢复;
//   - OperatorID/OperatorLevel:发起删除的操作者与权限快照,处理时按批动态
//     重查(禁用/降权/删号 → 暂停任务),0 = 存量任务按旧语义放行;
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

	// OperatorID / OperatorLevel:发起删除的操作者与权限快照。
	// 处理时按批动态重查操作者最新权限(禁用/降权即中止任务),快照仅用于
	// 语义判断与审计追溯;0 = 存量任务(无快照,按旧语义放行)。
	OperatorID    uint `gorm:"index"`     // 操作者 users.id
	OperatorLevel int  `gorm:"default:0"` // 发起时的权限等级快照(0 超管/1 管理员/3 普通)

	// LockOwner 锁持有者标识(进程实例唯一串,如 hostname-pid;空 = 无锁)。
	// 处理前条件更新抢占(乐观,不用 SELECT FOR UPDATE);完成释放带持有者条件。
	LockOwner     string     `gorm:"type:varchar(64);index"` // 锁持有者(core.InstanceID)
	LockExpiresAt *time.Time // 锁过期时间(进程崩溃后其他实例可抢占,防死锁)
}

// TableName delete_tasks。
func (DeleteTask) TableName() string { return "delete_tasks" }
