// bucket.go —— 存储桶基础层:创建 / 查询 / 列表 / 修改 / 删除入口(删除任务执行见 bucket_task.go)。
//
// 桶权限语义:桶代表"不同的电脑",同级别(权限不低于 owner)可越 owner 代管;
// 桶管理归属/等级规则在 api/perm.go(permCanManageBucket),本文件只做可行性判断
// (桶名规范、创建者存在、字段校验)与写库。
package server

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"orbitcloud/core"
	"orbitcloud/log"
	"orbitcloud/model"
	"orbitcloud/utils"
)

// CreateBucketArg 创建桶入参(api 层从请求体 + 鉴权上下文组装)。
type CreateBucketArg struct {
	OwnerID         uint   // 创建者 users.id(鉴权中间件注入)
	Name            string // 桶名(全局唯一)
	Description     string // 描述(可空)
	PermissionLevel int8   // 桶访问等级(<=0 = 跟随创建者等级)
}

// CreateBucket 创建桶:校验桶名(非空 + S3 规范)与创建者存在后写入。
// 错误语义:name 非法 → ErrInvalidInput;创建者不存在 → ErrNotFound;重名 → ErrConflict。
func CreateBucket(ctx context.Context, arg CreateBucketArg) (*model.Bucket, error) {
	// 入参归一
	name := strings.TrimSpace(arg.Name)
	if name == "" {
		return nil, ErrInvalidInput
	}
	if err := utils.ValidateS3BucketName(name); err != nil {
		return nil, err // 桶名须符合 S3 命名规范
	}

	// 校验创建者存在
	var creator model.User
	err := core.DB.WithContext(ctx).Where("id = ? AND deleted_at IS NULL", arg.OwnerID).First(&creator).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("create bucket: query owner: %w", err)
	}
	perm := arg.PermissionLevel
	if perm <= 0 {
		perm = creator.PermissionLevel // 缺省自动取创建者等级
	}

	// 落库用 map Create:permission_level 的 gorm tag 带 default:1,
	// struct Create 会把零值(0)替换为默认值,map 可保留显式 0
	if err := core.DB.WithContext(ctx).Model(&model.Bucket{}).Create(map[string]any{
		"name":             name,
		"description":      arg.Description,
		"permission_level": perm,
		"owner_id":         arg.OwnerID,
		"status":           1,
	}).Error; err != nil {
		if isUniqueViolation(err) {
			return nil, ErrConflict
		}
		return nil, fmt.Errorf("create bucket: %w", err)
	}
	// map Create 不写回主键,重查一次拿完整记录(含 ID)
	bucket, err := GetBucketByName(ctx, GetBucketByNameArg{Name: name})
	if err != nil {
		return nil, fmt.Errorf("create bucket: reload: %w", err)
	}

	// Storage.Put 已含"隐式建桶"语义,无需预建

	// 操作日志
	log.Infof("create bucket: user %d bucket %q (id %d) permission %d", arg.OwnerID, bucket.Name, bucket.ID, bucket.PermissionLevel)
	return bucket, nil
}

// GetBucketArg 桶按 ID 查询入参。
type GetBucketArg struct {
	ID uint // 桶 buckets.id
}

// GetBucket 按 ID 查询桶;不存在 → ErrNotFound。桶详情允许任意登录用户查看。
func GetBucket(ctx context.Context, arg GetBucketArg) (*model.Bucket, error) {
	id := arg.ID
	var bucket model.Bucket
	err := core.DB.WithContext(ctx).First(&bucket, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get bucket %d: %w", id, err)
	}
	return &bucket, nil
}

// GetBucketByNameArg 桶按名称查询入参。
type GetBucketByNameArg struct {
	Name string // 桶名(全局唯一)
}

// GetBucketByName 按名称查询桶(对象存储桶映射用)。
// 错误语义:不存在 → ErrNotFound。
func GetBucketByName(ctx context.Context, arg GetBucketByNameArg) (*model.Bucket, error) {
	name := arg.Name
	var bucket model.Bucket
	err := core.DB.WithContext(ctx).Where("name = ?", strings.TrimSpace(name)).First(&bucket).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get bucket by name %q: %w", name, err)
	}
	return &bucket, nil
}

// ListBucketsArg 桶列表入参。
type ListBucketsArg struct {
	UserID uint // 操作者(可见性过滤依据)
}

// ListBuckets 列出用户可见的桶:本人创建的或权限足够的(permission_level >= 用户等级),
// 按 updated_at 倒序。
func ListBuckets(ctx context.Context, arg ListBucketsArg) ([]model.Bucket, error) {
	userID := arg.UserID
	// 查当前用户权限
	user, err := GetUser(ctx, GetUserArg{ID: userID})
	if err != nil {
		return nil, err
	}

	// 查询可见桶(PermissionLevel 越小权限越高)
	var buckets []model.Bucket
	err = core.DB.WithContext(ctx).
		Where("owner_id = ? OR permission_level >= ?", userID, user.PermissionLevel).
		Order("updated_at DESC").Find(&buckets).Error
	if err != nil {
		return nil, fmt.Errorf("list buckets: %w", err)
	}

	// 空列表返回 [] 而非 nil(保证 JSON 序列化为 [])
	if buckets == nil {
		buckets = []model.Bucket{}
	}
	return buckets, nil
}

