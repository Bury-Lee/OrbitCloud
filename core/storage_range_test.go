package core

// storage_range_test.go —— local 驱动 GetRange 白盒测试:
// 区间裁剪 / EOF 语义 / 句柄关闭 / 对象缺失映射。

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"orbitcloud/config"
)

func newTestLocalStorage(t *testing.T) *localStorage {
	t.Helper()
	st, err := newLocalStorage(&config.Storage{Endpoint: t.TempDir()})
	if err != nil {
		t.Fatalf("newLocalStorage: %v", err)
	}
	return st
}

// payload 100 字节,内容为 "0123456789" 重复 10 次(下标 i 处字节 = '0'+i%10,便于断言)。
func rangePayload() []byte {
	return []byte(strings.Repeat("0123456789", 10))
}

func putRangeFixture(t *testing.T, st *localStorage, key string, data []byte) {
	t.Helper()
	if err := st.Put(context.Background(), "b1", key, bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatalf("Put: %v", err)
	}
}

func readAllRange(t *testing.T, rc io.ReadCloser) []byte {
	t.Helper()
	defer rc.Close()
	buf, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	return buf
}

func TestLocalGetRangeSubset(t *testing.T) {
	st := newTestLocalStorage(t)
	data := rangePayload()
	putRangeFixture(t, st, "1", data)

	// 中间段 [10, 29]
	rc, err := st.GetRange(context.Background(), "b1", "1", 10, 29)
	if err != nil {
		t.Fatalf("GetRange: %v", err)
	}
	got := readAllRange(t, rc)
	if want := data[10:30]; !bytes.Equal(got, want) {
		t.Fatalf("range [10,29] = %q, want %q", got, want)
	}
}

func TestLocalGetRangeEdge(t *testing.T) {
	st := newTestLocalStorage(t)
	data := rangePayload()
	putRangeFixture(t, st, "1", data)
	size := int64(len(data))

	cases := []struct {
		start, end int64
	}{
		{0, 0},           // 首字节
		{0, size - 1},    // 整文件
		{size - 1, size - 1}, // 末字节
		{3, 3},           // 单字节
	}
	for _, tc := range cases {
		rc, err := st.GetRange(context.Background(), "b1", "1", tc.start, tc.end)
		if err != nil {
			t.Fatalf("GetRange(%d,%d): %v", tc.start, tc.end, err)
		}
		got := readAllRange(t, rc)
		if want := data[tc.start : tc.end+1]; !bytes.Equal(got, want) {
			t.Fatalf("range [%d,%d] = %q, want %q", tc.start, tc.end, got, want)
		}
	}
}

func TestLocalGetRangeEOF(t *testing.T) {
	st := newTestLocalStorage(t)
	putRangeFixture(t, st, "1", rangePayload())

	// 读满 length 后应 EOF(多读一次返回 io.EOF)
	rc, err := st.GetRange(context.Background(), "b1", "1", 0, 9)
	if err != nil {
		t.Fatalf("GetRange: %v", err)
	}
	buf := make([]byte, 10)
	n, err := rc.Read(buf)
	if n != 10 || err != nil {
		t.Fatalf("Read: n=%d err=%v, want 10,nil", n, err)
	}
	n2, err2 := rc.Read(buf)
	if n2 != 0 || err2 != io.EOF {
		t.Fatalf("second Read: n=%d err=%v, want 0,EOF", n2, err2)
	}
	_ = rc.Close()
}

func TestLocalGetRangeClose(t *testing.T) {
	st := newTestLocalStorage(t)
	putRangeFixture(t, st, "1", rangePayload())

	rc, err := st.GetRange(context.Background(), "b1", "1", 0, 4)
	if err != nil {
		t.Fatalf("GetRange: %v", err)
	}
	// 未读完即 Close:句柄应被释放(底层 os.File 二次 Close 返回 "already closed",
	// 属正常;调用方惯例忽略 Close 错误,此处仅断言不 panic)
	if err := rc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_ = rc.Close()
}

func TestLocalGetRangeObjectNotFound(t *testing.T) {
	st := newTestLocalStorage(t)
	_, err := st.GetRange(context.Background(), "b1", "nope", 0, 9)
	if !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("GetRange missing = %v, want ErrObjectNotFound", err)
	}
}

func TestLocalGetRangeShorterThanRequested(t *testing.T) {
	st := newTestLocalStorage(t)
	putRangeFixture(t, st, "1", rangePayload()) // 100 字节

	// 请求区间越过实际 EOF:读到文件尾自然结束(不做截断报错,由 api 层 416 兜底)
	rc, err := st.GetRange(context.Background(), "b1", "1", 90, 999)
	if err != nil {
		t.Fatalf("GetRange: %v", err)
	}
	got := readAllRange(t, rc)
	if want := rangePayload()[90:]; !bytes.Equal(got, want) {
		t.Fatalf("short read = %q, want %q", got, want)
	}
}
