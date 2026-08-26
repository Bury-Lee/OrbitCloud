// quota_test.go —— 桶配额审查(CheckBucketQuota)单元测试:
//   - quota=0(不限)放行;
//   - 未超限放行;
//   - 超限拒绝(ErrQuotaExceeded);
//   - 临界值(used+delta == quota)放行;
//   - delta<=0 直接放行(不查库)。
// 基于内存 SQLite + 内存存储 mock(与 p0_acl_test.go 同环境)。
package server

import (
	"context"
	"errors"
	"testing"

	"orbitcloud/core"
	"orbitcloud/model"
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