// UpdateBucketInput 桶可更新字段(指针字段 nil 表示不更新;JSON 绑定走 api 层 DTO)。
type UpdateBucketInput struct {
	Description     string // 描述
	PermissionLevel *int8  // 访问所需最低权限
	Quota           *int64 // 容量配额(字节;0=不限)
	Status          *int   // 1 正常 / 0 禁用
}

// 桶管理权限判定已迁移至 api.permCanManageBucket(owner / 管理员 / 同级代管)。

// UpdateBucketArg 桶更新入参。
type UpdateBucketArg struct {
	OperatorID uint              // 操作者 users.id(操作日志)
	BucketID   uint              // 目标桶
	In         UpdateBucketInput // 待更新字段(指针字段 nil 表示不更新)
}

// UpdateBucket 更新桶:无更新字段 → ErrInvalidInput;桶不存在 → ErrNotFound。
// 管理权限由 api 层 permCanManageBucket 预检。
func UpdateBucket(ctx context.Context, arg UpdateBucketArg) (*model.Bucket, error) {
	operatorID, bucketID, in := arg.OperatorID, arg.BucketID, arg.In
	// 查桶(存在性)
	bucket, err := GetBucket(ctx, GetBucketArg{ID: bucketID})
	if err != nil {
		return nil, err
	}

	// 构造增量更新 map(只放非零字段)
	updates := map[string]any{}
	if in.Description != "" {
		updates["description"] = in.Description
	}
	if in.PermissionLevel != nil {
		if *in.PermissionLevel < 0 {
			return nil, ErrInvalidInput
		}
		updates["permission_level"] = *in.PermissionLevel
	}
	if in.Quota != nil {
		if *in.Quota < 0 {
			return nil, ErrInvalidInput
		}
		updates["quota"] = *in.Quota
	}
	if in.Status != nil {
		if *in.Status != 0 && *in.Status != 1 {
			return nil, ErrInvalidInput
		}
		updates["status"] = *in.Status
	}
	if len(updates) == 0 {
		return nil, ErrInvalidInput
	}

	// 执行更新
	if err := core.DB.WithContext(ctx).Model(&model.Bucket{}).Where("id = ?", bucketID).Updates(updates).Error; err != nil {
		return nil, fmt.Errorf("update bucket %d: %w", bucketID, err)
	}

	log.Infof("update bucket: operator %d bucket %d (%q) updated", operatorID, bucketID, bucket.Name)
	return GetBucket(ctx, GetBucketArg{ID: bucketID})
}

// DeleteBucketArg 桶删除入参。
type DeleteBucketArg struct {
	OperatorID uint // 操作者 users.id(操作日志)
	BucketID   uint // 目标桶
}

// DeleteBucket 删除桶(管理权限由 api 层预检):先落 DeleteTask 记录,
// 再置桶禁用(status=0)阻止删除期间新增文件,最后触发 processDeleteTask
// 整桶清理;中断残留由启动扫描 / cron 续跑幂等重试。桶不存在 → ErrNotFound。
func DeleteBucket(ctx context.Context, arg DeleteBucketArg) error {
	operatorID, bucketID := arg.OperatorID, arg.BucketID
	// 查桶(存在性)
	bucket, err := GetBucket(ctx, GetBucketArg{ID: bucketID})
	if err != nil {
		return err
	}

	// 先落删除任务、再置桶禁用:若中途崩溃,任务已落库可续跑,不会出现"桶禁用且无任务"的死状态
	task := &model.DeleteTask{BucketID: bucketID, Status: 0}
	if err := core.DB.WithContext(ctx).Create(task).Error; err != nil {
		return fmt.Errorf("delete bucket %d: create delete task: %w", bucketID, err)
	}

	// 桶置禁用:防删除期间新文件成为孤儿
	if err := core.DB.WithContext(ctx).Model(&model.Bucket{}).
		Where("id = ?", bucketID).Update("status", 0).Error; err != nil {
		return fmt.Errorf("delete bucket %d: disable bucket: %w", bucketID, err)
	}

	// 触发处理(尽力而为,失败不阻断返回——残留任务由启动/cron 续跑)
	if err := ProcessDeleteTask(ctx, ProcessDeleteTaskArg{TaskID: task.ID}); err != nil {
		log.Errorf("delete bucket %d: process task %d: %v", bucketID, task.ID, err)
	}

	log.Infof("delete bucket: operator %d bucket %d (%q) delete task %d queued", operatorID, bucketID, bucket.Name, task.ID)
	return nil
}
