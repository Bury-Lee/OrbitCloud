// form.go —— 脚本配置项元数据(前端动态表单渲染依据)与默认值。
package genscript

import (
	"strconv"
	"strings"

	"deploy-tool/internal/model"
)

// scriptForm 静态定义: 配置分组 → 字段 → 控件元数据。
// 作用: 前端 GetScriptForm 直接返回本结构,渲染下拉/输入/开关;提交 ScriptConfig 回来。
var scriptForm = []model.FieldGroup{
	{
		Name: "目标与执行", Key: "exec",
		Fields: []model.FieldDef{
			{Key: "target", Label: "生成目标", Type: "select", Options: []string{"all", "backend", "smb", "frontend", "restart", "rollback"}, Default: "all", Hint: "all = 后端 + SMB 网关 + 前端(按下方勾选)"},
			{Key: "include_sudo", Label: "使用 sudo 执行", Type: "checkbox", Default: "true", Hint: "关闭后需以 root 直接运行"},
			{Key: "app_env", Label: "应用环境", Type: "select", Options: []string{"dev", "test", "prod"}, Default: "prod", Hint: "写入 config.yaml app.environment"},
			{Key: "log_level", Label: "后端日志级别", Type: "select", Options: []string{"info", "debug", "warn", "error"}, Default: "info", Hint: "生产建议 info;debug 仅排障时用"},
		},
	},
	{
		Name: "后端服务", Key: "backend",
		Fields: []model.FieldDef{
			{Key: "server_host", Label: "监听地址", Type: "input", Default: "0.0.0.0", Hint: "0.0.0.0 = 所有网卡"},
			{Key: "server_port", Label: "监听端口", Type: "number", Default: "8080", Required: true, Hint: "与前端 /api 反代端口一致"},
			{Key: "pprof_enabled", Label: "开启 pprof", Type: "checkbox", Default: "false", Hint: "生产不建议常开"},
			{Key: "deployed_path", Label: "部署目录", Type: "input", Default: "/opt/orbitcloud", Required: true},
			{Key: "repo_path", Label: "远端仓库目录", Type: "input", Default: "~/桌面/OrbitCloud", Hint: "前端构建/源码构建时使用"},
		},
	},
	{
		Name: "安全与管理员", Key: "security",
		Fields: []model.FieldDef{
			{Key: "jwt_secret", Label: "JWT 密钥", Type: "password", Default: "", Placeholder: "留空 = 自动生成", Required: true, Hint: "生产必改,建议 ≥32 位随机串;留空自动生成并写入脚本"},
			{Key: "jwt_expire_minutes", Label: "令牌有效期(分钟)", Type: "number", Default: "720"},
			{Key: "superadmin", Label: "超级管理员用户名", Type: "input", Default: "admin", Hint: "部署后执行 --add-superadmin"},
			{Key: "superpass", Label: "超级管理员密码", Type: "password", Default: "", Required: true, Hint: "≥8 位;留空且用户名非空 = 校验失败"},
		},
	},
	{
		Name: "数据库", Key: "database",
		Fields: []model.FieldDef{
			{Key: "db_source", Label: "元数据库", Type: "select", Options: []string{"sqlite", "postgres"}, Default: "sqlite", Hint: "生产建议 postgres;sqlite 仅开发/单机"},
			{Key: "db_dsn", Label: "DSN", Type: "input", Default: "orbitcloud.db", Placeholder: "sqlite: 文件路径;postgres: postgres://...", Required: true, Hint: "生产可用环境变量 ORBITCLOUD_DB_DSN 覆盖"},
		},
	},
	{
		Name: "对象存储", Key: "storage",
		Fields: []model.FieldDef{
			{Key: "storage_driver", Label: "存储驱动", Type: "select", Options: []string{"s3", "local"}, Default: "s3", Hint: "s3 = 对象存储(生产);local = 本地目录(开发)"},
			{Key: "storage_provider", Label: "提供商标识", Type: "select", Options: []string{"rustfs", "minio", "s3"}, Default: "rustfs"},
			{Key: "storage_endpoint", Label: "S3 端点", Type: "input", Default: "http://127.0.0.1:9000", Required: true, Hint: "driver=s3 时必填"},
			{Key: "storage_access_key", Label: "Access Key", Type: "input", Default: "rustfsadmin"},
			{Key: "storage_secret_key", Label: "Secret Key", Type: "password", Default: "rustfsadmin"},
			{Key: "storage_chunk_size", Label: "分片大小(MB)", Type: "number", Default: "5", Hint: "建议 1~64"},
			{Key: "storage_min_replicas", Label: "最小副本数", Type: "number", Default: "1", Hint: "单 RustFS 节点建议 1;多节点按节点数"},
			{Key: "deploy_rustfs", Label: "一并部署 RustFS", Type: "checkbox", Default: "true", Hint: "勾选后脚本安装并启动 RustFS 单节点"},
			{Key: "rustfs_path", Label: "RustFS 数据目录", Type: "input", Default: "/opt/rustfs/data"},
			{Key: "rustfs_port", Label: "RustFS 端口", Type: "number", Default: "9000"},
			{Key: "node_id", Label: "节点 ID", Type: "input", Default: "node1"},
			{Key: "node_path", Label: "节点 bucket/目录", Type: "input", Default: "orbitcloud-node1", Hint: "s3: bucket 名;local: 本地目录"},
			{Key: "node_max_space", Label: "节点配额(GB,0=不限)", Type: "number", Default: "0"},
		},
	},
	{
		Name: "SMB 网关", Key: "smb",
		Fields: []model.FieldDef{
			{Key: "smb_listen", Label: "SMB 监听", Type: "input", Default: "0.0.0.0:445", Required: true},
			{Key: "smb_shared_key", Label: "共享密钥", Type: "password", Default: "", Placeholder: "留空 = 自动生成", Hint: "两侧必须一致;留空自动生成并同时写入后端与网关配置"},
			{Key: "smb_log_level", Label: "网关日志级别", Type: "select", Options: []string{"info", "debug"}, Default: "info"},
			{Key: "smb_netbios_name", Label: "NetBIOS 名", Type: "input", Default: "ORBITCLOUD"},
		},
	},
	{
		Name: "前端", Key: "frontend",
		Fields: []model.FieldDef{
			{Key: "deploy_frontend", Label: "部署前端", Type: "checkbox", Default: "true", Hint: "需服务器 Nginx;构建需 Node"},
			{Key: "nginx_port", Label: "前端端口", Type: "number", Default: "80", Required: true, Hint: "不能与后端端口相同"},
			{Key: "frontend_dir", Label: "前端静态目录", Type: "input", Default: "/opt/orbitcloud/web"},
		},
	},
}

