// Package model 数据库模型定义(GORM)。
//
// 核心表:User(账号)、Bucket(存储桶)、Folder(目录树节点,parent_id 自引用,
// ParentID=0 = 桶根虚拟)、File(文件条目,FolderID=0 = 桶根);
// 扩展表:UserGroup + UserGroupMember(用户组,条目级可见性 ACL)、
// RefreshToken(登录刷新)、ShareLink(分享,ItemType 区分 file/folder)、
// OperationLog(操作审计,可选)、DeleteTask/CopyTask(后台删除/复制任务)。
//
// 统一约定:
//   - 所有表内嵌 gorm.Model(ID/CreatedAt/UpdatedAt/DeletedAt);
//   - 删除策略:桶/文件/文件夹/任务表采用硬删除;文件夹删除先置 Isable=false
//     (逻辑不可达)再后台硬删;用户/刷新令牌/分享保留软删;刷新令牌与日志表由
//     cron 定时硬删;
//   - 状态字段 int:1 正常 / 0 禁用;
//   - 唯一索引兜底并发创建,业务层负责把约束错误转成友好提示;
//   - 时间统一 UTC 存储。
package model

import (
	"time"

	"gorm.io/gorm"
)

// User 账号。User 不一定是人类,也可能是代表某台长期登录的计算机。
type User struct {
	gorm.Model
	Username        string     `gorm:"type:varchar(64);uniqueIndex;not null"` // 登录名(全局唯一)
	Password        string     `gorm:"type:varchar(255);not null"`            // 密码哈希(bcrypt),禁止明文
	Name            string     `gorm:"type:varchar(64)"`                      // 名字(可选)
	Remarks         string     // 备注
	Email           string     `gorm:"type:varchar(128);index"` // 邮箱(可选,找回/通知)
	PermissionLevel int8       `gorm:"default:1;index"`         // 权限:0 最高,数值越大权限越低
	LastLogin       *time.Time // 最近登录时间
	Status          int        `gorm:"default:1;index"` // 1 正常 0 禁用
}

// PermissionLevel 权限级别类型(数值越小权限越高)
type PermissionLevel int8

// 权限级别常量(0~9,分级授权用;数值越小权限越高)
// 权限级别常量 (0~3, 数值越小权限越高)
// 极简通用权限模型，适用于大多数企业/SaaS平台
const (
	// SuperAdmin 超级管理员 - 系统最高权限，拥有所有租户/全局资源控制权
	// 对应场景：平台创始人、系统负责人
	SuperAdmin PermissionLevel = 0

	// Admin 管理员 - 管理本租户/组织内所有资源，受平台策略约束
	// 对应场景：企业主、部门负责人、项目主管
	Admin PermissionLevel = 1

	// SpecialUser 特殊用户 - 拥有超越普通用户的特定权限（如数据导出、审批等）
	// 对应场景：财务人员、人事专员、高级分析师
	SpecialUser PermissionLevel = 2

	// User 普通用户 - 基础操作权限，只能管理自己的数据和被授权的协作内容
	// 对应场景：普通员工、注册会员
	NormalUser PermissionLevel = 3
)

func (p PermissionLevel) String() string {
	switch p {
	case SuperAdmin:
		return "超级管理员"
	case Admin:
		return "管理员"
	case SpecialUser:
		return "特殊用户"
	case NormalUser:
		return "普通用户"
	default:
		return "未知"
	}
}

// TableName users。
func (User) TableName() string { return "users" }

// Bucket 存储桶(一个桶对应对象存储中的一个 bucket / 前缀)。
type Bucket struct {
	gorm.Model
	Name            string `gorm:"type:varchar(100);uniqueIndex;not null"` // 桶名(全局唯一)
	Description     string `gorm:"type:varchar(255)"`                      // 描述
	PermissionLevel int8   `gorm:"default:1;index"`                        // 访问所需最低权限(0 最高)
	OwnerID         uint   `gorm:"index;not null"`                         // 创建者 users.id
	Owner           User   `gorm:"foreignKey:OwnerID"`                     // 关联对象(懒加载)
	Quota           int64  `gorm:"default:0"`                              // 容量配额(字节;0=不限)
	UsedSpace       int64  `gorm:"default:0"`                              // 已用空间(冗余统计,可后台校正)
	Status          int    `gorm:"default:1;index"`                        // 1 正常 / 0 禁用(含删除中:置 0 后不可上传/读取,删除任务完成即删记录)
}

// TableName buckets。
func (Bucket) TableName() string { return "buckets" }
