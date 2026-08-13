package server

import (
	"errors"

	"gorm.io/gorm"

	"orbitcloud/common"
)

// 错误哨兵统一别名(定义见 common/errors.go,api 层经 common.HTTPStatus 映射 HTTP 状态码)。
var (
	ErrInvalidInput  = common.ErrInvalidInput
	ErrUnauthorized  = common.ErrUnauthorized
	ErrForbidden     = common.ErrForbidden
	ErrNotFound      = common.ErrNotFound
	ErrConflict      = common.ErrConflict
	ErrQuotaExceeded = common.ErrQuotaExceeded
	// ErrRangeNotSatisfiable Range 区间越界/非法 → 416(断点续传/随机读)。
	ErrRangeNotSatisfiable = common.ErrRangeNotSatisfiable
)

// isUniqueViolation 判断是否为唯一约束冲突(gorm.ErrDuplicatedKey → ErrConflict)。
func isUniqueViolation(err error) bool {
	return errors.Is(err, gorm.ErrDuplicatedKey)
}

// Package server 业务服务层(被 api 层调用,依赖 model 持久化与对象存储)。
//
// 实现方式:全部为包级函数,无结构体关联与全局状态;函数内直接访问 core 全局单例
// (core.DB / core.JWT / core.Storage)。用户维度权限校验在 api 层完成,
// 本层只做对象维度可行性判断(存在性、状态、命名冲突、业务规则)。