// DefaultScriptConfig 返回带默认值的脚本配置(表单回填与生成兜底共用)。
// 参数: 无
// 返回: *model.ScriptConfig
func DefaultScriptConfig() *model.ScriptConfig {
	return &model.ScriptConfig{
		Target:             "all",
		IncludeSudo:        true,
		AppEnv:             "prod",
		LogLevel:           "info",
		ServerHost:         "0.0.0.0",
		ServerPort:         8080,
		PprofEnabled:       false,
		JwtSecret:          "",
		JwtExpireMinutes:   720,
		SuperAdmin:         "admin",
		SuperPass:          "",
		DbSource:           "sqlite",
		DbDSN:              "orbitcloud.db",
		StorageDriver:      "s3",
		StorageProvider:    "rustfs",
		StorageEndpoint:    "http://127.0.0.1:9000",
		StorageAccessKey:   "rustfsadmin",
		StorageSecretKey:   "rustfsadmin",
		StorageChunkSize:   5,
		StorageMinReplicas: 1,
		DeployRustFS:       true,
		RustFSPath:         "/opt/rustfs/data",
		RustFSPort:         9000,
		NodeID:             "node1",
		NodePath:           "orbitcloud-node1",
		NodeMaxSpace:       0,
		SmbListen:          "0.0.0.0:445",
		SmbSharedKey:       "",
		SmbLogLevel:        "info",
		SmbNetbiosName:     "ORBITCLOUD",
		DeployFrontend:     true,
		NginxPort:          80,
		FrontendDir:        "/opt/orbitcloud/web",
		DeployedPath:       "/opt/orbitcloud",
		RepoPath:           "~/桌面/OrbitCloud",
	}
}

