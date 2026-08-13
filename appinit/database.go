package appinit

// 数据库初始化:建立连接 + 自动建表 + 自检重连;按运行模式调整 GORM 日志级别。

import (
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"orbitcloud/config"
	"orbitcloud/log"
)

// InitDB 初始化元数据库(自动建表 + 重连;不可用将持续重试)。
// 运行模式为 debug 时 GORM 日志切 Info(SQL 全量输出)。
// 返回 *gorm.DB;由 main 赋值给 core.DB。
func InitDB(cfg *config.Config) (*gorm.DB, error) {
	db, err := cfg.Database.InitDB(gormLoggerLevel(cfg.Database.LogLevel))
	if err != nil {
		return nil, err
	}
	return log.DB_log(db, cfg.Server.Mode), nil
}

// gormLoggerLevel 把 config 的 log_level 字符串映射为 log 包的 GORM logger。
func gormLoggerLevel(lvl string) logger.Interface {
	switch strings.ToLower(lvl) {
	case "silent":
		return log.NewGormLogger(logger.Silent)
	case "error":
		return log.NewGormLogger(logger.Error)
	case "info":
		return log.NewGormLogger(logger.Info)
	default: // "" / "warn"
		return log.NewGormLogger(logger.Warn)
	}
}
