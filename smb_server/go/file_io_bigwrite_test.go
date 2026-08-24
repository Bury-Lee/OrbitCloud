// file_io_bigwrite_test.go —— 大文件分块写入复现(触发写回缓存提前冲刷)。
package smbgateway

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"gorm.io/gorm/logger"

	"orbitcloud/config"
	"orbitcloud/core"
	"orbitcloud/model"
)

// 12MB 分块写入(1MB 块),触发 4MiB 提前冲刷,验证对象完整性。
func TestBigWriteChunksIntegration(t *testing.T) {
	dsn := fmt.Sprintf("file:test-big-%d?mode=memory&cache=shared", time.Now().UnixNano())
	cfg := config.Database{Source: "sqlite", Sources: []config.DBConnConfig{{Source: "sqlite", DSN: dsn}}}
	db, err := cfg.InitDB(logger.Default)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { if sqlDB, err := db.DB(); err == nil { _ = sqlDB.Close() } }()
	core.DB = db
	core.Storage = newMemStorage()

	u := &model.User{Username: "admin", Password: "x", PermissionLevel: model.SuperAdmin, Status: 1}
	core.DB.Create(u)
	b := &model.Bucket{Name: "tb", PermissionLevel: model.SuperAdmin, OwnerID: u.ID, Status: 1}
	core.DB.Create(b)
	f := &model.File{BucketID: b.ID, FolderID: 0, Name: "big.bin", FileSize: 0, UploadedBy: u.ID}
	core.DB.Create(f)

	sr, _ := NewSessionRegistry()
	sessionRegistry = sr
	svc := NewFileOpsService(core.Storage, NewHandleRegistry())
	ctx := context.Background()
	info, _ := resolveSession(ctx, u.ID)
	ctx = withSession(ctx, info)
	share := ShareInfo{BucketID: b.ID, ShareName: "tb", Mode: "readwrite"}

	open, err := svc.Open(ctx, &OpenArgs{BucketID: b.ID, Path: "big.bin", Read: true, Write: true, Intent: "open"}, share, 1)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// 分块写入 12MB(1MB 块,每块内容 = 块号)
	total := 12 * 1024 * 1024
	chunk := 1024 * 1024
	for off := 0; off < total; off += chunk {
		data := bytes.Repeat([]byte{byte(off / chunk)}, chunk)
		if _, err := svc.Write(ctx, open.HandleID, uint64(off), data); err != nil {
			t.Fatalf("write @%d: %v", off, err)
		}
	}
	if err := svc.Close(ctx, open.HandleID); err != nil {
		t.Fatalf("close: %v", err)
	}

	got := core.Storage.(*memStorage).objects["orbitcloud-"+fmt.Sprint(b.ID)+"/"+fmt.Sprint(f.ID)]
	if len(got) != total {
		t.Fatalf("len = %d, want %d", len(got), total)
	}
	// 逐块校验
	for off := 0; off < total; off += chunk {
		want := byte(off / chunk)
		for i := off; i < off+chunk; i += 4096 {
			for j := i; j < i+4096 && j < off+chunk; j++ {
				if got[j] != want {
					t.Fatalf("byte %d = %d, want %d (块 %d)", j, got[j], want, off/chunk)
				}
			}
		}
	}
}
