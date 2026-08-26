// Package remote 封装对目标服务器的所有远程访问,统一走 sshbox.exe 子进程。
// 安全约定(硬约束):
//   - 只允许执行本文件 allowedPrefixes 白名单内的只读命令;
//   - 单条命令超时 60s;输出经 Sanitize 脱敏(密钥字段替换为状态词);
//   - 本层不写服务器任何文件;取文件用 sshbox pull 到本地临时目录后读回。
package remote

import (
	"encoding/json"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"deploy-tool/internal/model"
)

// ---------- 全局变量(常量) ----------

// sshTimeout 单条远程命令超时(含 ssh 连接建立时间)。
var sshTimeout = 60 * time.Second

// allowedPrefixes 只读命令白名单前缀。
var allowedPrefixes = []string{
	"systemctl is-active", "ss -tln", "df -h", "free -h", "hostname",
	"uptime -p", "uname -a", "python3 -c", "ls -d", "test -d", "test -f",
	"cat /opt/orbitcloud/", "go version", "rustc --version", "node -v",
	"/opt/orbitcloud/bin/orbitcloud --version", "find /opt/orbitcloud",
	"cd /home/", "cd ~/", "ls ~/",
}

// sensitiveKeys 审计输出中需要脱敏的配置键。
var sensitiveKeys = []string{"secret", "shared_key", "access_key", "secret_key", "password", "pass"}

// ---------- 接口 ----------

// Execer 远程命令执行接口。
type Execer interface {
	Exec(cmd string) (stdout string, code int, err error)
	// ExecRaw 同 Exec 但不做脱敏(仅供内部 yaml 结构化解析等场景;
	// 密钥不会外露: 上层只提取状态词展示)。
	ExecRaw(cmd string) (stdout string, code int, err error)
	Fetch(remotePath string) (content string, err error)
	Ping() error
}

// ---------- 生产实现 ----------

// SshboxExecer 基于 sshbox.exe 子进程的 Execer 实现。
type SshboxExecer struct {
	cfg    *model.ToolConfig
	tmpDir string
}

// NewSshboxExecer 构造执行器。
// 参数: cfg *model.ToolConfig 已补齐默认值的工具配置
// 返回: *SshboxExecer; err 校验失败
// 实现逻辑:
//   1. 校验 cfg.SSHBoxPath 存在且为文件;不存在 → 返回错误(提示在设置页指定)
//   2. os.MkdirTemp 建临时目录(pull 用)
//   3. 若 cfg.Host 为空 → 读取 sshbox.json(cfg.SSHBoxJSON 或同目录)合并
//      Host/User/Pass/Port 缺省值;文件缺失时留空(由 sshbox 自身行为兜底,不报错)
func NewSshboxExecer(cfg *model.ToolConfig) (*SshboxExecer, error) {
	if cfg == nil {
		return nil, errors.New("配置为空")
	}
	path := cfg.SSHBoxPath
	if path == "" {
		return nil, errors.New("sshbox 路径未配置: 请在设置页指定 tool/sshbox.exe")
	}
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return nil, fmt.Errorf("sshbox.exe 不存在: %s", path)
	}
	tmpDir, err := os.MkdirTemp("", "orbit-deploy-*")
	if err != nil {
		return nil, err
	}

	// 合并 sshbox.json 缺省值(Host 为空才触发,避免覆盖用户显式配置)
	if strings.TrimSpace(cfg.Host) == "" {
		mergeFromSSHBoxJSON(cfg)
	}
	return &SshboxExecer{cfg: cfg, tmpDir: tmpDir}, nil
}

// mergeFromSSHBoxJSON 从 sshbox.json 合并连接参数缺省值(内部)。
// 注: 原生 SSH 模式下 sshbox.json 仅作凭据数据源,不依赖 sshbox.exe 程序。
// 候选顺序: cfg.SSHBoxJSON → sshbox.exe 同目录 → exe 同目录 → exe 旁 tool/ 目录。
func mergeFromSSHBoxJSON(cfg *model.ToolConfig) {
	candidates := []string{cfg.SSHBoxJSON}
	if cfg.SSHBoxPath != "" {
		candidates = append(candidates, filepath.Join(filepath.Dir(cfg.SSHBoxPath), "sshbox.json"))
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, "sshbox.json"),
			filepath.Join(dir, "tool", "sshbox.json"))
	}

	var j struct {
		Host string `json:"host"`
		User string `json:"user"`
		Pass string `json:"pass"`
		Port int    `json:"port"`
	}
	found := false
	for _, p := range candidates {
		if p == "" {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if err := json.Unmarshal(data, &j); err == nil {
			found = true
			break
		}
	}
	if !found {
		return
	}
	if cfg.Host == "" {
		cfg.Host = j.Host
	}
	if cfg.User == "" {
		cfg.User = j.User
	}
	if cfg.Pass == "" {
		cfg.Pass = j.Pass
	}
	if cfg.Port == 0 {
		cfg.Port = j.Port
	}
}

