// genscript.go —— 参数化脚本生成: 根据表单配置生成"确定值"部署脚本。
// 铁律: 本包只拼文本,绝不执行任何命令;脚本内容对用户完全可见。
// 确定性: 所有留空的密钥(如 jwt_secret/smb_shared_key)在生成时即确定并写入脚本,
//         用户可完整预览脚本内的每一个值。
package genscript

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"deploy-tool/internal/model"
	"deploy-tool/internal/precheck"
)

// nowFn 可替换的时间源(内部,便于单测固定文件名)。
var nowFn = time.Now

// ---------- 生成 ----------

// Generate 生成确定值部署脚本(纯文本拼装,不落盘)。
// 参数: cfg *model.ToolConfig 工具配置(服务器地址等); sc *model.ScriptConfig 表单配置;
//       checks []model.CheckItem 预检结果(内嵌为注释段)
// 返回: model.GenScriptResult; error 校验失败(error 级问题)
// 实现逻辑:
//   1. Validate(sc) 校验,存在 error 级问题 → 返回错误(列出全部 error)
//   2. 兜底默认值: 空字段补 DefaultScriptConfig 值
//   3. 密钥确定性: jwt_secret/smb_shared_key 留空 → crypto/rand 生成 hex,写入脚本
//   4. 按 Target 拼装: header → 预检段 → 变量段 → 部署步骤段 → 健康检查段
func Generate(cfg *model.ToolConfig, sc *model.ScriptConfig, checks []model.CheckItem) (model.GenScriptResult, error) {
	v := Validate(sc)
	if !v.OK {
		var msgs []string
		for _, it := range v.Issues {
			if it.Level == "error" {
				msgs = append(msgs, "· "+it.Message)
			}
		}
		return model.GenScriptResult{}, fmt.Errorf("配置校验未通过:\n%s", strings.Join(msgs, "\n"))
	}
	sc = withDefaults(sc)

	// 密钥确定性
	if strings.TrimSpace(sc.JwtSecret) == "" {
		sc.JwtSecret = randomHex(32)
	}
	if strings.TrimSpace(sc.SmbSharedKey) == "" {
		sc.SmbSharedKey = randomHex(32)
	}

	host := cfg.Host
	if host == "" {
		host = "(读 sshbox.json)"
	}
	sudoHint := ""
	if !sc.IncludeSudo {
		sudoHint = "# 注意: 已选择不使用 sudo,请确保以 root 执行\n"
	}
	header := fmt.Sprintf(`#!/usr/bin/env bash
# ============================================================
# OrbitCloud 部署脚本 —— 由 OrbitCloudDeployTool 生成(确定值)
#   目标    : %s
#   服务器  : %s
#   生成时间: %s
#   说明    : 脚本内所有配置均为确定值,请完整审阅后执行:
#             %s
# ============================================================
%s`, sc.Target, host, nowFn().Format("2006-01-02 15:04:05"),
		sudoCmd(sc.IncludeSudo, "<本文件>"), sudoHint)

	steps := stepsFor(sc)

	script := header + "\n\n" +
		precheck.RenderBlock(checks) + "\n" +
		"# ============ 部署步骤(确定值) ============\n" +
		steps + "\n\n" +
		healthBlock(sc.SuperAdmin, sc.SuperPass)

	lines := len(strings.Split(strings.TrimRight(script, "\n"), "\n"))
	return model.GenScriptResult{
		Script:  script,
		Lines:   lines,
		ExecCmd: sudoCmd(sc.IncludeSudo, "orbitcloud-deploy.sh"),
	}, nil
}

// stepsFor 按目标生成部署步骤段(内部)。
// 实现: 各目标片段拼接;片段间用分隔注释衔接。
func stepsFor(sc *model.ScriptConfig) string {
	var parts []string
	switch sc.Target {
	case "restart":
		parts = append(parts, restartSteps())
	case "rollback":
		parts = append(parts, rollbackSteps())
	case "frontend":
		if sc.DeployFrontend {
			parts = append(parts, backendVars(sc), frontendSteps(sc))
		}
	case "smb":
		parts = append(parts, backendVars(sc), smbSteps(sc))
	case "backend":
		parts = append(parts, backendVars(sc), backendSteps(sc))
	case "all":
		parts = append(parts, backendVars(sc), backendSteps(sc))
		if sc.DeployRustFS && sc.StorageDriver == "s3" {
			parts = append(parts, rustfsSteps(sc))
		}
		if sc.DeployFrontend {
			parts = append(parts, frontendSteps(sc))
		}
	}
	return strings.Join(parts, "\n")
}

