// Package audit 实现只读审计:环境、服务、端口、配置关键项,并自动产出警告与建议。
package audit

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"deploy-tool/internal/model"
	"deploy-tool/internal/remote"
)

// ---------- 全局变量(常量) ----------

// minDiskPercent 磁盘告警阈值:根分区可用占比低于该值出运维建议。
var minDiskPercent = 20.0

// ---------- 核心函数 ----------

// Run 执行一次完整审计。
// 参数: exe remote.Execer 远程执行器; cfg *model.ToolConfig 工具配置
// 返回: *model.AuditResult 审计汇总; error 仅连接级失败
func Run(exe remote.Execer, cfg *model.ToolConfig) (*model.AuditResult, error) {
	env := Env(exe)

	// 服务与端口
	services := map[string]bool{}
	for _, s := range model.Services {
		out, code, err := exe.Exec("systemctl is-active " + s)
		services[s] = err == nil && code == 0 && strings.TrimSpace(out) == "active"
	}
	ports := map[string]bool{}
	for p, label := range model.Ports {
		out, code, err := exe.Exec("ss -tln")
		_ = out
		ports[fmt.Sprintf("%d(%s)", p, label)] = err == nil && code == 0 && strings.Contains(out, ":"+strconv.Itoa(p)+" ")
	}

	// 部署状态与配置
	deployed := false
	if out, code, _ := exe.Exec("test -d " + cfg.DeployedPath); code == 0 {
		_ = out
		deployed = true
	}
	var oc, gw map[string]any
	if deployed {
		oc, _, _ = FetchYaml(exe, cfg.DeployedPath+"/config/config.yaml")
		gw, _, _ = FetchYaml(exe, cfg.DeployedPath+"/smb-gateway/config.yaml")
	}

	// 配置关键项(脱敏)
	items := configItems(oc, gw, deployed)

	// 警告与建议
	warnings := detectWarnings(oc, gw, deployed, services)
	tips := detectTips(oc, gw, env, deployed)

	return &model.AuditResult{
		Env:      env,
		Services: services,
		Ports:    ports,
		Items:    items,
		Warnings: warnings,
		Tips:     tips,
		Deployed: deployed,
	}, nil
}

// Env 采集环境信息。
// 参数: exe remote.Execer
// 返回: model.EnvInfo
func Env(exe remote.Execer) model.EnvInfo {
	e := model.EnvInfo{}
	if out, code, _ := exe.Exec("hostname"); code == 0 {
		e.Hostname = strings.TrimSpace(out)
	}
	if out, code, _ := exe.Exec("uptime -p"); code == 0 {
		e.Uptime = strings.TrimSpace(out)
	}
	if out, code, _ := exe.Exec("free -h"); code == 0 {
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, "Mem:") {
				f := strings.Fields(line)
				if len(f) >= 3 {
					e.Mem = f[1] + " / " + f[2]
				}
			}
		}
	}
	if out, code, _ := exe.Exec("df -h /"); code == 0 {
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if len(lines) >= 2 {
			f := strings.Fields(lines[len(lines)-1])
			if len(f) >= 5 {
				e.Disk = f[2] + " 已用 / " + f[3] + " 可用 / " + f[4] + " 已用%"
			}
		}
	}
	e.GoVersion = versionOf(exe, "go version", "go")
	e.RustVersion = versionOf(exe, "rustc --version", "rustc")
	e.NodeVersion = versionOf(exe, "node -v", "node")
	if out, code, _ := exe.Exec(cfgOrbitVer()); code == 0 {
		e.OrbitVersion = strings.TrimSpace(strings.Split(out, "\n")[0])
	} else {
		e.OrbitVersion = "-"
	}
	return e
}

// cfgOrbitVer 取后端版本命令(白名单内)。
func cfgOrbitVer() string { return "/opt/orbitcloud/bin/orbitcloud --version" }

