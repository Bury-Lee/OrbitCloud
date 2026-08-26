// quota_test.go —— 桶配额 + 用户配额审查与记账测试:
//   - CheckBucketQuota / CheckUserQuota:不限 / 未超 / 超限 / 临界 / 零负增量;
//   - 上传链路:超用户配额拒绝(413 语义)、记账归属上传者、与桶用量同事务;
//   - 复制链路:操作者记账 + 超限拒绝;
//   - 删除链路:按文件归属者归还用户用量;
//   - 管理员改配额(UpdateUser.Quota)。
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
		b := mkQuotaBucket(t, "q-full", 10, 10) // 已满
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
		if err := CheckUserQuota(ctx, u.ID, 1<<30); err != nil {
			t.Fatalf("nil quota should pass, got %v", err)
		}
	})
	t.Run("zero_quota_readonly_rejects", func(t *testing.T) {
		zero := int64(0)
		u := mkUserQuota(t, "uq-readonly", &zero, 0) // 0 = 仅可读
		if err := CheckUserQuota(ctx, u.ID, 1); !errors.Is(err, ErrQuotaExceeded) {
			t.Fatalf("quota=0 should reject any growth, got %v", err)
		}
		// 无增量放行(覆盖写持平不算新增占用)
		if err := CheckUserQuota(ctx, u.ID, 0); err != nil {
			t.Fatalf("delta=0 should pass, got %v", err)
		}
	})
	t.Run("within_passes", func(t *testing.T) {
		quota := int64(1000)
		u := mkUserQuota(t, "uq-within", &quota, 400)
		if err := CheckUserQuota(ctx, u.ID, 600); err != nil {
			t.Fatalf("used+delta == quota should pass, got %v", err)
		}
	})
	t.Run("exceeds_rejected", func(t *testing.T) {
		quota := int64(1000)
		u := mkUserQuota(t, "uq-exceed", &quota, 400)
		if err := CheckUserQuota(ctx, u.ID, 601); !errors.Is(err, ErrQuotaExceeded) {
			t.Fatalf("want ErrQuotaExceeded, got %v", err)
		}
	})
}

// TestUserQuotaBlocksUpload 上传链路:超用户配额拒绝,记账归属上传者。
func TestUserQuotaBlocksUpload(t *testing.T) {
	newP0TestDB(t)
	ctx := context.Background()
	admin := mkUserP0(t, "uq-admin", model.Admin)
	quota := int64(100)
	u := mkUserQuota(t, "uq-uploader", &quota, 90) // 配额 100,已用 90
	b := mkBucketP0(t, "uq-bucket", model.NormalUser, admin.ID)
	setUsed(t, b.ID, 0)

	// 超限:90 + 20 > 100 → 拒绝,无任何记账
	if _, err := UploadFile(ctx, UploadFileArg{
		UserID: u.ID, BucketID: b.ID, Filename: "big.txt", Reader: bytes.NewReader(make([]byte, 20)),
	}); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("upload over user quota: want ErrQuotaExceeded, got %v", err)
	}
	if got := userUsed(t, u.ID); got != 90 {
		t.Fatalf("user used = %d, want 90(拒绝不应记账)", got)
	}

	// 临界:90 + 10 == 100 → 放行,记账 +10
	if _, err := UploadFile(ctx, UploadFileArg{
		UserID: u.ID, BucketID: b.ID, Filename: "ok.txt", Reader: bytes.NewReader(make([]byte, 10)),
	}); err != nil {
		t.Fatalf("upload at limit: %v", err)
	}
	if got := userUsed(t, u.ID); got != 100 {
		t.Fatalf("user used = %d, want 100", got)
	}
	// 桶用量同步入账
	var bb model.Bucket
	if err := core.DB.First(&bb, b.ID).Error; err != nil || bb.UsedSpace != 10 {
		t.Fatalf("bucket used = %d, want 10", bb.UsedSpace)
	}
	// 再上传 → 已满拒绝
	if _, err := UploadFile(ctx, UploadFileArg{
		UserID: u.ID, BucketID: b.ID, Filename: "more.txt", Reader: bytes.NewReader([]byte("x")),
	}); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("upload after full: want ErrQuotaExceeded, got %v", err)
	}
}

// TestUserQuotaOnCopy 复制链路:操作者记账 + 超限拒绝。
func TestUserQuotaOnCopy(t *testing.T) {
	newP0TestDB(t)
	ctx := context.Background()
	admin := mkUserP0(t, "uq-copy-admin", model.Admin)
	owner := mkUserQuota(t, "uq-owner", nil, 0) // 源文件归属者(不限)
	quota := int64(100)
	operator := mkUserQuota(t, "uq-operator", &quota, 90)
	b1 := mkBucketP0(t, "uq-src", model.NormalUser, admin.ID)
	b2 := mkBucketP0(t, "uq-dst", model.NormalUser, admin.ID)

	// 源文件(UploadedBy=owner,放对象存储)
	f := mkFileP0(t, b1.ID, 0, "a.txt", 20)
	putMemObject(t, b1.ID, f.ID, "01234567890123456789")

	// 复制 → 操作者超限(90+20>100)拒绝
	if _, err := CopyFile(ctx, CopyFileArg{
		UserID: operator.ID, SrcBucketID: b1.ID, SrcFileID: f.ID,
		DstBucketID: b2.ID, DstDirPath: "", DstFilename: "copy.txt",
	}); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("copy over user quota: want ErrQuotaExceeded, got %v", err)
	}
	if got := userUsed(t, operator.ID); got != 90 {
		t.Fatalf("operator used = %d, want 90", got)
	}

	// 源归属者操作(owner 不限配额)→ 成功,记账给 owner
	if _, err := CopyFile(ctx, CopyFileArg{
		UserID: owner.ID, SrcBucketID: b1.ID, SrcFileID: f.ID,
		DstBucketID: b2.ID, DstDirPath: "", DstFilename: "copy2.txt",
	}); err != nil {
		t.Fatalf("copy by unlimited user: %v", err)
	}
	if got := userUsed(t, owner.ID); got != 20 {
		t.Fatalf("owner used = %d, want 20(复制计入操作者)", got)
	}
}

// TestUserQuotaReturnedOnDelete 删除链路:按文件归属者归还用户用量。
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
	if got := userUsed(t, u.ID); got != 70 {
		t.Fatalf("user used = %d, want 70(删除按归属者归还)", got)
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