// backendVars 公共变量段(内部): 部署目录与凭据变量。
func backendVars(sc *model.ScriptConfig) string {
	return fmt.Sprintf(`# ---- 变量(确定值) ----
DEPLOY_DIR=%s
JWT_SECRET=%s
SHARED_KEY=%s
`, sc.DeployedPath, shQuote(sc.JwtSecret), shQuote(sc.SmbSharedKey))
}

// backendSteps 后端部署段(内部): 二进制 → config.yaml → systemd → 超管。
func backendSteps(sc *model.ScriptConfig) string {
	return fmt.Sprintf(`# ---- 1. 后端(orbitcloud) ----
sudo mkdir -p "$DEPLOY_DIR"/{bin,config,logs}
# 二进制来源: 设置环境变量 ORBITCLOUD_BIN 指定本机路径,或先传输到服务器
if [ -n "${ORBITCLOUD_BIN:-}" ] && [ -f "$ORBITCLOUD_BIN" ]; then
  sudo install -m 0755 "$ORBITCLOUD_BIN" "$DEPLOY_DIR/bin/orbitcloud"
else
  echo "[提示] 未检测到后端二进制: 请先 scp 到服务器或用 ORBITCLOUD_BIN=/path 指定"
fi

# ---- config.yaml(确定值) ----
sudo tee "$DEPLOY_DIR/config/config.yaml" > /dev/null <<'YAML'
%s
YAML
sudo chown -R $(id -un) "$DEPLOY_DIR" 2>/dev/null || true

# ---- systemd ----
sudo tee /etc/systemd/system/orbitcloud.service > /dev/null <<'UNIT'
[Unit]
Description=OrbitCloud (HTTP + SMB Gateway)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
WorkingDirectory=$DEPLOY_DIR/config
ExecStart=$DEPLOY_DIR/bin/orbitcloud
Restart=always
RestartSec=3
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
UNIT
sudo systemctl daemon-reload
sudo systemctl enable --now orbitcloud
sleep 2
systemctl is-active orbitcloud || echo "[警告] orbitcloud 启动失败,请查看: sudo journalctl -u orbitcloud -n 50"
`, backendYAML(sc))
}

// rustfsSteps RustFS 部署段(内部)。
func rustfsSteps(sc *model.ScriptConfig) string {
	return fmt.Sprintf(`# ---- 2. RustFS(对象存储,单节点) ----
# 二进制来源: 设置环境变量 ORBITCLOUD_RUSTFS_BIN,或先传输到服务器
if [ -n "${ORBITCLOUD_RUSTFS_BIN:-}" ] && [ -f "$ORBITCLOUD_RUSTFS_BIN" ]; then
  sudo install -m 0755 "$ORBITCLOUD_RUSTFS_BIN" /usr/local/bin/rustfs
else
  echo "[提示] 未检测到 RustFS 二进制: 请先传输或用 ORBITCLOUD_RUSTFS_BIN=/path 指定"
fi
sudo mkdir -p %s
sudo tee /etc/systemd/system/rustfs.service > /dev/null <<'UNIT'
[Unit]
Description=RustFS S3 Object Storage
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/rustfs server --address 127.0.0.1:%d %s
Environment=RUSTFS_ACCESS_KEY=%s
Environment=RUSTFS_SECRET_KEY=%s
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
UNIT
sudo systemctl daemon-reload
sudo systemctl enable --now rustfs
sleep 2
systemctl is-active rustfs || echo "[警告] RustFS 启动失败: sudo journalctl -u rustfs -n 50"
`, sc.RustFSPath, sc.RustFSPort, sc.RustFSPath,
		shQuote(sc.StorageAccessKey), shQuote(sc.StorageSecretKey))
}

