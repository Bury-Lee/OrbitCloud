package server

// quota.go —— 桶配额审查(写入前拦截)。
// 现状修复:此前 Quota 仅记账不拦截(ErrQuotaExceeded 为死代码),超配额仍可上传;
// 本文件提供统一审查入口:所有"增加桶用量"的写路径(上传/复制/移动/复制任务/
// SMB 写回)在落盘前调用,超限即拒绝(Web → HTTP 413;SMB → STATUS_DISK_FULL)。

import (
	"context"

	"gorm.io/gorm"

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

// CheckUserQuota 用户配额审查(写入前)。配额三态语义:
//   nil(不限)→ 放行;0(仅可读)→ 拒绝任何新增占用(delta > 0);
//   >0 → used_space + delta > quota 即拒绝。
// 与桶配额并行:上传/复制/SMB 写回需桶、用户两级配额同时通过,任一超限即拒绝。
//
// 入参:ctx 上下文;userID 行为者 users.id;delta 本次写入将增加的字节数(须 > 0)
// 返回:ErrQuotaExceeded(超限/仅可读)| nil(放行);查用户失败原样上抛
func CheckUserQuota(ctx context.Context, userID uint, delta int64) error {
	if delta <= 0 { // 无增量 → 无需审查
		return nil
	}
	var u model.User // 用户记录(配额/已用)
	if err := core.DB.WithContext(ctx).First(&u, userID).Error; err != nil { // 查用户
		return err // 失败上抛(调用方按原语义处理)
	}
	if u.Quota == nil { // nil = 不限
		return nil // 放行
	}
	if *u.Quota == 0 { // 0 = 仅可读
		return ErrQuotaExceeded // 拒绝任何新增占用
	}
	if u.UsedSpace+delta > *u.Quota { // 写入后超限?
		return ErrQuotaExceeded // 拒绝
	}
	return nil // 放行
}

// addUserUsedSpace 用户用量入账(原子自增;事务内调用传 tx,否则传 core.DB 句柄)。
// 用于上传(操作者)/复制(操作者)等"增加占用"路径,与桶用量同事务,防并发覆盖。
//
// 入参:db 事务或全局句柄;userID 归属用户;size 增量(字节,>0)
// 返回:error 更新失败
func addUserUsedSpace(db *gorm.DB, userID uint, size int64) error {
	if size <= 0 {
		return nil
	}
	return db.Model(&model.User{}).Where("id = ?", userID).
		UpdateColumn("used_space", gorm.Expr("used_space + ?", size)).Error
}

// returnUserUsedSpace 用户用量归还(原子扣减,钳制不为负;事务内调用传 tx)。
// 用于硬删/回收站清除等"释放占用"路径,按文件归属者(UploadedBy)归还。
//
// 入参:db 事务或全局句柄;userID 归属用户;size 归还字节数(>0)
// 返回:error 更新失败
func returnUserUsedSpace(db *gorm.DB, userID uint, size int64) error {
	if size <= 0 {
		return nil
	}
	return db.Model(&model.User{}).Where("id = ?", userID).
		UpdateColumn("used_space", gorm.Expr("CASE WHEN used_space - ? < 0 THEN 0 ELSE used_space - ? END", size, size)).Error
}
