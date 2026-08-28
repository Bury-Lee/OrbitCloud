// Package migrate 数据库结构升级器(区别于 gorm AutoMigrate 的表/列同步):
// 每个"历史数据/结构升级"注册为带版本号的步骤,启动时按版本顺序幂等应用,
// 已应用版本记录在 schema_versions 表;步骤内部自带幂等(IF EXISTS 等),
// 中途失败不记版本,下次启动重试。由 config.InitDB 在 AutoMigrate 之后调用。
package migrate

import (
	"fmt"
	"log"
	"time"

	"gorm.io/gorm"
)

// CurrentVersion 全部已注册升级步骤的集合版本(新增步骤时 +1)。
const CurrentVersion = 1

// step 单个升级步骤。
type step struct {
	version int                  // 版本号(升序,≥1)
	desc    string               // 描述(日志/错误定位用)
	apply   func(*gorm.DB) error // 应用逻辑(须幂等,失败可重试)
}

// steps 全部升级步骤(按 version 升序;各步骤见本包 *_step.go 文件)。
var steps = []step{
	{
		version: 1,
		desc:    "回收站软删:files/folders 唯一名索引改部分索引(WHERE deleted_at IS NULL)",
		apply:   applyRecycleBinIndexes,
	},
}

// schemaVersion schema_versions 表行(记录已应用的升级版本)。
type schemaVersion struct {
	Version   int       `gorm:"primaryKey"` // 已应用步骤的版本号
	AppliedAt time.Time // 应用时间(UTC)
}

// TableName schema_versions。
func (schemaVersion) TableName() string { return "schema_versions" }

// Upgrade 幂等应用全部未执行的升级步骤(在 AutoMigrate 之后、业务启动之前调用)。
// 返回已应用步骤数;任一步骤失败 → 返回错误(版本不记录,下次启动重试)。
func Upgrade(db *gorm.DB) (int, error) {
	// 版本表(自管,不进业务 AutoMigrate 列表)
	if err := db.AutoMigrate(&schemaVersion{}); err != nil {
		return 0, fmt.Errorf("migrate: ensure version table: %w", err)
	}

	// 当前已应用最大版本(空表 = 0)
	var current int
	if err := db.Model(&schemaVersion{}).
		Select("COALESCE(MAX(version), 0)").Scan(&current).Error; err != nil {
		return 0, fmt.Errorf("migrate: read current version: %w", err)
	}

	// 按版本顺序应用未执行的步骤
	applied := 0
	for _, s := range steps {
		if s.version <= current {
			continue // 已应用(或更低版本),跳过
		}
		if err := s.apply(db); err != nil {
			return applied, fmt.Errorf("migrate: step %d (%s): %w", s.version, s.desc, err)
		}
		if err := db.Create(&schemaVersion{Version: s.version, AppliedAt: time.Now()}).Error; err != nil {
			return applied, fmt.Errorf("migrate: record version %d: %w", s.version, err)
		}
		applied++
	}
	if applied > 0 {
		log.Printf("migrate: applied %d upgrade step(s), schema now v%d", applied, current+applied)
	}
	return applied, nil
}
