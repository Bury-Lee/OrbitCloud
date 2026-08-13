// file.go —— 文件条目(files 表)。实体数据存对象存储,key = 主键 ID 字符串。
//
// 本表只含文件:"文件夹"概念整体迁入 folders 表;桶根 = 虚拟(桶名即根),
// 桶根下文件 FolderID=0,无根实例行。
//
// 唯一性:(bucket_id, folder_id, name_lower) 联合唯一——只在同一文件夹内生效,
// 不同目录下重名完全允许;兜底"同文件夹内并发同名"。
//
// 对象键设计:S3 key = 条目记录主键 ID 转字符串,不依赖文件名/内容/路径;
// 每个记录独占一个对象 → 复制粘贴(同内容新建文件)互不影响,删除互不牵连。
package model

import (
	"strings"

	"gorm.io/gorm"
)

// File 文件条目。
type File struct {
	gorm.Model
	BucketID   uint   `gorm:"uniqueIndex:uk_file_lower,priority:1;index;not null"`             // 所属桶 buckets.id
	FolderID   uint   `gorm:"uniqueIndex:uk_file_lower,priority:2;index;not null"`             // 所在文件夹 folders.id;0 = 桶根(虚拟)
	Name       string `gorm:"type:varchar(255);not null"`                                      // 文件名(不含 "/"、".."及 Windows 禁止符号)
	NameLower  string `gorm:"type:varchar(255);uniqueIndex:uk_file_lower,priority:3;not null"` // 小写规范化(唯一索引/查找用,BeforeSave 维护)
	FileSize   int64  `gorm:"default:0"`                                                       // 字节
	FileType   string `gorm:"type:varchar(50)"`                                                // MIME 类型(不再有 "Folder" 值)
	MD5        string `gorm:"type:varchar(32);index"`                                          // 采样 MD5(下载校验/审计)
	UploadedBy uint   `gorm:"index;not null"` // 创建者 users.id
	// 条目级可见性(用户组 ACL):可见组 ID 的 JSON 数组字符串(如 "[1,5]");
	// 空串 = 不限制(按桶权限);非空 = 仅创建者/管理员/组内成员可访问(见 server/visibility.go)。
	VisibleToGroups string `gorm:"type:text"` // 可见用户组(user_groups.id)JSON 数组
}

// TableName files。
func (File) TableName() string { return "files" }

// BeforeSave 自动维护小写规范化列。
func (f *File) BeforeSave(tx *gorm.DB) error {
	f.NameLower = strings.ToLower(f.Name)
	return nil
}