// smbSteps SMB 网关部署段(内部): 二进制 → config.yaml(共享密钥与后端一致)→ systemd。
func smbSteps(sc *model.ScriptConfig) string {
	return fmt.Sprintf(`# ---- SMB 网关(smb-gateway) ----
sudo mkdir -p "$DEPLOY_DIR/smb-gateway/logs"
if [ -n "${ORBITCLOUD_SMB_BIN:-}" ] && [ -f "$ORBITCLOUD_SMB_BIN" ]; then
  sudo install -m 0755 "$ORBITCLOUD_SMB_BIN" "$DEPLOY_DIR/smb-gateway/smb-gateway"
else
  echo "[提示] 未检测到 SMB 网关二进制: 请先传输或用 ORBITCLOUD_SMB_BIN=/path 指定"
fi

sudo tee "$DEPLOY_DIR/smb-gateway/config.yaml" > /dev/null <<'YAML'
%s
YAML
sudo tee /etc/systemd/system/smb-gateway.service > /dev/null <<'UNIT'
[Unit]
Description=OrbitCloud SMB Gateway (Rust)
After=network-online.target orbitcloud.service
Wants=orbitcloud.service

[Service]
Type=simple
WorkingDirectory=$DEPLOY_DIR/smb-gateway
ExecStart=$DEPLOY_DIR/smb-gateway/smb-gateway
Restart=always
RestartSec=3
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
UNIT
sudo systemctl daemon-reload
sudo systemctl enable --now smb-gateway
sleep 2
systemctl is-active smb-gateway || echo "[警告] smb-gateway 启动失败: sudo journalctl -u smb-gateway -n 50"
`, smbYAML(sc))
}

// frontendSteps 前端部署段(内部): 构建(需 Node)或 dist 传输 + nginx 配置。
func frontendSteps(sc *model.ScriptConfig) string {
	return fmt.Sprintf(`# ---- 前端(Vue3 + Nginx) ----
sudo mkdir -p %s
if command -v node > /dev/null 2>&1; then
  cd %s/frontend && npm install && npm run build && sudo cp -r dist/* %s/
else
  echo "[提示] 服务器无 Node: 请本地构建 frontend/dist 后 scp 到 %s"
fi
sudo tee /etc/nginx/conf.d/orbitcloud.conf > /dev/null <<'NGINX'
server {
    listen %d;
    server_name _;
    root %s;
    index index.html;
    location / { try_files $uri $uri/ /index.html; }
    location /api/ {
        proxy_pass http://127.0.0.1:%d;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_buffering off;
        proxy_read_timeout 600s;
        client_max_body_size 0;
    }
}
NGINX
sudo rm -f /etc/nginx/sites-enabled/default 2>/dev/null || true
nginx -t && sudo systemctl reload nginx || echo "[警告] nginx 配置校验失败"
`, sc.FrontendDir, sc.RepoPath, sc.FrontendDir, sc.FrontendDir,
		sc.NginxPort, sc.FrontendDir, sc.ServerPort)
}

// restartSteps 重启段(内部)。
func restartSteps() string {
	return `# ---- 重启服务 ----
sudo systemctl restart orbitcloud smb-gateway
`
}

// rollbackSteps 回滚段(内部): 从备份目录恢复二进制。
func rollbackSteps() string {
	return `# ---- 回滚: 从备份目录恢复上一版二进制 ----
# 前置: 部署时已将旧版备份到 /opt/orbitcloud/backup/
if [ -f /opt/orbitcloud/backup/orbitcloud ]; then
  sudo cp /opt/orbitcloud/backup/orbitcloud /opt/orbitcloud/bin/orbitcloud
fi
if [ -f /opt/orbitcloud/backup/smb-gateway ]; then
  sudo cp /opt/orbitcloud/backup/smb-gateway /opt/orbitcloud/smb-gateway/smb-gateway
fi
sudo systemctl restart orbitcloud smb-gateway
`
}

// healthBlock 部署后健康检查段(内部)。
func healthBlock(superAdmin, superPass string) string {
	var smoke string
	if superAdmin != "" && superPass != "" {
		smoke = fmt.Sprintf(`python3 - <<'PY'
import json,urllib.request
req=urllib.request.Request("http://127.0.0.1:8080/api/v1/auth/login",
    data=json.dumps({"username":%q,"password":%q}).encode(),
    headers={"Content-Type":"application/json"})
print("登录冒烟:", json.loads(urllib.request.urlopen(req,timeout=10).read())["code"])
PY`, superAdmin, superPass)
	} else {
		smoke = "# 登录冒烟: 请手动打开 http://<服务器>/ 验证登录"
	}
	return `# ============ 部署后健康检查 ============
for s in orbitcloud smb-gateway rustfs nginx; do
  printf "%-14s: %s\n" "$s" "$(systemctl is-active $s 2>/dev/null)"
done
echo "--- 端口监听 ---"
ss -tln | grep -E ':(80|445|8080|9000|9101) ' || echo "(部分端口未监听)"
echo "--- API 冒烟 ---"
` + smoke + `
`
}

