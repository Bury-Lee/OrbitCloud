package log

// 简易日志库封装(基于标准库 log / fmt):
//   - 全局 Logger 默认输出到 os.Stdout,可用 Init 或 LogPrint 切换目标;
//   - 级别过滤:MinLogLevel(默认 INFO),低于该级别的调用直接跳过;
//   - Debug/Info/Warn/Error/Fatal/Panic 及其 f 变体统一走 LogPrint;
//   - Fatal 输出后 os.Exit(1);Panic 输出后 panic。

import (
	"fmt"
	"io"
	"log"
	"os"
	"runtime"
	"time"
)

var Logger *log.Logger

// Level 日志级别。
type Level int

const (
	DEBUG Level = iota
	INFO
	WARN
	ERROR
	FATAL
	PANIC
)

func (l Level) String() string {
	switch l {
	case DEBUG:
		return "DEBUG"
	case INFO:
		return "INFO"
	case WARN:
		return "WARN"
	case ERROR:
		return "ERROR"
	case FATAL:
		return "FATAL"
	case PANIC:
		return "PANIC"
	default:
		return "UNKNOWN"
	}
}

var MinLogLevel Level = INFO // 默认只输出 INFO 及以上级别

// 文件轮转(见 rotate.go):输出到文件时按日期建子文件夹 {dir}/{YYYY-MM-DD}/,
// 文件名 {prefix}-{HH-MM-SS}.log;单文件超过 config.Log.MaxSize(KB)或跨天时自动切换新文件。
// outWriter 返回当前日志输出目标:Logger 已初始化 → Logger.Writer(),否则 os.Stdout。
func outWriter() io.Writer {
	if Logger != nil {
		return Logger.Writer()
	}
	return os.Stdout
}

// Init 初始化全局 Logger(可重复调用;out 为空 → os.Stdout)。
// 由 appinit.InitLog 在启动期调用,与 config.Log 的 level/output/max_size 对齐
// (Format 字段 json|text 当前未参与初始化,见 appinit/log.go)。
func Init(out io.Writer, level Level) {
	if out == nil {
		out = os.Stdout
	}
	if level < DEBUG || level > PANIC {
		level = INFO // 非法级别 → INFO
	}
	MinLogLevel = level
	Logger = log.New(out, "", log.LstdFlags|log.Lmicroseconds) // 前缀由 LogPrint 逐行拼接
}

// 终端 ANSI 颜色(仅 TTY 输出时生效;文件/管道/重定向不着色)。
const (
	colorReset   = "\x1b[0m"
	colorCyan    = "\x1b[36m"   // DEBUG
	colorGreen   = "\x1b[32m"   // INFO
	colorYellow  = "\x1b[33m"   // WARN
	colorRed     = "\x1b[31m"   // ERROR
	colorMagenta = "\x1b[35m"   // FATAL
	colorBoldRed = "\x1b[1;31m" // PANIC
)

// levelColor 返回级别对应的 ANSI 前景色码;未知级别返回空串(不着色)。
func levelColor(lvl Level) string {
	switch lvl {
	case DEBUG:
		return colorCyan
	case INFO:
		return colorGreen
	case WARN:
		return colorYellow
	case ERROR:
		return colorRed
	case FATAL:
		return colorMagenta
	case PANIC:
		return colorBoldRed
	default:
		return ""
	}
}

