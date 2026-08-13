// user_group.go —— 用户组(user_groups 表)+ 用户-组关联(user_group_members 表)。
//
// 用途:文件/文件夹可配置"仅指定组可见"——建立用户组与成员关联,实现比单维
// PermissionLevel 更灵活的权限配置。
//
// 权限语义:
//   - File/Folder.VisibleToGroups 字段存 JSON 数组(组 ID 列表,如 "[1,5]");
//   - 空串 = 不限制,按桶级权限可见(现有行为);
//   - 非空 = 仅创建者 / 管理员(权限 <= 1)/ 可见组内成员可访问,权限级不兜底;
//   - 组没有权限等级概念:加入组不改变用户自身 PermissionLevel,组不参与
//     等级体系判定。
package model

import "gorm.io/gorm"

// UserGroup 用户组(部门/项目组等逻辑分组)。
type UserGroup struct {
	gorm.Model
	Name        string `gorm:"type:varchar(64);uniqueIndex;not null"` // 组名(全局唯一,大小写敏感)
	Description string `gorm:"type:varchar(255)"`                     // 描述
	CreatedBy   uint   `gorm:"index;not null"`                        // 创建者 users.id
	Status      int    `gorm:"default:1;index"`                       // 1 正常 / 0 禁用
}

// TableName user_groups。
func (UserGroup) TableName() string { return "user_groups" }

// UserGroupMember 用户-组关联(中间表,联合唯一兜底并发重复添加)。
type UserGroupMember struct {
	gorm.Model
	GroupID uint `gorm:"uniqueIndex:uk_ugm,priority:1;index;not null"` // 组 user_groups.id
	UserID  uint `gorm:"uniqueIndex:uk_ugm,priority:2;index;not null"` // 用户 users.id
}

// TableName user_group_members。
func (UserGroupMember) TableName() string { return "user_group_members" }
