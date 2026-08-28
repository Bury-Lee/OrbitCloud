// write_cache_test.go —— 写回缓存冲刷拼装回归测试。
//
// 覆盖重构后的截断语义(设计讨论确认):
//   - Truncate 后越界写:截断作废的旧数据不得"复活",扩展区零填充;
//   - 纯截断(无写段)也必须落盘(原实现 truncateTo 指针 + dirty 缺失会丢截断);
//   - 多次截断取最小(只作废不恢复);
//   - 段超出逻辑 EOF 部分裁剪丢弃。
package smbgateway

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"

	"orbitcloud/core"
)

// memStorage 内存对象存储(mock;与 server 包测试同构)。
type memStorage struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newMemStorage() *memStorage {
	return &memStorage{objects: map[string][]byte{}}
}

func (m *memStorage) Put(ctx context.Context, bucket, key string, r io.Reader, size int64) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.objects[bucket+"/"+key] = b
	m.mu.Unlock()
	return nil
}

func (m *memStorage) Get(ctx context.Context, bucket, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.objects[bucket+"/"+key]
	if !ok {
		return nil, core.ErrObjectNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (m *memStorage) GetRange(ctx context.Context, bucket, key string, start, end int64) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.objects[bucket+"/"+key]
	if !ok {
		return nil, core.ErrObjectNotFound
	}
	return io.NopCloser(bytes.NewReader(b[start : end+1])), nil
}

func (m *memStorage) Delete(ctx context.Context, bucket, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, bucket+"/"+key)
	return nil
}

func (m *memStorage) DeleteBucket(ctx context.Context, bucket string) error {
	return nil
}

func (m *memStorage) Ping(ctx context.Context) error { return nil }

func mustFlush(t *testing.T, st core.ObjectStorage, spec writeSpec) []byte {
	t.Helper()
	c := NewWriteBackCache()
	if err := c.FlushToStorage(context.Background(), st, "b", "1", spec); err != nil {
		t.Fatalf("flush: %v", err)
	}
	m, ok := st.(*memStorage)
	if !ok {
		t.Fatal("not memStorage")
	}
	return m.objects["b/1"]
}

// 用户确认的关键场景:Truncate(100) 后 Write(150,100),Flush 时
// 截断作废的 [100,200) 旧数据不得"复活",[100,150) 零填充。
func TestFlushTruncateThenWriteBeyond(t *testing.T) {
	st := newMemStorage()
	st.objects["b/1"] = bytes.Repeat([]byte{'A'}, 200)

	spec := writeSpec{
		size:       250, // 截断 100 后写扩展:Write(150, 100) → EOF 250
		truncateAt: 100, // 截断点:对象 [100,∞) 旧数据作废
		segments: []writeRange{
			{off: 150, end: 250, data: bytes.Repeat([]byte{'B'}, 100)},
		},
		dirty: true,
	}
	got := mustFlush(t, st, spec)

	if len(got) != 250 {
		t.Fatalf("len = %d, want 250", len(got))
	}
	// [0,100) = 旧数据 A
	if !bytes.Equal(got[:100], bytes.Repeat([]byte{'A'}, 100)) {
		t.Fatalf("[0,100) = %q, want A*100 (旧数据保留)", got[:100])
	}
	// [100,150) = 零填充(截断作废区,不得复活旧数据)
	if !bytes.Equal(got[100:150], make([]byte, 50)) {
		t.Fatalf("[100,150) = %q, want 零填充", got[100:150])
	}
	// [150,250) = 新写入 B
	if !bytes.Equal(got[150:], bytes.Repeat([]byte{'B'}, 100)) {
		t.Fatalf("[150,250) = %q, want B*100", got[150:])
	}
}

// 纯截断(无写段)必须落盘:原实现 dirty 缺失 + truncateTo 指针会丢截断。
func TestFlushTruncateOnlyNoWrites(t *testing.T) {
	st := newMemStorage()
	st.objects["b/1"] = bytes.Repeat([]byte{'A'}, 200)

	spec := writeSpec{size: 100, truncateAt: 100, dirty: true} // 无写段
	got := mustFlush(t, st, spec)

	if len(got) != 100 {
		t.Fatalf("len = %d, want 100(纯截断须落盘)", len(got))
	}
	if !bytes.Equal(got, bytes.Repeat([]byte{'A'}, 100)) {
		t.Fatalf("content = %q, want A*100", got)
	}
}

