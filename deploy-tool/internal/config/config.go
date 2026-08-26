// Package config 负责工具自有配置(config.json)的读写。
// 约定: 配置只存在于 exe 同目录 config.json;写入采用临时文件+原子替换。
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"deploy-tool/internal/model"
)

// cfgPathFn 返回 config.json 的绝对路径(函数变量,便于单测替换)。
var cfgPathFn = func() string {
	exe, err := os.Executable()
	if err != nil {
		return "config.json"
	}
	return filepath.Join(filepath.Dir(exe), "config.json")
}

// cfgPath 工具配置路径(包内统一入口)。
var cfgPath = cfgPathFn()

// SetConfigPath 覆盖配置路径(单测用)。
func SetConfigPath(p string) { cfgPath = p }

// Load 读取工具配置;文件不存在或解析失败时返回默认值并回写。
// 参数: 无
// 返回: *model.ToolConfig 有效配置(必有值,不返回 nil)
func Load() *model.ToolConfig {
	def := defaults()

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			_ = Save(def)
			return def
		}
		// 读取失败(权限等): 改名备份后重建
		_ = os.Rename(cfgPath, cfgPath+".bak")
		_ = Save(def)
		return def
	}

	var c model.ToolConfig
	if err := json.Unmarshal(data, &c); err != nil {
		_ = os.Rename(cfgPath, cfgPath+".bak")
		_ = Save(def)
		return def
	}

	// 缺省字段回填
	if c.SSHBoxPath == "" {
		c.SSHBoxPath = def.SSHBoxPath
	}
	if c.SSHBoxJSON == "" {
		c.SSHBoxJSON = def.SSHBoxJSON
	}
	if c.RepoPath == "" {
		c.RepoPath = def.RepoPath
	}
	if c.DeployedPath == "" {
		c.DeployedPath = def.DeployedPath
	}
	if c.Port == 0 {
		c.Port = 22
	}
	return &c
}

// defaults 构造默认配置(内部)。
// 实现: 默认使用原生 SSH 通道(独立工具,不依赖外部 sshbox.exe);
//       SSHBoxPath/SSHBoxJSON 留空,如需 sshbox.exe 兼容模式在设置页手动填写。
func defaults() *model.ToolConfig {
	return &model.ToolConfig{
		Port:         22,
		RepoPath:     "~/桌面/OrbitCloud",
		DeployedPath: "/opt/orbitcloud",
		LastGen: model.GenOpts{
			Target:      "all",
			IncludeSudo: true,
			LogLevel:    "info",
		},
	}
}

// Save 原子写回配置。
// 参数: cfg *model.ToolConfig 要持久化的配置
// 返回: error 写盘失败原因
func Save(cfg *model.ToolConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(cfgPath); dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
	tmp := cfgPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, cfgPath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// AppendHistory 追加一条历史并裁剪到上限 50 条。
// 参数: cfg *model.ToolConfig 会被就地修改并落盘
// 返回: error 写盘失败原因
func AppendHistory(cfg *model.ToolConfig, h model.History) error {
	if h.Time == "" {
		h.Time = time.Now().Format(time.RFC3339)
	}
	cfg.History = append(cfg.History, h)
	if len(cfg.History) > 50 {
		cfg.History = cfg.History[len(cfg.History)-50:]
	}
	return Save(cfg)
}
