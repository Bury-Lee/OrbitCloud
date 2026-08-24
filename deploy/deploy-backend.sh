#!/usr/bin/env bash
# ============================================================
# OrbitCloud 后端部署脚本(Linux, systemd)
#
# 步骤:
#   1. 安装二进制 /opt/orbitcloud/bin/orbitcloud
#   2. 生成/校验 config.yaml(缺失则用 -initConfig 落盘, 提示修改)
#   3. 注入 smb_gateway 分节(shared_key 自动生成, 与 SMB 网关脚本共享)
#   4. 创建超级管理员(--add-superadmin, 已存在则跳过)
#   5. 注册 systemd 服务并启动
#
# 用法:
#   sudo ./deploy/deploy-backend.sh [--superadmin admin <密码>]
#   sudo ./deploy/deploy-backend.sh --reset      # 重置配置后全新部署
# ============================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(dirname "$SCRIPT_DIR")"
DIST="$REPO_ROOT/build/dist"

OC_HOME="/opt/orbitcloud"
OC_BIN="$OC_HOME/bin/orbitcloud"
OC_CFG_DIR="$OC_HOME/config"
OC_CFG="$OC_CFG_DIR/config.yaml"
KEY_FILE="$OC_HOME/.smb-gateway-key"   # 共享密钥(SMB 网关脚本复用; 敏感, 0600)

SUPERADMIN=""
SUPERADMIN_PASS=""
RESET=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --superadmin) SUPERADMIN="$2"; SUPERADMIN_PASS="$3"; shift 2 ;;
    --reset)      RESET=true ;;
    -h|--help) sed -n '2,16p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "未知参数: $1"; exit 1 ;;
  esac
  shift
done

step() { printf "\n\033[1;36m==> %s\033[0m\n" "$1"; }
ok()   { printf "    \033[1;32m[OK]\033[0m %s\n" "$1"; }
warn() { printf "    \033[1;33m[WARN]\033[0m %s\n" "$1"; }

[[ "$(id -u)" -eq 0 ]] || { echo "请以 root 运行(sudo)"; exit 1; }
[[ -f "$DIST/orbitcloud" ]] || { echo "缺少产物 $DIST/orbitcloud, 先运行 deploy/build.sh"; exit 1; }

# ---------- 1. 落位 ----------
step "1. 安装二进制"
mkdir -p "$OC_BIN" "$OC_CFG_DIR" "$OC_HOME/logs" "$OC_HOME/data"
install -m 0755 "$DIST/orbitcloud" "$OC_BIN/orbitcloud"
ok "$OC_BIN/orbitcloud"

# ---------- 2. 配置 ----------
step "2. 生成/校验 config.yaml"
if [[ ! -f "$OC_CFG" || "$RESET" == true ]]; then
  (cd "$OC_CFG_DIR" && "$OC_BIN/orbitcloud" -initConfig)
  warn "已生成默认配置 $OC_CFG —— 生产必改:"
  warn "  jwt.secret / storage.access_key / secret_key / nodes[].endpoint(或 driver: local)"
else
  ok "使用已有配置 $OC_CFG"
fi

# ---------- 3. 共享密钥(smb_gateway 分节) ----------
step "3. 注入 SMB 网关共享密钥"
if [[ ! -f "$KEY_FILE" ]]; then
  umask 077
  openssl rand -hex 32 > "$KEY_FILE"
  ok "已生成新密钥 → $KEY_FILE"
fi
SHARED_KEY="$(cat "$KEY_FILE")"

python3 - "$OC_CFG" "$SHARED_KEY" <<'PYEOF'
import sys, re
cfg, key = sys.argv[1], sys.argv[2]
text = open(cfg, encoding="utf-8").read()
block = ("smb_gateway:\n"
         "  listen_addr: \"127.0.0.1:9101\"\n"
         f"  shared_key: \"{key}\"\n"
         "  channel_buffer: 1024\n"
         "  max_concurrent: 64\n")
if re.search(r'^smb_gateway:', text, re.M):
    text = re.sub(r'^smb_gateway:.*?(?=^[a-z_]+:|\Z)', block, text, flags=re.S | re.M)
else:
    text = text.rstrip() + "\n\n" + block
open(cfg, "w", encoding="utf-8").write(text)
PYEOF
ok "smb_gateway 分节已写入(shared_key 与 SMB 网关一致)"

# ---------- 4. 超级管理员 ----------
step "4. 创建超级管理员"
if [[ -n "$SUPERADMIN" ]]; then
  if "$OC_BIN/orbitcloud" --add-superadmin "$SUPERADMIN" "$SUPERADMIN_PASS" 2>&1 | grep -q "created"; then
    ok "超级管理员 $SUPERADMIN 已创建"
  else
    warn "超级管理员已存在或创建失败(用户名冲突时忽略, 用 --reset 可重来)"
  fi
else
  warn "未指定 --superadmin, 跳过(可后续执行: $OC_BIN/orbitcloud --add-superadmin <user> <pass>)"
fi

# ---------- 5. systemd 服务 ----------
step "5. 注册 systemd 服务"
cat > /etc/systemd/system/orbitcloud.service <<EOF
[Unit]
Description=OrbitCloud (HTTP + SMB Gateway)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=$OC_CFG_DIR
ExecStart=$OC_BIN/orbitcloud
Restart=always
RestartSec=3
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable --now orbitcloud
sleep 2
systemctl is-active orbitcloud >/dev/null 2>&1 \
  && ok "orbitcloud 已启动(active)" \
  || { echo "orbitcloud 启动失败, 查看: journalctl -u orbitcloud -f"; exit 1; }

step "后端部署完成"
echo "    API    : http://<服务器IP>:8080/api/v1"
echo "    配置   : $OC_CFG"
echo "    日志   : journalctl -u orbitcloud -f"