// 截断到 0:对象清空。
func TestFlushTruncateToZero(t *testing.T) {
	st := newMemStorage()
	st.objects["b/1"] = bytes.Repeat([]byte{'A'}, 200)

	spec := writeSpec{size: 0, truncateAt: 0, dirty: true}
	got := mustFlush(t, st, spec)
	if len(got) != 0 {
		t.Fatalf("len = %d, want 0", len(got))
	}
}

// 多次截断取最小(只作废不恢复):Truncate(100) 后 Truncate(200) 扩展,
// 对象 [100,200) 旧数据不得复活,应为零填充。
func TestFlushMultipleTruncateKeepsMin(t *testing.T) {
	st := newMemStorage()
	st.objects["b/1"] = bytes.Repeat([]byte{'A'}, 200)

	spec := writeSpec{size: 200, truncateAt: 100, dirty: true} // 两次截断后的状态
	got := mustFlush(t, st, spec)

	if len(got) != 200 {
		t.Fatalf("len = %d, want 200", len(got))
	}
	if !bytes.Equal(got[:100], bytes.Repeat([]byte{'A'}, 100)) {
		t.Fatalf("[0,100) = %q, want A*100", got[:100])
	}
	if !bytes.Equal(got[100:], make([]byte, 100)) {
		t.Fatalf("[100,200) = %q, want 零填充(扩展区)", got[100:])
	}
}

// 无截断正常写:段覆盖 + 间隙从对象读。
func TestFlushNormalWriteNoTruncate(t *testing.T) {
	st := newMemStorage()
	st.objects["b/1"] = bytes.Repeat([]byte{'A'}, 200)

	spec := writeSpec{
		size:       200,
		truncateAt: -1,
		segments: []writeRange{
			{off: 0, end: 50, data: bytes.Repeat([]byte{'B'}, 50)},
		},
		dirty: true,
	}
	got := mustFlush(t, st, spec)
	if !bytes.Equal(got[:50], bytes.Repeat([]byte{'B'}, 50)) {
		t.Fatalf("[0,50) = %q, want B*50", got[:50])
	}
	if !bytes.Equal(got[50:], bytes.Repeat([]byte{'A'}, 150)) {
		t.Fatalf("[50,200) = %q, want A*150", got[50:])
	}
}

// 新文件(对象不存在):全部零填充 + 写段。
func TestFlushNewFileObjectMissing(t *testing.T) {
	st := newMemStorage() // 无对象

	spec := writeSpec{
		size:       100,
		truncateAt: -1,
		segments: []writeRange{
			{off: 0, end: 100, data: bytes.Repeat([]byte{'C'}, 100)},
		},
		dirty: true,
	}
	got := mustFlush(t, st, spec)
	if !bytes.Equal(got, bytes.Repeat([]byte{'C'}, 100)) {
		t.Fatalf("content = %q, want C*100", got)
	}
}

// 段起点超出逻辑 EOF(截断后旧段残留):拼装裁剪丢弃。
func TestFlushSegmentsBeyondEOFDiscarded(t *testing.T) {
	st := newMemStorage()
	st.objects["b/1"] = bytes.Repeat([]byte{'A'}, 200)

	spec := writeSpec{
		size:       120, // Truncate(120) 后 EOF 收缩
		truncateAt: 120,
		segments: []writeRange{
			{off: 150, end: 200, data: bytes.Repeat([]byte{'B'}, 50)}, // 超出 EOF
		},
		dirty: true,
	}
	got := mustFlush(t, st, spec)
	if len(got) != 120 {
		t.Fatalf("len = %d, want 120", len(got))
	}
	if !bytes.Equal(got, bytes.Repeat([]byte{'A'}, 120)) {
		t.Fatalf("content = %q, want A*120(段丢弃)", got)
	}
}

// 非 dirty 快照:直接返回,不触碰对象。
func TestFlushNotDirtyNoop(t *testing.T) {
	st := newMemStorage()
	st.objects["b/1"] = []byte("keep")
	c := NewWriteBackCache()
	if err := c.FlushToStorage(context.Background(), st, "b", "1", writeSpec{size: 4, truncateAt: -1, dirty: false}); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if got := st.objects["b/1"]; string(got) != "keep" {
		t.Fatalf("object changed to %q, want keep", got)
	}
}
