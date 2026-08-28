// system.go —— 运维/系统接口服务层:系统运行状况、日志文件浏览与下载。
// 仅管理员使用(权限由 api 层 AdminMiddleware 预检);本文件只做数据读取与可行性。
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"orbitcloud/core"
	"orbitcloud/log"
)

// SystemStatusArg 系统状况查询入参(预留,当前无参数)。
type SystemStatusArg struct{}

// SystemStatus 系统运行状况:启动时间/运行时长、Go 运行时、DB 与对象存储连通性。
func SystemStatus(ctx context.Context, arg SystemStatusArg) (map[string]any, error) {
	// DB 连通性(写库 ping;gorm 的 Ping 失败仅置 false,不阻塞查询)
	dbAlive := true
	if sqlDB, err := core.DB.DB(); err != nil {
		dbAlive = false
	} else if err := sqlDB.PingContext(ctx); err != nil {
		dbAlive = false
	}

	// 对象存储连通性(超时兜底,避免接口悬挂)
	storageAlive := true
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := core.Storage.Ping(pingCtx); err != nil {
		storageAlive = false
	}

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	status := map[string]any{
		"app_name":         "orbitcloud",
		"version":          "unknown",
		"environment":      "unknown",
		"boot_time":        core.BootTime,
		"uptime_seconds":   int64(time.Since(core.BootTime).Seconds()),
		"go_version":       runtime.Version(),
		"num_cpu":          runtime.NumCPU(),
		"num_goroutine":    runtime.NumGoroutine(),
		"heap_alloc_bytes": mem.HeapAlloc,
		"db_alive":         dbAlive,
		"storage_alive":    storageAlive,
	}
	if cfg := core.GlobalConfig; cfg != nil {
		status["version"] = cfg.App.Version
		status["environment"] = cfg.App.Environment
	}
	return status, nil
}

// LogFileInfo 日志文件条目(目录树展平:目录名 = 日期目录,文件 = 日志文件)。
type LogFileInfo struct {
	Dir      string    `json:"dir"`       // 所在日期目录(相对日志根;根下文件 = "")
	Name     string    `json:"name"`      // 文件名
	Size     int64     `json:"size"`      // 字节
	ModTime  time.Time `json:"mod_time"`  // 修改时间
	Path     string    `json:"path"`      // 相对日志根的路径(下载接口回传用)
	Complete bool      `json:"complete"`  // 是否完整文件(非正在写入的临时文件)
}

// logRoot 日志文件根目录(配置 output_path;空 = 未启用文件日志,返回空列表)。
func logRoot() string {
	if core.GlobalConfig == nil {
		return ""
	}
	return strings.TrimSpace(core.GlobalConfig.Log.OutputPath)
}

// ListLogFiles 列出日志文件(递归一层日期目录,按目录/文件名排序)。
// 仅列 .log 文件;临时/写入中文件(tail 标记)忽略。
func ListLogFiles(ctx context.Context, arg any) ([]LogFileInfo, error) {
	root := logRoot()
	if root == "" {
		return nil, nil // 未启用文件日志(stdout 模式)
	}
	root = filepath.Clean(root)

	infos := []LogFileInfo{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // 单项访问失败跳过(如权限),不中断整体
		}
		if info.IsDir() {
			return nil
		}
		// 仅 .log 文件
		if strings.ToLower(filepath.Ext(info.Name())) != ".log" {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		dir := filepath.Dir(rel)
		if dir == "." {
			dir = ""
		}
		infos = append(infos, LogFileInfo{
			Dir:      dir,
			Name:     info.Name(),
			Size:     info.Size(),
			ModTime:  info.ModTime(),
			Path:     rel,
			Complete: !strings.Contains(info.Name(), "-writing") && !strings.HasSuffix(info.Name(), ".tmp"),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list log files: %w", err)
	}
	sort.Slice(infos, func(i, j int) bool {
		if infos[i].Dir != infos[j].Dir {
			return infos[i].Dir < infos[j].Dir
		}
		return infos[i].ModTime.After(infos[j].ModTime)
	})
	return infos, nil
}

// GetLogFileArg 日志文件下载入参。
type GetLogFileArg struct {
	Path string // 相对日志根的路径(如 "2026-08-28/app-14-30-05.log")
}

// GetLogFile 打开日志文件供下载(返回内容流与大小):
// 路径必须位于日志根内(防目录穿越),且扩展名为 .log。文件不存在 → ErrNotFound。
func GetLogFile(ctx context.Context, arg GetLogFileArg) (io.ReadCloser, *LogFileInfo, error) {
	root := logRoot()
	if root == "" {
		return nil, nil, ErrNotFound
	}
	root = filepath.Clean(root)

	// 安全:拒绝绝对路径(Unix 斜杠开头 / Windows 盘符)与 .. 段,且必须为 .log 文件
	raw := filepath.ToSlash(arg.Path)
	if raw == "" || strings.HasPrefix(raw, "/") {
		return nil, nil, ErrInvalidInput
	}
	for _, seg := range strings.Split(raw, "/") {
		if seg == ".." {
			return nil, nil, ErrInvalidInput
		}
	}
	clean := filepath.Clean(arg.Path)
	if clean == "." || filepath.IsAbs(clean) || strings.Contains(clean, "..") {
		return nil, nil, ErrInvalidInput
	}
	if strings.ToLower(filepath.Ext(clean)) != ".log" {
		return nil, nil, ErrInvalidInput
	}
	full := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, nil, ErrInvalidInput // 穿越根目录
	}

	f, err := os.Open(full)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, fmt.Errorf("open log file: %w", err)
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, fmt.Errorf("stat log file: %w", err)
	}
	if st.IsDir() {
		f.Close()
		return nil, nil, ErrInvalidInput
	}
	info := &LogFileInfo{
		Dir:     filepath.Dir(rel),
		Name:    st.Name(),
		Size:    st.Size(),
		ModTime: st.ModTime(),
		Path:    rel,
	}
	log.Infof("system: download log file %q (%d bytes)", rel, st.Size())
	return f, info, nil
}
