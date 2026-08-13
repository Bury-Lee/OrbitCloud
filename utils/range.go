package utils

// range.go —— HTTP Range 请求头解析(单文件字节区间,断点续传/随机读底层)。
// 契约:
//   - 只支持单区间 "bytes=start-end | start- | -suffix",其它单位/非法 → ErrRangeNotSatisfiable(416);
//   - 多区间(含逗号)→ 忽略 Range 头返回 (nil, nil),调用方按 200 全量处理(RFC 7233 §4.1);
//   - 越界(Start ≥ size 或 size=0)→ ErrRangeNotSatisfiable,响应体 Content-Range: bytes */size;
//   - 无 Range 头 → 返回 (nil, nil),调用方按 200 全量处理。

import (
	"strconv"
	"strings"

	"orbitcloud/common"
)

// ParseRange 解析 Range 请求头为归一化区间。
//   - header:请求头原始值(可含空白,如 "bytes=0-99" / "bytes=-500" / "bytes=100-");
//   - size:文件总字节数(来自文件元数据 FileSize),决定越界判定与后缀区间裁剪。
// 空值或值内含逗号(多区间)→ 返回 (nil, nil) 忽略 Range 头;前缀不匹配、数值非法/
// 溢出、start>end、suffix≤0、size==0 或 Start≥size → ErrRangeNotSatisfiable。
// end 超出文件尾时裁剪到 size-1(RFC 7233 语义,非 416);返回值可直接交给 core.Storage.GetRange。
func ParseRange(header string, size int64) (*ByteRange, error) {
	h := strings.TrimSpace(header)
	if h == "" {
		return nil, nil // 无 Range:调用方按 200 全量处理
	}

	// 剥离前缀 "bytes="(大小写不敏感)
	if len(h) < len("bytes=") || !strings.EqualFold(h[:len("bytes=")], "bytes=") {
		return nil, common.ErrRangeNotSatisfiable
	}
	value := h[len("bytes="):]

	// 多区间(不支持 multipart)→ 忽略 Range 头,按 200 全量处理(RFC 7233 §4.1)
	if strings.Contains(value, ",") {
		return nil, nil
	}

	dash := strings.IndexByte(value, '-')
	if dash < 0 {
		return nil, common.ErrRangeNotSatisfiable
	}
	startPart, endPart := value[:dash], value[dash+1:]

	// 按分支解析(数值须非负整数)
	switch {
	case startPart == "" && endPart == "":
		// "bytes=-" 无意义
		return nil, common.ErrRangeNotSatisfiable

	case startPart == "":
		// "-suffix":文件尾部 suffix 字节
		suffix, err := parseUintPart(endPart)
		if err != nil || suffix <= 0 {
			return nil, common.ErrRangeNotSatisfiable
		}
		start := size - suffix
		if start < 0 {
			start = 0 // suffix ≥ size → 整文件
		}
		return finalizeRange(start, size-1, size)

	default:
		start, err := parseUintPart(startPart)
		if err != nil {
			return nil, common.ErrRangeNotSatisfiable
		}
		if endPart == "" {
			// "start-":读到文件尾
			return finalizeRange(start, size-1, size)
		}
		// "start-end"
		end, err := parseUintPart(endPart)
		if err != nil || start > end {
			return nil, common.ErrRangeNotSatisfiable
		}
		return finalizeRange(start, end, size)
	}
}

// parseUintPart 解析非负整数段(空/负数/溢出/非数字 → 错误)。
// 先做纯数字预检(拒绝 "+5" 这类带符号写法,RFC 7233 只允许 DIGIT),再交
// strconv.ParseInt 判定溢出(手写累加在超长输入下会 int64 正数回绕,不能自检)。
func parseUintPart(s string) (int64, error) {
	if s == "" {
		return 0, common.ErrRangeNotSatisfiable
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, common.ErrRangeNotSatisfiable
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, common.ErrRangeNotSatisfiable
	}
	return n, nil
}

// finalizeRange 边界校验并构造归一化区间:
// size == 0 或 Start ≥ size → ErrRangeNotSatisfiable;
// end 超出文件尾 → 裁剪到 size-1(RFC 7233 §2.1:区间解释为余下部分,非 416)。
func finalizeRange(start, end, size int64) (*ByteRange, error) {
	if size == 0 || start >= size {
		return nil, common.ErrRangeNotSatisfiable
	}
	if end >= size {
		end = size - 1
	}
	return &ByteRange{Start: start, End: end}, nil
}
