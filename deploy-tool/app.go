// app.go —— Wails 应用绑定对象(App)。
// 职责: 前端 window.go 的唯一调用面;每个方法都是"校验 → 调用 internal 层 → 返回"。
package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"deploy-tool/internal/audit"
	"deploy-tool/internal/config"
	"deploy-tool/internal/genscript"
	"deploy-tool/internal/model"
	"deploy-tool/internal/precheck"
	"deploy-tool/internal/remote"
)

// App Wails 绑定对象(前端唯一调用面)。
type App struct {
	// mu 串行化有状态调用(审计/预检/生成),避免并发操作同一份配置快照。
	mu sync.Mutex
	// cfg 当前工具配置内存快照;每次操作前 config.Load 刷新,操作后 config.Save 落盘。
	cfg *model.ToolConfig
	// exe 远程执行器(生产 = *remote.NativeExecer,默认原生 SSH;设置页保存后重建)。
	exe remote.Execer
	// ctx Wails 上下文(OnStartup 注入)。
	ctx context.Context
}

// NewApp 构造 App。
// 参数: cfg *model.ToolConfig 已补齐默认值的工具配置; exe remote.Execer 远程执行器
// 返回: *App
func NewApp(cfg *model.ToolConfig, exe remote.Execer) *App {
	return &App{cfg: cfg, exe: exe}
}

// startup Wails 启动回调,保存上下文。
// 参数: ctx context.Context Wails 提供的应用上下文
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

// refresh 刷新配置快照与执行器(内部)。
// 实现: cfg = config.Load();若 exe 为 nil 尝试重建。
func (a *App) refresh() {
	a.cfg = config.Load()
	if a.exe == nil {
		if exe, err := newExecer(a.cfg); err == nil {
			a.exe = exe
		}
	}
}

// newExecer 按配置构造执行器(内部)。
// 实现: SSHBoxPath 非空 → sshbox.exe 兼容模式;否则原生 SSH(默认,独立自包含)。
func newExecer(cfg *model.ToolConfig) (remote.Execer, error) {
	if strings.TrimSpace(cfg.SSHBoxPath) != "" {
		return remote.NewSshboxExecer(cfg)
	}
	return remote.NewNativeExecer(cfg)
}

// LoadConfig 返回当前工具配置(前端设置页初始化回填)。
// 参数: 无
// 返回: *model.ToolConfig 配置副本(Pass 脱敏: 已存在密码时返回空串,避免明文进前端)
func (a *App) LoadConfig() *model.ToolConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.refresh()
	c := *a.cfg
	if c.Pass != "" {
		c.Pass = "" // 脱敏: 不回显已保存密码
	}
	return &c
}

// SaveConfig 保存设置页提交的配置。
// 参数: c *model.ToolConfig 前端提交(Pass 为空串 = 不修改现有密码)
// 返回: error 校验失败或写盘失败
func (a *App) SaveConfig(c *model.ToolConfig) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.refresh()

	// 校验
	if strings.TrimSpace(c.Host) == "" {
		return errors.New("服务器地址(Host)不能为空")
	}
	if c.Port < 0 || c.Port > 65535 {
		return errors.New("端口必须在 0~65535 之间(0 = 读 sshbox.json)")
	}

	// 密码: 空串 = 沿用旧值
	if c.Pass == "" {
		c.Pass = a.cfg.Pass
	}
	// 缺省路径回填
	if c.RepoPath == "" {
		c.RepoPath = a.cfg.RepoPath
	}
	if c.DeployedPath == "" {
		c.DeployedPath = a.cfg.DeployedPath
	}
	if c.SSHBoxPath == "" {
		c.SSHBoxPath = a.cfg.SSHBoxPath
	}
	if c.SSHBoxJSON == "" {
		c.SSHBoxJSON = a.cfg.SSHBoxJSON
	}

	if err := config.Save(c); err != nil {
		return err
	}
	a.cfg = c
	// 重建执行器(连接参数可能已变化)
	exe, err := newExecer(c)
	if err != nil {
		return fmt.Errorf("配置已保存,但执行器初始化失败: %v", err)
	}
	a.exe = exe
	_ = config.AppendHistory(a.cfg, model.History{
		Time: time.Now().Format(time.RFC3339), Action: "save-config", Target: "server", Summary: "配置已保存",
	})
	return nil
}

// TestConn 测试与目标服务器连通性。
// 参数: c *model.ToolConfig 可为草稿(未保存也允许测试)
// 返回: string 成功 = "连接成功: <hostname 输出>"; 失败 = 人类可读错误描述
func (a *App) TestConn(c *model.ToolConfig) (string, error) {
	if c == nil {
		a.mu.Lock()
		a.refresh()
		c = a.cfg
		a.mu.Unlock()
	}
	exe, err := newExecer(c)
	if err != nil {
		return "", err
	}
	out, code, err := exe.Exec("hostname")
	if err != nil || code != 0 {
		return "", fmt.Errorf("连接失败: %v", err)
	}
	return "连接成功: " + strings.TrimSpace(out), nil
}

// RunAudit 执行一次完整审计(审计页"开始审计")。
// 参数: 无
// 返回: *model.AuditResult 审计汇总; error 仅连接级失败
func (a *App) RunAudit() (*model.AuditResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.refresh()
	if a.exe == nil {
		return nil, errors.New("远程执行器未就绪,请先在设置页确认服务器信息并测试连接")
	}
	res, err := audit.Run(a.exe, a.cfg)
	if err != nil {
		return nil, err
	}
	_ = config.AppendHistory(a.cfg, model.History{
		Time: time.Now().Format(time.RFC3339), Action: "audit", Target: "server",
		Summary: fmt.Sprintf("%d 警告, %d 建议", len(res.Warnings), len(res.Tips)),
	})
	return res, nil
}

