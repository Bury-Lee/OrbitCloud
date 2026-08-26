// 超额状态下 SMB 写回审查集成测试(本地复现,验证 Close 路径审查拒绝):
// 前置:qt 配额 50MB、used 55MB(已超额)、文件 f.txt 1MB
// 场景1:overwrite 打开 → 写 3MB(增长 delta=+2MB,<4MiB 缓存阈值)→ Close
//        期望:Close 返回 ErrQuotaExceeded;记录/用量/对象均不变(旧数据保留)。
// 场景2:新建文件写 3MB → Close 拒绝 → 记录被清理(防 0 字节幽灵文件)。
package smbgateway

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"orbitcloud/core"
	"orbitcloud/model"
)

func TestOverwriteOpenQuotaRejected(t *testing.T) {
	db := newITDB(t)
	defer func() { if sqlDB, err := db.DB(); err == nil { _ = sqlDB.Close() } }()

	q := int64(50 << 20)
	u := &model.User{Username: "ow-rej", Password: "x", PermissionLevel: model.SuperAdmin, Status: 1, Quota: &q, UsedSpace: 55 << 20}
	if err := core.DB.Create(u).Error; err != nil {
		t.Fatal(err)
	}
	b := &model.Bucket{Name: "ow-rej-b", PermissionLevel: model.SuperAdmin, OwnerID: u.ID, Status: 1, UsedSpace: 55 << 20}
	if err := core.DB.Create(b).Error; err != nil {
		t.Fatal(err)
	}
	f := &model.File{BucketID: b.ID, FolderID: 0, Name: "f.txt", FileSize: 1 << 20, UploadedBy: u.ID}
	if err := core.DB.Create(f).Error; err != nil {
		t.Fatal(err)
	}
	core.Storage.(*memStorage).objects["orbitcloud-"+fmt.Sprint(b.ID)+"/"+fmt.Sprint(f.ID)] = bytes.Repeat([]byte{'E'}, 1<<20)

	sr, _ := NewSessionRegistry()
	sessionRegistry = sr
	svc := NewFileOpsService(core.Storage, NewHandleRegistry())
	ctx := context.Background()
	info, _ := resolveSession(ctx, u.ID)
	ctx = withSession(ctx, info)
	share := ShareInfo{BucketID: b.ID, ShareName: "ow-rej-b", Mode: "readwrite"}

	// 场景 1:覆盖已有 1MB 文件,写 3MB(增量 +2MB,<4MiB 阈值)→ Close 审查拒绝,旧数据保留
	open, err := svc.Open(ctx, &OpenArgs{BucketID: b.ID, Path: "f.txt", Write: true, Intent: "overwrite_or_create"}, share, 1)
	if err != nil {
		t.Fatalf("open overwrite: %v", err)
	}
	if _, err := svc.Write(ctx, open.HandleID, 0, bytes.Repeat([]byte{'F'}, 3<<20)); err != nil {
		t.Fatalf("write 3MB: %v", err)
	}
	if err := svc.Close(ctx, open.HandleID); err == nil {
		t.Fatal("close over-quota: want error, got nil")
	} else {
		t.Logf("close rejected as expected: %v", err)
	}
	// 覆盖场景:记录/用量/对象不变(旧数据保留)
	var rec model.File
	if err := core.DB.First(&rec, f.ID).Error; err != nil || rec.FileSize != 1<<20 {
		t.Fatalf("record size = %d, want %d(旧记录保留)", rec.FileSize, 1<<20)
	}
	var uu model.User
	if err := core.DB.First(&uu, u.ID).Error; err != nil || uu.UsedSpace != 55<<20 {
		t.Fatalf("user used = %d, want %d", uu.UsedSpace, 55<<20)
	}
	got := core.Storage.(*memStorage).objects["orbitcloud-"+fmt.Sprint(b.ID)+"/"+fmt.Sprint(f.ID)]
	if len(got) != 1<<20 {
		t.Fatalf("object len = %d, want %d(未覆盖)", len(got), 1<<20)
	}

	// 场景 2:新建文件写 3MB → Close 审查拒绝 → 记录被清理(防幽灵文件)
	open2, err := svc.Open(ctx, &OpenArgs{BucketID: b.ID, Path: "new.txt", Write: true, Intent: "overwrite_or_create"}, share, 1)
	if err != nil {
		t.Fatalf("open new: %v", err)
	}
	if _, err := svc.Write(ctx, open2.HandleID, 0, bytes.Repeat([]byte{'G'}, 3<<20)); err != nil {
		t.Fatalf("write new 3MB: %v", err)
	}
	if err := svc.Close(ctx, open2.HandleID); err == nil {
		t.Fatal("close new over-quota: want error, got nil")
	}
	// 新文件记录应被清理(幽灵文件修复)
	var cnt int64
	if err := core.DB.Unscoped().Model(&model.File{}).Where("name = ?", "new.txt").Count(&cnt).Error; err != nil || cnt != 0 {
		t.Fatalf("new file rows = %d, want 0(幽灵记录已清理)", cnt)
	}
	if _, ok := core.Storage.(*memStorage).objects["orbitcloud-"+fmt.Sprint(b.ID)+"/new.txt"]; ok {
		t.Fatal("new object should be absent")
	}
}
