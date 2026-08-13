package log

// GormLogger 单元测试:验证级别过滤、输出走 log 包、Trace 判定规则。
// 注意:测试会临时替换全局 Logger/MinLogLevel(串行,不 t.Parallel)。

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm/logger"
)

// withBuf 把全局日志切到 buf 并恢复原状。
func withBuf(t *testing.T, buf *bytes.Buffer) {
	t.Helper()
	oldLogger, oldLevel := Logger, MinLogLevel
	Init(buf, DEBUG)
	t.Cleanup(func() {
		Logger, MinLogLevel = oldLogger, oldLevel
	})
}

func TestNewGormLoggerDefaults(t *testing.T) {
	gl := NewGormLogger(logger.Warn)
	if gl.LogLevel != logger.Warn {
		t.Fatalf("LogLevel = %v, want %v", gl.LogLevel, logger.Warn)
	}
	if gl.SlowThreshold != 200*time.Millisecond {
		t.Fatalf("SlowThreshold = %v, want 200ms", gl.SlowThreshold)
	}
	if gl.IgnoreRecordNotFoundError {
		t.Fatal("IgnoreRecordNotFoundError default should be false")
	}
}

func TestLogModeReturnsCopy(t *testing.T) {
	gl := NewGormLogger(logger.Warn)
	next := gl.LogMode(logger.Info)
	if gl.LogLevel != logger.Warn {
		t.Fatal("LogMode must not mutate the receiver")
	}
	ngl, ok := next.(*GormLogger)
	if !ok {
		t.Fatalf("LogMode returned %T, want *GormLogger (so DB.Debug() keeps log 包封装)", next)
	}
	if ngl.LogLevel != logger.Info {
		t.Fatalf("copy LogLevel = %v, want Info", ngl.LogLevel)
	}
}

func TestInfoWarnErrorLevelFilter(t *testing.T) {
	var buf bytes.Buffer
	withBuf(t, &buf)
	gl := NewGormLogger(logger.Warn) // 低于 Warn 不输出

	gl.Info(context.Background(), "migrate table %s", "users")
	if buf.Len() != 0 {
		t.Fatalf("Info should be filtered at Warn level, got %q", buf.String())
	}

	gl.Warn(context.Background(), "slow %s", "sql")
	gl.Error(context.Background(), "boom %s", "err")
	out := buf.String()
	if !strings.Contains(out, "[WARN]") || !strings.Contains(out, "slow sql") {
		t.Fatalf("warn line missing: %q", out)
	}
	if !strings.Contains(out, "[ERROR]") || !strings.Contains(out, "boom err") {
		t.Fatalf("error line missing: %q", out)
	}
}

func TestTraceErrorLevel(t *testing.T) {
	var buf bytes.Buffer
	withBuf(t, &buf)
	gl := NewGormLogger(logger.Error)

	begin := time.Now().Add(-10 * time.Millisecond)
	gl.Trace(context.Background(), begin, func() (string, int64) {
		return "SELECT 1", 1
	}, errors.New("db down"))

	out := buf.String()
	if !strings.Contains(out, "[ERROR]") || !strings.Contains(out, "db down") || !strings.Contains(out, "SELECT 1") {
		t.Fatalf("trace error line missing: %q", out)
	}
}

func TestTraceIgnoresRecordNotFound(t *testing.T) {
	var buf bytes.Buffer
	withBuf(t, &buf)

	gl := NewGormLogger(logger.Error)
	gl.IgnoreRecordNotFoundError = true // 业务上查询无记录常见,不刷 Error
	gl.Trace(context.Background(), time.Now(), func() (string, int64) {
		return "SELECT * FROM users WHERE id=1", 0
	}, logger.ErrRecordNotFound)
	if buf.Len() != 0 {
		t.Fatalf("record not found should be ignored, got %q", buf.String())
	}

	gl.IgnoreRecordNotFoundError = false // 恢复 gorm 默认:仍记 Error
	gl.Trace(context.Background(), time.Now(), func() (string, int64) {
		return "SELECT * FROM users WHERE id=1", 0
	}, logger.ErrRecordNotFound)
	if !strings.Contains(buf.String(), "record not found") {
		t.Fatalf("record not found should be logged when not ignored, got %q", buf.String())
	}
}

func TestTraceSlowQueryWarn(t *testing.T) {
	var buf bytes.Buffer
	withBuf(t, &buf)
	gl := NewGormLogger(logger.Warn)
	gl.SlowThreshold = time.Millisecond

	begin := time.Now().Add(-5 * time.Millisecond)
	gl.Trace(context.Background(), begin, func() (string, int64) {
		return "SELECT 1", 2
	}, nil)

	out := buf.String()
	if !strings.Contains(out, "[WARN]") || !strings.Contains(out, "SLOW SQL") || !strings.Contains(out, "[rows:2]") {
		t.Fatalf("slow query line missing: %q", out)
	}
}

func TestTraceInfoPrintsAllSQL(t *testing.T) {
	var buf bytes.Buffer
	withBuf(t, &buf)
	gl := NewGormLogger(logger.Info)

	gl.Trace(context.Background(), time.Now(), func() (string, int64) {
		return "SELECT * FROM buckets", -1
	}, nil)

	out := buf.String()
	if !strings.Contains(out, "[INFO]") || !strings.Contains(out, "SELECT * FROM buckets") || !strings.Contains(out, "[rows:-]") {
		t.Fatalf("info sql line missing: %q", out)
	}
}

func TestTraceSilentNoOutput(t *testing.T) {
	var buf bytes.Buffer
	withBuf(t, &buf)
	gl := NewGormLogger(logger.Silent)

	gl.Trace(context.Background(), time.Now(), func() (string, int64) {
		return "SELECT 1", 1
	}, errors.New("should be dropped"))
	gl.Error(context.Background(), "should be dropped")

	if buf.Len() != 0 {
		t.Fatalf("Silent level should output nothing, got %q", buf.String())
	}
}
