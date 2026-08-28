// enter_test.go —— ReconcileUserUsages 用户用量对账测试:
//   - 用量漂移修正(以 files 表为权威);
//   - 软删行仍计入(Unscoped 口径:软删不归还用量,与记账语义一致);
//   - 无漂移不动。
//
// 基于内存 SQLite(config.InitDB 建表,与 p0 测试同模式)。
package cron

import (
	"fmt"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"orbitcloud/config"
	"orbitcloud/model"
)

// newCronTestDB 内存 SQLite 建库(AutoMigrate 全模型)并返回句柄。
func newCronTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:test-cron-%d?mode=memory&cache=shared", time.Now().UnixNano())
	cfg := config.Database{
		Source:  "sqlite",
		Sources: []config.DBConnConfig{{Source: "sqlite", DSN: dsn}},
	}
	db, err := cfg.InitDB(logger.Default)
	if err != nil {
		t.Fatalf("init db: %v", err)
	}
	return db
}

func TestReconcileUserUsages(t *testing.T) {
	db := newCronTestDB(t)

	// 场景 1:漂移修正 —— 用户记账 100,实际文件 60 → 修正为 60
	u1 := &model.User{Username: "cron-u1", Password: "x", UsedSpace: 100}
	if err := db.Create(u1).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := db.Create(&model.File{BucketID: 1, FolderID: 0, Name: "a.txt", FileSize: 60, UploadedBy: u1.ID}).Error; err != nil {
		t.Fatalf("create file: %v", err)
	}
	fixed, err := ReconcileUserUsages(db)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if fixed != 1 {
		t.Fatalf("fixed = %d, want 1", fixed)
	}
	var u1b model.User
	if err := db.First(&u1b, u1.ID).Error; err != nil || u1b.UsedSpace != 60 {
		t.Fatalf("user used = %d, want 60", u1b.UsedSpace)
	}

	// 场景 2:软删行仍计入(Unscoped 口径)→ 活跃 60 + 软删 40 = 100,与记账一致,无漂移
	u2 := &model.User{Username: "cron-u2", Password: "x", UsedSpace: 100}
	if err := db.Create(u2).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := db.Create(&model.File{BucketID: 1, FolderID: 0, Name: "b0.txt", FileSize: 60, UploadedBy: u2.ID}).Error; err != nil {
		t.Fatalf("create file: %v", err)
	}
	f2 := &model.File{BucketID: 1, FolderID: 0, Name: "b.txt", FileSize: 40, UploadedBy: u2.ID}
	if err := db.Create(f2).Error; err != nil {
		t.Fatalf("create file: %v", err)
	}
	if err := db.Delete(&model.File{}, f2.ID).Error; err != nil { // 软删(置 deleted_at)
		t.Fatalf("soft delete file: %v", err)
	}
	fixed, err = ReconcileUserUsages(db)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if fixed != 0 {
		t.Fatalf("fixed = %d, want 0(软删行仍占用用量,无漂移)", fixed)
	}

	// 场景 3:无文件用户(SUM 空 → 0),记账漂移修正为 0
	u3 := &model.User{Username: "cron-u3", Password: "x", UsedSpace: 999}
	if err := db.Create(u3).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	fixed, err = ReconcileUserUsages(db)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	var u3b model.User
	if err := db.First(&u3b, u3.ID).Error; err != nil || u3b.UsedSpace != 0 {
		t.Fatalf("user used = %d, want 0(空用户修正为 0)", u3b.UsedSpace)
	}
}
