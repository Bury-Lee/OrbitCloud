package utils

// range_test.go —— ParseRange 单元测试:合法解析 / 多区间忽略 / 非法拒绝三组用例。

import (
	"errors"
	"testing"

	"orbitcloud/common"
)

func TestParseRangeOK(t *testing.T) {
	cases := []struct {
		name   string
		header string
		size   int64
		want   ByteRange
	}{
		{"start-end 完整段", "bytes=0-99", 1000, ByteRange{0, 99}},
		{"start-end 中间段", "bytes=500-599", 1000, ByteRange{500, 599}},
		{"start-end 单字节", "bytes=7-7", 1000, ByteRange{7, 7}},
		{"start- 到文件尾", "bytes=900-", 1000, ByteRange{900, 999}},
		{"start- 首字节", "bytes=0-", 1000, ByteRange{0, 999}},
		{"-suffix 尾部", "bytes=-100", 1000, ByteRange{900, 999}},
		{"-suffix 恰好整文件", "bytes=-1000", 1000, ByteRange{0, 999}},
		{"-suffix 大于文件", "bytes=-5000", 1000, ByteRange{0, 999}},
		{"end 越界裁剪到文件尾", "bytes=0-2000", 1000, ByteRange{0, 999}},
		{"start-end 末尾边界", "bytes=999-999", 1000, ByteRange{999, 999}},
		{"无 Range 头(空)", "", 1000, ByteRange{}},
		{"无 Range 头(纯空白)", "   ", 1000, ByteRange{}},
		{"大小写前缀", "BYTES=0-1", 1000, ByteRange{0, 1}},
		{"前缀空白", " bytes=0-1", 1000, ByteRange{0, 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			br, err := ParseRange(tc.header, tc.size)
			if err != nil {
				t.Fatalf("ParseRange(%q, %d) err = %v, want nil", tc.header, tc.size, err)
			}
			if tc.header == "" || tc.header == "   " {
				if br != nil {
					t.Fatalf("ParseRange(%q) = %+v, want nil", tc.header, br)
				}
				return
			}
			if br == nil {
				t.Fatalf("ParseRange(%q) = nil, want %+v", tc.header, tc.want)
			}
			if *br != tc.want {
				t.Fatalf("ParseRange(%q, %d) = %+v, want %+v", tc.header, tc.size, *br, tc.want)
			}
		})
	}
}

func TestParseRangeLength(t *testing.T) {
	br := ByteRange{Start: 0, End: 99}
	if got := br.Length(); got != 100 {
		t.Fatalf("Length() = %d, want 100", got)
	}
	one := ByteRange{Start: 7, End: 7}
	if got := one.Length(); got != 1 {
		t.Fatalf("Length() = %d, want 1", got)
	}
}

// TestParseRangeMultiRangeIgnored 多区间 → (nil, nil) 忽略 Range 头(调用方按 200 全量);
// RFC 7233 §4.1 允许不支持 multipart/byteranges 的服务器忽略 Range 头。
func TestParseRangeMultiRangeIgnored(t *testing.T) {
	cases := []struct {
		name   string
		header string
		size   int64
	}{
		{"多区间", "bytes=0-1,5-6", 1000},
		{"多区间带空白", "bytes=0-1, 5-6", 1000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			br, err := ParseRange(tc.header, tc.size)
			if err != nil {
				t.Fatalf("ParseRange(%q, %d) err = %v, want nil(忽略)", tc.header, tc.size, err)
			}
			if br != nil {
				t.Fatalf("ParseRange(%q, %d) = %+v, want nil(忽略 Range 头 → 200 全量)", tc.header, tc.size, br)
			}
		})
	}
}

func TestParseRangeReject(t *testing.T) {
	cases := []struct {
		name   string
		header string
		size   int64
	}{
		{"非 bytes 单位", "items=0-99", 1000},
		{"无等号", "bytes0-99", 1000},
		{"缺横杠", "bytes=099", 1000},
		{"两个横杠", "bytes=0-1-2", 1000},
		{"start 非数字", "bytes=a-5", 1000},
		{"end 非数字", "bytes=0-b", 1000},
		{"start 带正号", "bytes=+5-9", 1000},
		{"start 带负号", "bytes=-5-9", 1000},
		{"空区间", "bytes=-", 1000},
		{"start>end", "bytes=100-99", 1000},
		{"suffix 为 0", "bytes=-0", 1000},
		{"suffix 非法", "bytes=-abc", 1000},
		{"start 溢出 int64", "bytes=99999999999999999999-", 1000},
		{"end 溢出 int64", "bytes=0-99999999999999999999", 1000},
		{"空文件任意区间", "bytes=0-0", 0},
		{"start 越界(=size)", "bytes=1000-", 1000},
		{"start 越界(>size)", "bytes=1001-2000", 1000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			br, err := ParseRange(tc.header, tc.size)
			if !errors.Is(err, common.ErrRangeNotSatisfiable) {
				t.Fatalf("ParseRange(%q, %d) err = %v (br=%+v), want ErrRangeNotSatisfiable",
					tc.header, tc.size, err, br)
			}
		})
	}
}
