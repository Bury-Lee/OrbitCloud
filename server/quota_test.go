// quota_test.go —— 桶配额 + 用户配额审查与记账测试:
//   - CheckBucketQuota:不限 / 未超 / 超限 / 临界 / 零负增量(桶为硬限额);
//   - CheckUserQuota:不限 / 仅可读 / 未超放行 / 越过限额放行(软限额)/ 已超额拒绝;
//   - 上传链路:已超额拒绝(413 语义)、记账归属上传者、与桶用量同事务;
//   - 复制链路:操作者记账 + 已超额拒绝;
//   - 删除链路:按文件归属者归还用户用量;
//   - 管理员改配额(UpdateUser.Quota)。
//
// 基于内存 SQLite + 内存存储 mock(与 p0_acl_test.go 同环境)。
package server

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"orbitcloud/core"
	"orbitcloud/model"
	"orbitcloud/utils"
)

// mkQuotaBucket 建带配额的桶(复用 p0 环境;name 须唯一,桶名有唯一索引)。
func mkQuotaBucket(t *testing.T, name string, quota, used int64) *model.Bucket {
	t.Helper()
	b := &model.Bucket{Name: name, Quota: quota, UsedSpace: used, Status: 1}
	if err := core.DB.Create(b).Error; err != nil {
		t.Fatalf("create quota bucket: %v", err)
	}
	return b
}

func TestCheckBucketQuota(t *testing.T) {
	newP0TestDB(t) // 内存 SQLite + memStorage

	t.Run("unlimited_quota_passes", func(t *testing.T) {
		b := mkQuotaBucket(t, "q-unlimited", 0, 100) // 配额 0 = 不限
		if err := CheckBucketQuota(context.Background(), b.ID, 1<<30); err != nil {
			t.Fatalf("quota=0 should pass, got %v", err)
		}
	})

	t.Run("within_quota_passes", func(t *testing.T) {
		b := mkQuotaBucket(t, "q-within", 1000, 400) // 用 400/1000
		if err := CheckBucketQuota(context.Background(), b.ID, 600); err != nil {
			t.Fatalf("used+delta == quota should pass, got %v", err)
		}
	})

	t.Run("exceeds_quota_rejected", func(t *testing.T) {
		b := mkQuotaBucket(t, "q-exceed", 1000, 400) // 用 400/1000
		err := CheckBucketQuota(context.Background(), b.ID, 601)
		if !errors.Is(err, ErrQuotaExceeded) {
			t.Fatalf("want ErrQuotaExceeded, got %v", err)
		}
	})

	t.Run("negative_or_zero_delta_passes_without_query", func(t *testing.T) {
		b := mkQuotaBucket(t, "q-full", 10, 10)                                 // 已满
		if err := CheckBucketQuota(context.Background(), b.ID, 0); err != nil { // 0 增量
			t.Fatalf("delta=0 should pass, got %v", err)
		}
		if err := CheckBucketQuota(context.Background(), b.ID, -5); err != nil { // 负增量(删/截断)
			t.Fatalf("delta<0 should pass, got %v", err)
		}
	})
}

// ============================ 用户配额 ============================

// mkUserQuota 建带配额的用户(测试用;quota 三态:nil=不限 / 0=仅可读 / >0=限额)。
func mkUserQuota(t *testing.T, name string, quota *int64, used int64) *model.User {
	t.Helper()
	u := &model.User{Username: name, Password: "x", Name: name, PermissionLevel: model.NormalUser, Status: 1, Quota: quota, UsedSpace: used}
	if err := core.DB.Create(u).Error; err != nil {
		t.Fatalf("create user %s: %v", name, err)
	}
	return u
}

// userUsed 读取用户用量(测试用)。
func userUsed(t *testing.T, userID uint) int64 {
	t.Helper()
	var u model.User
	if err := core.DB.First(&u, userID).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	return u.UsedSpace
}

// mkFileP0 直接建文件记录(测试用)。
func mkFileP0(t *testing.T, bucketID, folderID uint, name string, size int64) *model.File {
	t.Helper()
	f := &model.File{BucketID: bucketID, FolderID: folderID, Name: name, FileSize: size, UploadedBy: 1}
	if err := core.DB.Create(f).Error; err != nil {
		t.Fatalf("create file %s: %v", name, err)
	}
	return f
}

