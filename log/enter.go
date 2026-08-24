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
	"path/filepath"
	"runtime"
	"strings"
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

// shortPath 把编译期文件路径压缩为便于阅读的短路径(统一 / 分隔):
//   - 优先剥离 module 前缀(orbitcloud/xxx → xxx,配合 -trimpath 构建);
//   - 其次取相对当前工作目录的路径;
//   - 兜底仅保留文件名。
func shortPath(file string) string {
	file = filepath.ToSlash(file)
	const module = "orbitcloud/"
	if strings.HasPrefix(file, module) {
		return strings.TrimPrefix(file, module)
	}
	if cwd, err := os.Getwd(); err == nil {
		if rel, err := filepath.Rel(cwd, file); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.Base(file)
}

// shortCaller 返回调用方的精简代码位置(file:line,如 api/auth_api.go:56)。
// skip 为相对本函数的栈帧数:0 为 Caller 自身,1 为调用方。
// 调用栈统一约定:output ← 包装函数(或 LogPrint)← 用户代码 → skip=3。
func shortCaller(skip int) string {
	if _, file, line, ok := runtime.Caller(skip); ok {
		return fmt.Sprintf("%s:%d", shortPath(file), line)
	}
	return ""
}

// formatTags 把前缀标签列表渲染为日志标记串:["gorm","sql"] → "[gorm] [sql] "。
// 空列表/空标签会被跳过,返回空串(不带任何标记)。
func formatTags(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	var b strings.Builder
	for _, t := range tags {
		if strings.TrimSpace(t) == "" {
			continue
		}
		b.WriteByte('[')
		b.WriteString(t)
		b.WriteString("] ")
	}
	return b.String()
}

// output 写一条日志:级别过滤在调用方已做,此处仅负责格式化与写入。
// 格式:[2006-01-02 15:04:05.000] [LEVEL] [tag1] [tag2] [path/file.go:line] content;
// tags 为前缀标签列表,按 formatTags 渲染成 [tag] 标记;终端输出按级别着色。
// 代码位置标签自动附加(追加在业务标签之后、内容之前):
//   - debug 模式(MinLogLevel == DEBUG):所有级别均附带,便于开发定位;
//   - 错误级别(ERROR/FATAL/PANIC):与级别配置无关恒附带,便于线上排查错误来源。
func output(out io.Writer, lvl Level, tags []string, prefix string, t time.Time, content string) error {
	if out == nil {
		out = os.Stdout
	}
	//终端输出
	lvlTag := lvl.String()
	if isTerminal(out) {
		lvlTag = levelColor(lvl) + lvlTag + colorReset
	}
	// 代码位置标签:debug 模式全级别,或 ERROR 及以上级别(调用栈偏移 3,见 shortCaller)
	if MinLogLevel == DEBUG || lvl >= ERROR {
		if pos := shortCaller(3); pos != "" {
			tags = append(tags, pos)
		}
	}
	tagStr := formatTags(tags)
	var line string
	if tagStr != "" {
		if prefix != "" {
			line = fmt.Sprintf("[%s] [%s] %s%s %s\n", t.Format("2006-01-02 15:04:05.000"), lvlTag, tagStr, prefix, content)
		} else {
			line = fmt.Sprintf("[%s] [%s] %s%s\n", t.Format("2006-01-02 15:04:05.000"), lvlTag, tagStr, content)
		}
	} else {
		line = fmt.Sprintf("[%s] [%s] %s %s\n", t.Format("2006-01-02 15:04:05.000"), lvlTag, prefix, content)
	}
	_, err := fmt.Fprint(out, line)

	// 可选:日志接收器(如日志入库)——由 appinit.AttachLogDBWriter 经 log.EnableLogDBWriter
	// 注册(见 log/dbwriter.go);级别过滤/重试/防递归由接收器自行实现。
	// 接收器收到带 [tag] 标记的完整内容,入库后标记不丢失。
	if s := getSink(); s != nil {
		s(lvl, tagStr+content)
	}
	return err
}

// ============ Debug 级别 ============
// Debug 记录调试信息，通常仅在开发环境使用
func Debug(msg ...any) error {
	if MinLogLevel <= DEBUG {
		return output(outWriter(), DEBUG, nil, "", time.Now(), fmt.Sprint(msg...))
	}
	return nil
}

func Debugf(format string, args ...any) error {
	if MinLogLevel <= DEBUG {
		return output(outWriter(), DEBUG, nil, "", time.Now(), fmt.Sprintf(format, args...))
	}
	return nil
}

// DebugfP 带前缀标签的 Debugf:prefix 为标签列表,按标签渲染成 [tag] 标记,
// 输出形如 [时间] [DEBUG] [tag1] [tag2] 内容。
func DebugfP(format string, prefix []string, args ...any) error {
	if MinLogLevel <= DEBUG {
		return output(outWriter(), DEBUG, prefix, "", time.Now(), fmt.Sprintf(format, args...))
	}
	return nil
}

// ============ Info 级别 ============
// 记录信息
func Info(msg ...any) error {
	if MinLogLevel <= INFO {
		return output(outWriter(), INFO, nil, "", time.Now(), fmt.Sprint(msg...))
	}
	return nil
}

func Infof(format string, args ...any) error {
	if MinLogLevel <= INFO {
		return output(outWriter(), INFO, nil, "", time.Now(), fmt.Sprintf(format, args...))
	}
	return nil
}

// InfofP 带前缀标签的 Infof:prefix 为标签列表,按标签渲染成 [tag] 标记,
// 输出形如 [时间] [INFO] [tag1] [tag2] 内容。
func InfofP(format string, prefix []string, args ...any) error {
	if MinLogLevel <= INFO {
		return output(outWriter(), INFO, prefix, "", time.Now(), fmt.Sprintf(format, args...))
	}
	return nil
}

// ============ Warn 级别 ============
func Warn(msg ...any) error {
	if MinLogLevel <= WARN {
		return output(outWriter(), WARN, nil, "", time.Now(), fmt.Sprint(msg...))
	}
	return nil
}

func Warnf(format string, args ...any) error {
	if MinLogLevel <= WARN {
		return output(outWriter(), WARN, nil, "", time.Now(), fmt.Sprintf(format, args...))
	}
	return nil
}

// WarnfP 带前缀标签的 Warnf:prefix 为标签列表,按标签渲染成 [tag] 标记,
// 输出形如 [时间] [WARN] [tag1] [tag2] 内容。
func WarnfP(format string, prefix []string, args ...any) error {
	if MinLogLevel <= WARN {
		return output(outWriter(), WARN, prefix, "", time.Now(), fmt.Sprintf(format, args...))
	}
	return nil
}

// ============ Error 级别 ============
func Error(msg ...any) error {
	if MinLogLevel <= ERROR {
		return output(outWriter(), ERROR, nil, "", time.Now(), fmt.Sprint(msg...))
	}
	return nil
}

func Errorf(format string, args ...any) error {
	if MinLogLevel <= ERROR {
		return output(outWriter(), ERROR, nil, "", time.Now(), fmt.Sprintf(format, args...))
	}
	return nil
}

// ErrorfP 带前缀标签的 Errorf:prefix 为标签列表,按标签渲染成 [tag] 标记,
// 输出形如 [时间] [ERROR] [tag1] [tag2] 内容。
func ErrorfP(format string, prefix []string, args ...any) error {
	if MinLogLevel <= ERROR {
		return output(outWriter(), ERROR, prefix, "", time.Now(), fmt.Sprintf(format, args...))
	}
	return nil
}

// ============ Fatal 级别 ============
// Fatal 记录日志后调用 os.Exit(1) 终止程序
func Fatal(msg ...any) {
	if MinLogLevel <= FATAL {
		output(outWriter(), FATAL, nil, "", time.Now(), fmt.Sprint(msg...)) // 错误仅记 stderr,不阻断退出
	}
	os.Exit(1)
}

func Fatalf(format string, args ...any) {
	if MinLogLevel <= FATAL {
		output(outWriter(), FATAL, nil, "", time.Now(), fmt.Sprintf(format, args...))
	}
	os.Exit(1)
}

// FatalfP 带前缀标签的 Fatalf:输出后 os.Exit(1) 终止程序。
func FatalfP(format string, prefix []string, args ...any) {
	if MinLogLevel <= FATAL {
		output(outWriter(), FATAL, prefix, "", time.Now(), fmt.Sprintf(format, args...)) // 错误仅记 stderr,不阻断退出
	}
	os.Exit(1)
}

// ============ Panic 级别 ============
// Panic 记录日志后调用 panic() 抛出异常
func Panic(msg ...any) {
	if MinLogLevel <= PANIC {
		output(outWriter(), PANIC, nil, "", time.Now(), fmt.Sprint(msg...))
	}
	panic(fmt.Sprint(msg...))
}

func Panicf(format string, args ...any) {
	if MinLogLevel <= PANIC {
		output(outWriter(), PANIC, nil, "", time.Now(), fmt.Sprintf(format, args...))
	}
	panic(fmt.Sprintf(format, args...))
}

// PanicfP 带前缀标签的 Panicf:输出后 panic() 抛出异常。
func PanicfP(format string, prefix []string, args ...any) {
	if MinLogLevel <= PANIC {
		output(outWriter(), PANIC, prefix, "", time.Now(), fmt.Sprintf(format, args...))
	}
	panic(fmt.Sprintf(format, args...))
}

// LogPrint 统一日志写入入口:组装并写入一行日志到 out(格式见 output)。
func LogPrint(out io.Writer, Level Level, prefix string, Time time.Time, Content string) error {
	return output(out, Level, nil, prefix, Time, Content)
}
