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

// 覆盖打开(overwrite intent)对称记账:200B 旧文件覆盖为 50B 新内容 →
// 记录/对象/桶用量/用户用量按新旧差值精确入账,不再虚增旧大小;
// 且打开时记录与对象保持旧值(崩溃/断连不丢数据)。
func TestOverwriteOpenSymmetricAccounting(t *testing.T) {
	db := newITDB(t)
	defer func() { if sqlDB, err := db.DB(); err == nil { _ = sqlDB.Close() } }()

	q := int64(1000)
	u := &model.User{Username: "ow-admin", Password: "x", PermissionLevel: model.SuperAdmin, Status: 1, Quota: &q, UsedSpace: 200}
	if err := core.DB.Create(u).Error; err != nil {
		t.Fatal(err)
	}
	b := &model.Bucket{Name: "ow-b", PermissionLevel: model.SuperAdmin, OwnerID: u.ID, Status: 1, UsedSpace: 200}
	if err := core.DB.Create(b).Error; err != nil {
		t.Fatal(err)
	}
	f := &model.File{BucketID: b.ID, FolderID: 0, Name: "f.txt", FileSize: 200, UploadedBy: u.ID}
	if err := core.DB.Create(f).Error; err != nil {
		t.Fatal(err)
	}
	core.Storage.(*memStorage).objects["orbitcloud-"+fmt.Sprint(b.ID)+"/"+fmt.Sprint(f.ID)] = bytes.Repeat([]byte{'E'}, 200)

	sr, _ := NewSessionRegistry()
	sessionRegistry = sr
	svc := NewFileOpsService(core.Storage, NewHandleRegistry())
	ctx := context.Background()
	info, _ := resolveSession(ctx, u.ID)
	ctx = withSession(ctx, info)
	share := ShareInfo{BucketID: b.ID, ShareName: "ow-b", Mode: "readwrite"}

	// 覆盖打开:记录与对象保持旧值(不预清空)
	open, err := svc.Open(ctx, &OpenArgs{BucketID: b.ID, Path: "f.txt", Write: true, Intent: "overwrite_or_create"}, share, 1)
	if err != nil {
		t.Fatalf("open overwrite: %v", err)
	}
	var rec model.File
	if err := core.DB.First(&rec, f.ID).Error; err != nil || rec.FileSize != 200 {
		t.Fatalf("record size = %d, want 200(打开时不预清零)", rec.FileSize)
	}
	if len(core.Storage.(*memStorage).objects["orbitcloud-"+fmt.Sprint(b.ID)+"/"+fmt.Sprint(f.ID)]) != 200 {
		t.Fatalf("object preemptively deleted at open(崩溃会丢数据)")
	}

	// 写新内容 50B(旧数据整体作废)
	if _, err := svc.Write(ctx, open.HandleID, 0, bytes.Repeat([]byte{'F'}, 50)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := svc.Close(ctx, open.HandleID); err != nil {
		t.Fatalf("close: %v", err)
	}

	// 对象 = F*50(truncateAt=0 裁剪,旧数据不复活)
	got := core.Storage.(*memStorage).objects["orbitcloud-"+fmt.Sprint(b.ID)+"/"+fmt.Sprint(f.ID)]
	if !bytes.Equal(got, bytes.Repeat([]byte{'F'}, 50)) {
		t.Fatalf("object = %q, want F*50(旧数据作废)", got)
	}
	if err := core.DB.First(&rec, f.ID).Error; err != nil || rec.FileSize != 50 {
		t.Fatalf("record size = %d, want 50", rec.FileSize)
	}
	// 对称记账:200 - 200 + 50 = 50(此前会虚增为 250)
	var uu model.User
	if err := core.DB.First(&uu, u.ID).Error; err != nil || uu.UsedSpace != 50 {
		t.Fatalf("user used = %d, want 50(对称记账)", uu.UsedSpace)
	}
	var bb model.Bucket
	if err := core.DB.First(&bb, b.ID).Error; err != nil || bb.UsedSpace != 50 {
		t.Fatalf("bucket used = %d, want 50(对称记账)", bb.UsedSpace)
	}
}

// 覆盖打开扩大内容(200→300):配额审查按真实增量(100)而非全量(300),未超额放行。
func TestOverwriteOpenGrowQuotaCheck(t *testing.T) {
	db := newITDB(t)
	defer func() { if sqlDB, err := db.DB(); err == nil { _ = sqlDB.Close() } }()

	q := int64(400)
	u := &model.User{Username: "ow-grow", Password: "x", PermissionLevel: model.SuperAdmin, Status: 1, Quota: &q, UsedSpace: 200}
	if err := core.DB.Create(u).Error; err != nil {
		t.Fatal(err)
	}
	b := &model.Bucket{Name: "ow-b2", PermissionLevel: model.SuperAdmin, OwnerID: u.ID, Status: 1, UsedSpace: 200}
	if err := core.DB.Create(b).Error; err != nil {
		t.Fatal(err)
	}
	f := &model.File{BucketID: b.ID, FolderID: 0, Name: "f.txt", FileSize: 200, UploadedBy: u.ID}
	if err := core.DB.Create(f).Error; err != nil {
		t.Fatal(err)
	}
	core.Storage.(*memStorage).objects["orbitcloud-"+fmt.Sprint(b.ID)+"/"+fmt.Sprint(f.ID)] = bytes.Repeat([]byte{'E'}, 200)

	sr, _ := NewSessionRegistry()
	sessionRegistry = sr
	svc := NewFileOpsService(core.Storage, NewHandleRegistry())
	ctx := context.Background()
	info, _ := resolveSession(ctx, u.ID)
	ctx = withSession(ctx, info)
	share := ShareInfo{BucketID: b.ID, ShareName: "ow-b2", Mode: "readwrite"}

	// 配额 400,已用 200;覆盖增长 100(300 总量)→ 未超额,放行
	open, err := svc.Open(ctx, &OpenArgs{BucketID: b.ID, Path: "f.txt", Write: true, Intent: "overwrite_or_create"}, share, 1)
	if err != nil {
		t.Fatalf("open overwrite: %v", err)
	}
	if _, err := svc.Write(ctx, open.HandleID, 0, bytes.Repeat([]byte{'G'}, 300)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := svc.Close(ctx, open.HandleID); err != nil {
		t.Fatalf("close: %v", err)
	}
	got := core.Storage.(*memStorage).objects["orbitcloud-"+fmt.Sprint(b.ID)+"/"+fmt.Sprint(f.ID)]
	if !bytes.Equal(got, bytes.Repeat([]byte{'G'}, 300)) {
		t.Fatalf("object len = %d, want 300", len(got))
	}
	var uu model.User
	if err := core.DB.First(&uu, u.ID).Error; err != nil || uu.UsedSpace != 300 {
		t.Fatalf("user used = %d, want 300(200-200+300)", uu.UsedSpace)
	}
}

// SMB 删除目录(Unlink)复用 server 层 DeleteDir + ProcessDeleteTask:
// 目录树(含子目录/文件/分享)全部物理清理,桶与用户用量按归属者归还。
func TestUnlinkDirUsesServerDelete(t *testing.T) {
	db := newITDB(t)
	defer func() { if sqlDB, err := db.DB(); err == nil { _ = sqlDB.Close() } }()

	u := &model.User{Username: "unlink-admin", Password: "x", PermissionLevel: model.SuperAdmin, Status: 1, UsedSpace: 150}
	if err := core.DB.Create(u).Error; err != nil {
		t.Fatal(err)
	}
	b := &model.Bucket{Name: "unlink-b", PermissionLevel: model.SuperAdmin, OwnerID: u.ID, Status: 1, UsedSpace: 150}
	if err := core.DB.Create(b).Error; err != nil {
		t.Fatal(err)
	}
	// 目录树:dir(f1=100B) / sub(f2=50B)
	dir := &model.Folder{BucketID: b.ID, ParentID: 0, Name: "dir", UploadedBy: u.ID, Isable: true}
	if err := core.DB.Create(dir).Error; err != nil {
		t.Fatal(err)
	}
	sub := &model.Folder{BucketID: b.ID, ParentID: dir.ID, Name: "sub", UploadedBy: u.ID, Isable: true}
	if err := core.DB.Create(sub).Error; err != nil {
		t.Fatal(err)
	}
	f1 := &model.File{BucketID: b.ID, FolderID: dir.ID, Name: "f1.txt", FileSize: 100, UploadedBy: u.ID}
	f2 := &model.File{BucketID: b.ID, FolderID: sub.ID, Name: "f2.txt", FileSize: 50, UploadedBy: u.ID}
	if err := core.DB.Create(f1).Error; err != nil || core.DB.Create(f2).Error != nil {
		t.Fatal("create files")
	}
	core.Storage.(*memStorage).objects["orbitcloud-"+fmt.Sprint(b.ID)+"/"+fmt.Sprint(f1.ID)] = bytes.Repeat([]byte{'a'}, 100)
	core.Storage.(*memStorage).objects["orbitcloud-"+fmt.Sprint(b.ID)+"/"+fmt.Sprint(f2.ID)] = bytes.Repeat([]byte{'b'}, 50)
	// 分享链接:指向 f1(文件)与 dir(目录)
	s1 := &model.ShareLink{BucketItemID: f1.ID, ItemType: "file", CreatorID: u.ID, Token: "tk-file"}
	s2 := &model.ShareLink{BucketItemID: dir.ID, ItemType: "folder", CreatorID: u.ID, Token: "tk-dir"}
	if err := core.DB.Create(s1).Error; err != nil || core.DB.Create(s2).Error != nil {
		t.Fatal("create share links")
	}

	sr, _ := NewSessionRegistry()
	sessionRegistry = sr
	svc := NewFileOpsService(core.Storage, NewHandleRegistry())
	ctx := context.Background()
	info, _ := resolveSession(ctx, u.ID)
	ctx = withSession(ctx, info)

	// Unlink 目录(复用 server.DeleteDir + ProcessDeleteTask 同步完成)
	if err := svc.Unlink(ctx, &UnlinkArgs{BucketID: b.ID, Path: "/dir"}); err != nil {
		t.Fatalf("unlink dir: %v", err)
	}

	// 目录树记录与对象全部删除
	var cnt int64
	for _, table := range []any{&model.Folder{}, &model.File{}} {
		if err := core.DB.Unscoped().Model(table).Where("bucket_id = ?", b.ID).Count(&cnt).Error; err != nil || cnt != 0 {
			t.Fatalf("%T rows = %d, want 0", table, cnt)
		}
	}
	// 分享级联删除
	if err := core.DB.Unscoped().Model(&model.ShareLink{}).Where("creator_id = ?", u.ID).Count(&cnt).Error; err != nil || cnt != 0 {
		t.Fatalf("share rows = %d, want 0(级联删除)", cnt)
	}
	// 用量归还(150-100-50=0)
	var uu model.User
	if err := core.DB.First(&uu, u.ID).Error; err != nil || uu.UsedSpace != 0 {
		t.Fatalf("user used = %d, want 0", uu.UsedSpace)
	}
	var bb model.Bucket
	if err := core.DB.First(&bb, b.ID).Error; err != nil || bb.UsedSpace != 0 {
		t.Fatalf("bucket used = %d, want 0", bb.UsedSpace)
	}
	// 对象已删
	if _, ok := core.Storage.(*memStorage).objects["orbitcloud-"+fmt.Sprint(b.ID)+"/"+fmt.Sprint(f1.ID)]; ok {
		t.Fatal("f1 object should be deleted")
	}
	if _, ok := core.Storage.(*memStorage).objects["orbitcloud-"+fmt.Sprint(b.ID)+"/"+fmt.Sprint(f2.ID)]; ok {
		t.Fatal("f2 object should be deleted")
	}
}