// setUsed 设置桶用量(测试用)。
func setUsed(t *testing.T, bucketID uint, used int64) {
	t.Helper()
	if err := core.DB.Model(&model.Bucket{}).Where("id = ?", bucketID).Update("used_space", used).Error; err != nil {
		t.Fatalf("set used: %v", err)
	}
}

// putMemObject 向内存存储放入对象(key = 文件 ID 字符串;测试用)。
func putMemObject(t *testing.T, bucketID, fileID uint, content string) {
	t.Helper()
	ms, ok := core.Storage.(*memStorage)
	if !ok {
		t.Fatal("storage 不是 memStorage")
	}
	_ = ms.Put(context.Background(), utils.BucketEncoder(bucketID), strconv.FormatUint(uint64(fileID), 10),
		strings.NewReader(content), int64(len(content)))
}

func TestCheckUserQuota(t *testing.T) {
	newP0TestDB(t)
	ctx := context.Background()

	t.Run("nil_quota_unlimited_passes", func(t *testing.T) {
		u := mkUserQuota(t, "uq-unlimited", nil, 100) // nil = 不限
		if err := CheckUserQuota(ctx, u.ID); err != nil {
			t.Fatalf("nil quota should pass, got %v", err)
		}
	})
	t.Run("zero_quota_readonly_rejects", func(t *testing.T) {
		zero := int64(0)
		u := mkUserQuota(t, "uq-readonly", &zero, 0) // 0 = 仅可读
		if err := CheckUserQuota(ctx, u.ID); !errors.Is(err, ErrQuotaExceeded) {
			t.Fatalf("quota=0 should reject any growth, got %v", err)
		}
	})
	t.Run("within_passes", func(t *testing.T) {
		quota := int64(1000)
		u := mkUserQuota(t, "uq-within", &quota, 400)
		if err := CheckUserQuota(ctx, u.ID); err != nil {
			t.Fatalf("under quota should pass, got %v", err)
		}
	})
	t.Run("crossing_allowed", func(t *testing.T) {
		quota := int64(1000)
		u := mkUserQuota(t, "uq-cross", &quota, 400)
		if err := CheckUserQuota(ctx, u.ID); err != nil {
			t.Fatalf("not over quota yet: crossing write should pass, got %v", err)
		}
	})
	t.Run("over_quota_rejected", func(t *testing.T) {
		quota := int64(1000)
		u := mkUserQuota(t, "uq-over", &quota, 1100) // 已超额
		if err := CheckUserQuota(ctx, u.ID); !errors.Is(err, ErrQuotaExceeded) {
			t.Fatalf("over quota should reject any growth, got %v", err)
		}
		// 恰好等于配额 = 未超额 → 放行
		at := int64(1000)
		u2 := mkUserQuota(t, "uq-at", &at, 1000)
		if err := CheckUserQuota(ctx, u2.ID); err != nil {
			t.Fatalf("at quota exactly should pass, got %v", err)
		}
	})
}

// TestUserQuotaBlocksUpload 上传链路:未超额放行(可越过限额),已超额拒绝,记账归属上传者。
func TestUserQuotaBlocksUpload(t *testing.T) {
	newP0TestDB(t)
	ctx := context.Background()
	admin := mkUserP0(t, "uq-admin", model.Admin)
	quota := int64(100)
	u := mkUserQuota(t, "uq-uploader", &quota, 90) // 配额 100,已用 90
	b := mkBucketP0(t, "uq-bucket", model.NormalUser, admin.ID)
	setUsed(t, b.ID, 0)

	// 未超额:允许上传并越过限额(90 + 20 = 110 > 100,软配额放行),记账 +20
	if _, err := UploadFile(ctx, UploadFileArg{
		UserID: u.ID, BucketID: b.ID, Filename: "big.txt", Reader: bytes.NewReader(make([]byte, 20)),
	}); err != nil {
		t.Fatalf("upload crossing quota should pass (soft quota): %v", err)
	}
	if got := userUsed(t, u.ID); got != 110 {
		t.Fatalf("user used = %d, want 110(越过限额记账)", got)
	}
	// 桶用量同步入账
	var bb model.Bucket
	if err := core.DB.First(&bb, b.ID).Error; err != nil || bb.UsedSpace != 20 {
		t.Fatalf("bucket used = %d, want 20", bb.UsedSpace)
	}

	// 已超额(110 > 100):再上传 → 拒绝,无记账
	if _, err := UploadFile(ctx, UploadFileArg{
		UserID: u.ID, BucketID: b.ID, Filename: "more.txt", Reader: bytes.NewReader([]byte("x")),
	}); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("upload after over quota: want ErrQuotaExceeded, got %v", err)
	}
	if got := userUsed(t, u.ID); got != 110 {
		t.Fatalf("user used = %d, want 110(拒绝不应记账)", got)
	}

	// 恰好等于配额 = 未超额 → 仍放行(允许越过)
	u2 := mkUserQuota(t, "uq-atlimit", &quota, 100)
	b2 := mkBucketP0(t, "uq-bucket2", model.NormalUser, admin.ID)
	if _, err := UploadFile(ctx, UploadFileArg{
		UserID: u2.ID, BucketID: b2.ID, Filename: "at.txt", Reader: bytes.NewReader([]byte("x")),
	}); err != nil {
		t.Fatalf("upload at exactly quota should pass: %v", err)
	}
	if got := userUsed(t, u2.ID); got != 101 {
		t.Fatalf("user used = %d, want 101", got)
	}
}