// versionOf 版本探测: 命令失败/输出空 → 回退常见安装路径探测(内部)。
// 注: sshbox 远程为非交互 shell,PATH 不含用户级工具链,故用绝对路径兜底。
func versionOf(exe remote.Execer, cmd, prefix string) string {
	out, code, _ := exe.Exec(cmd)
	if code == 0 && strings.TrimSpace(out) != "" {
		line := strings.TrimSpace(strings.Split(out, "\n")[0])
		return strings.TrimPrefix(line, prefix+" ")
	}
	// 回退: 常见安装路径探测(只读 ls -d,白名单内)
	probe := map[string][]string{
		"go":     {"/usr/local/go", "/home/*/go-toolchain/go"},
		"rustc":  {"/home/*/rust-toolchain/installed", "/usr/local/rustup"},
		"node":   {"/opt/node-v24.19.0-linux-x64", "/usr/local/bin/node", "/usr/local/lib/nodejs"},
		"orbit":  {"/opt/orbitcloud/bin"},
	}
	for _, p := range probe[prefix] {
		if o, c, _ := exe.Exec("ls -d " + p); c == 0 && strings.TrimSpace(o) != "" {
			return "已安装(" + strings.TrimSpace(o) + ")"
		}
	}
	return "未安装"
}

// FetchYaml 远端 yaml 解析为 map(内部,audit/precheck 共用)。
// 参数: exe remote.Execer; path 远端配置路径
// 返回: data map[string]any 结构化配置; ok bool 文件存在; err error 解析失败
func FetchYaml(exe remote.Execer, path string) (map[string]any, bool, error) {
	script := "import yaml,json,sys;print(json.dumps(yaml.safe_load(open(sys.argv[1]))))"
	cmd := "python3 -c '" + script + "' " + path
	out, code, err := exe.ExecRaw(cmd)
	if err != nil {
		return nil, false, err
	}
	if code != 0 || strings.TrimSpace(out) == "" {
		return nil, false, nil // 文件不存在或 yaml 缺失
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		return nil, true, fmt.Errorf("yaml 解析失败(%s): %v", path, err)
	}
	return m, true, nil
}

// configItems 抽取配置关键项(内部,敏感值只显示状态)。
func configItems(oc, gw map[string]any, deployed bool) []model.ConfigItem {
	var items []model.ConfigItem
	if !deployed {
		items = append(items, model.ConfigItem{Name: "部署状态", Value: "未部署", OK: true})
		return items
	}

	// 后端
	items = append(items, model.ConfigItem{Name: "部署状态", Value: "已部署", OK: true})

	jwt := str(oc, "jwt", "secret")
	if jwt == "" {
		items = append(items, model.ConfigItem{Name: "后端 jwt.secret", Value: "未注入", OK: false})
	} else if strings.Contains(jwt, "please-change") || len(jwt) < 32 {
		items = append(items, model.ConfigItem{Name: "后端 jwt.secret", Value: "默认/过短(需改)", OK: false})
	} else {
		items = append(items, model.ConfigItem{Name: "后端 jwt.secret", Value: "已注入", OK: true})
	}

	ocKey := str(oc, "smb_gateway", "shared_key")
	gwKey := str(gw, "gateway", "shared_key")
	items = append(items, model.ConfigItem{Name: "后端 smb_gateway.shared_key", Value: keyStatus(ocKey), OK: ocKey != ""})
	items = append(items, model.ConfigItem{Name: "网关 shared_key", Value: keyStatus(gwKey), OK: gwKey != ""})
	if ocKey != "" && gwKey != "" {
		items = append(items, model.ConfigItem{Name: "两侧 shared_key 一致", Value: fmt.Sprintf("%v", ocKey == gwKey), OK: ocKey == gwKey})
	} else {
		items = append(items, model.ConfigItem{Name: "两侧 shared_key 一致", Value: "未注入", OK: false})
	}

	driver := str(oc, "storage", "driver")
	if driver == "" {
		driver = "(未读取)"
	}
	items = append(items, model.ConfigItem{Name: "storage.driver", Value: driver, OK: driver == "s3" || driver == "local"})

	provider := str(oc, "storage", "provider")
	endpoint := str(oc, "storage", "endpoint")
	items = append(items, model.ConfigItem{Name: "storage.provider", Value: provider, OK: true})
	items = append(items, model.ConfigItem{Name: "storage.endpoint", Value: endpoint, OK: endpoint != ""})

	ocLevel := str(oc, "log", "level")
	gwLevel := str(gw, "log", "level")
	items = append(items, model.ConfigItem{Name: "后端 log.level", Value: ocLevel, OK: ocLevel != "debug"})
	items = append(items, model.ConfigItem{Name: "网关 log.level", Value: gwLevel, OK: gwLevel != "debug"})

	pprof := str(oc, "server", "pprof_enabled")
	items = append(items, model.ConfigItem{Name: "server.pprof_enabled", Value: pprof, OK: pprof != "true"})

	return items
}

