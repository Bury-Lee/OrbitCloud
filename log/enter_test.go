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
	if err := output(&buf, ERROR, nil, "", time.Now(), "boom"); err != nil {
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

	if err := output(f, WARN, nil, "", time.Now(), "slow query"); err != nil {
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
	// debug 模式:应附带调用方代码位置标签(enter_test.go)
	if !strings.Contains(got, "[enter_test.go:") {
		t.Fatalf("debug output should include caller [file:line] tag, got %q", got)
	}
	if !strings.Contains(got, "debug hello") {
		t.Fatalf("content missing, got %q", got)
	}
}

func TestNonDebugFileOutputSkipsCaller(t *testing.T) {
	defer func(l Level) { MinLogLevel = l }(MinLogLevel)
	defer func(l *log.Logger) { Logger = l }(Logger)

	MinLogLevel = INFO // 非 Debug 模式:INFO 输出不带调用方位置
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
		t.Fatalf("non-debug INFO output must not include caller, got %q", got)
	}
}

func TestDebugBufferOutputIncludesCaller(t *testing.T) {
	defer func(l Level) { MinLogLevel = l }(MinLogLevel)
	defer func(l *log.Logger) { Logger = l }(Logger)

	MinLogLevel = DEBUG
	var buf bytes.Buffer
	Init(&buf, DEBUG)
	// debug 模式:内存 writer 同样附带代码位置标签(与输出目标无关)
	if err := Debugf("mem"); err != nil {
		t.Fatalf("Debugf: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "[enter_test.go:") {
		t.Fatalf("debug output should include caller [file:line] tag, got %q", got)
	}
	if !strings.Contains(got, "mem") {
		t.Fatalf("content missing, got %q", got)
	}
}

func TestErrorOutputAlwaysIncludesCaller(t *testing.T) {
	defer func(l Level) { MinLogLevel = l }(MinLogLevel)
	defer func(l *log.Logger) { Logger = l }(Logger)

	MinLogLevel = INFO // 非 debug 模式
	var buf bytes.Buffer
	Init(&buf, INFO)

	if err := Errorf("boom %d", 42); err != nil {
		t.Fatalf("Errorf: %v", err)
	}
	got := buf.String()
	// 错误级别:与级别配置无关恒附带代码位置标签
	if !strings.Contains(got, "[enter_test.go:") {
		t.Fatalf("error output should include caller [file:line] tag, got %q", got)
	}
	if !strings.Contains(got, "boom 42") {
		t.Fatalf("content missing, got %q", got)
	}
}

func TestFormatTags(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, ""},
		{[]string{}, ""},
		{[]string{"gorm"}, "[gorm] "},
		{[]string{"gorm", "sql"}, "[gorm] [sql] "},
		{[]string{"a", "", "b", "  "}, "[a] [b] "},
	}
	for _, c := range cases {
		if got := formatTags(c.in); got != c.want {
			t.Errorf("formatTags(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestShortPath(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"orbitcloud/api/auth_api.go", "api/auth_api.go"},     // module 前缀剥离(配合 -trimpath)
		{"orbitcloud\\api\\auth_api.go", "api/auth_api.go"},   // 反斜杠归一化
		{"C:\\src\\orbitcloud\\main.go", "main.go"},           // 无法相对化(跨盘)→ base 兜底
		{"/opt/orbitcloud/core/storage.go", "storage.go"},     // 无法相对化(异盘根)→ base 兜底
	}
	for _, c := range cases {
		if got := shortPath(c.in); got != c.want {
			t.Errorf("shortPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestPrefixedLevelFuncs 验证带前缀的 P 变体:输出含 [tag] 标记、内容完整、级别过滤生效。
func TestPrefixedLevelFuncs(t *testing.T) {
	defer func(l Level) { MinLogLevel = l }(MinLogLevel)
	defer func(l *log.Logger) { Logger = l }(Logger)
	MinLogLevel = DEBUG

	var buf bytes.Buffer
	Init(&buf, DEBUG)

	if err := DebugfP("debug %d", []string{"dbg", "query"}, 1); err != nil {
		t.Fatalf("DebugfP: %v", err)
	}
	if err := InfofP("info %s", []string{"gorm"}, "x"); err != nil {
		t.Fatalf("InfofP: %v", err)
	}
	if err := WarnfP("warn %s", []string{"gorm", "slow"}, "y"); err != nil {
		t.Fatalf("WarnfP: %v", err)
	}
	if err := ErrorfP("error %d", []string{"file"}, 7); err != nil {
		t.Fatalf("ErrorfP: %v", err)
	}
	got := buf.String()
	// debug 模式输出含代码位置标签,业务 [tag] 标记紧随级别标签之后
	for _, want := range []string{
		"[DEBUG] ",
		"[dbg] [query] ",
		"debug 1",
		"[INFO] ",
		"[gorm] ",
		"info x",
		"[WARN] ",
		"[gorm] [slow] ",
		"warn y",
		"[ERROR] ",
		"[file] ",
		"error 7",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q, got:\n%s", want, got)
		}
	}
	// 每条日志均应带代码位置来源标签
	if strings.Count(got, "[enter_test.go:") != 4 {
		t.Fatalf("expected 4 caller tags, got:\n%s", got)
	}

	// 级别过滤:WARN 级别下 DebugfP/InfofP 不输出
	buf.Reset()
	Init(&buf, WARN)
	if err := InfofP("should skip", []string{"gorm"}); err != nil {
		t.Fatalf("InfofP: %v", err)
	}
	if got := buf.String(); got != "" {
		t.Fatalf("info must be filtered at WARN level, got %q", got)
	}
}

// TestSinkReceivesTags 验证接收器(sink)收到的内容包含 [tag] 标记(入库不丢标记)。
func TestSinkReceivesTags(t *testing.T) {
	defer func(l Level) { MinLogLevel = l }(MinLogLevel)
	defer func(l *log.Logger) { Logger = l }(Logger)
	defer SetLogSink(nil)

	var buf bytes.Buffer
	Init(&buf, DEBUG)
	MinLogLevel = DEBUG

	var got []string
	SetLogSink(func(lvl Level, msg string) { got = append(got, msg) })

	if err := InfofP("hello %s", []string{"gorm"}, "world"); err != nil {
		t.Fatalf("InfofP: %v", err)
	}
	// debug 模式:sink 收到的内容应包含 [tag] 标记与代码位置来源(入库不丢上下文)
	if len(got) != 1 || !strings.HasPrefix(got[0], "[gorm] ") ||
		!strings.Contains(got[0], "[enter_test.go:") || !strings.HasSuffix(got[0], "hello world") {
		t.Fatalf("sink msg = %v, want [gorm]-tagged msg with caller tag and content", got)
	}
}
