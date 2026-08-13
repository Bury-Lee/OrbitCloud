// folder.go —— 目录树节点(folders 表)。
// 权威结构 = (bucket_id, parent_id, name),完整路径为派生值(沿 parent 链向上拼接,读取时计算)。
//
// 桶根语义(桶名 = 虚拟根,无实例行):
//   - ParentID=0 表示"父为桶根"(该文件夹直接挂在桶根下);
//   - 桶根下文件的 FolderID=0;
//   - 路径解析从桶根开始(第一段查 parent_id=0),桶名不落库。
//
// 删除语义:
//   - Isable=false 表示"已被删除":其下所有子文件/子文件夹立即不可用不可达(读路径 404);
//   - 物理清理由后台删除任务深度优先逐文件夹完成;Isable 为回收站功能预留。
//
// 大小写语义:NameLower 存小写,唯一索引/查找全部走 NameLower。
package model

import (
	"strings"

	"gorm.io/gorm"
)

// Folder 目录树节点。
type Folder struct {
	gorm.Model
	Isable     bool   `gorm:"default:true"`                                                      // 可用标记:false = 已删除(不可用不可达),其所有子文件同样不可达
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
