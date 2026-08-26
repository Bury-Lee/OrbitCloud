package common

// errors_test.go —— 哨兵错误 → HTTP 状态码映射测试(含包装错误链)。

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
)

// TestHTTPStatusDirect 每个哨兵错误直接映射。
func TestHTTPStatusDirect(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{ErrInvalidInput, http.StatusBadRequest},                          // 400
		{ErrUnauthorized, http.StatusUnauthorized},                        // 401
		{ErrForbidden, http.StatusForbidden},                              // 403
		{ErrNotFound, http.StatusNotFound},                                // 404
		{ErrConflict, http.StatusConflict},                                // 409
		{ErrQuotaExceeded, http.StatusRequestEntityTooLarge},              // 413
		{ErrRangeNotSatisfiable, http.StatusRequestedRangeNotSatisfiable}, // 416
		{ErrTooManyRequests, http.StatusServiceUnavailable},               // 503
	}
	for _, tc := range cases {
		if got := HTTPStatus(tc.err); got != tc.want {
			t.Errorf("HTTPStatus(%v) = %d, want %d", tc.err, got, tc.want)
		}
	}
}

// TestHTTPStatusWrapped 通过 %w 包装的错误链也应命中哨兵(errors.Is 语义)。
func TestHTTPStatusWrapped(t *testing.T) {
	wrapped := fmt.Errorf("处理失败: %w", ErrNotFound)
	if got := HTTPStatus(wrapped); got != http.StatusNotFound {
		t.Errorf("HTTPStatus(wrapped ErrNotFound) = %d, want 404", got)
	}
	multi := fmt.Errorf("db: %w", fmt.Errorf("inner: %w", ErrForbidden))
	if got := HTTPStatus(multi); got != http.StatusForbidden {
		t.Errorf("HTTPStatus(深层包装) = %d, want 403", got)
	}
}

// TestHTTPStatusUnknown 未知错误一律 500(internal error)。
func TestHTTPStatusUnknown(t *testing.T) {
	cases := []error{
		errors.New("boom"),
		fmt.Errorf("io: %w", errors.New("eof")),
		nil, // 防御:nil 不应 panic
	}
	for _, tc := range cases {
		want := http.StatusInternalServerError
		if got := HTTPStatus(tc); got != want {
			t.Errorf("HTTPStatus(%v) = %d, want %d", tc, got, want)
		}
	}
}

// TestSentinelDistinct 哨兵错误两两不同(防止误用同一变量)。
func TestSentinelDistinct(t *testing.T) {
	all := []error{ErrInvalidInput, ErrUnauthorized, ErrForbidden, ErrNotFound,
		ErrConflict, ErrQuotaExceeded, ErrRangeNotSatisfiable, ErrTooManyRequests}
	seen := map[string]bool{}
	for _, e := range all {
		key := e.Error()
		if seen[key] {
			t.Errorf("哨兵错误重复: %q", key)
		}
		seen[key] = true
	}
}
