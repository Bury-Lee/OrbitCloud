# OrbitCloud 部署运维工具 · 骨架索引(Wails v2 + Go)

> 伪代码已按标准项目文件结构**落地为真实文件**(每个文件即最终代码骨架:
> 结构体/全局变量/函数签名 + 详细逻辑注释,实现待评审后补全)。
> 本文件仅作索引与评审入口,具体内容以各源码文件为准。

## 文件树

```
deploy-tool/
├── go.mod                          # module deploy-tool(依赖注释,M1 时 go mod tidy)
├── wails.json                      # Wails 构建配置(单窗口/产品名)
├── main.go                         # 入口: 装配 → wails.Run(逻辑注释见文件)
├── app.go                          # App 绑定对象: 前端唯一调用面(10 个方法签名+逻辑注释)
├── internal/
│   ├── model/types.go              # 共享数据结构: Services/Ports 常量表、ToolConfig、
│   │                               #   History、GenOpts、AuditResult、EnvInfo、ConfigItem、
│   │                               #   Suggestion、CheckItem、GenScriptResult(逐字段注释)
│   ├── config/config.go            # config.json 读写: Load/Save/AppendHistory + cfgPath
│   ├── remote/sshbox.go            # 远程执行层: Execer 接口、SshboxExecer、
│   │                               #   白名单/超时/脱敏常量、Sanitize
│   ├── audit/audit.go              # 审计: Run/Env/FetchYaml/detectWarnings/detectTips
│   ├── precheck/precheck.go        # 预检: Run(7 项判定)/RenderBlock + 阈值常量
│   └── genscript/genscript.go      # 脚本生成: Generate/healthBlock/Save/repoRoot
│                                   #   + 内嵌模板(frontendSteps/rollbackSteps/restartSteps)
└── frontend/                       # Wails 前端(React + TS)
    ├── package.json / vite.config.ts / index.html
    └── src/
        ├── main.tsx                # React 挂载入口
        ├── App.tsx                 # 顶部状态条 + 四个 Tab
        ├── types.ts                # 与 Go JSON 对齐的 TS 类型
        ├── api/remote.ts           # window.go 调用封装(10 个方法)
        └── pages/  Audit.tsx  Precheck.tsx  Script.tsx  Settings.tsx
```

## 评审要点

| 文件 | 重点核对 |
|---|---|
| internal/model/types.go | 字段齐全性: 是否缺部署/运维所需字段(如回滚备份路径) |
| internal/remote/sshbox.go | 白名单是否够用(审计要执行的命令都覆盖);脱敏键是否完整 |
| internal/audit/audit.go | 警告/建议判定规则是否符合预期(逐条在注释中) |
| internal/precheck/precheck.go | 7 项预检是否覆盖"磁盘/工具链/目录/健康/配置/仓库" |
| internal/genscript/genscript.go | 6 个目标(含 rollback 模板)与内嵌 deploy/*.sh 的方式 |
| app.go | 前端 10 个方法是否满足四个页面需要 |
| frontend/src/pages/*.tsx | 每个页面的交互点(按钮/表单/预览)是否到位 |

## 实施顺序(M1 → M5)

| 阶段 | 内容 |
|---|---|
| M1 | wails init 模板接入(assets/embed 生成),go mod tidy,窗口能开 |
| M2 | config + remote 实现(打通 192.168.10.107 测试连接) |
| M3 | audit + precheck 实现,审计页/预检页可用 |
| M4 | genscript 实现,脚本页预览/复制/保存 |
| M5 | 历史/待办/脱敏复核,wails build 打包单 exe |
