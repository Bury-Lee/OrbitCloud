package common

// page_test.go —— 分页工具纯函数测试:NewOption 归一、keyset 游标编解码、
// 排序子句安全组装、末条 ID 提取;NewOptionFromQuery 走 gin 测试上下文。

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestNewOptionNormalize(t *testing.T) {
	cases := []struct {
		name           string
		page, pageSize int
		wantPage       int
		wantPageSize   int
	}{
		{"正常", 3, 20, 3, 20},
		{"page=0 → 1", 0, 20, 1, 20},
		{"page 负 → 1", -5, 20, 1, 20},
		{"pageSize=0 → 10", 1, 0, 1, 10},
		{"pageSize 负 → 10", 1, -3, 1, 10},
		{"pageSize>100 → 100", 1, 999, 1, 100},
		{"pageSize=100 边界", 1, 100, 1, 100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opt := NewOption(tc.page, tc.pageSize)
			if opt.Page != tc.wantPage || opt.PageSize != tc.wantPageSize {
				t.Fatalf("NewOption(%d,%d) = {%d,%d}, want {%d,%d}",
					tc.page, tc.pageSize, opt.Page, opt.PageSize, tc.wantPage, tc.wantPageSize)
			}
		})
	}
}

func TestNewOptionFromQuery(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET",
		"/?page=3&page_size=50&cursor=123&key=report&order=asc&start_time=2026-01-02T03:04:05Z&end_time=bad-time",
		nil)
	opt := NewOptionFromQuery(c)
	if opt.Page != 3 {
		t.Errorf("Page = %d, want 3", opt.Page)
	}
	if opt.PageSize != 50 {
		t.Errorf("PageSize = %d, want 50", opt.PageSize)
	}
	if opt.Cursor != 123 {
		t.Errorf("Cursor = %d, want 123", opt.Cursor)
	}
	if opt.Key != "report" {
		t.Errorf("Key = %q, want report", opt.Key)
	}
	if opt.Order != "asc" {
		t.Errorf("Order = %q, want asc", opt.Order)
	}
	if opt.StartTime.IsZero() {
		t.Error("StartTime 应解析 RFC3339")
	}
	if !opt.EndTime.IsZero() {
		t.Errorf("EndTime 非法格式应保持零值, got %v", opt.EndTime)
	}
}

func TestEncodeDecodeKeysetID(t *testing.T) {
	// 编解码往返
	for _, id := range []uint{0, 1, 42, 99999999, ^uint(0)} {
		enc := EncodeKeysetID(id)
		if got := DecodeKeysetID(enc); got != id {
			t.Errorf("round-trip %d → %q → %d", id, enc, got)
		}
	}
	// 非法输入 → 0(按"无游标/首页"处理)
	for _, s := range []string{"", "  ", "abc", "-1", "1.5", "99999999999999999999999999"} {
		if got := DecodeKeysetID(s); got != 0 {
			t.Errorf("DecodeKeysetID(%q) = %d, want 0", s, got)
		}
	}
}

func TestKeysetIDCond(t *testing.T) {
	cases := []struct {
		name     string
		prefix   string
		cursor   string
		wantCond string
		wantArgs []any
	}{
		{"空游标 → 空谓词", "", "", "", nil},
		{"非法游标 → 空谓词", "", "abc", "", nil},
		{"合法游标", "", "42", "id < ?", []any{uint(42)}},
		{"带表别名", "t", "7", "t.id < ?", []any{uint(7)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cond, args := KeysetIDCond(tc.prefix, tc.cursor)
			if cond != tc.wantCond {
				t.Errorf("cond = %q, want %q", cond, tc.wantCond)
			}
			if len(args) != len(tc.wantArgs) {
				t.Fatalf("args = %v, want %v", args, tc.wantArgs)
			}
			for i := range args {
				if args[i] != tc.wantArgs[i] {
					t.Errorf("args[%d] = %v, want %v", i, args[i], tc.wantArgs[i])
				}
			}
		})
	}
}

func TestBuildOrder(t *testing.T) {
	cases := []struct {
		name string
		opt  Option
		want string
	}{
		{"Orderby+Order ASC", Option{PageInfo: PageInfo{Order: "asc"}, Orderby: "created_at"}, "created_at ASC"},
		{"Orderby+Order DESC", Option{PageInfo: PageInfo{Order: "desc"}, Orderby: "id"}, "id DESC"},
		{"Order 非法方向 → DESC", Option{PageInfo: PageInfo{Order: "up"}, Orderby: "id"}, "id DESC"},
		{"Orderby 非法字段名 → 回退 id DESC", Option{PageInfo: PageInfo{Order: "asc"}, Orderby: "id; DROP"}, "id DESC"},
		{"仅 DefaultOrder", Option{DefaultOrder: "id DESC"}, "id DESC"},
		{"全空 → 不排序", Option{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildOrder(&tc.opt); got != tc.want {
				t.Errorf("buildOrder = %q, want %q", got, tc.want)
			}
		})
	}
}

type idStruct struct {
	ID uint
}

type idInt64Struct struct {
	ID int64
}

type idPtrStruct struct {
	ID *uint
}

func TestExtractLastID(t *testing.T) {
	if got := extractLastID([]int(nil)); got != 0 {
		t.Errorf("空切片 = %d, want 0", got)
	}
	if got := extractLastID([]idStruct{{ID: 9}, {ID: 42}}); got != 42 {
		t.Errorf("uint ID = %d, want 42", got)
	}
	if got := extractLastID([]idInt64Struct{{ID: 7}}); got != 7 {
		t.Errorf("int64 ID = %d, want 7", got)
	}
	if got := extractLastID([]idPtrStruct{{ID: nil}}); got != 0 {
		t.Errorf("nil 指针字段 = %d, want 0", got)
	}
}