// backendYAML 生成后端 config.yaml 全文(内部,值全部确定)。
func backendYAML(sc *model.ScriptConfig) string {
	su := ""
	if strings.TrimSpace(sc.SuperAdmin) != "" {
		su = fmt.Sprintf(`# 超级管理员(部署后创建,见下方步骤)
`)
	}
	_ = su
	return fmt.Sprintf(`app:
  name: orbitcloud
  version: 1.0.0
  environment: %s
  timezone: Asia/Shanghai

server:
  host: %s
  port: %d
  read_timeout: 1
  write_timeout: 0
  idle_timeout: 0
  max_header_bytes: 1048576
  mode: %s
  pprof_enabled: %t
  pprof_dir: ""

jwt:
  secret: %s
  expire_minutes: %d
  issuer: orbitcloud
  algorithm: HS256

refresh_jwt:
  expire_minutes: 10080
  issuer: orbitcloud

storage:
  provider: %s
  endpoint: %s
  access_key: %s
  secret_key: %s
  region: us-east-1
  use_ssl: false
  timeout: 30
  max_retries: 3
  chunk_size: %d
  min_replicas: %d
  driver: %s
  repair_interval: 30s
  repair_concurrency: 4

health:
  check_interval: 5s
  fail_threshold: 3

pool:
  max_workers: 64
  queue_size: 1024

request_pool:
  max_concurrent: 256
  queue_size: 512
  streaming_max: 64
  mode: block

exec_pool:
  max_workers: 128
  queue_size: 512
  stream_workers: 64
  stream_queue: 256

nodes:
  - id: %s
    driver: %s
    path: %s
    endpoint: %s
    access_key: ""
    secret_key: ""
    max_space: %d
    enabled: true

database:
  source: %s
  charset: "utf8mb4"
  log_level: warn
  max_idle_conns: 5
  max_open_conns: 20
  sources:
    - dsn: %s

bucket:
  admin_create_only: false

smb_gateway:
  listen_addr: "127.0.0.1:9101"
  shared_key: %s
  shared_key_env: "ORBITCLOUD_SMB_GATEWAY_KEY"
  channel_buffer: 1024
  max_concurrent: 64

log:
  level: %s
  format: text
  output_path: "./logs/"
  max_size: 0
  db_level: ""
  retention_days: 30
`, sc.AppEnv,
		yamlQuote(sc.ServerHost), sc.ServerPort,
		serverMode(sc.AppEnv), sc.PprofEnabled,
		yamlQuote(sc.JwtSecret), sc.JwtExpireMinutes,
		sc.StorageProvider, yamlQuote(sc.StorageEndpoint),
		yamlQuote(sc.StorageAccessKey), yamlQuote(sc.StorageSecretKey),
		sc.StorageChunkSize, sc.StorageMinReplicas, sc.StorageDriver,
		sc.NodeID, sc.StorageDriver, sc.NodePath, yamlQuote(sc.StorageEndpoint), sc.NodeMaxSpace,
		sc.DbSource, yamlQuote(sc.DbDSN),
		yamlQuote(sc.SmbSharedKey),
		sc.LogLevel)
}

// smbYAML 生成 SMB 网关 config.yaml 全文(内部)。
func smbYAML(sc *model.ScriptConfig) string {
	return fmt.Sprintf(`smb:
  listen: %s
  netbios_name: %s

gateway:
  addr: "127.0.0.1:9101"
  shared_key: %s
  shared_key_env: ""
  heartbeat_secs: 30
  sync_interval_secs: 60
  channel_buffer: 1024
  rpc_timeout_secs: 30

log:
  level: %s
  output: "%s/smb-gateway/logs"
  file_prefix: "smb-gateway"
  db_enabled: true
  db_path: "%s/smb-gateway/logs/logs.db"
  db_level: "info"
  retention_days: 30
  cleanup_interval_secs: 3600
`, yamlQuote(sc.SmbListen), yamlQuote(sc.SmbNetbiosName),
		yamlQuote(sc.SmbSharedKey), sc.SmbLogLevel,
		sc.DeployedPath, sc.DeployedPath)
}

