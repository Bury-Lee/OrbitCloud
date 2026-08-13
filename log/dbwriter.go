package log

// 日志入库实现:把日志写入 logs 表(model.Log)。
//   - EnableLogDBWriter 直接读取 core 全局变量(core.GlobalConfig.Log.DBLevel / core.DB)
//     判断是否启用,内部注册写库 sink;
//   - persistLog 执行 INSERT:失败自动重试 3 次(50ms/100ms 退避),全部失败经
//     LocalNote 本地补记一条(绕过 sink,防递归);
//   - 防递归:写库经 db.Session(&gorm.Session{Logger: logger.Discard}) 静音 GORM 日志;
//   - 并发写库经 dbWriteMu 串行化(适配 SQLite 单写者,对 PostgreSQL 亦无害)。
//
// 依赖方向:log → core(读全局)→ config,无循环引用。

import (
	"fmt"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"orbitcloud/core"
	"orbitcloud/model"
)

// dbWriteMu 串行化日志写库:SQLite 单写者场景防锁竞争;PG 场景亦无害(日志量小)。
var dbWriteMu sync.Mutex

// EnableLogDBWriter 启用日志入库(由 main 在 core.GlobalConfig / core.DB 就绪后调用)。
// 全局变量未就绪或 config.Log.DBLevel 为空/非法时直接跳过;否则注册内部写库 sink,
// 把该级别及以上的日志写入 logs 表。可重复调用(幂等:按当前全局配置重新注册)。
func EnableLogDBWriter() {
	cfg := core.GlobalConfig
	if cfg == nil || core.DB == nil {
		return
	}
	minLvl, ok := ParseDBLevel(cfg.Log.DBLevel)
	if !ok {
		return // 未启用
	}
	app := cfg.App.Name
	db := core.DB

	SetLogSink(func(lvl Level, msg string) {
		if lvl < minLvl {
			return // 低于最低入库级别
		}
		persistLog(db, app, lvl, msg)
	})
	Infof("log: db writer enabled (min level %s)", minLvl.String())
}

// persistLog 把一条日志写入 logs 表(model.Log):INSERT 失败自动重试 3 次,
// 全部失败本地补记一条 LocalNote(绕过 sink,防递归)。
func persistLog(db *gorm.DB, app string, lvl Level, msg string) {
	dbWriteMu.Lock()
	defer dbWriteMu.Unlock()

	// 防递归:Discard GORM 日志(Session 浅拷贝,不共享 Logger,防 GormLogger 回调再触发 sink)
	sdb := db.Session(&gorm.Session{Logger: logger.Discard})
	// 内容截断(防长 SQL/大文本撑爆 text 列)
	const maxContent = 2000
	if len(msg) > maxContent {
		msg = msg[:maxContent] + "...(truncated)"
	}

	rec := &model.Log{App: app, Level: int8(lvl), Content: msg}
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 50 * time.Millisecond) // 50ms / 100ms
		}
		if err = sdb.Create(rec).Error; err == nil {
			return
		}
	}
	LocalNote(fmt.Sprintf("[log-db] persist failed after 3 retries: %v (level=%s content=%.120s)", err, lvl.String(), msg))
}