// TestUserQuotaOnCopy 复制链路:操作者记账 + 已超额拒绝(软配额)。
func TestUserQuotaOnCopy(t *testing.T) {
	newP0TestDB(t)
	ctx := context.Background()
	admin := mkUserP0(t, "uq-copy-admin", model.Admin)
	owner := mkUserQuota(t, "uq-owner", nil, 0) // 源文件归属者(不限)
	quota := int64(100)
	overOperator := mkUserQuota(t, "uq-over-operator", &quota, 110) // 已超额
	normalOperator := mkUserQuota(t, "uq-operator", &quota, 90)     // 未超额
	b1 := mkBucketP0(t, "uq-src", model.NormalUser, admin.ID)
	b2 := mkBucketP0(t, "uq-dst", model.NormalUser, admin.ID)

	// 源文件(UploadedBy=owner,放对象存储)
	f := mkFileP0(t, b1.ID, 0, "a.txt", 20)
	putMemObject(t, b1.ID, f.ID, "01234567890123456789")

	// 已超额操作者(110 > 100)→ 复制拒绝,无记账
	if _, err := CopyFile(ctx, CopyFileArg{
		UserID: overOperator.ID, SrcBucketID: b1.ID, SrcFileID: f.ID,
		DstBucketID: b2.ID, DstDirPath: "", DstFilename: "copy.txt",
	}); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("copy by over-quota operator: want ErrQuotaExceeded, got %v", err)
	}
	if got := userUsed(t, overOperator.ID); got != 110 {
		t.Fatalf("over operator used = %d, want 110", got)
	}

	// 未超额操作者(90/100)→ 放行(允许越过限额),记账给操作者 → 110
	if _, err := CopyFile(ctx, CopyFileArg{
		UserID: normalOperator.ID, SrcBucketID: b1.ID, SrcFileID: f.ID,
		DstBucketID: b2.ID, DstDirPath: "", DstFilename: "copy2.txt",
	}); err != nil {
		t.Fatalf("copy by under-quota operator: %v", err)
	}
	if got := userUsed(t, normalOperator.ID); got != 110 {
		t.Fatalf("operator used = %d, want 110(越过限额记账)", got)
	}

	// 源归属者操作(owner 不限配额)→ 成功,记账给 owner
	if _, err := CopyFile(ctx, CopyFileArg{
		UserID: owner.ID, SrcBucketID: b1.ID, SrcFileID: f.ID,
		DstBucketID: b2.ID, DstDirPath: "", DstFilename: "copy3.txt",
	}); err != nil {
		t.Fatalf("copy by unlimited user: %v", err)
	}
	if got := userUsed(t, owner.ID); got != 20 {
		t.Fatalf("owner used = %d, want 20(复制计入操作者)", got)
	}
}

