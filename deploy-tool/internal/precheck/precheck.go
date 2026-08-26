// Package precheck 实现部署前预检:磁盘/工具链/目录/健康/配置可读,输出 PASS/WARN 两档。
// 约定: 只警告不阻断;结果内嵌进生成脚本开头。
package precheck

import (
	"fmt"
	"strconv"
	"strings"

	"deploy-tool/internal/audit"
	"deploy-tool/internal/model"
	"deploy-tool/internal/remote"
)

// ---------- 全局变量(常量) ----------

// minDiskGB 磁盘可用最小阈值(GB),低于出 WARN。
var minDiskGB = 5.0

// minGoVersion / minRustVersion 工具链最低版本。
var (
	minGoVersion   = "1.25"
	minRustVersion = "1.95"
)

// ---------- 核心函数 ----------

// Run 执行全部预检项。
// 参数: exe remote.Execer; cfg *model.ToolConfig
// 返回: []model.CheckItem 全部检查项(PASS/WARN); error 仅连接级失败
func Run(exe remote.Execer, cfg *model.ToolConfig) ([]model.CheckItem, error) {
	items := []model.CheckItem{}

	// 1. 磁盘
	items = append(items, checkDisk(exe))

	// 2. Go 工具链
	items = append(items, checkVersion(exe, "go version", "Go 工具链", minGoVersion, "go", "go version"))

	// 3. Rust 工具链
	items = append(items, checkVersion(exe, "rustc --version", "Rust 工具链", minRustVersion, "rustc", "rustc --version"))

	// 4. 部署目录
	if _, code, _ := exe.Exec("test -d " + cfg.DeployedPath); code == 0 {
		items = append(items, model.CheckItem{Name: "部署目录", Result: "PASS", Detail: cfg.DeployedPath + " 存在"})
	} else {
		items = append(items, model.CheckItem{Name: "部署目录", Result: "WARN", Detail: cfg.DeployedPath + " 不存在(首次部署将自动创建)"})
	}

	// 5. 服务健康
	if _, code, _ := exe.Exec("test -d " + cfg.DeployedPath); code == 0 {
		bad := []string{}
		for _, s := range model.Services {
			o, c, _ := exe.Exec("systemctl is-active " + s)
			if c == 0 && strings.TrimSpace(o) == "active" {
				continue
			}
			bad = append(bad, s)
		}
		if len(bad) == 0 {
			items = append(items, model.CheckItem{Name: "服务健康", Result: "PASS", Detail: "全部服务 active"})
		} else {
			items = append(items, model.CheckItem{Name: "服务健康", Result: "WARN", Detail: "未运行: " + strings.Join(bad, ", ")})
		}
	} else {
		items = append(items, model.CheckItem{Name: "服务健康", Result: "WARN", Detail: "未部署,跳过"})
	}

	// 6. 配置可读
	if _, code, _ := exe.Exec("test -d " + cfg.DeployedPath); code == 0 {
		_, ok1, _ := audit.FetchYaml(exe, cfg.DeployedPath+"/config/config.yaml")
		_, ok2, _ := audit.FetchYaml(exe, cfg.DeployedPath+"/smb-gateway/config.yaml")
		if ok1 && ok2 {
			items = append(items, model.CheckItem{Name: "配置可读", Result: "PASS", Detail: "两侧 config.yaml 均可解析"})
		} else {
			items = append(items, model.CheckItem{Name: "配置可读", Result: "WARN", Detail: "存在缺失/不可解析的配置(yaml 或 python3 未装会触发)"})
		}
	} else {
		items = append(items, model.CheckItem{Name: "配置可读", Result: "WARN", Detail: "未部署,跳过"})
	}

	// 7. 远端仓库
	if _, code, _ := exe.Exec("test -d " + cfg.RepoPath); code == 0 {
		items = append(items, model.CheckItem{Name: "远端仓库", Result: "PASS", Detail: cfg.RepoPath + " 存在"})
	} else {
		items = append(items, model.CheckItem{Name: "远端仓库", Result: "WARN", Detail: cfg.RepoPath + " 不存在(生成脚本将内嵌本地 deploy/*.sh,不影响)"})
	}

	return items, nil
}

// checkDisk 磁盘空间检查(内部)。
func checkDisk(exe remote.Execer) model.CheckItem {
	out, code, _ := exe.Exec("df -h /")
	if code != 0 {
		return model.CheckItem{Name: "磁盘空间", Result: "WARN", Detail: "df 执行失败"}
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return model.CheckItem{Name: "磁盘空间", Result: "WARN", Detail: "df 输出异常"}
	}
	f := strings.Fields(lines[len(lines)-1])
	if len(f) < 4 {
		return model.CheckItem{Name: "磁盘空间", Result: "WARN", Detail: "df 输出异常"}
	}
	avail := f[3] // 如 "53G"
	num, _ := strconv.ParseFloat(strings.TrimSuffix(avail, "G"), 64)
	if num >= minDiskGB {
		return model.CheckItem{Name: "磁盘空间", Result: "PASS", Detail: fmt.Sprintf("根分区可用 %s >= %dGB", avail, int(minDiskGB))}
	}
	return model.CheckItem{Name: "磁盘空间", Result: "WARN", Detail: fmt.Sprintf("根分区可用 %s < %dGB,建议清理/扩容", avail, int(minDiskGB))}
}

// checkVersion 工具链版本检查(内部)。
// 注: 远程非交互 shell 的 PATH 不含用户级工具链,命令失败时回退常见安装路径探测。
func checkVersion(exe remote.Execer, cmd, name, minVer, prefix, installHint string) model.CheckItem {
	out, code, _ := exe.Exec(cmd)
	if code == 0 && strings.TrimSpace(out) != "" {
		line := strings.TrimSpace(strings.Split(out, "\n")[0])
		ver := strings.TrimPrefix(line, prefix+" ")
		if ver >= minVer {
			return model.CheckItem{Name: name, Result: "PASS", Detail: ver + " >= " + minVer}
		}
		return model.CheckItem{Name: name, Result: "WARN", Detail: ver + " < " + minVer + "(需要 " + minVer + "+)"}
	}
	// 回退: 常见安装路径探测
	probe := map[string][]string{
		"go":    {"/usr/local/go", "/home/*/go-toolchain/go"},
		"rustc": {"/home/*/rust-toolchain/installed", "/usr/local/rustup"},
		"node":  {"/opt/node-v24.19.0-linux-x64", "/usr/local/bin/node", "/usr/local/lib/nodejs"},
	}
	for _, p := range probe[prefix] {
		if o, c, _ := exe.Exec("ls -d " + p); c == 0 && strings.TrimSpace(o) != "" {
			return model.CheckItem{Name: name, Result: "PASS", Detail: "已安装(" + strings.TrimSpace(o) + ")"}
		}
	}
	return model.CheckItem{Name: name, Result: "WARN", Detail: fmt.Sprintf("未安装(%s)", installHint)}
}

// RenderBlock 把预检结果渲染为脚本开头的预检段文本。
// 参数: items []model.CheckItem 预检结果
// 返回: string 脚本片段(注释形式,不中断执行)
func RenderBlock(items []model.CheckItem) string {
	var b strings.Builder
	b.WriteString("# ==== 部署前预检(只警告不中断) ====\n")
	for _, it := range items {
		b.WriteString(fmt.Sprintf("# [%s] %s: %s\n", it.Result, it.Name, it.Detail))
	}
	b.WriteString("# 以上警告仅提示,脚本继续执行,请自行评估风险后确认\n")
	return b.String()
}
