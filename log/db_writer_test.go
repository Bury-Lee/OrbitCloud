package log

// 日志接收器(sink)机制单元测试:ParseDBLevel / SetLogSink 回调触发 / 清除 / LocalNote。

import (
	"bytes"
	"strings"
	"sync"
	"testing"
)

func TestParseDBLevel(t *testing.T) {
	cases := []struct {
		in  string
		lvl Level
		ok  bool
	}{
		{"", INFO, false},
		{"info", INFO, true},
		{"INFO", INFO, true},
		{"warn", WARN, true},
		{"error", ERROR, true},
		{"debug", DEBUG, true},
		{" verbose ", INFO, false},
	}
	for _, c := range cases {
		lvl, ok := ParseDBLevel(c.in)
		if ok != c.ok || (ok && lvl != c.lvl) {
			t.Errorf("ParseDBLevel(%q) = (%v,%v), want (%v,%v)", c.in, lvl, ok, c.lvl, c.ok)
		}
	}
}

func TestSetLogSinkTriggered(t *testing.T) {
	var mu sync.Mutex
	var got []string
	SetLogSink(func(lvl Level, msg string) {
		mu.Lock()
		got = append(got, msg)
		mu.Unlock()
	})
	defer SetLogSink(nil)

	Infof("sink-test: hello %d", 42)
	Warnf("sink-test: warn %s", "x")

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("sink called %d times, want 2 (got %v)", len(got), got)
	}
	if !strings.Contains(got[0], "sink-test: hello 42") || !strings.Contains(got[1], "sink-test: warn x") {
		t.Errorf("sink msgs mismatch: %v", got)
	}
}

func TestSetLogSinkClear(t *testing.T) {
	called := false
	SetLogSink(func(lvl Level, msg string) { called = true })
	SetLogSink(nil) // 清除后不再回调

	Infof("no-sink-now")
	if called {
		t.Error("sink should not be called after SetLogSink(nil)")
	}
}

func TestLocalNote(t *testing.T) {
	var buf bytes.Buffer
	old := Logger
	Init(&buf, INFO) // 重定向到 buf
	defer func() { Logger = old }()

	// 注册一个会 panic 的 sink,验证 LocalNote 不触发 sink(绕过接收器,防递归)
	panicked := false
	SetLogSink(func(lvl Level, msg string) { panicked = true })
	defer SetLogSink(nil)

	LocalNote("local-note-test")
	if !strings.Contains(buf.String(), "[WARN] local-note-test") {
		t.Errorf("expected LocalNote output, got: %q", buf.String())
	}
	if panicked {
		t.Error("LocalNote must bypass the sink (no recursion)")
	}
}
