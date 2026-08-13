package log

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestRotateWriter 构造可注入时钟的轮转 writer(与 NewRotateWriter 同字段,便于控制时间)。
func newTestRotateWriter(dir, prefix string, maxSizeKB int64, now func() time.Time) *rotateWriter {
	return &rotateWriter{
		dir:     dir,
		prefix:  prefix,
		maxSize: maxSizeKB * 1024,
		now:     now,
	}
}

func writeN(t *testing.T, w *rotateWriter, n int) {
	t.Helper()
	if _, err := w.Write(bytes.Repeat([]byte("a"), n)); err != nil {
		t.Fatalf("write %d bytes: %v", n, err)
	}
}

// listFiles 列出 dir 下非目录条目(按文件名排序)。
func listFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

func sizeOf(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fi.Size()
}

// 大小超限 → 同日期文件夹内切换新文件(同一秒轮转自动加 .1 序号,不混写)。
func TestRotateWriterBySize(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.Local)
	w := newTestRotateWriter(dir, "orbitcloud", 1, func() time.Time { return now }) // 上限 1KB
	defer w.Close()

	writeN(t, w, 800) // 未超限
	writeN(t, w, 800) // 800+800 > 1024 → 轮转

	dayDir := filepath.Join(dir, "2026-07-15")
	files := listFiles(t, dayDir)
	if len(files) != 2 {
		t.Fatalf("expect 2 files after size rotate, got %v", files)
	}
	// ReadDir 按文件名排序:先 orbitcloud-10-00-00.log,再 orbitcloud-10-00-00.1.log
	if sizeOf(t, filepath.Join(dayDir, files[0])) != 800 {
		t.Fatalf("first file size = %d, want 800", sizeOf(t, filepath.Join(dayDir, files[0])))
	}
	if sizeOf(t, filepath.Join(dayDir, files[1])) != 800 {
		t.Fatalf("second file size = %d, want 800", sizeOf(t, filepath.Join(dayDir, files[1])))
	}
}

// 日期变化 → 切换到新日期子文件夹(跨天轮转)。
func TestRotateWriterByDate(t *testing.T) {
	dir := t.TempDir()
	day1 := time.Date(2026, 7, 15, 23, 59, 59, 0, time.Local)
	day2 := time.Date(2026, 7, 16, 0, 0, 1, 0, time.Local)
	cur := day1
	w := newTestRotateWriter(dir, "orbitcloud", 0, func() time.Time { return cur })
	defer w.Close()

	writeN(t, w, 100)
	cur = day2
	writeN(t, w, 200)

	if files := listFiles(t, filepath.Join(dir, "2026-07-15")); len(files) != 1 {
		t.Fatalf("day1 dir expect 1 file, got %v", files)
	}
	day2Files := listFiles(t, filepath.Join(dir, "2026-07-16"))
	if len(day2Files) != 1 {
		t.Fatalf("day2 dir expect 1 file, got %v", day2Files)
	}
	if sizeOf(t, filepath.Join(dir, "2026-07-16", day2Files[0])) != 200 {
		t.Fatalf("day2 file size != 200")
	}
}

// 文件名 = {prefix}-{HH}-{MM}-{SS}.log。
func TestRotateWriterNaming(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 15, 14, 30, 5, 0, time.Local)
	w := newTestRotateWriter(dir, "app", 0, func() time.Time { return now })
	defer w.Close()
	writeN(t, w, 10)

	files := listFiles(t, filepath.Join(dir, "2026-07-15"))
	if len(files) != 1 {
		t.Fatalf("expect 1 file, got %v", files)
	}
	if files[0] != "app-14-30-05.log" {
		t.Fatalf("unexpected file name %q", files[0])
	}
}

// NewRotateWriter 的 outputPath 解析:带扩展名 → 目录=父目录/前缀=文件名;无扩展名 → 视为目录,前缀默认 orbitcloud。
func TestNewRotateWriterParsing(t *testing.T) {
	base := t.TempDir()
	dayDirName := time.Now().Format("2006-01-02")

	// 带扩展名: ./logs/orbitcloud.log → 目录 ./logs, 前缀 orbitcloud
	withExt, err := NewRotateWriter(filepath.Join(base, "logs", "orbitcloud.log"), 0)
	if err != nil {
		t.Fatalf("NewRotateWriter(with ext): %v", err)
	}
	defer withExt.(*rotateWriter).Close()
	if _, err := withExt.Write([]byte("x")); err != nil {
		t.Fatalf("write: %v", err)
	}
	names := listFiles(t, filepath.Join(base, "logs", dayDirName))
	if len(names) != 1 || !strings.HasPrefix(names[0], "orbitcloud-") || !strings.HasSuffix(names[0], ".log") {
		t.Fatalf("with-ext: unexpected files %v", names)
	}

	// 无扩展名: 视为目录, 前缀默认 orbitcloud
	asDir, err := NewRotateWriter(filepath.Join(base, "raw"), 0)
	if err != nil {
		t.Fatalf("NewRotateWriter(as dir): %v", err)
	}
	defer asDir.(*rotateWriter).Close()
	if _, err := asDir.Write([]byte("y")); err != nil {
		t.Fatalf("write: %v", err)
	}
	names = listFiles(t, filepath.Join(base, "raw", dayDirName))
	if len(names) != 1 || !strings.HasPrefix(names[0], "orbitcloud-") {
		t.Fatalf("as-dir: unexpected files %v", names)
	}
}