// TestUserQuotaReturnedOnDelete 删除链路:文件删除进回收站(GORM 软删)——记录/
// 对象/用量全部保留,不归还空间(软删不归还,回收站清除时才归还),读取 404。
func TestUserQuotaReturnedOnDelete(t *testing.T) {
	newP0TestDB(t)
	ctx := context.Background()
	admin := mkUserP0(t, "uq-del-admin", model.Admin)
	quota := int64(1000)
	u := mkUserQuota(t, "uq-del", &quota, 100)
	b := mkBucketP0(t, "uq-del-bucket", model.NormalUser, admin.ID)
	setUsed(t, b.ID, 100)

	f := mkFileP0(t, b.ID, 0, "a.txt", 30) // UploadedBy=1(admin)… 修正:归属 u
	core.DB.Model(&model.File{}).Where("id = ?", f.ID).Update("uploaded_by", u.ID)
	putMemObject(t, b.ID, f.ID, "012345678901234567890123456789")

	if err := DeleteFile(ctx, DeleteFileArg{UserID: admin.ID, BucketID: b.ID, FileID: f.ID}); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	// 回收站软删不归还用量(软删不归还,清除才归还)
	if got := userUsed(t, u.ID); got != 100 {
		t.Fatalf("user used = %d, want 100(软删不归还)", got)
	}
	var bb model.Bucket
	if err := core.DB.First(&bb, b.ID).Error; err != nil || bb.UsedSpace != 100 {
		t.Fatalf("bucket used = %d, want 100(软删不归还)", bb.UsedSpace)
	}
	// 记录软删保留(deleted_at 非空),对象保留(回收站可恢复)
	var rec model.File
	if err := core.DB.Unscoped().First(&rec, f.ID).Error; err != nil {
		t.Fatalf("recycle row must be retained: %v", err)
	}
	if !rec.DeletedAt.Valid {
		t.Fatal("file row should be soft-deleted (deleted_at set)")
	}
	key := utils.BucketEncoder(b.ID) + "/" + strconv.FormatUint(uint64(f.ID), 10)
	if _, ok := core.Storage.(*memStorage).objects[key]; !ok {
		t.Fatal("object should be retained in recycle bin")
	}
	// 回收站内条目读取 404(软删过滤)
	if _, err := GetFile(ctx, GetFileArg{BucketID: b.ID, FileID: f.ID}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("recycled file read: want ErrNotFound, got %v", err)
	}
}

// TestUpdateUserQuota 管理员设置用户配额(设置/仅可读/置回不限/负值拒绝)。
func TestUpdateUserQuota(t *testing.T) {
	newP0TestDB(t)
	ctx := context.Background()
	admin := mkUserP0(t, "uq-upd-admin", model.Admin)
	u := mkUserP0(t, "uq-upd", model.NormalUser)

	// 设置 5GB(经 PUT /users/:id quota 字段)
	quota := int64(5 << 30)
	upd, err := UpdateUser(ctx, UpdateUserArg{OperatorID: admin.ID, TargetID: u.ID,
		In: UpdateUserInput{Quota: &quota}})
	if err != nil {
		t.Fatalf("UpdateUser quota: %v", err)
	}
	if upd.Quota == nil || *upd.Quota != 5<<30 {
		t.Fatalf("quota = %v, want %d", upd.Quota, int64(5<<30))
	}
	// 置 0 = 仅可读(经 UpdateUser quota 字段)
	zero := int64(0)
	upd, err = UpdateUser(ctx, UpdateUserArg{OperatorID: admin.ID, TargetID: u.ID,
		In: UpdateUserInput{Quota: &zero}})
	if err != nil {
		t.Fatalf("UpdateUser quota=0: %v", err)
	}
	if upd.Quota == nil || *upd.Quota != 0 {
		t.Fatalf("quota = %v, want 0(仅可读)", upd.Quota)
	}
	// 置回不限(经专用接口 UpdateUserQuota,Quota=nil → NULL)
	upd, err = UpdateUserQuota(ctx, UpdateUserQuotaArg{OperatorID: admin.ID, TargetID: u.ID})
	if err != nil {
		t.Fatalf("UpdateUserQuota clear: %v", err)
	}
	if upd.Quota != nil {
		t.Fatalf("quota = %v, want nil(不限)", upd.Quota)
	}
	// 负值拒绝(两个入口一致)
	neg := int64(-1)
	if _, err := UpdateUser(ctx, UpdateUserArg{OperatorID: admin.ID, TargetID: u.ID,
		In: UpdateUserInput{Quota: &neg}}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("negative quota: want ErrInvalidInput, got %v", err)
	}
	if _, err := UpdateUserQuota(ctx, UpdateUserQuotaArg{OperatorID: admin.ID, TargetID: u.ID, Quota: &neg}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("UpdateUserQuota negative: want ErrInvalidInput, got %v", err)
	}
}