// Close 释放临时目录。
func (s *SshboxExecer) Close() error {
	return os.RemoveAll(s.tmpDir)
}

// (SshboxExecer) Exec 执行命令。
// 参数: cmd 命令原文(必须命中白名单)
// 返回: stdout 合并输出(已脱敏); code 远端退出码; err 连接失败/超时/白名单拒绝
func (s *SshboxExecer) Exec(cmd string) (string, int, error) {
	out, code, err := s.execCmd(cmd)
	return Sanitize(out), code, err
}

// (SshboxExecer) ExecRaw 执行命令(不做脱敏)。
func (s *SshboxExecer) ExecRaw(cmd string) (string, int, error) {
	return s.execCmd(cmd)
}

// execCmd 白名单+超时执行(内部公共逻辑)。
func (s *SshboxExecer) execCmd(cmd string) (string, int, error) {
	// 白名单前缀校验
	ok := false
	for _, p := range allowedPrefixes {
		if strings.HasPrefix(cmd, p) {
			ok = true
			break
		}
	}
	if !ok {
		return "", -1, fmt.Errorf("命令不在只读白名单,已拒绝: %s", truncate(cmd, 60))
	}

	ctx, cancel := context.WithTimeout(context.Background(), sshTimeout)
	defer cancel()

	args := s.buildCmd(cmd)
	p := exec.CommandContext(ctx, s.cfg.SSHBoxPath, args...)
	out, err := p.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return "", -1, errors.New("远程执行超时(60s)")
	}
	if err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			return string(out), p.ProcessState.ExitCode(), nil
		}
		return "", -1, err
	}
	return string(out), 0, nil
}

// (SshboxExecer) Fetch 取回远端文件。
// 参数: remotePath 远端路径(仅允许部署目录/家目录下文件)
// 返回: content 文件文本; err 取回失败
func (s *SshboxExecer) Fetch(remotePath string) (string, error) {
	// 路径校验: 允许部署目录前缀或家目录配置类路径
	lower := strings.ToLower(remotePath)
	allow := []string{
		strings.ToLower(s.cfg.DeployedPath),
		"/home/",
	}
	ok := false
	for _, p := range allow {
		if strings.HasPrefix(lower, p) {
			ok = true
			break
		}
	}
	if !ok || strings.Contains(remotePath, "..") {
		return "", fmt.Errorf("路径越界,已拒绝: %s", truncate(remotePath, 80))
	}

	ctx, cancel := context.WithTimeout(context.Background(), sshTimeout)
	defer cancel()

	args := s.baseArgs()
	args = append(args, "pull", remotePath, s.tmpDir)
	p := exec.CommandContext(ctx, s.cfg.SSHBoxPath, args...)
	if out, err := p.CombinedOutput(); err != nil {
		return "", fmt.Errorf("pull 失败: %v (%s)", err, truncate(Sanitize(string(out)), 200))
	}

	local := filepath.Join(s.tmpDir, filepath.Base(remotePath))
	data, err := os.ReadFile(local)
	if err != nil {
		return "", err
	}
	_ = os.Remove(local)
	return string(data), nil
}

// (SshboxExecer) Ping 连通性测试。
// 返回: error
func (s *SshboxExecer) Ping() error {
	out, code, err := s.Exec("uname -a")
	if err != nil {
		return err
	}
	if code != 0 || strings.TrimSpace(out) == "" {
		return errors.New("远程执行 uname 失败")
	}
	return nil
}

// baseArgs sshbox 连接参数(内部)。
func (s *SshboxExecer) baseArgs() []string {
	args := []string{}
	if h := strings.TrimSpace(s.cfg.Host); h != "" {
		args = append(args, "-host", h)
	}
	if u := strings.TrimSpace(s.cfg.User); u != "" {
		args = append(args, "-user", u)
	}
	if p := s.cfg.Pass; p != "" {
		args = append(args, "-pass", p)
	}
	if s.cfg.Port > 0 {
		args = append(args, "-port", fmt.Sprintf("%d", s.cfg.Port))
	}
	return args
}

// buildCmd 组装 sshbox 命令行参数(内部)。
func (s *SshboxExecer) buildCmd(cmd string) []string {
	args := s.baseArgs()
	args = append(args, "run", cmd)
	return args
}

// Sanitize 敏感信息脱敏(包级,供单测直接调用)。
// 参数: text 原始输出(可能含配置明文)
// 返回: string 脱敏后文本
func Sanitize(text string) string {
	for _, key := range sensitiveKeys {
		// key: value 与 key:"value" 两种形式
		re := regexp.MustCompile(`(?i)(\b` + regexp.QuoteMeta(key) + `\b\s*[=:]\s*)(?:"[^"]*"|'[^']*'|\S+)`)
		text = re.ReplaceAllString(text, `${1}<已注入/长度隐藏>`)
	}
	return text
}

// truncate 长文本截断(内部工具函数)。
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
