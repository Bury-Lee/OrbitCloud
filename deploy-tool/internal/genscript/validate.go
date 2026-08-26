// validate.go —— 脚本配置校验: 不建议/不恰当的配置项给出 error(阻断)与 warn(提示)。
package genscript

import (
	"strconv"
	"strings"

	"deploy-tool/internal/model"
)

// Validate 校验脚本配置。
// 参数: sc *model.ScriptConfig 用户填写的配置
// 返回: *model.ValidateResult issues 列表(error 优先排序)+ OK(无 error 级问题)
// 实现逻辑: 逐规则判定,error 追加到 errs,warn 追加到 warns,最后合并排序。
func Validate(sc *model.ScriptConfig) *model.ValidateResult {
	var errs, warns []model.ValidationIssue

	addErr := func(field, msg string) { errs = append(errs, model.ValidationIssue{Level: "error", Field: field, Message: msg}) }
	addWarn := func(field, msg string) { warns = append(warns, model.ValidationIssue{Level: "warn", Field: field, Message: msg}) }

	// ---- 必填与基础 ----
	if sc == nil {
		errs = append(errs, model.ValidationIssue{Level: "error", Field: "", Message: "配置为空"})
		return &model.ValidateResult{Issues: append(errs, warns...), OK: len(errs) == 0}
	}
	if sc.ServerPort < 1 || sc.ServerPort > 65535 {
		addErr("server_port", "后端端口必须在 1~65535")
	}
	if sc.JwtExpireMinutes < 30 {
		addWarn("jwt_expire_minutes", "令牌有效期过短(<30 分钟),频繁重登影响体验")
	}
	if sc.StorageChunkSize < 1 || sc.StorageChunkSize > 64 {
		addWarn("storage_chunk_size", "分片大小建议 1~64MB,当前值可能导致性能问题")
	}
	if sc.StorageMinReplicas < 1 {
		addErr("storage_min_replicas", "最小副本数必须 ≥1")
	}

	// ---- 安全 ----
	if strings.TrimSpace(sc.JwtSecret) == "" {
		addWarn("jwt_secret", "JWT 密钥留空,将自动生成随机串(推荐勾选/自行填写 32 位以上)")
	} else if len(sc.JwtSecret) < 32 {
		addErr("jwt_secret", "JWT 密钥过短(<32 位),生产不安全")
	} else if strings.Contains(sc.JwtSecret, "please-change") {
		addErr("jwt_secret", "JWT 密钥仍为默认占位值,必须更换")
	}
	if strings.TrimSpace(sc.SuperAdmin) != "" && len(sc.SuperPass) < 8 {
		addErr("superpass", "超级管理员密码不足 8 位")
	}
	if sc.SuperPass != "" && sc.SuperPass == sc.SuperAdmin {
		addWarn("superpass", "管理员密码与用户名相同,存在弱口令风险")
	}
	if sc.PprofEnabled {
		addWarn("pprof_enabled", "生产开启 pprof 有信息泄露风险,建议仅排障时按需开启")
	}
	if sc.AppEnv == "prod" && sc.LogLevel == "debug" {
		addWarn("log_level", "生产环境 debug 日志量过大,建议 info")
	}

	// ---- 存储 ----
	if sc.StorageDriver == "s3" {
		if strings.TrimSpace(sc.StorageEndpoint) == "" {
			addErr("storage_endpoint", "driver=s3 必须填写 S3 端点")
		}
		if strings.TrimSpace(sc.StorageAccessKey) == "" || strings.TrimSpace(sc.StorageSecretKey) == "" {
			addErr("storage_access_key", "S3 凭据不能为空")
		}
		if sc.StorageAccessKey == "rustfsadmin" || sc.StorageSecretKey == "rustfsadmin" {
			addWarn("storage_access_key", "仍使用 RustFS 默认凭据 rustfsadmin,生产建议修改")
		}
		if strings.Contains(sc.StorageEndpoint, "127.0.0.1") || strings.Contains(sc.StorageEndpoint, "localhost") {
			addWarn("storage_endpoint", "端点指向本机(127.0.0.1),仅同一台服务器部署时可访问")
		}
		if sc.DeployRustFS && sc.StorageMinReplicas > 1 {
			addWarn("storage_min_replicas", "单 RustFS 节点副本数 >1 无法真正跨节点冗余,建议 1")
		}
		if sc.StorageMinReplicas > 3 {
			addWarn("storage_min_replicas", "副本数过多会放大写放大,建议 ≤3")
		}
	} else if sc.StorageDriver == "local" {
		if sc.StorageEndpoint != "" && strings.Contains(sc.StorageEndpoint, "://") {
			addWarn("storage_endpoint", "driver=local 时 endpoint 应为本地目录路径,不应是 URL")
		}
	} else {
		addErr("storage_driver", "存储驱动必须是 s3 或 local")
	}
	if sc.StorageProvider != "rustfs" && sc.StorageProvider != "minio" && sc.StorageProvider != "s3" {
		addErr("storage_provider", "不支持的提供商标识(可选 rustfs|minio|s3)")
	}

	// ---- 数据库 ----
	switch sc.DbSource {
	case "sqlite":
		if sc.DbDSN == "" {
			addErr("db_dsn", "SQLite 需要数据库文件路径(如 orbitcloud.db)")
		}
	case "postgres":
		if !strings.HasPrefix(sc.DbDSN, "postgres://") {
			addErr("db_dsn", "postgres DSN 应以 postgres:// 开头")
		}
	default:
		addErr("db_source", "元数据库必须是 sqlite 或 postgres")
	}

	// ---- SMB ----
	if strings.TrimSpace(sc.SmbSharedKey) != "" && len(sc.SmbSharedKey) < 16 {
		addErr("smb_shared_key", "SMB 共享密钥不足 16 字节,握手会被拒绝")
	}
	if sc.SmbLogLevel == "debug" {
		addWarn("smb_log_level", "SMB 网关 debug 日志量大,生产建议 info")
	}
	if !strings.Contains(sc.SmbListen, ":") {
		addErr("smb_listen", "SMB 监听格式应为 host:port(如 0.0.0.0:445)")
	}

	// ---- 端口冲突 ----
	if sc.NginxPort == sc.ServerPort {
		addErr("nginx_port", "前端端口与后端端口相同,会冲突")
	}
	if sc.ServerPort == 445 || sc.ServerPort == 9000 {
		addWarn("server_port", "后端端口与 SMB(445)/对象存储(9000)相同,可能冲突")
	}
	if sc.RustFSPort == sc.ServerPort {
		addErr("rustfs_port", "RustFS 端口与后端端口相同,会冲突")
	}
	if sc.NginxPort != 80 && sc.NginxPort < 1024 {
		addWarn("nginx_port", "非标准 80 端口,访问需带端口号")
	}

	// ---- 路径 ----
	if sc.DeployedPath == "" {
		addErr("deployed_path", "部署目录不能为空")
	}
	if sc.FrontendDir == sc.DeployedPath {
		addWarn("frontend_dir", "前端目录与部署目录相同,会互相覆盖,建议用子目录(如 /opt/orbitcloud/web)")
	}

	issues := append(errs, warns...)
	return &model.ValidateResult{Issues: issues, OK: len(errs) == 0}
}

// parseNum 字符串转 int(内部,非法返回 0)。
func parseNum(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}