// isTerminal 判断 out 是否为终端(字符设备)。Windows 控制台同样满足
// ModeCharDevice;重定向到文件/管道时返回 false(文件日志不着色)。
func isTerminal(out io.Writer) bool {
	if out == nil {
		return false
	}
	f, ok := out.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// isFileOutput 判断 out 是否为文件输出:轮转文件 writer(*rotateWriter),
// 或非终端的 *os.File。终端 stdout 与内存 writer(如 bytes.Buffer)不算文件输出。
func isFileOutput(out io.Writer) bool {
	switch w := out.(type) {
	case *rotateWriter:
		return true
	case *os.File:
		return !isTerminal(w)
	}
	return false
}

// callerInfo 返回调用方的文件路径与行号(格式 file:line)。
// skip 为相对本函数的栈帧数:0 为 Caller 自身,1 为调用方。
func callerInfo(skip int) string {
	if _, file, line, ok := runtime.Caller(skip); ok {
		return fmt.Sprintf("%s:%d", file, line)
	}
	return ""
}

// output 写一条日志:级别过滤在调用方已做,此处仅负责格式化与写入。
// 格式:[2006-01-02 15:04:05.000] [LEVEL] [prefix] content;终端输出按级别着色。
// Debug 模式且输出到文件时,内容前附带调用方文件路径与行号(见 callerInfo)。
func output(out io.Writer, lvl Level, prefix string, t time.Time, content string) error {
	if out == nil {
		out = os.Stdout
	}
	//终端输出
	lvlTag := lvl.String()
	if isTerminal(out) {
		lvlTag = levelColor(lvl) + lvlTag + colorReset
	}
	// Debug 模式 + 文件输出:附带调用方文件路径与行号(调用栈偏移 4)
	if MinLogLevel == DEBUG && isFileOutput(out) {
		prefix = callerInfo(4)
	}
	line := fmt.Sprintf("[%s] [%s] %s %s\n", t.Format("2006-01-02 15:04:05.000"), lvlTag, prefix, content)
	_, err := fmt.Fprint(out, line)

	// 可选:日志接收器(如日志入库)——由 appinit.AttachLogDBWriter 经 log.EnableLogDBWriter
	// 注册(见 log/dbwriter.go);级别过滤/重试/防递归由接收器自行实现。
	if s := getSink(); s != nil {
		s(lvl, content)
	}
	return err
}

// ============ Debug 级别 ============
// Debug 记录调试信息，通常仅在开发环境使用
func Debug(msg ...any) error {
	if MinLogLevel <= DEBUG {
		return LogPrint(outWriter(), DEBUG, "", time.Now(), fmt.Sprint(msg...))
	}
	return nil
}

func Debugf(format string, args ...any) error {
	if MinLogLevel <= DEBUG {
		return LogPrint(outWriter(), DEBUG, "", time.Now(), fmt.Sprintf(format, args...))
	}
	return nil
}

// ============ Info 级别 ============
// 记录信息
func Info(msg ...any) error {
	if MinLogLevel <= INFO {
		return LogPrint(outWriter(), INFO, "", time.Now(), fmt.Sprint(msg...))
	}
	return nil
}

func Infof(format string, args ...any) error {
	if MinLogLevel <= INFO {
		return LogPrint(outWriter(), INFO, "", time.Now(), fmt.Sprintf(format, args...))
	}
	return nil
}

// ============ Warn 级别 ============
func Warn(msg ...any) error {
	if MinLogLevel <= WARN {
		return LogPrint(outWriter(), WARN, "", time.Now(), fmt.Sprint(msg...))
	}
	return nil
}

func Warnf(format string, args ...any) error {
	if MinLogLevel <= WARN {
		return LogPrint(outWriter(), WARN, "", time.Now(), fmt.Sprintf(format, args...))
	}
	return nil
}

// ============ Error 级别 ============
func Error(msg ...any) error {
	if MinLogLevel <= ERROR {
		return LogPrint(outWriter(), ERROR, "", time.Now(), fmt.Sprint(msg...))
	}
	return nil
}

func Errorf(format string, args ...any) error {
	if MinLogLevel <= ERROR {
		return LogPrint(outWriter(), ERROR, "", time.Now(), fmt.Sprintf(format, args...))
	}
	return nil
}

// ============ Fatal 级别 ============
// Fatal 记录日志后调用 os.Exit(1) 终止程序
func Fatal(msg ...any) {
	if MinLogLevel <= FATAL {
		LogPrint(outWriter(), FATAL, "", time.Now(), fmt.Sprint(msg...)) // 错误仅记 stderr,不阻断退出
	}
	os.Exit(1)
}

func Fatalf(format string, args ...any) {
	if MinLogLevel <= FATAL {
		LogPrint(outWriter(), FATAL, "", time.Now(), fmt.Sprintf(format, args...))
	}
	os.Exit(1)
}

// ============ Panic 级别 ============
// Panic 记录日志后调用 panic() 抛出异常
func Panic(msg ...any) {
	if MinLogLevel <= PANIC {
		LogPrint(outWriter(), PANIC, "", time.Now(), fmt.Sprint(msg...))
	}
	panic(fmt.Sprint(msg...))
}

func Panicf(format string, args ...any) {
	if MinLogLevel <= PANIC {
		LogPrint(outWriter(), PANIC, "", time.Now(), fmt.Sprintf(format, args...))
	}
	panic(fmt.Sprintf(format, args...))
}

// LogPrint 统一日志写入入口:组装并写入一行日志到 out(格式见 output)。
func LogPrint(out io.Writer, Level Level, prefix string, Time time.Time, Content string) error {
	return output(out, Level, prefix, Time, Content)
}