// TestRecycleBinReUploadSameName 回收站软删行不占用唯一名索引:文件删除进回收站后,
// 同目录重新上传同名文件成功(唯一索引已改为部分索引 WHERE deleted_at IS NULL);
// 活动行之间同名仍受唯一约束保护。
func TestRecycleBinReUploadSameName(t *testing.T) {
	newP0TestDB(t)
	ctx := context.Background()
	admin := mkUserP0(t, "rb-admin", model.Admin)
	b := mkBucketP0(t, "rb-bucket", model.NormalUser, admin.ID)
	setUsed(t, b.ID, 0)

	f := mkFileP0(t, b.ID, 0, "same.txt", 10)
	putMemObject(t, b.ID, f.ID, "0123456789")
	if err := DeleteFile(ctx, DeleteFileArg{UserID: admin.ID, BucketID: b.ID, FileID: f.ID}); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	// 同名重新上传(软删行仍在,部分唯一索引放行;应用层 uniqueName 不重命名)
	nf, err := UploadFile(ctx, UploadFileArg{
		UserID: admin.ID, BucketID: b.ID, Filename: "same.txt", Reader: bytes.NewReader(make([]byte, 5)),
	})
	if err != nil {
		t.Fatalf("re-upload same name after recycle: %v", err)
	}
	if nf.Name != "same.txt" {
		t.Fatalf("new file name = %q, want same.txt(回收站行不占名)", nf.Name)
	}
	// 回收站旧记录 + 活动新记录各一行(同 (bucket,folder,name_lower) 共存)
	var n int64
	if err := core.DB.Unscoped().Model(&model.File{}).
		Where("bucket_id = ? AND folder_id = ? AND name_lower = 'same.txt'", b.ID, 0).Count(&n).Error; err != nil || n != 2 {
		t.Fatalf("rows = %d, want 2(回收站 1 + 活动 1)", n)
	}
	// 活动行之间同名仍唯一(直接建行绕过应用层,由部分索引兜底拒绝)
	dup := &model.File{BucketID: b.ID, FolderID: 0, Name: "same.txt", FileSize: 1, UploadedBy: admin.ID}
	if err := core.DB.Create(dup).Error; err == nil {
		t.Fatal("duplicate active same-name row should violate partial unique index")
	}
}

// TestProcessDeleteTaskReturnsUserUsage 整桶删除(DeleteTask,DirID=0)按归属者归还用户用量:
// 此前收尾事务只删记录/桶/分享,从不归还 → 用户用量永久虚高(与 processDeleteDirTask 不一致)。
func TestProcessDeleteTaskReturnsUserUsage(t *testing.T) {
	newP0TestDB(t)
	ctx := context.Background()
	admin := mkUserP0(t, "dt-admin", model.Admin)
	quota := int64(1000)
	u1 := mkUserQuota(t, "dt-u1", &quota, 200) // 有配额用户(200 已用,含桶内 120)
	u2 := mkUserQuota(t, "dt-u2", nil, 100)    // 无限用户(100 已用,含桶内 80)
	b := mkBucketP0(t, "dt-bucket", model.NormalUser, admin.ID)
	setUsed(t, b.ID, 200)

	// 桶内两个文件(对象 + 记录),分别归属 u1/u2
	f1 := mkFileP0(t, b.ID, 0, "a.txt", 120)
	core.DB.Model(&model.File{}).Where("id = ?", f1.ID).Update("uploaded_by", u1.ID)
	putMemObject(t, b.ID, f1.ID, strings.Repeat("x", 120))
	f2 := mkFileP0(t, b.ID, 0, "b.txt", 80)
	core.DB.Model(&model.File{}).Where("id = ?", f2.ID).Update("uploaded_by", u2.ID)
	putMemObject(t, b.ID, f2.ID, strings.Repeat("y", 80))

	// 落整桶删除任务并同步处理
	task := &model.DeleteTask{BucketID: b.ID, DirID: 0, Status: 0}
	if err := core.DB.Create(task).Error; err != nil {
		t.Fatalf("create delete task: %v", err)
	}
	if err := ProcessDeleteTask(ctx, ProcessDeleteTaskArg{TaskID: task.ID}); err != nil {
		t.Fatalf("ProcessDeleteTask: %v", err)
	}

	// 用量按归属者归还(200-120=80;100-80=20)
	if got := userUsed(t, u1.ID); got != 80 {
		t.Fatalf("u1 used = %d, want 80(整桶删除归还 120)", got)
	}
	if got := userUsed(t, u2.ID); got != 20 {
		t.Fatalf("u2 used = %d, want 20(整桶删除归还 80)", got)
	}
	// 桶与任务已硬删
	var cnt int64
	if err := core.DB.Unscoped().Model(&model.Bucket{}).Where("id = ?", b.ID).Count(&cnt).Error; err != nil || cnt != 0 {
		t.Fatalf("bucket rows = %d, want 0", cnt)
	}
	if err := core.DB.Unscoped().Model(&model.DeleteTask{}).Where("id = ?", task.ID).Count(&cnt).Error; err != nil || cnt != 0 {
		t.Fatalf("task rows = %d, want 0", cnt)
	}
}

