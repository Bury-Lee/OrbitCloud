# OrbitCloud 部署运维工具(单窗口桌面版)

**定位:一个窗口搞定"审计 → 预检 → 生成部署脚本"**。
工具运行在 Windows,通过 SSH 连接目标服务器做**只读**审计与检查,
部署动作一律**生成 `.sh` 脚本**交给用户审阅后自行执行,全程透明、可追溯。

---

## 为什么改版

| 旧版(Flask 网页) | 新版(单窗口桌面) |
|---|---|
| 依赖服务器端 Python + Flask,常驻进程 | 双击即用,无服务端依赖、无常驻 |
| 网页远程触发部署(黑盒、越权风险) | 只生成脚本,执行权完全在用户手里 |
| 需浏览器打开、端口暴露 | 原生单窗口,数据只在本地 |
| 正则解析配置 | yaml 真解析(PyYAML / serde_yaml) |

## 技术选型

| 项 | 选择 | 理由 |
|---|---|---|
| 壳 | Wails v2(Go) | 单窗口 + 系统 WebView2(Win10/11 自带),Go 编译秒级、`wails build` 直接出 exe(~10MB);与项目主语言统一 |
| 前端 UI | React + TS(Vite) | 审计表格、脚本预览、复制按钮等界面用 Web 技术最顺手;与 Bruno(Electron+React)同思路 |
| 后端逻辑 | Go(标准库 + Wails 绑定) | 调 sshbox 子进程、yaml 解析、脚本拼装全部 Go 写,可与 orbitcloud 共享类型 |
| SSH | 复用仓库 `tool/sshbox.exe` 子进程 | 现有资产、配置互通(`sshbox.json`),免引入新依赖 |
| 配置解析 | 远端 `python3 -c yaml.safe_load` + 本地 `gopkg.in/yaml.v3` | yaml 真解析,不再正则猜 |
| 持久化 | `deploy-tool/config.json`(exe 同目录) | 工具自有配置:服务器信息、路径、历史、待办 |

## 功能模块

| 模块 | 说明 |
|---|---|
| 审计(只读) | 环境(主机/内存/磁盘/工具链版本)、服务与端口状态、两侧配置关键项(shared_key 一致性、jwt.secret、log.level、storage.driver、桶配额);自动产出**警告**(冲突/非健康)与**建议**(加固/优化/运维) |
| 预检(只警告不阻断) | 磁盘空间、go/rust 工具链版本、目标目录可写、服务健康 → PASS / WARN 两档,内嵌到生成脚本开头 |
| 脚本生成 | 按目标(完整 / 仅后端 / 仅 SMB / 仅前端 / 重启 / 回滚)生成 `.sh`:预检段 + 步骤(内嵌引用仓库 `deploy/*.sh`)+ 部署后健康检查;窗口内预览、一键复制、保存到本地 |
| 建议与待办 | 审计建议清单可勾选存为待办(写入 `config.json`,同步 `tem/TODO.md` 跟踪) |
| 工具配置 | 服务器地址/凭据(复用 `tool/sshbox.json`)、远端仓库路径、历史记录,存 `config.json` |

## 使用方式

```
1. 双击 OrbitCloudDeployTool.exe 打开一个窗口
2. (可选)设置页确认目标服务器与路径
3. 点「审计」→ 查看环境/服务/配置/警告/建议
4. 点「预检」→ 查看 PASS/WARN 明细
5. 脚本页选目标 → 点「生成」→ 审阅脚本内容
6. 「复制执行命令」→ 到服务器上执行(sudo bash xxx.sh)
```

工具**永不执行**部署命令;需要 root 的操作全部发生在用户自己执行的脚本内。

## 安全边界

- 远程仅执行只读命令(`systemctl is-active` / `ss` / `df` / yaml 读取);不写服务器任何文件
- 密钥类审计项只显示"已注入 / 未注入 / 两侧一致",**不回显明文**
- 凭据不新增存储:复用 `tool/sshbox.json`(与 sshbox 一致)
- 生成的脚本保留在用户指定位置,执行全程可追溯

## 目录结构

```
deploy-tool/
├── go.mod                       # module deploy-tool
├── main.go                      # 入口:创建窗口,启动 Wails
├── app.go                       # App 绑定:前端 invoke 的唯一入口(审计/预检/生成/配置)
├── wails.json                   # Wails 构建配置
├── internal/
│   ├── model/types.go           # 共享数据结构(审计结果/预检项/生成选项等)
│   ├── config/config.go         # 工具配置结构与 config.json 读写
│   ├── remote/sshbox.go         # 远程执行层(sshbox.exe 子进程封装,只读)
│   ├── audit/audit.go           # 审计:环境/服务/端口/配置/警告/建议
│   ├── precheck/precheck.go     # 预检:PASS/WARN
│   └── genscript/genscript.go   # 部署脚本生成(内嵌 deploy/*.sh)
├── frontend/                    # Wails 前端(React + TS)
│   ├── package.json / vite.config.ts / index.html
│   └── src/
│       ├── App.tsx              # 主窗口 Tab 布局
│       ├── api/remote.ts        # window.go 绑定调用封装
│       └── pages/  Audit.tsx  Precheck.tsx  Script.tsx  Settings.tsx
└── config.json                  # 工具自有配置(运行时自动生成)
```

## 构建

```bash
# 需要: Go 1.25+(本机 1.26.4)、Node 22+(本机 v22.19.0)、WebView2(Win10/11 自带)
cd deploy-tool
wails init 结构就绪后:
go mod tidy
wails build            # 产出 build/bin/OrbitCloudDeployTool.exe
# 开发模式: wails dev(窗口热更新)
```

## 现状

- 本 README 为改版目标;伪代码见 `deploy-tool/pseudocode.md`,评审通过后开始实现
- 旧 Flask 版(`app.py` / `templates/`)在实现完成后移除
