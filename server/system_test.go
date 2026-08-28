// system_test.go —— 运维服务层测试:日志文件列表(目录/忽略临时文件/排序)、
// 日志下载(路径穿越/非 .log/不存在 拒绝)。
package server

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"orbitcloud/config"
	"orbitcloud/core"
)

// withLogRoot 临时把日志根指向 dir,测试结束恢复。
func withLogRoot(t *testing.T, dir string) {
	t.Helper()
	old := core.GlobalConfig
	core.GlobalConfig = &config.Config{Log: config.Log{OutputPath: dir}}
	t.Cleanup(func() { core.GlobalConfig = old })
}

func TestListAndGetLogFiles(t *testing.T) {
	dir := t.TempDir()
	// 结构:根下 root.log;日期目录下 app-14-30-05.log 与写入中临时文件(.tmp 应忽略)
	if err := os.MkdirAll(filepath.Join(dir, "2026-08-28"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "root.log"), []byte("root"), 0o644); err != nil {
		t.Fatalf("write root.log: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "2026-08-28", "app-14-30-05.log"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write app log: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "2026-08-28", "app-14-30-05.log.tmp"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write tmp: %v", err)
	}
	withLogRoot(t, dir)

	// 列表:2 个 .log(临时文件忽略),按目录/修改时间排序
	infos, err := ListLogFiles(context.Background(), nil)
	if err != nil {
		t.Fatalf("list log files: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("files = %d, want 2(忽略 .tmp)", len(infos))
	}
	byPath := map[string]LogFileInfo{}
	for _, f := range infos {
		byPath[filepath.ToSlash(f.Path)] = f // Windows 下统一为正斜杠断言
	}
	if f, ok := byPath["root.log"]; !ok || f.Size != 4 {
		t.Fatalf("root.log missing or size wrong: %+v", f)
	}
	if f, ok := byPath["2026-08-28/app-14-30-05.log"]; !ok || f.Size != 5 || !f.Complete {
		t.Fatalf("app log missing or size wrong: %+v", f)
	}

	// 下载:正常路径 → 内容原样
	rc, info, err := GetLogFile(context.Background(), GetLogFileArg{Path: "2026-08-28/app-14-30-05.log"})
	if err != nil {
		t.Fatalf("get log file: %v", err)
	}
	defer rc.Close()
	body, err := io.ReadAll(rc)
	if err != nil || string(body) != "hello" {
		t.Fatalf("content = %q err %v, want hello", body, err)
	}
	if info.Size != 5 {
		t.Fatalf("size = %d, want 5", info.Size)
	}

	// 安全:穿越/绝对路径/非 .log → ErrInvalidInput;不存在 → ErrNotFound
	for _, p := range []string{"../secret.log", "..\\secret.log", "/etc/passwd.log", "a.txt", "./"} {
		if _, _, err := GetLogFile(context.Background(), GetLogFileArg{Path: p}); err != ErrInvalidInput {
			t.Fatalf("path %q: want ErrInvalidInput, got %v", p, err)
		}
	}
	if _, _, err := GetLogFile(context.Background(), GetLogFileArg{Path: "2026-08-28/nope.log"}); err != ErrNotFound {
		t.Fatalf("missing file: want ErrNotFound, got %v", err)
	}
}
