// Package model 定义工具内部共享的数据结构。
// 约定: 各层(remote/audit/precheck/genscript/app)只通过本包类型交互,避免包间耦合与字段名漂移;
//       所有结构体直接序列化为 JSON 供前端渲染(字段名即前端类型名)。
package model

// ---------- 常量表(全局) ----------

// Services 审计的服务名列表。
// 作用: 统一驱动"服务状态"审计与生成脚本中的"部署后健康检查"两处循环。
var Services = []string{"orbitcloud", "smb-gateway", "rustfs", "nginx"}

// Ports 审计的端口列表,值是端口用途说明(界面展示用)。
// 作用: 统一驱动"端口监听"审计与生成脚本中的健康检查段落。
var Ports = map[int]string{
	8080: "后端 API",
	445:  "SMB",
	9000: "对象存储",
	9101: "网关内部",
	80:   "前端",
}

// ---------- 工具配置 ----------

// ToolConfig 工具自有配置(config.json 内容)。
// 作用: 持久化用户的连接参数、路径偏好、操作历史与待办,保证工具"记住上次状态"。
type ToolConfig struct {
	// SSHBoxPath sshbox.exe 绝对路径;空 = 自动探测(工具旁 tool/sshbox.exe)。
	SSHBoxPath string `json:"sshbox_path"`
	// SSHBoxJSON sshbox.json 路径;空 = 与 sshbox.exe 同目录。
	SSHBoxJSON string `json:"sshbox_json"`
	// Host 目标服务器地址;空 = 读 sshbox.json。
	Host string `json:"host"`
	// User SSH 用户名;空 = 读 sshbox.json。
	User string `json:"user"`
	// Pass SSH 密码;空 = 读 sshbox.json。注: 本字段不进界面回显、不进历史摘要。
	Pass string `json:"pass"`
	// Port SSH 端口;0 = 读 sshbox.json(默认 22)。
	Port int `json:"port"`
	// RepoPath 远端仓库目录(默认 ~/桌面/OrbitCloud),预检与生成脚本时引用。
	RepoPath string `json:"repo_path"`
	// DeployedPath 远端部署目录(默认 /opt/orbitcloud),审计/预检/脚本共用。
	DeployedPath string `json:"deployed_path"`
	// LastGen 上次脚本生成选项(脚本页回填,减少重复选择)。
	LastGen GenOpts `json:"last_gen"`
	// History 操作历史(上限 50 条,新追加在尾部)。
	History []History `json:"history"`
	// Todos 审计建议勾选的待办项(纯文本,可导出到 tem/TODO.md)。
	Todos []string `json:"todos"`
}

// History 一条操作历史记录。
// 作用: 供设置页表格展示与追溯(何时对哪个目标做了什么)。
type History struct {
	// Time 操作发生时间(RFC3339 字符串,Wails 绑定不支持 time.Time 故用 string)。
	Time string `json:"time"`
	// Action 动作: audit / check / gen-script / save-script / save-config / test-conn。
	Action string `json:"action"`
	// Target 目标: 全部/后端/SMB/前端/重启/回滚,或 "server"。
	Target string `json:"target"`
	// Summary 结果摘要(如 "3 警告 2 建议" / "脚本已生成 46 行")。
	Summary string `json:"summary"`
}

// GenOpts 脚本生成的选项。
// 作用: 由脚本页勾选后传入 genscript,并回填到 config.json 的 LastGen。
type GenOpts struct {
	// Target 生成目标: all|backend|smb|frontend|restart|rollback。
	Target string `json:"target"`
	// IncludeSudo 生成的脚本是否使用 sudo 执行(默认 true;root 用户时可关)。
	IncludeSudo bool `json:"include_sudo"`
	// LogLevel 生成脚本内提示的日志级别(debug|info,默认 info)。
	LogLevel string `json:"log_level"`
	// SuperAdmin 可选: 部署后要创建/确认的超级管理员用户名(空 = 跳过)。
	SuperAdmin string `json:"superadmin"`
	// SuperPass 可选: 对应密码(仅注入生成脚本,不进 config.json)。
	SuperPass string `json:"superpass"`
}

