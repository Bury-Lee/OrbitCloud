// folder.go —— 目录树节点(folders 表)。
// 权威结构 = (bucket_id, parent_id, name),完整路径为派生值(沿 parent 链向上拼接,读取时计算)。
//
// 桶根语义(桶名 = 虚拟根,无实例行):
//   - ParentID=0 表示"父为桶根"(该文件夹直接挂在桶根下);
//   - 桶根下文件的 FolderID=0;
//   - 路径解析从桶根开始(第一段查 parent_id=0),桶名不落库。
//
// 删除语义(Isable 三态,见 FolderUnusable / FolderUsable / FolderDeleting):
//   - FolderUnusable(0)="已被删除":其下所有子文件/子文件夹立即不可用不可达(读路径 404);
//   - FolderDeleting(2)="正在删除":删除任务处理中,文件夹可见可读,但拒绝一切写入
//     (上传/建目录/移动/复制目标),内部条目清空后才转入 FolderUnusable + 软删;
//   - 删除进回收站 = 最终 FolderUnusable + GORM 软删(置 deleted_at=now),记录/对象/用量保留,
//     回收站清除时才物理清理(见 server/file_delete.go 与 DeleteTask)。
//
// 大小写语义:NameLower 存小写,唯一索引/查找全部走 NameLower。
package model

import (
	"strings"

	"gorm.io/gorm"
)

// 文件夹可用状态(Isable 三态)。
const (
	FolderUnusable = 0 // 已删除/不可达(读路径 404)
	FolderUsable   = 1 // 正常(读写均允许)
	FolderDeleting = 2 // 正在删除(可见可读,拒绝写入;删除任务处理中)
)

// Folder 目录树节点。
type Folder struct {
	gorm.Model
	Isable     int8   `gorm:"default:1"`                                                         // 可用状态:0 已删除/1 正常/2 正在删除(见 FolderUnusable/FolderUsable/FolderDeleting)
	BucketID   uint   `gorm:"uniqueIndex:uk_folder_lower,priority:1;index;not null"`             // 所属桶 buckets.id
	ParentID   uint   `gorm:"uniqueIndex:uk_folder_lower,priority:2;index;not null"`             // 父目录 folders.id;0 = 桶根(虚拟,无实例)
	Name       string `gorm:"type:varchar(255);not null"`                                        // 目录名(不含 "/")
	NameLower  string `gorm:"type:varchar(255);uniqueIndex:uk_folder_lower,priority:3;not null"` // 小写规范化(唯一索引/查找用,BeforeSave 维护)
	UploadedBy uint   `gorm:"index;not null"`                                                    // 创建者 users.id
	// 条目级可见性(用户组 ACL):可见组 ID 的 JSON 数组字符串(如 "[1,5]");
	// 空串 = 不限制(按桶权限);非空 = 仅创建者/管理员/组内成员可访问(见 server/visibility.go)。
	VisibleToGroups string `gorm:"type:text"` // 可见用户组(user_groups.id)JSON 数组
}

// TableName folders。
func (Folder) TableName() string { return "folders" }

// BeforeSave 自动维护小写规范化列。
func (f *Folder) BeforeSave(tx *gorm.DB) error {
	f.NameLower = strings.ToLower(f.Name)
	return nil
}
