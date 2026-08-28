package common

// page_test.go —— 分页工具测试:NewOption 归一、ParseOption 参数解析、
// keyset 游标编解码、排序子句安全组装、末条 ID 提取、PageQuery 集成分页。

import (
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"orbitcloud/core"
	"orbitcloud/model"
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

func TestParseOption(t *testing.T) {
	values := url.Values{}
	values.Set("page", "3")
	values.Set("page_size", "50")
	values.Set("cursor", "123")
	values.Set("key", "report")
	values.Set("order", "asc")
	opt := ParseOption(values)
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

	// 缺省:空参数 → 默认第 1 页 / 每页 10 条
	def := ParseOption(url.Values{})
	if def.Page != 1 || def.PageSize != 10 {
		t.Errorf("default = {%d,%d}, want {1,10}", def.Page, def.PageSize)
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

// newPageTestDB 内存 SQLite 建库(仅 User 表)并挂 core.DB(PageQuery 走全局 DB)。
func newPageTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:test-page-%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{TranslateError: true})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	core.DB = db
	return db
}

// mkUser 造用户(测试用;username 全局唯一)。
func mkUser(t *testing.T, db *gorm.DB, i int) *model.User {
	t.Helper()
	u := &model.User{Username: fmt.Sprintf("u%02d", i), Password: "x", Name: fmt.Sprintf("user-%d", i), PermissionLevel: model.NormalUser, Status: 1}
	if err := db.Create(u).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return u
}

// TestPageQuery 统一分页入口集成测试:分页/总数/游标续翻/时间范围/关键词搜索。
func TestPageQuery(t *testing.T) {
	db := newPageTestDB(t)

	// 25 个用户 + 1 个早于时间窗的用户(用于时间范围过滤)
	past := time.Now().Add(-48 * time.Hour)
	old := &model.User{Username: "old", Password: "x", PermissionLevel: model.NormalUser, Status: 1}
	if err := db.Create(old).Error; err != nil {
		t.Fatalf("create old user: %v", err)
	}
	if err := db.Model(&model.User{}).Where("id = ?", old.ID).Updates(map[string]any{"created_at": past, "updated_at": past}).Error; err != nil {
		t.Fatalf("backdate old user: %v", err)
	}
	for i := 0; i < 25; i++ {
		mkUser(t, db, i)
	}

	// 1. offset 分页:page=1,size=10,按 id 升序
	opt := NewOption(1, 10)
	opt.DefaultOrder = "id ASC"
	res, err := PageQuery(model.User{}, *opt, nil)
	if err != nil {
		t.Fatalf("page query: %v", err)
	}
	if len(res.List) != 10 || res.Total != 26 || res.TotalPages != 3 || !res.HasMore {
		t.Fatalf("page1 = %d items / total %d / pages %d / has_more %v, want 10/26/3/true", len(res.List), res.Total, res.TotalPages, res.HasMore)
	}
	if res.NextCursor != int64(res.List[len(res.List)-1].ID) {
		t.Fatalf("next_cursor = %d, want 末条 ID %d", res.NextCursor, res.List[len(res.List)-1].ID)
	}

	// 2. 游标续翻:page=1 但带 cursor(排序 id ASC 时 WHERE id > cursor)
	opt2 := NewOption(1, 10)
	opt2.DefaultOrder = "id ASC"
	opt2.Cursor = res.NextCursor
	res2, err := PageQuery(model.User{}, *opt2, nil)
	if err != nil {
		t.Fatalf("cursor query: %v", err)
	}
	if len(res2.List) != 10 || res2.List[0].ID != res.List[9].ID+1 {
		t.Fatalf("cursor page = %d items, first %d; want 10, first %d", len(res2.List), res2.List[0].ID, res.List[9].ID+1)
	}

	// 3. 时间范围:created_at 闭区间 → 只命中 old(48h 前)与今天的数据
	start := past.Add(-time.Hour)
	end := past.Add(time.Hour)
	res3, err := PageQuery(model.User{}, *NewOption(1, 50), &TimeInfo{StartTime: start, EndTime: end})
	if err != nil {
		t.Fatalf("time range query: %v", err)
	}
	if len(res3.List) != 1 || res3.List[0].ID != old.ID || res3.Total != 1 {
		t.Fatalf("time range = %d items / total %d, want 1/1(仅 old)", len(res3.List), res3.Total)
	}

	// 4. 关键词搜索:KeyList 限定列,OR 语义
	opt4 := NewOption(1, 50)
	opt4.Key = "user-1"
	opt4.KeyList = []string{"name", "username"}
	res4, err := PageQuery(model.User{}, *opt4, nil)
	if err != nil {
		t.Fatalf("key query: %v", err)
	}
	if len(res4.List) != 11 { // user-10..user-19(10 个 name 匹配)+ u1? username=u01 不匹配 "user-1" 前缀, name=user-1 1 个
		t.Fatalf("key query = %d items, want 11(name=user-1 精确 1 + user-10..user-19 前缀 10)", len(res4.List))
	}

	// 5. 空表 → 空切片非 nil(JSON 输出 [] 而非 null)
	opt5 := NewOption(1, 10)
	opt5.Filters = []func(*gorm.DB) *gorm.DB{
		func(q *gorm.DB) *gorm.DB { return q.Where("1 = 0") },
	}
	res5, err := PageQuery(model.User{}, *opt5, nil)
	if err != nil {
		t.Fatalf("empty query: %v", err)
	}
	if res5.List == nil || len(res5.List) != 0 {
		t.Fatalf("empty list = %#v, want 非 nil 空切片", res5.List)
	}
}