// ---------- 审计 ----------

// AuditResult 一次完整审计的汇总结果(JSON 序列化后直接驱动前端渲染)。
type AuditResult struct {
	// Env 环境信息(主机/内存/磁盘/版本)。
	Env EnvInfo `json:"env"`
	// Services 服务名 → 是否 active(统一 bool,便于前端点灯)。
	Services map[string]bool `json:"services"`
	// Ports "端口(用途)" → 是否监听。
	Ports map[string]bool `json:"ports"`
	// Items 配置关键项列表(已脱敏)。
	Items []ConfigItem `json:"items"`
	// Warnings 冲突/非健康警告(前端红色展示,只提示不阻断)。
	Warnings []string `json:"warnings"`
	// Tips 建议清单(前端黄色展示,可勾选为待办)。
	Tips []Suggestion `json:"tips"`
	// Deployed 是否已部署(DeployedPath 存在),供脚本页做首次部署提示。
	Deployed bool `json:"deployed"`
}

// EnvInfo 服务器环境信息。
// 作用: 审计页"环境卡片"渲染数据源。
type EnvInfo struct {
	// Hostname hostname 命令输出。
	Hostname string `json:"hostname"`
	// Uptime uptime -p 输出(如 "up 2 days")。
	Uptime string `json:"uptime"`
	// Mem 内存 已用/总量(free -h)。
	Mem string `json:"mem"`
	// Disk 根分区磁盘 使用/容量/百分比(df -h /)。
	Disk string `json:"disk"`
	// GoVersion go version;未装 = "未安装"。
	GoVersion string `json:"go_version"`
	// RustVersion rustc --version;未装 = "未安装"。
	RustVersion string `json:"rust_version"`
	// NodeVersion node -v;未装 = "未安装"(前端构建需要)。
	NodeVersion string `json:"node_version"`
	// OrbitVersion /opt/orbitcloud/bin/orbitcloud --version;未部署 = "-"。
	OrbitVersion string `json:"orbit_version"`
}

// ConfigItem 一条配置关键项(已脱敏)。
// 作用: 审计页"配置项表"的一行;敏感值统一由 audit 层替换为状态词。
type ConfigItem struct {
	// Name 展示名(如 "后端 jwt.secret")。
	Name string `json:"name"`
	// Value 脱敏后的值(如 "已注入" / "默认值需改" / "s3")。
	Value string `json:"value"`
	// OK 是否健康(前端据此着色);false = 该项本身有问题。
	OK bool `json:"ok"`
}

// Suggestion 一条建议(可勾选为待办)。
type Suggestion struct {
	// Text 建议内容(如 "生产环境 log.level=debug → 建议 info")。
	Text string `json:"text"`
	// Kind 类别: security(安全加固)|config(配置优化)|ops(运维建议)。
	Kind string `json:"kind"`
}

// ---------- 预检 ----------

// CheckItem 一条预检结果。
// 作用: 预检页列表行;结果内嵌进生成脚本开头(只警告不中断)。
type CheckItem struct {
	// Name 检查项名(如 "磁盘空间")。
	Name string `json:"name"`
	// Result "PASS" | "WARN"。
	Result string `json:"result"`
	// Detail 实测值与判定说明(如 "根分区可用 53G > 5GB")。
	Detail string `json:"detail"`
}

// ---------- 脚本生成 ----------

// GenScriptResult 脚本生成结果(生成本身不执行任何操作)。
type GenScriptResult struct {
	// Path 本机保存路径(用户点击"保存"后才有值;预览阶段为空)。
	Path string `json:"path"`
	// Script 完整脚本内容(供预览/复制)。
	Script string `json:"script"`
	// Lines 脚本行数(摘要用)。
	Lines int `json:"lines"`
	// ExecCmd 建议执行命令(如 "sudo bash /path/xxx.sh"),供一键复制。
	ExecCmd string `json:"exec_cmd"`
}

