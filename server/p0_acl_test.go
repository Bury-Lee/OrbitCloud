// p0_acl_test.go —— ACL 相关服务层单测:
//   - 分享通道受限状态:受限条目(自身/祖先 VisibleToGroups 非空)可创建分享,
//     创建后即公开,访问侧不再校验受限状态(创建即授权);
//   - 文件夹复制任务继承源可见组:子目录/文件创建时拷贝 VisibleToGroups,
//     防复制后泄露给全桶;
//   - 目录段名校验:CreateDir 与路径解析拒绝 Windows 保留名/非法字符。
// 用内存 SQLite + 内存对象存储 mock(不走外部依赖)。
package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"orbitcloud/config"
	"orbitcloud/core"
	"orbitcloud/model"
	"orbitcloud/utils"
)

// memStorage 内存对象存储(测试 mock,进程内 map)。
type memStorage struct {
	objects map[string][]byte
}

func newMemStorage() *memStorage { return &memStorage{objects: map[string][]byte{}} }

func (m *memStorage) Put(ctx context.Context, bucket, key string, r io.Reader, size int64) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.objects[bucket+"/"+key] = b
	return nil
}

func (m *memStorage) Get(ctx context.Context, bucket, key string) (io.ReadCloser, error) {
	b, ok := m.objects[bucket+"/"+key]
	if !ok {
		return nil, core.ErrObjectNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (m *memStorage) GetRange(ctx context.Context, bucket, key string, start, end int64) (io.ReadCloser, error) {
	b, ok := m.objects[bucket+"/"+key]
	if !ok {
		return nil, core.ErrObjectNotFound
	}
	return io.NopCloser(bytes.NewReader(b[start : end+1])), nil
}

func (m *memStorage) Delete(ctx context.Context, bucket, key string) error {
	delete(m.objects, bucket+"/"+key)
	return nil
}

func (m *memStorage) DeleteBucket(ctx context.Context, bucket string) error {
	for k := range m.objects {
		if len(k) > len(bucket) && k[:len(bucket)+1] == bucket+"/" {
			delete(m.objects, k)
		}
	}
	return nil
}

func (m *memStorage) Ping(ctx context.Context) error { return nil }

// newP0TestDB 内存 SQLite 建库(AutoMigrate 全模型)并挂 core.DB / core.Storage。
func newP0TestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:test-p0-%d?mode=memory&cache=shared", time.Now().UnixNano())
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

func mkUserP0(t *testing.T, username string, perm int8) *model.User {
	t.Helper()
	u := &model.User{Username: username, Password: "x", Name: username, PermissionLevel: perm, Status: 1}
	if err := core.DB.Create(u).Error; err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
	return u
}

func mkBucketP0(t *testing.T, name string, perm int8, owner uint) *model.Bucket {
	t.Helper()
	b := &model.Bucket{Name: name, PermissionLevel: perm, OwnerID: owner, Status: 1}
	if err := core.DB.Create(b).Error; err != nil {
		t.Fatalf("create bucket %s: %v", name, err)
	}
	return b
}

func mkFolderP0(t *testing.T, bucketID, parentID, uploadedBy uint, name string) *model.Folder {
	t.Helper()
	f := &model.Folder{BucketID: bucketID, ParentID: parentID, Name: name, UploadedBy: uploadedBy, Isable: true}
	if err := core.DB.Create(f).Error; err != nil {
		t.Fatalf("create folder %s: %v", name, err)
	}
	return f
}

func mkGroupP0(t *testing.T, name string) *model.UserGroup {
	t.Helper()
	g := &model.UserGroup{Name: name, Status: 1}
	if err := core.DB.Create(g).Error; err != nil {
		t.Fatalf("create group %s: %v", name, err)
	}
	return g
}

func setVisible(t *testing.T, folderID uint, groups string) {
	t.Helper()
	if err := core.DB.Model(&model.Folder{}).Where("id = ?", folderID).
		Update("visible_to_groups", groups).Error; err != nil {
		t.Fatalf("set visible groups on folder %d: %v", folderID, err)
	}
}

func dropP0DB(db *gorm.DB) {
	if sqlDB, err := db.DB(); err == nil {
		_ = sqlDB.Close()
	}
	time.Sleep(10 * time.Millisecond)
}

// TestShareRestrictedItem 受限条目(自身或祖先 VisibleToGroups 非空)可创建分享——
// 分享 = 显式授权通道,创建后即公开;创建时不做受限状态校验。
func TestShareRestrictedItem(t *testing.T) {
	db := newP0TestDB(t)
	defer dropP0DB(db)
	ctx := context.Background()

	admin := mkUserP0(t, "admin", 1)
	doctor := mkUserP0(t, "doctor", 5)
	b := mkBucketP0(t, "b1", 9, admin.ID)
	g := mkGroupP0(t, "g1")

	// 受限祖先(仅组 g1 可见),其下子目录自身不设限
	restricted := mkFolderP0(t, b.ID, 0, doctor.ID, "restricted")
	setVisible(t, restricted.ID, fmt.Sprintf("[%d]", g.ID))
	inner := mkFolderP0(t, b.ID, restricted.ID, doctor.ID, "inner")

	// 受限祖先下的条目 → 可创建分享
	if _, err := CreateShare(ctx, CreateShareArg{UserID: doctor.ID, FileID: inner.ID}); err != nil {
		t.Fatalf("share under restricted ancestor: %v", err)
	}
	// 条目自身受限 → 也可创建分享
	if _, err := CreateShare(ctx, CreateShareArg{UserID: doctor.ID, FileID: restricted.ID}); err != nil {
		t.Fatalf("share restricted folder itself: %v", err)
	}
	// 无限制祖先的目录 → 可分享
	open := mkFolderP0(t, b.ID, 0, doctor.ID, "open")
	if _, err := CreateShare(ctx, CreateShareArg{UserID: doctor.ID, FileID: open.ID}); err != nil {
		t.Fatalf("share open folder: %v", err)
	}
}

// TestShareAccessAfterRestriction 分享创建后,条目/祖先被设限不影响已有分享
// (创建即授权,访问侧不再校验受限状态)。
func TestShareAccessAfterRestriction(t *testing.T) {
	db := newP0TestDB(t)
	defer dropP0DB(db)
	ctx := context.Background()

	admin := mkUserP0(t, "admin", 1)
	doctor := mkUserP0(t, "doctor", 5)
	b := mkBucketP0(t, "b2", 9, admin.ID)
	g := mkGroupP0(t, "g2")

	root := mkFolderP0(t, b.ID, 0, doctor.ID, "root")
	child := mkFolderP0(t, b.ID, root.ID, doctor.ID, "child")

	// 创建时祖先无限制 → 分享成功
	share, err := CreateShare(ctx, CreateShareArg{UserID: doctor.ID, FileID: child.ID})
	if err != nil {
		t.Fatalf("create share: %v", err)
	}
	if _, _, err := ResolveShare(ctx, ResolveShareArg{Token: share.Token}); err != nil {
		t.Fatalf("resolve before restriction: %v", err)
	}

	// 祖先目录事后设限 → 存量分享仍可解析(创建即授权,权限回收不影响已有分享)
	setVisible(t, root.ID, fmt.Sprintf("[%d]", g.ID))
	if _, _, err := ResolveShare(ctx, ResolveShareArg{Token: share.Token}); err != nil {
		t.Fatalf("resolve after ancestor restricted: %v", err)
	}
	// 条目自身事后设限 → 同样不影响
	setVisible(t, child.ID, fmt.Sprintf("[%d]", g.ID))
	if _, _, err := ResolveShare(ctx, ResolveShareArg{Token: share.Token}); err != nil {
		t.Fatalf("resolve after item restricted: %v", err)
	}
}

// TestCopyTaskInheritVisibleGroups P0-2 复制继承:顶层/子目录/文件在复制任务
// 创建目标记录时继承源 VisibleToGroups(防复制后泄露给全桶)。
func TestCopyTaskInheritVisibleGroups(t *testing.T) {
	db := newP0TestDB(t)
	defer dropP0DB(db)
	ctx := context.Background()

	admin := mkUserP0(t, "admin", 1)
	doctor := mkUserP0(t, "doctor", 5)
	srcB := mkBucketP0(t, "src", 9, admin.ID)
	dstB := mkBucketP0(t, "dst", 9, admin.ID)
	g := mkGroupP0(t, "g3")
	groups := fmt.Sprintf("[%d]", g.ID)

	// 源结构:restricted(受限)→ sub(受限)→ a.txt(受限)
	src := mkFolderP0(t, srcB.ID, 0, doctor.ID, "restricted")
	setVisible(t, src.ID, groups)
	sub := mkFolderP0(t, srcB.ID, src.ID, doctor.ID, "sub")
	setVisible(t, sub.ID, groups)
	f := &model.File{
		BucketID: srcB.ID, FolderID: sub.ID, Name: "a.txt",
		FileSize: 4, FileType: "text/plain", MD5: "x",
		UploadedBy: doctor.ID, VisibleToGroups: groups,
	}
	if err := core.DB.Create(f).Error; err != nil {
		t.Fatalf("create source file: %v", err)
	}
	if err := core.Storage.Put(ctx, utils.BucketEncoder(srcB.ID), objectKeyForFile(f.ID),
		bytes.NewReader([]byte("data")), 4); err != nil {
		t.Fatalf("put source object: %v", err)
	}

	// 复制(同步执行复制任务,mock 存储)
	top, err := CopyFolder(ctx, CopyFolderArg{UserID: admin.ID, SrcBucketID: srcB.ID, SrcFolderID: src.ID, DstBucketID: dstB.ID})
	if err != nil {
		t.Fatalf("copy folder: %v", err)
	}

	// 断言:顶层目录继承
	if top.VisibleToGroups != groups {
		t.Fatalf("top folder visible groups = %q, want %q", top.VisibleToGroups, groups)
	}
	// 断言:子目录继承
	var dstSub model.Folder
	if err := core.DB.Where("bucket_id = ? AND parent_id = ?", dstB.ID, top.ID).
		First(&dstSub).Error; err != nil {
		t.Fatalf("load dst sub folder: %v", err)
	}
	if dstSub.VisibleToGroups != groups {
		t.Fatalf("dst sub folder visible groups = %q, want %q", dstSub.VisibleToGroups, groups)
	}
	// 断言:文件继承
	var dstFile model.File
	if err := core.DB.Where("bucket_id = ? AND folder_id = ?", dstB.ID, dstSub.ID).
		First(&dstFile).Error; err != nil {
		t.Fatalf("load dst file: %v", err)
	}
	if dstFile.VisibleToGroups != groups {
		t.Fatalf("dst file visible groups = %q, want %q", dstFile.VisibleToGroups, groups)
	}
	// 断言:对象已复制(新 key 可读)
	if _, err := core.Storage.Get(ctx, utils.BucketEncoder(dstB.ID), objectKeyForFile(dstFile.ID)); err != nil {
		t.Fatalf("dst object missing: %v", err)
	}
}

// TestCreateDirRejectsReservedName P1-2 目录段名校验:CreateDir 拒绝 Windows
// 保留名/非法字符(与文件名同一套校验)。
func TestCreateDirRejectsReservedName(t *testing.T) {
	db := newP0TestDB(t)
	defer dropP0DB(db)
	ctx := context.Background()

	admin := mkUserP0(t, "admin", 1)
	b := mkBucketP0(t, "b3", 9, admin.ID)

	for _, bad := range []string{"CON", "PRN", "AUX", "NUL", "a:b", "x?y", `a"b`, "d ", "e."} {
		if _, err := CreateDir(ctx, CreateDirArg{UserID: admin.ID, BucketID: b.ID, DirPath: bad}); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("create dir %q: want ErrInvalidInput, got %v", bad, err)
		}
	}
	// 合法段名正常创建
	if _, err := CreateDir(ctx, CreateDirArg{UserID: admin.ID, BucketID: b.ID, DirPath: "ok-dir"}); err != nil {
		t.Fatalf("create dir ok-dir: %v", err)
	}
	// 路径中间段同样校验(ResolveDirPath 建链路径)
	if _, err := CreateDir(ctx, CreateDirArg{UserID: admin.ID, BucketID: b.ID, DirPath: "CON/sub"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("create dir CON/sub: want ErrInvalidInput, got %v", err)
	}
}

// TestGroupWhitelistPriority 白名单优先判定语义(权限级不兜底):
// 设置了可见组的目录 → 仅管理员(<=1)/组内成员可访问,仅满足权限级门槛
// (桶级)不属于可见组 → 拒绝;组 = 纯白名单参考,加入组不改变用户权限等级。
func TestGroupWhitelistPriority(t *testing.T) {
	db := newP0TestDB(t)
	defer dropP0DB(db)
	ctx := context.Background()

	admin := mkUserP0(t, "admin", 1)
	doctor := mkUserP0(t, "doctor", 5) // 权限级 5(满足桶级 9)但不在组内
	b := mkBucketP0(t, "b4", 9, admin.ID)
	g := mkGroupP0(t, "g4")

	// 目录设组 [g4],非组内 doctor 应被拒绝
	dir := mkFolderP0(t, b.ID, 0, admin.ID, "whitelisted")
	setVisible(t, dir.ID, fmt.Sprintf("[%d]", g.ID))

	// 仅满足权限级门槛但不在组 → 拒绝(级别不兜底)
	if err := checkAncestorsAccessTree(ctx, doctor.ID, b.ID, dir.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("level-only user: want ErrForbidden, got %v", err)
	}
	// 管理员(<=1)→ 豁免
	if err := checkAncestorsAccessTree(ctx, admin.ID, b.ID, dir.ID); err != nil {
		t.Fatalf("admin: %v", err)
	}
	// 加入组后 → 通过(组内成员,权限级未变)
	if err := core.DB.Create(&model.UserGroupMember{GroupID: g.ID, UserID: doctor.ID}).Error; err != nil {
		t.Fatalf("join group: %v", err)
	}
	if err := checkAncestorsAccessTree(ctx, doctor.ID, b.ID, dir.ID); err != nil {
		t.Fatalf("group member: %v", err)
	}
	// 加入组不改变用户自身权限等级(组与等级彼此隔离)
	var u model.User
	if err := core.DB.First(&u, doctor.ID).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	if u.PermissionLevel != 5 {
		t.Fatalf("join group changed user level: got %d, want 5", u.PermissionLevel)
	}
	// 未设组的目录 → 白名单空,条目判定不拦截(桶级门槛由 api 层预检)
	open := mkFolderP0(t, b.ID, 0, doctor.ID, "open")
	if err := checkAncestorsAccessTree(ctx, doctor.ID, b.ID, open.ID); err != nil {
		t.Fatalf("open dir: %v", err)
	}
}
