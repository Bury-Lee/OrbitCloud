package log

// 日志接收器(sink)机制:log 包主体只负责输出(stdout/文件);
// 需要"日志旁路"(如入库)的组件经 SetLogSink 注册回调,output() 写出一行日志后
// 把 (级别, 内容) 派发给接收器。接收器自行实现级别过滤/重试/防递归;
// LocalNote 直接写输出目标、绕过 sink,供接收器内部"本地补记"。

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// logSink 全局日志接收器(默认 nil = 不派发;SetLogSink 注册/清除)。
var (
	sinkMu sync.RWMutex
	sink   func(lvl Level, msg string)
)

// SetLogSink 注册/清除日志接收器。fn 为 nil 时清除(派发停止)。
// 接收器会收到 output() 写出的每条日志的 (级别, 内容);并发安全,可重复调用。
func SetLogSink(fn func(lvl Level, msg string)) {
	sinkMu.Lock()
	defer sinkMu.Unlock()
	sink = fn
}

// getSink 返回当前接收器(nil = 未注册)。
func getSink() func(lvl Level, msg string) {
	sinkMu.RLock()
	defer sinkMu.RUnlock()
	return sink
}

// LocalNote 本地直接输出一条告警/说明:绕过接收器(不经 output 派发,防递归)。
// 供接收器实现(如写库失败本地补记)调用;格式与全局日志一致,级别固定 WARN。
func LocalNote(msg string) {
	line := fmt.Sprintf("[%s] [WARN] %s\n", time.Now().Format("2006-01-02 15:04:05.000"), msg)
	_, _ = fmt.Fprint(outWriter(), line)
}

// ParseDBLevel 把配置字符串解析为 log.Level。
// 空串或非法值 → (INFO, false)(表示不启用入库);debug/info/warn/error 忽略大小写。
func ParseDBLevel(s string) (Level, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return DEBUG, true
	case "info":
		return INFO, true
	case "warn":
		return WARN, true
	case "error":
		return ERROR, true
	default:
		return INFO, false
	}
}
