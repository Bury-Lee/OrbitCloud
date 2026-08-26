#!/usr/bin/env bash
# ============================================================
# OrbitCloud SMB 网关部署脚本(Linux, systemd)
#
# 步骤:
#   1. 安装二进制 /opt/orbitcloud/smb-gateway/smb-gateway
#   2. 生成 SMB.yaml(gateway.addr 指向本机后端私有 Socket 9101;
#      shared_key 复用 deploy-backend.sh 生成的密钥, 两侧必须一致)
#   3. 注册 systemd 服务并启动(监听 445, 需 root)
#
# 用法:
#   sudo ./deploy/deploy-smb-gateway.sh [--listen 0.0.0.0:445] [--log-level debug]
# ============================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(dirname "$SCRIPT_DIR")"
DIST="$REPO_ROOT/build/dist"

GW_HOME="/opt/orbitcloud/smb-gateway"
GW_BIN="$GW_HOME/smb-gateway"
GW_CFG="$GW_HOME/SMB.yaml"
KEY_FILE="/opt/orbitcloud/.smb-gateway-key"
LISTEN="0.0.0.0:445"
LOG_LEVEL="debug"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --listen) LISTEN="$2"; shift ;;
    --log-level) LOG_LEVEL="$2"; shift ;;
    -h|--help) sed -n '2,14p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "未知参数: $1"; exit 1 ;;
  esac
  shift
done

step() { printf "\n\033[1;36m==> %s\033[0m\n" "$1"; }
ok()   { printf "    \033[1;32m[OK]\033[0m %s\n" "$1"; }
warn() { printf "    \033[1;33m[WARN]\033[0m %s\n" "$1"; }

[[ "$(id -u)" -eq 0 ]] || { echo "请以 root 运行(sudo)"; exit 1; }
[[ -f "$DIST/smb-gateway" ]] || { echo "缺少产物 $DIST/smb-gateway, 先运行 deploy/build.sh"; exit 1; }
[[ -f "$KEY_FILE" ]] || { echo "缺少共享密钥 $KEY_FILE(先运行 deploy-backend.sh)"; exit 1; }
SHARED_KEY="$(cat "$KEY_FILE")"

# ---------- 1. 安装二进制 ----------
step "1. 安装二进制"
mkdir -p "$GW_HOME"
install -m 0755 "$DIST/smb-gateway" "$GW_BIN"
ok "$GW_BIN"

# ---------- 2. 配置 ----------
step "2. 生成 SMB.yaml"
cat > "$GW_CFG" <<EOF
# OrbitCloud SMB 网关配置(deploy-smb-gateway.sh 自动生成)
smb:
  listen: "$LISTEN"                # SMB 监听(root 权限下可用标准 445)
  netbios_name: "ORBITCLOUD"

gateway:
  addr: "127.0.0.1:9101"           # 必须与后端 smb_gateway.listen_addr 一致
  shared_key: "$SHARED_KEY"        # 与后端 smb_gateway.shared_key 一致
  shared_key_env: ""
  heartbeat_secs: 30
  sync_interval_secs: 60
  channel_buffer: 1024

log:
  level: "$LOG_LEVEL"
  output: "$GW_HOME/logs"
  file_prefix: "smb-gateway"
EOF
ok "$GW_CFG"

# ---------- 3. systemd 服务 ----------
step "3. 注册 systemd 服务"
cat > /etc/systemd/system/smb-gateway.service <<EOF
[Unit]
Description=OrbitCloud SMB Gateway (Rust)
After=network-online.target orbitcloud.service
Wants=orbitcloud.service

[Service]
Type=simple
WorkingDirectory=$GW_HOME
ExecStart=$GW_BIN
Restart=always
RestartSec=3
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable --now smb-gateway
sleep 2
systemctl is-active smb-gateway >/dev/null 2>&1 \
  && ok "smb-gateway 已启动(active)" \
  || { echo "smb-gateway 启动失败, 查看: journalctl -u smb-gateway -f"; exit 1; }

step "SMB 网关部署完成"
echo "    共享访问: \\\\<服务器IP>\\<桶名>(账号 = OrbitCloud 用户)"
echo "    日志    : journalctl -u smb-gateway -f"
