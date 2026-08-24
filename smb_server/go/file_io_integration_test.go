// file_io_integration_test.go —— 真实服务链路集成测试:
// Open(写) → Truncate(SET_END_OF_FILE)→ 越界 Write → Close,
// 验证截断作废的旧数据不复活(用户确认的关键场景,真实调用链复现)。
package smbgateway

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"orbitcloud/config"
	"orbitcloud/core"
	"orbitcloud/model"
)

func newITDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:test-io-%d?mode=memory&cache=shared", time.Now().UnixNano())
	cfg := config.Database{
		Source:  "sqlite",
		Sources: []config.DBConnConfig{{Source: "sqlite", DSN: dsn}},
	}
	db, err := cfg.InitDB(logger.Default)
	if err != nil {
		t.Fatalf("init db: %v", err)
	}
	core.DB = db
	core.Storage = newMemStorage()
	return db
}

// 场景:对象 200B(旧数据 E)→ Truncate(100) → Write(150,50) → Close
// 期望:200B = E*100 + 零*50 + F*50(截断区零填充,不复活)
func TestTruncateThenWriteBeyondIntegration(t *testing.T) {
	db := newITDB(t)
	defer func() { if sqlDB, err := db.DB(); err == nil { _ = sqlDB.Close() } }()

	// 用户 + 桶 + 文件记录(200B)+ 对象
	u := &model.User{Username: "admin", Password: "x", PermissionLevel: model.SuperAdmin, Status: 1}
	if err := core.DB.Create(u).Error; err != nil {
		t.Fatal(err)
	}
	b := &model.Bucket{Name: "tb", PermissionLevel: model.SuperAdmin, OwnerID: u.ID, Status: 1}
	if err := core.DB.Create(b).Error; err != nil {
		t.Fatal(err)
	}
	f := &model.File{BucketID: b.ID, FolderID: 0, Name: "f.txt", FileSize: 200, UploadedBy: u.ID}
	if err := core.DB.Create(f).Error; err != nil {
		t.Fatal(err)
	}
	core.Storage.(*memStorage).objects["orbitcloud-"+fmt.Sprint(b.ID)+"/"+fmt.Sprint(f.ID)] = bytes.Repeat([]byte{'E'}, 200)

	// 身份注册表 + 服务
	sr, err := NewSessionRegistry()
	if err != nil {
		t.Fatal(err)
	}
	sessionRegistry = sr
	svc := NewFileOpsService(core.Storage, NewHandleRegistry())

	ctx := context.Background()
	info, err := resolveSession(ctx, u.ID)
	if err != nil {
		t.Fatalf("resolve session: %v", err)
	}
	ctx = withSession(ctx, info)
	share := ShareInfo{BucketID: b.ID, ShareName: "tb", Mode: "readwrite"}

	// Open(写)
	open, err := svc.Open(ctx, &OpenArgs{BucketID: b.ID, Path: "f.txt", Read: true, Write: true, Intent: "open"}, share, 1)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// Truncate(100)
	if err := svc.Truncate(ctx, open.HandleID, 100); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	// Write(150, 50)
	if _, err := svc.Write(ctx, open.HandleID, 150, bytes.Repeat([]byte{'F'}, 50)); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Close(冲刷落盘)
	if err := svc.Close(ctx, open.HandleID); err != nil {
		t.Fatalf("close: %v", err)
	}

	got := core.Storage.(*memStorage).objects["orbitcloud-"+fmt.Sprint(b.ID)+"/"+fmt.Sprint(f.ID)]
	if len(got) != 200 {
		t.Fatalf("len = %d, want 200", len(got))
	}
	if !bytes.Equal(got[:100], bytes.Repeat([]byte{'E'}, 100)) {
		t.Fatalf("[0,100) = %q, want E*100", got[:100])
	}
	if !bytes.Equal(got[100:150], make([]byte, 50)) {
		t.Fatalf("[100,150) = %q, want 零填充(截断作废区不复活)", got[100:150])
	}
	if !bytes.Equal(got[150:], bytes.Repeat([]byte{'F'}, 50)) {
		t.Fatalf("[150,200) = %q, want F*50", got[150:])
	}
}

// 纯截断(无写)也必须落盘。
func TestTruncateOnlyIntegration(t *testing.T) {
	db := newITDB(t)
	defer func() { if sqlDB, err := db.DB(); err == nil { _ = sqlDB.Close() } }()

	u := &model.User{Username: "admin", Password: "x", PermissionLevel: model.SuperAdmin, Status: 1}
	core.DB.Create(u)
	b := &model.Bucket{Name: "tb", PermissionLevel: model.SuperAdmin, OwnerID: u.ID, Status: 1}
	core.DB.Create(b)
	f := &model.File{BucketID: b.ID, FolderID: 0, Name: "f.txt", FileSize: 200, UploadedBy: u.ID}
	core.DB.Create(f)
	core.Storage.(*memStorage).objects["orbitcloud-"+fmt.Sprint(b.ID)+"/"+fmt.Sprint(f.ID)] = bytes.Repeat([]byte{'E'}, 200)

	sr, _ := NewSessionRegistry()
	sessionRegistry = sr
	svc := NewFileOpsService(core.Storage, NewHandleRegistry())
	ctx := context.Background()
	info, _ := resolveSession(ctx, u.ID)
	ctx = withSession(ctx, info)
	share := ShareInfo{BucketID: b.ID, ShareName: "tb", Mode: "readwrite"}

	open, err := svc.Open(ctx, &OpenArgs{BucketID: b.ID, Path: "f.txt", Read: true, Write: true, Intent: "open"}, share, 1)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := svc.Truncate(ctx, open.HandleID, 100); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if err := svc.Close(ctx, open.HandleID); err != nil {
		t.Fatalf("close: %v", err)
	}
	got := core.Storage.(*memStorage).objects["orbitcloud-"+fmt.Sprint(b.ID)+"/"+fmt.Sprint(f.ID)]
	if len(got) != 100 {
		t.Fatalf("len = %d, want 100(纯截断须落盘)", len(got))
	}
}