// TestCopyFolderOverQuotaRejected 已超额操作者发起文件夹复制 → 直接 ErrQuotaExceeded,
// 不建目标目录、不落任务(此前会落任务后逐文件失败,永久卡死)。
func TestCopyFolderOverQuotaRejected(t *testing.T) {
	newP0TestDB(t)
	ctx := context.Background()
	admin := mkUserP0(t, "cq-admin", model.Admin)
	quota := int64(100)
	over := mkUserQuota(t, "cq-over", &quota, 110) // 已超额
	src := mkBucketP0(t, "cq-src", model.NormalUser, admin.ID)
	dst := mkBucketP0(t, "cq-dst", model.NormalUser, admin.ID)
	srcFolder := mkFolderP0(t, src.ID, 0, over.ID, "sub")
	f := mkFileP0(t, src.ID, srcFolder.ID, "a.txt", 20)
	putMemObject(t, src.ID, f.ID, strings.Repeat("a", 20))

	if _, err := CopyFolder(ctx, CopyFolderArg{
		UserID: over.ID, SrcBucketID: src.ID, SrcFolderID: srcFolder.ID,
		DstBucketID: dst.ID, DstDirPath: "",
	}); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("over-quota CopyFolder: want ErrQuotaExceeded, got %v", err)
	}
	// 无任务、目标桶无任何目录
	var cnt int64
	if err := core.DB.Model(&model.CopyTask{}).Count(&cnt).Error; err != nil || cnt != 0 {
		t.Fatalf("copy task rows = %d, want 0", cnt)
	}
	if err := core.DB.Model(&model.Folder{}).Where("bucket_id = ?", dst.ID).Count(&cnt).Error; err != nil || cnt != 0 {
		t.Fatalf("dst folder rows = %d, want 0", cnt)
	}
}

// TestCopyTaskQuotaAbandoned 存量复制任务遇配额拒绝 → 任务放弃(硬删),防每轮无限重试;
// 已复制部分保留(目标无新增残留文件)。
func TestCopyTaskQuotaAbandoned(t *testing.T) {
	newP0TestDB(t)
	ctx := context.Background()
	admin := mkUserP0(t, "ca-admin", model.Admin)
	quota := int64(100)
	over := mkUserQuota(t, "ca-over", &quota, 110) // 已超额
	src := mkBucketP0(t, "ca-src", model.NormalUser, admin.ID)
	dst := mkBucketP0(t, "ca-dst", model.NormalUser, admin.ID)
	srcFolder := mkFolderP0(t, src.ID, 0, over.ID, "sub")
	dstFolder := mkFolderP0(t, dst.ID, 0, over.ID, "sub-copy")
	f := mkFileP0(t, src.ID, srcFolder.ID, "a.txt", 20)
	putMemObject(t, src.ID, f.ID, strings.Repeat("a", 20))

	task := &model.CopyTask{
		BucketID: src.ID, SourceFolderID: srcFolder.ID,
		DstBucketID: dst.ID, DstFolderID: dstFolder.ID,
		DstDirPath: "", DstName: "sub-copy", UploadedBy: over.ID, Status: 0,
	}
	if err := core.DB.Create(task).Error; err != nil {
		t.Fatalf("create copy task: %v", err)
	}
	if err := ProcessCopyTask(ctx, ProcessCopyTaskArg{TaskID: task.ID}); err == nil {
		t.Fatal("over-quota copy task: want error, got nil")
	}
	// 任务已硬删(不再续跑)
	var cnt int64
	if err := core.DB.Unscoped().Model(&model.CopyTask{}).Where("id = ?", task.ID).Count(&cnt).Error; err != nil || cnt != 0 {
		t.Fatalf("copy task rows = %d, want 0", cnt)
	}
	// 目标目录下无新增文件
	if err := core.DB.Model(&model.File{}).Where("bucket_id = ? AND folder_id = ?", dst.ID, dstFolder.ID).Count(&cnt).Error; err != nil || cnt != 0 {
		t.Fatalf("dst file rows = %d, want 0", cnt)
	}
}