// RunPrecheck 执行部署前预检(LSP 式实时预检)。
// 参数: c *model.ToolConfig 草稿配置 —— 前端每次修改表单/选项时传入(未保存也允许);
//       传 nil 或全空配置 = 使用已保存配置(对应"重新预检"按钮)
// 返回: []model.CheckItem PASS/WARN 明细; error 连接级失败(前端在预检条展示)
func (a *App) RunPrecheck(c *model.ToolConfig) ([]model.CheckItem, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.refresh()

	draft := a.cfg
	if c != nil && (c.SSHBoxPath != "" || c.Host != "" || c.User != "") {
		// 草稿: 补齐缺省字段
		d := *c
		if d.RepoPath == "" {
			d.RepoPath = a.cfg.RepoPath
		}
		if d.DeployedPath == "" {
			d.DeployedPath = a.cfg.DeployedPath
		}
		draft = &d
	}

	exe, err := newExecer(draft)
	if err != nil {
		return nil, err
	}
	items, err := precheck.Run(exe, draft)
	if err != nil {
		return nil, err
	}
	return items, nil
}

// GetScriptForm 返回脚本页表单定义(分组/控件/默认值 + 上次填值)。
// 参数: 无
// 返回: *model.ScriptForm 前端据此渲染动态表单
// 实现逻辑: a.refresh();return genscript.BuildForm(a.cfg.LastGen)
func (a *App) GetScriptForm() *model.ScriptForm {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.refresh()
	return genscript.BuildForm(a.cfg.LastGen)
}

// ValidateScript 校验脚本配置(前端表单修改即调用,LSP 式实时提示)。
// 参数: sc *model.ScriptConfig 当前表单值
// 返回: *model.ValidateResult error(阻断)/warn(提示)列表
// 实现逻辑: 直接转发 genscript.Validate(sc),不落盘、不写历史。
func (a *App) ValidateScript(sc *model.ScriptConfig) *model.ValidateResult {
	return genscript.Validate(sc)
}

// GenScript 生成确定值部署脚本(脚本页"生成",纯文本拼装,不执行)。
// 参数: sc *model.ScriptConfig 表单配置(含全部确定值)
// 返回: model.GenScriptResult(脚本全文/行数/建议执行命令); error 校验失败(error 级)
func (a *App) GenScript(sc *model.ScriptConfig) (model.GenScriptResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.refresh()

	if a.exe == nil {
		return model.GenScriptResult{}, errors.New("远程执行器未就绪,请先到设置页测试连接")
	}
	// 实时预检结果内嵌进脚本
	checks, err := precheck.Run(a.exe, a.cfg)
	if err != nil {
		checks = nil // 预检失败不阻断生成(只警告)
	}
	result, err := genscript.Generate(a.cfg, sc, checks)
	if err != nil {
		return model.GenScriptResult{}, err
	}
	// 仅持久化非敏感选项(密钥不落盘)
	a.cfg.LastGen = model.GenOpts{
		Target:      sc.Target,
		IncludeSudo: sc.IncludeSudo,
		LogLevel:    sc.LogLevel,
		SuperAdmin:  sc.SuperAdmin,
	}
	_ = config.Save(a.cfg)
	_ = config.AppendHistory(a.cfg, model.History{
		Time: time.Now().Format(time.RFC3339), Action: "gen-script", Target: sc.Target,
		Summary: fmt.Sprintf("%d 行(确定值)", result.Lines),
	})
	return result, nil
}

// SaveScript 把脚本保存到本机文件。
// 参数: content 预览中的脚本全文; dir 目标目录(空 = 桌面)
// 返回: string 实际保存绝对路径; error 保存失败
func (a *App) SaveScript(content, dir string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.refresh()
	path, err := genscript.Save(a.cfg, content, dir)
	if err != nil {
		return "", err
	}
	_ = config.AppendHistory(a.cfg, model.History{
		Time: time.Now().Format(time.RFC3339), Action: "save-script", Target: a.cfg.LastGen.Target,
		Summary: path,
	})
	return path, nil
}

// GetHistory 返回操作历史(设置页表格,倒序)。
// 参数: 无
// 返回: []model.History 最近 50 条,新在前
func (a *App) GetHistory() []model.History {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.refresh()
	out := make([]model.History, len(a.cfg.History))
	copy(out, a.cfg.History)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time > out[j].Time })
	return out
}

// ToggleTodo 勾选/取消一条审计建议为待办。
// 参数: text 建议文本; on true=加入 / false=移除
// 返回: error 写盘失败
func (a *App) ToggleTodo(text string, on bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.refresh()
	if on {
		for _, t := range a.cfg.Todos {
			if t == text {
				return nil // 已存在
			}
		}
		a.cfg.Todos = append(a.cfg.Todos, text)
	} else {
		out := a.cfg.Todos[:0]
		for _, t := range a.cfg.Todos {
			if t != text {
				out = append(out, t)
			}
		}
		a.cfg.Todos = out
	}
	return config.Save(a.cfg)
}

// ExportTodos 导出待办为 Markdown 文本(供粘贴进 tem/TODO.md)。
// 参数: 无
// 返回: string 形如 "- [ ] <待办1>\n- [ ] <待办2>"
func (a *App) ExportTodos() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.refresh()
	var b strings.Builder
	for _, t := range a.cfg.Todos {
		b.WriteString("- [ ] ")
		b.WriteString(t)
		b.WriteString("\n")
	}
	return b.String()
}
