package server

// quota.go —— 桶配额审查(写入前拦截)。
// 现状修复:此前 Quota 仅记账不拦截(ErrQuotaExceeded 为死代码),超配额仍可上传;
// 本文件提供统一审查入口:所有"增加桶用量"的写路径(上传/复制/移动/复制任务/
// SMB 写回)在落盘前调用,超限即拒绝(Web → HTTP 413;SMB → STATUS_DISK_FULL)。

import (
	"context"

	"orbitcloud/core"
	"orbitcloud/model"
)

// CheckBucketQuota 桶配额审查(写入前):quota <= 0(不限)直接放行;
// used_space + delta > quota → ErrQuotaExceeded(413 / STATUS_DISK_FULL)。
// 说明:查询为只读;审查通过后的记账路径均为 SQL 原子更新
// (used_space = used_space ± n),并发下不会出现超卖。
//
// 入参:ctx 上下文;bucketID 目标桶 ID;delta 本次写入将增加的字节数(须 > 0)
// 返回:ErrQuotaExceeded(超限)| nil(放行);查桶失败原样上抛
func CheckBucketQuota(ctx context.Context, bucketID uint, delta int64) error {
	if delta <= 0 { // 无增量(如覆盖写回持平)→ 无需审查
		return nil
	}
	var b model.Bucket // 桶记录(配额/已用)
	if err := core.DB.WithContext(ctx).First(&b, bucketID).Error; err != nil { // 查桶
		return err // 失败上抛(调用方按原语义处理)
	}
	if b.Quota <= 0 { // 配额 0 = 不限
		return nil // 放行
	}
	if b.UsedSpace+delta > b.Quota { // 写入后超限?
		return ErrQuotaExceeded // 拒绝(HTTP 413 / SMB STATUS_DISK_FULL)
	}
	return nil // 放行
}
