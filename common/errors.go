package common

// 哨兵错误统一定义于此,server 层以别名引用(见 server/enter.go),
// api 层经 HTTPStatus 映射为统一响应。

import (
	"errors"
	"net/http"
)

var (
	// ErrInvalidInput 参数不合法 → 400
	ErrInvalidInput = errors.New("invalid input")
	// ErrUnauthorized 凭据错误/未登录 → 401
	ErrUnauthorized = errors.New("unauthorized")
	// ErrForbidden 权限不足/账号禁用 → 403
	ErrForbidden = errors.New("forbidden")
	// ErrNotFound 目标不存在 → 404
	ErrNotFound = errors.New("not found")
	// ErrConflict 唯一约束冲突/重复创建 → 409
	ErrConflict = errors.New("conflict")
	// ErrQuotaExceeded 容量配额不足 → 413
	ErrQuotaExceeded = errors.New("quota exceeded")
	// ErrRangeNotSatisfiable Range 请求头非法/区间越界 → 416
	// (断点续传/随机读;响应体带 Content-Range: bytes */size,见 api/stream.go)
	ErrRangeNotSatisfiable = errors.New("range not satisfiable")
)

// HTTPStatus 哨兵错误 → HTTP 状态码映射(未匹配 → 500)。
// 支持 errors.Is 链式判断(包装错误同样命中)。
func HTTPStatus(err error) int {
	switch {
	case errors.Is(err, ErrInvalidInput):
		return http.StatusBadRequest // 400
	case errors.Is(err, ErrUnauthorized):
		return http.StatusUnauthorized // 401
	case errors.Is(err, ErrForbidden):
		return http.StatusForbidden // 403
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound // 404
	case errors.Is(err, ErrConflict):
		return http.StatusConflict // 409
	case errors.Is(err, ErrQuotaExceeded):
		return http.StatusRequestEntityTooLarge // 413
	case errors.Is(err, ErrRangeNotSatisfiable):
		return http.StatusRequestedRangeNotSatisfiable // 416
	default:
		return http.StatusInternalServerError // 500
	}
}