// BuildForm 构建脚本页表单定义(含上次填值回填)。
// 参数: last model.GenOpts 上次生成选项(来自 config.json;新版字段映射为空)
// 返回: *model.ScriptForm
// 实现逻辑: 深拷贝 scriptForm(防外部修改);Last 从 DefaultScriptConfig 序列化为 map[string]string
//           (wails 前端直接渲染;字段值均为字符串/布尔字符串化)。
func BuildForm(last model.GenOpts) *model.ScriptForm {
	groups := make([]model.FieldGroup, len(scriptForm))
	for i, g := range scriptForm {
		groups[i] = g
		groups[i].Fields = append([]model.FieldDef(nil), g.Fields...)
	}
	def := DefaultScriptConfig()
	lastMap := map[string]string{}
	if def.Target != "" {
		lastMap["target"] = def.Target
	}
	lastMap["include_sudo"] = strconv.FormatBool(def.IncludeSudo)
	lastMap["app_env"] = def.AppEnv
	lastMap["log_level"] = def.LogLevel
	lastMap["server_host"] = def.ServerHost
	lastMap["server_port"] = strconv.Itoa(def.ServerPort)
	lastMap["pprof_enabled"] = strconv.FormatBool(def.PprofEnabled)
	lastMap["deployed_path"] = def.DeployedPath
	lastMap["repo_path"] = def.RepoPath
	lastMap["jwt_secret"] = def.JwtSecret
	lastMap["jwt_expire_minutes"] = strconv.Itoa(def.JwtExpireMinutes)
	lastMap["superadmin"] = def.SuperAdmin
	lastMap["superpass"] = def.SuperPass
	lastMap["db_source"] = def.DbSource
	lastMap["db_dsn"] = def.DbDSN
	lastMap["storage_driver"] = def.StorageDriver
	lastMap["storage_provider"] = def.StorageProvider
	lastMap["storage_endpoint"] = def.StorageEndpoint
	lastMap["storage_access_key"] = def.StorageAccessKey
	lastMap["storage_secret_key"] = def.StorageSecretKey
	lastMap["storage_chunk_size"] = strconv.Itoa(def.StorageChunkSize)
	lastMap["storage_min_replicas"] = strconv.Itoa(def.StorageMinReplicas)
	lastMap["deploy_rustfs"] = strconv.FormatBool(def.DeployRustFS)
	lastMap["rustfs_path"] = def.RustFSPath
	lastMap["rustfs_port"] = strconv.Itoa(def.RustFSPort)
	lastMap["node_id"] = def.NodeID
	lastMap["node_path"] = def.NodePath
	lastMap["node_max_space"] = strconv.Itoa(def.NodeMaxSpace)
	lastMap["smb_listen"] = def.SmbListen
	lastMap["smb_shared_key"] = def.SmbSharedKey
	lastMap["smb_log_level"] = def.SmbLogLevel
	lastMap["smb_netbios_name"] = def.SmbNetbiosName
	lastMap["deploy_frontend"] = strconv.FormatBool(def.DeployFrontend)
	lastMap["nginx_port"] = strconv.Itoa(def.NginxPort)
	lastMap["frontend_dir"] = def.FrontendDir
	return &model.ScriptForm{Groups: groups, Last: lastMap}
}

// parseBool 字符串转布尔(容错: 空/非法 = false)。
func parseBool(s string) bool {
	return strings.EqualFold(s, "true") || s == "1"
}