// ---------- 脚本生成 · 参数化配置 ----------

// ScriptConfig 脚本生成的完整配置(由前端表单收集,生成"确定值"部署脚本)。
// 字段说明见各字段注释;空值语义: 带"空=自动生成/默认"的字段留空时由生成器兜底。
type ScriptConfig struct {
	// ---- 目标与执行 ----
	// Target 生成目标: all|backend|smb|frontend|restart|rollback。
	Target string `json:"target"`
	// IncludeSudo 生成的脚本是否用 sudo 执行(默认 true)。
	IncludeSudo bool `json:"include_sudo"`
	// AppEnv 应用环境: dev|test|prod(写入 config.yaml app.environment)。
	AppEnv string `json:"app_env"`

	// ---- 后端服务 ----
	// ServerHost 后端监听地址(默认 0.0.0.0)。
	ServerHost string `json:"server_host"`
	// ServerPort 后端监听端口(默认 8080)。
	ServerPort int `json:"server_port"`
	// LogLevel 后端日志级别 debug|info|warn|error。
	LogLevel string `json:"log_level"`
	// PprofEnabled 是否开启 pprof 管理接口(生产不建议)。
	PprofEnabled bool `json:"pprof_enabled"`

	// ---- 安全 ----
	// JwtSecret JWT 签名密钥;空 = 脚本内自动生成随机串。
	JwtSecret string `json:"jwt_secret"`
	// JwtExpireMinutes 访问令牌有效期(分钟,默认 720)。
	JwtExpireMinutes int `json:"jwt_expire_minutes"`
	// SuperAdmin 部署后创建/确认的超级管理员用户名;空 = 跳过。
	SuperAdmin string `json:"superadmin"`
	// SuperPass 超级管理员密码(≥8 位);为空且 SuperAdmin 非空时报错。
	SuperPass string `json:"superpass"`

	// ---- 数据库 ----
	// DbSource 元数据库: sqlite|postgres。
	DbSource string `json:"db_source"`
	// DbDSN 数据库连接: sqlite = 文件路径(如 orbitcloud.db);postgres = 连接串。
	DbDSN string `json:"db_dsn"`

	// ---- 对象存储 ----
	// StorageDriver 存储驱动: s3(生产推荐)|local(本地目录)。
	StorageDriver string `json:"storage_driver"`
	// StorageProvider 存储提供商标识: rustfs|minio|s3。
	StorageProvider string `json:"storage_provider"`
	// StorageEndpoint 单节点 S3 端点(如 http://127.0.0.1:9000)。
	StorageEndpoint string `json:"storage_endpoint"`
	// StorageAccessKey S3 Access Key。
	StorageAccessKey string `json:"storage_access_key"`
	// StorageSecretKey S3 Secret Key。
	StorageSecretKey string `json:"storage_secret_key"`
	// StorageChunkSize 分片大小 MB(建议 1~64)。
	StorageChunkSize int `json:"storage_chunk_size"`
	// StorageMinReplicas 最小副本数(单节点建议 1)。
	StorageMinReplicas int `json:"storage_min_replicas"`
	// DeployRustFS 是否在脚本中一并部署 RustFS(仅 driver=s3 时有效)。
	DeployRustFS bool `json:"deploy_rustfs"`
	// RustFSPath RustFS 数据卷目录(默认 /opt/rustfs/data)。
	RustFSPath string `json:"rustfs_path"`
	// RustFSPort RustFS S3 监听端口(默认 9000)。
	RustFSPort int `json:"rustfs_port"`

	// ---- 存储节点 ----
	// NodeID 节点 ID(默认 node1)。
	NodeID string `json:"node_id"`
	// NodePath s3 驱动: bucket 名;local 驱动: 本地目录路径。
	NodePath string `json:"node_path"`
	// NodeMaxSpace 节点容量配额 GB(0 = 不限)。
	NodeMaxSpace int `json:"node_max_space"`

	// ---- SMB 网关 ----
	// SmbListen SMB 监听地址(默认 0.0.0.0:445)。
	SmbListen string `json:"smb_listen"`
	// SmbSharedKey 两侧共享密钥;空 = 脚本内自动生成并与后端保持一致。
	SmbSharedKey string `json:"smb_shared_key"`
	// SmbLogLevel SMB 网关日志级别 debug|info。
	SmbLogLevel string `json:"smb_log_level"`
	// SmbNetbiosName NetBIOS 显示名(默认 ORBITCLOUD)。
	SmbNetbiosName string `json:"smb_netbios_name"`

	// ---- 前端 ----
	// DeployFrontend 是否部署前端(需 nginx;默认 true)。
	DeployFrontend bool `json:"deploy_frontend"`
	// NginxPort 前端监听端口(默认 80)。
	NginxPort int `json:"nginx_port"`
	// FrontendDir 前端静态目录(默认 /opt/orbitcloud/web)。
	FrontendDir string `json:"frontend_dir"`

	// ---- 路径 ----
	// DeployedPath 远端部署目录(默认 /opt/orbitcloud)。
	DeployedPath string `json:"deployed_path"`
	// RepoPath 远端仓库目录(默认 ~/桌面/OrbitCloud,前端构建时用)。
	RepoPath string `json:"repo_path"`
}

