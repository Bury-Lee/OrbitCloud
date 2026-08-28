// clean_test.go —— cron 分批清理任务的 keyset 游标测试:
// batch=1 小批量强制多轮循环,验证游标累计能清完全部过期行且不过期行保留
// (覆盖 CleanExpiredLogs / CleanExpiredTokens / CleanExpiredShares /
// CleanExpiredDownloadTasks 四个清理函数)。
package cron

import (
	"testing"
	"time"

	"gorm.io/gorm"

	"orbitcloud/model"
)

// backdate 把行的时间列改到过去(模拟"已过期")。
func backdate(t *testing.T, db *gorm.DB, table any, id uint, col string, past time.Time) {
	t.Helper()
	if err := db.Unscoped().Model(table).Where("id = ?", id).Update(col, past).Error; err != nil {
		t.Fatalf("backdate %s: %v", col, err)
	}
}

// countAll 行数(Unscoped 全口径)。
func countAll(t *testing.T, db *gorm.DB, table any) int64 {
	t.Helper()
	var n int64
	if err := db.Unscoped().Model(table).Count(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func TestCleanExpiredLogsCursor(t *testing.T) {
	db := newCronTestDB(t)

	// operation_logs:3 条过期 + 1 条新鲜
	for i := 0; i < 4; i++ {
		if err := db.Create(&model.OperationLog{UserID: 1, Action: "test"}).Error; err != nil {
			t.Fatalf("create op log: %v", err)
		}
	}
	var opIDs []uint
	db.Unscoped().Model(&model.OperationLog{}).Order("id ASC").Pluck("id", &opIDs)
	for i := 0; i < 3; i++ {
		backdate(t, db, &model.OperationLog{}, opIDs[i], "created_at", time.Now().AddDate(0, 0, -40))
	}
	// logs:同样 3 过期 + 1 新鲜
	for i := 0; i < 4; i++ {
		if err := db.Create(&model.Log{App: "orbitcloud", Level: 2, Content: "x"}).Error; err != nil {
			t.Fatalf("create log: %v", err)
		}
	}
	var logIDs []uint
	db.Unscoped().Model(&model.Log{}).Order("id ASC").Pluck("id", &logIDs)
	for i := 0; i < 3; i++ {
		backdate(t, db, &model.Log{}, logIDs[i], "created_at", time.Now().AddDate(0, 0, -40))
	}

	total, err := CleanExpiredLogs(db, 30, 1) // batch=1:强制 3 轮游标
	if err != nil {
		t.Fatalf("clean logs: %v", err)
	}
	if total != 6 {
		t.Fatalf("cleaned = %d, want 6(两表各 3)", total)
	}
	if n := countAll(t, db, &model.OperationLog{}); n != 1 {
		t.Fatalf("op log rows = %d, want 1(新鲜保留)", n)
	}
	if n := countAll(t, db, &model.Log{}); n != 1 {
		t.Fatalf("log rows = %d, want 1(新鲜保留)", n)
	}
}

func TestCleanExpiredTokensCursor(t *testing.T) {
	db := newCronTestDB(t)
	for i := 0; i < 4; i++ {
		if err := db.Create(&model.RefreshToken{UserID: 1, Token: "tok-" + string(rune('a'+i)), ExpiresAt: time.Now().Add(time.Hour)}).Error; err != nil {
			t.Fatalf("create token: %v", err)
		}
	}
	var ids []uint
	db.Unscoped().Model(&model.RefreshToken{}).Order("id ASC").Pluck("id", &ids)
	for i := 0; i < 3; i++ {
		backdate(t, db, &model.RefreshToken{}, ids[i], "expires_at", time.Now().Add(-time.Hour))
	}

	total, err := CleanExpiredTokens(db, 1)
	if err != nil {
		t.Fatalf("clean tokens: %v", err)
	}
	if total != 3 {
		t.Fatalf("cleaned = %d, want 3", total)
	}
	if n := countAll(t, db, &model.RefreshToken{}); n != 1 {
		t.Fatalf("token rows = %d, want 1(新鲜保留)", n)
	}
}

func TestCleanExpiredSharesCursor(t *testing.T) {
	db := newCronTestDB(t)
	for i := 0; i < 4; i++ {
		exp := time.Now().Add(time.Hour)
		if err := db.Create(&model.ShareLink{CreatorID: 1, BucketItemID: uint(i + 1), ItemType: "file", Token: "share-" + string(rune('a'+i)), ExpiresAt: &exp}).Error; err != nil {
			t.Fatalf("create share: %v", err)
		}
	}
	var ids []uint
	db.Model(&model.ShareLink{}).Order("id ASC").Pluck("id", &ids)
	for i := 0; i < 3; i++ {
		backdate(t, db, &model.ShareLink{}, ids[i], "expires_at", time.Now().Add(-time.Hour))
	}

	total, err := CleanExpiredShares(db, 1)
	if err != nil {
		t.Fatalf("clean shares: %v", err)
	}
	if total != 3 {
		t.Fatalf("cleaned = %d, want 3", total)
	}
	// 分享为软删清理:过期行置 deleted_at,仅活跃行口径 = 1(新鲜保留)
	var n int64
	if err := db.Model(&model.ShareLink{}).Count(&n).Error; err != nil || n != 1 {
		t.Fatalf("active share rows = %d, want 1(新鲜保留,过期软删)", n)
	}
}

func TestCleanExpiredDownloadTasksCursor(t *testing.T) {
	db := newCronTestDB(t)
	for i := 0; i < 4; i++ {
		if err := db.Create(&model.DownloadTask{UserID: 1, BucketID: 1, FolderID: 1}).Error; err != nil {
			t.Fatalf("create download task: %v", err)
		}
	}
	var ids []uint
	db.Unscoped().Model(&model.DownloadTask{}).Order("id ASC").Pluck("id", &ids)
	for i := 0; i < 3; i++ {
		backdate(t, db, &model.DownloadTask{}, ids[i], "created_at", time.Now().AddDate(0, 0, -10))
	}

	total, err := CleanExpiredDownloadTasks(db, 7, 1)
	if err != nil {
		t.Fatalf("clean download tasks: %v", err)
	}
	if total != 3 {
		t.Fatalf("cleaned = %d, want 3", total)
	}
	if n := countAll(t, db, &model.DownloadTask{}); n != 1 {
		t.Fatalf("download task rows = %d, want 1(新鲜保留)", n)
	}
}
