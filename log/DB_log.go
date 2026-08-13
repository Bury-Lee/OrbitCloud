package log

// GORM 专用日志封装:实现 gorm.io/gorm/logger.Interface,
// 把 GORM 日志接入本 log 包——输出目标 / 级别过滤 / 文件轮转与全局日志一致,
// 日志头为统一的 [时间] [级别] 格式(不使用 gorm 默认的 os.Stdout 与 [file:line] 头)。
// 用法(见 appinit.InitDB):
//
//	gorm.Open(dialector, &gorm.Config{Logger: log.NewGormLogger(logger.Info)})
//	db.Logger = log.NewGormLogger(logger.Info) // 或 db.Debug()(等价 LogMode(Info))

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// GormLogger 实现 gorm.io/gorm/logger.Interface,是 GORM 与 log 包之间的适配层。
type GormLogger struct {
	// LogLevel GORM 日志级别: logger.Silent / Error / Warn / Info。
	LogLevel logger.LogLevel
	// SlowThreshold 慢查询阈值;<=0 时禁用慢查询告警(默认 200ms)。
	SlowThreshold time.Duration
	// IgnoreRecordNotFoundError 为 true 时,查询无记录(ErrRecordNotFound)不记 Error。
	IgnoreRecordNotFoundError bool
}

// NewGormLogger 创建接入 log 包的 GORM logger。
// level 为 GORM 日志级别(logger.Silent / Error / Warn / Info)。
// 默认 SlowThreshold=200ms、IgnoreRecordNotFoundError=false(与 gorm 默认行为一致)。
func NewGormLogger(level logger.LogLevel) *GormLogger {
	return &GormLogger{
		LogLevel:                  level,
		SlowThreshold:             200 * time.Millisecond,
		IgnoreRecordNotFoundError: false,
	}
}

// LogMode 返回同配置、不同日志级别的副本(GORM 内部会调用,如 DB.Debug()。
// 返回值仍是 *GormLogger,即 DB.Debug() 之后日志依旧走 log 包)。
func (l *GormLogger) LogMode(level logger.LogLevel) logger.Interface {
	nl := *l
	nl.LogLevel = level
	return &nl
}

// Info 记录 GORM info 级日志(如 AutoMigrate 的表结构变更)。
func (l *GormLogger) Info(_ context.Context, msg string, data ...interface{}) {
	if l.LogLevel >= logger.Info {
		Infof("[gorm] "+msg, data...)
	}
}

// Warn 记录 GORM warn 级日志(如慢查询)。
func (l *GormLogger) Warn(_ context.Context, msg string, data ...interface{}) {
	if l.LogLevel >= logger.Warn {
		Warnf("[gorm] "+msg, data...)
	}
}

// Error 记录 GORM error 级日志。
func (l *GormLogger) Error(_ context.Context, msg string, data ...interface{}) {
	if l.LogLevel >= logger.Error {
		Errorf("[gorm] "+msg, data...)
	}
}

// Trace 记录一条 SQL(耗时/影响行数/错误),判定规则与 gorm 默认 logger 对齐:
//   - err != nil → Error 级别(ErrRecordNotFound 是否忽略见 IgnoreRecordNotFoundError);
//   - 超过 SlowThreshold → Warn 级别(慢查询);
//   - LogLevel == Info → Info 级别(全量 SQL,调试用)。
func (l *GormLogger) Trace(_ context.Context, begin time.Time, fc func() (sql string, rowsAffected int64), err error) {
	if l.LogLevel <= logger.Silent {
		return
	}

	elapsed := time.Since(begin)
	switch {
	case err != nil && l.LogLevel >= logger.Error && (!errors.Is(err, logger.ErrRecordNotFound) || !l.IgnoreRecordNotFoundError):
		sql, rows := fc()
		Errorf("[gorm] %v [%.3fms] [rows:%s] %s", err, float64(elapsed.Nanoseconds())/1e6, rowsStr(rows), sql)
	case elapsed > l.SlowThreshold && l.SlowThreshold > 0 && l.LogLevel >= logger.Warn:
		sql, rows := fc()
		Warnf("[gorm] SLOW SQL >= %v [%.3fms] [rows:%s] %s", l.SlowThreshold, float64(elapsed.Nanoseconds())/1e6, rowsStr(rows), sql)
	case l.LogLevel == logger.Info:
		sql, rows := fc()
		Infof("[gorm] [%.3fms] [rows:%s] %s", float64(elapsed.Nanoseconds())/1e6, rowsStr(rows), sql)
	}
}

// rowsStr 把 rowsAffected(-1 表示未知/不适用)格式化为展示字符串。
func rowsStr(rows int64) string {
	if rows == -1 {
		return "-"
	}
	return fmt.Sprintf("%d", rows)
}

// DB_log 按运行模式调整 GORM 日志级别(供 appinit.InitDB 在 DB 就绪后调用)。
// mode 取自 config.Server.Mode(debug 时 SQL 全量输出),配置值由调用方传入,避免循环引用。
func DB_log(DB *gorm.DB, mode string) *gorm.DB {
	if mode == "debug" {
		// Debug 模式:GORM 日志切到 Info 级别(SQL 全量输出)。
		// DB.Debug() 等价于 LogMode(Info);LogMode 返回的仍是 log 包副本,日志输出与全局一致。
		DB = DB.Debug()
		DB.Logger = NewGormLogger(logger.Info) // 显式 Info 级别(SQL 全量输出)
	}
	return DB
}
