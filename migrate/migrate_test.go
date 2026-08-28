// migrate_test.go —— 升级器行为单测:
//   - 首次 Upgrade 应用全部步骤并记录版本;再次 Upgrade 幂等(不重复执行);
//   - 步骤 1(回收站部分索引)生效:软删行与活动行同名共存,活动行之间仍唯一。
//
// 内存 SQLite(glebarez),与 config.InitDB 同一方言。
package migrate

import (
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"orbitcloud/model"
)

// newTestDB 内存 SQLite 建库,按真实顺序建表(先 AutoMigrate 出完整唯一索引,
// 与 config.InitDB 一致)并执行升级器。
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:migrate-test-%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{TranslateError: true})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.File{}, &model.Folder{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}

// TestUpgradeIdempotent 首次 Upgrade 应用全部步骤并记录版本;
// 再次 Upgrade 幂等(已应用步骤不重复执行,版本表仍只有一条记录)。
func TestUpgradeIdempotent(t *testing.T) {
	db := newTestDB(t)

	n, err := Upgrade(db)
	if err != nil {
		t.Fatalf("first upgrade: %v", err)
	}
	if n != 1 {
		t.Fatalf("applied = %d, want 1", n)
	}
	var cnt int64
	if err := db.Model(&schemaVersion{}).Count(&cnt).Error; err != nil || cnt != 1 {
		t.Fatalf("version rows = %d, want 1", cnt)
	}

	// 幂等:再次升级无新步骤应用,版本记录不重复
	n2, err := Upgrade(db)
	if err != nil {
		t.Fatalf("second upgrade: %v", err)
	}
	if n2 != 0 {
		t.Fatalf("re-applied = %d, want 0(幂等)", n2)
	}
	if err := db.Model(&schemaVersion{}).Count(&cnt).Error; err != nil || cnt != 1 {
		t.Fatalf("version rows after re-upgrade = %d, want 1", cnt)
	}
}

// TestRecycleBinPartialIndex 步骤 1 效果:部分唯一索引(WHERE deleted_at IS NULL)
// 允许回收站软删行与活动行同名共存,活动行之间同名仍被唯一约束拒绝。
func TestRecycleBinPartialIndex(t *testing.T) {
	db := newTestDB(t)
	if _, err := Upgrade(db); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	mk := func(name string) *model.File {
		return &model.File{BucketID: 1, FolderID: 1, Name: name, FileSize: 1, UploadedBy: 1}
	}

	// 活动行 A
	f1 := mk("same.txt")
	if err := db.Create(f1).Error; err != nil {
		t.Fatalf("create f1: %v", err)
	}
	// 软删 A → 回收站行(deleted_at 非空)
	if err := db.Delete(&model.File{}, f1.ID).Error; err != nil {
		t.Fatalf("soft delete f1: %v", err)
	}
	// 同名活动行 B:回收站行不占名 → 允许
	f2 := mk("same.txt")
	if err := db.Create(f2).Error; err != nil {
		t.Fatalf("create f2 same name (recycle row coexists): %v", err)
	}
	// 第二个同名活动行 C:活动行之间仍唯一 → 拒绝
	f3 := mk("same.txt")
	if err := db.Create(f3).Error; err == nil {
		t.Fatal("duplicate active same-name row should violate partial unique index")
	}
}