// keyStatus 密钥状态词(内部,不回显明文)。
func keyStatus(k string) string {
	if k == "" {
		return "未注入"
	}
	return fmt.Sprintf("已注入(长度 %d)", len(k))
}

// str 从嵌套 map 取字符串(内部工具)。
func str(m map[string]any, keys ...string) string {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = mm[k]
	}
	if s, ok := cur.(string); ok {
		return strings.TrimSpace(s)
	}
	if b, ok := cur.(bool); ok {
		return strconv.FormatBool(b)
	}
	return ""
}

// checkOK 命令成功判定(内部,未使用暂保留)。
func checkOK(out string, code int) error {
	_ = out
	if code != 0 {
		return fmt.Errorf("code %d", code)
	}
	return nil
}

// detectWarnings 冲突与非健康设置检测(内部)。
func detectWarnings(oc, gw map[string]any, deployed bool, services map[string]bool) []string {
	var w []string
	if !deployed {
		w = append(w, "服务器尚未部署(/opt/orbitcloud 不存在),可到脚本页生成部署脚本")
		return w
	}

	ocKey, gwKey := str(oc, "smb_gateway", "shared_key"), str(gw, "gateway", "shared_key")
	if ocKey != "" && gwKey != "" && ocKey != gwKey {
		w = append(w, "两侧 shared_key 不一致,SMB 握手将失败")
	}

	driver := str(oc, "storage", "driver")
	if driver != "" && driver != "s3" && driver != "local" {
		w = append(w, "storage.driver 非法: "+driver)
	}
	endpoint := str(oc, "storage", "endpoint")
	if driver == "s3" && endpoint == "" {
		w = append(w, "storage.driver=s3 但未配置 storage.endpoint")
	}

	for name, ok := range services {
		if !ok {
			w = append(w, "服务 "+name+" 未运行(inactive)")
		}
	}
	return w
}

// detectTips 建议清单(内部)。
func detectTips(oc, gw map[string]any, env model.EnvInfo, deployed bool) []model.Suggestion {
	var tips []model.Suggestion
	if deployed {
		jwt := str(oc, "jwt", "secret")
		if strings.Contains(jwt, "please-change") || (jwt != "" && len(jwt) < 32) {
			tips = append(tips, model.Suggestion{Kind: "security", Text: "jwt.secret 为默认/过短,建议随机化并经 ORBITCLOUD_JWT_SECRET 环境变量注入"})
		}
		if str(oc, "log", "level") == "debug" || str(gw, "log", "level") == "debug" {
			tips = append(tips, model.Suggestion{Kind: "config", Text: "日志级别为 debug,生产环境建议改 info 降低日志量"})
		}
		if str(oc, "server", "pprof_enabled") == "true" {
			tips = append(tips, model.Suggestion{Kind: "security", Text: "server.pprof_enabled=true,建议仅排障时按需开启"})
		}
	} else {
		tips = append(tips, model.Suggestion{Kind: "ops", Text: "服务器尚未部署,可直接到脚本页生成部署脚本"})
	}
	if env.GoVersion == "未安装" {
		tips = append(tips, model.Suggestion{Kind: "ops", Text: "服务器未安装 Go,后端构建将失败(部署脚本会提示)"})
	}
	return tips
}