// FieldGroup 配置项分组(前端表单按组折叠渲染)。
type FieldGroup struct {
	// Name 分组名(如 "后端服务")。
	Name string `json:"name"`
	// Key 分组键(前端 v-model 前缀用)。
	Key string `json:"key"`
	// Fields 组内配置项定义。
	Fields []FieldDef `json:"fields"`
}

// FieldDef 一个配置项的元数据(前端据此渲染控件: 下拉/输入/开关)。
type FieldDef struct {
	// Key 字段名(对应 ScriptConfig 的 json key)。
	Key string `json:"key"`
	// Label 展示名(如 "JWT 密钥")。
	Label string `json:"label"`
	// Type 控件类型: input|password|number|select|checkbox。
	Type string `json:"type"`
	// Options select 的候选项(如 ["s3","local"])。
	Options []string `json:"options"`
	// Default 默认值(字符串化)。
	Default string `json:"default"`
	// Placeholder 输入框占位提示(如 "留空 = 自动生成")。
	Placeholder string `json:"placeholder"`
	// Hint 字段说明(如 "生产必改,建议 32 位以上随机串")。
	Hint string `json:"hint"`
	// Required 是否必填(提交时校验)。
	Required bool `json:"required"`
}

// ScriptForm 脚本页表单定义(配置分组 + 最近一次填值)。
type ScriptForm struct {
	// Groups 分组列表(前端渲染表单)。
	Groups []FieldGroup `json:"groups"`
	// Last 上次填值(回填用,来自 config.json LastGen 与默认值)。
	Last map[string]string `json:"last"`
}

// ValidationIssue 配置校验结果项。
type ValidationIssue struct {
	// Level error(阻断生成)|warn(允许生成,提示)。
	Level string `json:"level"`
	// Field 关联字段 key(前端定位高亮);空 = 全局。
	Field string `json:"field"`
	// Message 提示内容(如 "JWT 密钥过短,建议 ≥32 位随机串")。
	Message string `json:"message"`
}

// ValidateResult 校验汇总。
type ValidateResult struct {
	// Issues 全部问题(error 优先)。
	Issues []ValidationIssue `json:"issues"`
	// OK 是否有 error 级问题(false = 存在阻断项)。
	OK bool `json:"ok"`
}