// withDefaults 空字段兜底为默认值(内部)。
func withDefaults(sc *model.ScriptConfig) *model.ScriptConfig {
	def := DefaultScriptConfig()
	if sc.Target == "" {
		sc.Target = def.Target
	}
	if sc.AppEnv == "" {
		sc.AppEnv = def.AppEnv
	}
	if sc.LogLevel == "" {
		sc.LogLevel = def.LogLevel
	}
	if sc.ServerHost == "" {
		sc.ServerHost = def.ServerHost
	}
	if sc.ServerPort == 0 {
		sc.ServerPort = def.ServerPort
	}
	if sc.JwtExpireMinutes == 0 {
		sc.JwtExpireMinutes = def.JwtExpireMinutes
	}
	if sc.DbSource == "" {
		sc.DbSource = def.DbSource
	}
	if sc.DbDSN == "" {
		sc.DbDSN = def.DbDSN
	}
	if sc.StorageDriver == "" {
		sc.StorageDriver = def.StorageDriver
	}
	if sc.StorageProvider == "" {
		sc.StorageProvider = def.StorageProvider
	}
	if sc.StorageEndpoint == "" {
		sc.StorageEndpoint = def.StorageEndpoint
	}
	if sc.StorageChunkSize == 0 {
		sc.StorageChunkSize = def.StorageChunkSize
	}
	if sc.StorageMinReplicas == 0 {
		sc.StorageMinReplicas = def.StorageMinReplicas
	}
	if sc.RustFSPath == "" {
		sc.RustFSPath = def.RustFSPath
	}
	if sc.RustFSPort == 0 {
		sc.RustFSPort = def.RustFSPort
	}
	if sc.NodeID == "" {
		sc.NodeID = def.NodeID
	}
	if sc.NodePath == "" {
		sc.NodePath = def.NodePath
	}
	if sc.SmbListen == "" {
		sc.SmbListen = def.SmbListen
	}
	if sc.SmbLogLevel == "" {
		sc.SmbLogLevel = def.SmbLogLevel
	}
	if sc.SmbNetbiosName == "" {
		sc.SmbNetbiosName = def.SmbNetbiosName
	}
	if sc.NginxPort == 0 {
		sc.NginxPort = def.NginxPort
	}
	if sc.FrontendDir == "" {
		sc.FrontendDir = def.FrontendDir
	}
	if sc.DeployedPath == "" {
		sc.DeployedPath = def.DeployedPath
	}
	if sc.RepoPath == "" {
		sc.RepoPath = def.RepoPath
	}
	return sc
}

// ---------- 工具函数 ----------

// serverMode 按环境映射 gin 模式(内部)。
func serverMode(env string) string {
	if env == "prod" {
		return "release"
	}
	return "debug"
}

// sudoCmd 按 IncludeSudo 组装执行命令(内部)。
func sudoCmd(useSudo bool, path string) string {
	if useSudo {
		return "sudo bash " + path
	}
	return "bash " + path
}

// yamlQuote yaml 双引号转义(内部)。
func yamlQuote(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

// shQuote shell 单引号转义(内部,用于变量赋值)。
func shQuote(s string) string {
	return `'` + strings.ReplaceAll(s, `'`, `'\''`) + `'`
}

// randomHex 生成随机 hex 串(内部,密钥确定性来源)。
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("auto-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// Save 保存脚本到本机。
// 参数: cfg *model.ToolConfig(LastGen.Target 用于文件名); content 脚本全文; dir 目标目录(空 = 桌面)
// 返回: string 实际保存绝对路径; error 保存失败
func Save(cfg *model.ToolConfig, content, dir string) (string, error) {
	if dir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, "Desktop")
		} else {
			dir = os.TempDir()
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	target := cfg.LastGen.Target
	if target == "" {
		target = "all"
	}
	name := fmt.Sprintf("orbitcloud-deploy-%s-%s.sh", target, nowFn().Format("20060102-150405"))
	path := filepath.Join(dir, name)
	content = strings.ReplaceAll(content, "\r\n", "\n")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// repoRoot 定位仓库根(内部,兼容开发模式;发布模式不依赖仓库)。
func repoRoot() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	base := filepath.Dir(exe)
	for i := 0; i < 4; i++ {
		if _, err := os.Stat(filepath.Join(base, "deploy", "deploy.sh")); err == nil {
			return base
		}
		base = filepath.Dir(base)
	}
	return ""
}
