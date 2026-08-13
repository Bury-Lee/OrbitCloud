package log

// output / levelColor / isTerminal 单元测试:验证终端按级别着色、文件与
// 非终端输出不着色(串行,不依赖全局 Logger 状态)。

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLevelColorDistinct(t *testing.T) {
	// 每个级别应有独立 ANSI 颜色,且互不相同
	seen := map[string]string{}
	for _, lvl := range []Level{DEBUG, INFO, WARN, ERROR, FATAL, PANIC} {
		c := levelColor(lvl)
		if c == "" {
			t.Fatalf("levelColor(%v) = empty, want a color code", lvl)
		}
		if !strings.HasPrefix(c, "\x1b[") {
			t.Fatalf("levelColor(%v) = %q, want ANSI escape prefix", lvl, c)
		}
		if prev, dup := seen[c]; dup {
			t.Fatalf("level %v color %q duplicates %v", lvl, c, prev)
		}
		seen[c] = lvl.String()
	}
	if got := levelColor(Level(99)); got != "" {
		t.Fatalf("levelColor(unknown) = %q, want empty", got)
	}
}

func TestOutputNoColorForBuffer(t *testing.T) {
	var buf bytes.Buffer
	if err := output(&buf, ERROR, "", time.Now(), "boom"); err != nil {
		t.Fatalf("output: %v", err)
	}
	if got := buf.String(); strings.Contains(got, "\x1b[") {
		t.Fatalf("buffer output must not be colored, got %q", got)
	}
}

func TestOutputNoColorForFile(t *testing.T) {
	// *os.File 但非字符设备(日志文件)→ 不着色
	path := filepath.Join(t.TempDir(), "test.log")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if err := output(f, WARN, "", time.Now(), "slow query"); err != nil {
		t.Fatalf("output: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); strings.Contains(got, "\x1b[") {
		t.Fatalf("file output must not be colored, got %q", got)
	}
}

func TestIsTerminal(t *testing.T) {
	if isTerminal(nil) {
		t.Fatal("isTerminal(nil) = true, want false")
	}
	var buf bytes.Buffer
	if isTerminal(&buf) {
		t.Fatal("isTerminal(buffer) = true, want false")
	}
	f, err := os.CreateTemp(t.TempDir(), "x")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if isTerminal(f) {
		t.Fatal("isTerminal(file) = true, want false")
	}
}

func TestDebugFileOutputIncludesCaller(t *testing.T) {
	// 恢复全局状态
	defer func(l Level) { MinLogLevel = l }(MinLogLevel)
	defer func(l *log.Logger) { Logger = l }(Logger)

	MinLogLevel = DEBUG
	path := filepath.Join(t.TempDir(), "test.log")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	Logger = log.New(f, "", 0)

	if err := Debugf("debug hello"); err != nil {
		t.Fatalf("Debugf: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	// 文件输出 + Debug 模式:应附带调用方文件路径与行号(enter_test.go)
	if !strings.Contains(got, "enter_test.go:") {
		t.Fatalf("debug file output should include caller file:line, got %q", got)
	}
	if !strings.Contains(got, "debug hello") {
		t.Fatalf("content missing, got %q", got)
	}
}

func TestNonDebugFileOutputSkipsCaller(t *testing.T) {
	defer func(l Level) { MinLogLevel = l }(MinLogLevel)
	defer func(l *log.Logger) { Logger = l }(Logger)

	MinLogLevel = INFO // 非 Debug 模式:文件输出不带调用方位置
	path := filepath.Join(t.TempDir(), "test.log")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	Logger = log.New(f, "", 0)

	if err := Infof("info hello"); err != nil {
		t.Fatalf("Infof: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); strings.Contains(got, "enter_test.go:") {
		t.Fatalf("non-debug file output must not include caller, got %q", got)
	}
}

func TestDebugBufferOutputSkipsCaller(t *testing.T) {
	defer func(l Level) { MinLogLevel = l }(MinLogLevel)

	MinLogLevel = DEBUG
	var buf bytes.Buffer
	// 内存 writer 不是文件输出:即使 Debug 模式也不附带调用方
	if err := output(&buf, DEBUG, "", time.Now(), "mem"); err != nil {
		t.Fatalf("output: %v", err)
	}
	if got := buf.String(); strings.Contains(got, ".go:") {
		t.Fatalf("buffer output must not include caller, got %q", got)
	}
}
