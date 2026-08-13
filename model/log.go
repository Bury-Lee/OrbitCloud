package model

import "gorm.io/gorm"

// Log 通用日志入库表(config.Log.DBLevel 非空时由日志系统写入)。
// 保留期由 cron 定时任务按 config.Log.RetentionDays 分批清理。
type Log struct {
	gorm.Model
	App     string `gorm:"type:varchar(64);index"` // 应用名(如 orbitcloud)
	Level   int8   `gorm:"default:2;index"`        // 0 debug / 1 info / 2 warn / 3 error
	Content string `gorm:"type:text"`              // 日志内容
}

// TableName logs。
func (Log) TableName() string { return "logs" }

// OperationLog 操作审计日志(可选启用;量大会膨胀,建议按需开启/定期归档)。
type OperationLog struct {
	gorm.Model
	UserID     uint   `gorm:"index"`                  // 操作者 users.id(0 = 未登录)
	Action     string `gorm:"type:varchar(32);index"` // login | register | upload | download | delete | share | ...
	TargetType string `gorm:"type:varchar(32)"`       // user | bucket | file
	TargetID   string `gorm:"type:varchar(64)"`       // 目标主键
	Detail     string `gorm:"type:varchar(500)"`      // 补充信息(文件名 / 路径等)
	IP         string `gorm:"type:varchar(45)"`       // 来源 IP
}

// TableName operation_logs。
func (OperationLog) TableName() string { return "operation_logs" }
