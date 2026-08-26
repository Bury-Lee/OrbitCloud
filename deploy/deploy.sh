#!/usr/bin/env bash
# ============================================================
# OrbitCloud 一键部署(Linux, systemd)
# 用法:
#   sudo ./deploy/deploy.sh                          # 编译 + 部署全部组件
#   sudo ./deploy/deploy.sh --skip-build             # 跳过编译(用已有产物)
#   sudo ./deploy/deploy.sh --only-backend           # 仅部署后端
#   sudo ./deploy/deploy.sh --only-smb               # 仅部署 SMB 网关
#   sudo ./deploy/deploy.sh --only-frontend          # 仅部署前端(需 nginx)
#   sudo ./deploy/deploy.sh --superadmin admin <密码> # 创建/确认超级管理员
# 产物来源: build/dist/{orbitcloud,smb-gateway,web}(由 build/build.sh 生成)
# ============================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(dirname "$SCRIPT_DIR")"
DIST="$SCRIPT_DIR/dist"

SKIP_BUILD=false
ONLY_BACKEND=false
ONLY_SMB=false
ONLY_FRONTEND=false
SUPERADMIN=""
SUPERADMIN_PASS=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --skip-build)    SKIP_BUILD=true ;;
    --only-backend)  ONLY_BACKEND=true ;;
    --only-smb)      ONLY_SMB=true ;;
    --only-frontend) ONLY_FRONTEND=true ;;
    --superadmin)    SUPERADMIN="$2"; SUPERADMIN_PASS="$3"; shift 2 ;;
    -h|--help)
      sed -n '2,12p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) echo "未知参数: $1"; exit 1 ;;
  esac
  shift
done

step() { printf "\n\033[1;36m==> %s\033[0m\n" "$1"; }
ok()   { printf "    \033[1;32m[OK]\033[0m %s\n" "$1"; }
warn() { printf "    \033[1;33m[WARN]\033[0m %s\n" "$1"; }

[[ "$(id -u)" -eq 0 ]] || { echo "请以 root 运行(sudo)"; exit 1; }

# 工具链探测(root 下 PATH 可能不含普通用户安装的 go/cargo)
for cand in "$HOME/go/bin" /usr/local/go/bin /usr/local/bin; do
  [[ -x "$cand/go" ]] && { export PATH="$cand:$PATH"; break; }
done
command -v go >/dev/null 2>&1 || {
  for h in /home/*; do [[ -x "$h/go/bin/go" ]] && { export PATH="$h/go/bin:$PATH"; break; }; done
}
for cand in "$HOME/.cargo/bin" /usr/local/cargo/bin; do
  [[ -x "$cand/cargo" ]] && { export PATH="$cand:$PATH"; break; }
done
command -v cargo >/dev/null 2>&1 || {
  for h in /home/*; do [[ -x "$h/.cargo/bin/cargo" ]] && { export PATH="$h/.cargo/bin:$PATH"; break; }; done
}

# ---------- 1. 编译 ----------
if [[ "$SKIP_BUILD" == false && "$ONLY_FRONTEND" == false && "$ONLY_SMB" == false && "$ONLY_BACKEND" == false ]]; then
  step "编译全部组件"
  bash "$REPO_ROOT/build/build.sh"
else
  step "跳过编译(--skip-build/仅部分部署),使用已有产物"
fi

# ---------- 2. 后端 ----------
if [[ "$ONLY_SMB" == false && "$ONLY_FRONTEND" == false ]]; then
  if [[ -n "$SUPERADMIN" ]]; then
    "$SCRIPT_DIR/deploy-backend.sh" --superadmin "$SUPERADMIN" "$SUPERADMIN_PASS"
  else
    "$SCRIPT_DIR/deploy-backend.sh"
  fi
fi

# ---------- 3. SMB 网关 ----------
if [[ "$ONLY_BACKEND" == false && "$ONLY_FRONTEND" == false ]]; then
  "$SCRIPT_DIR/deploy-smb-gateway.sh"
fi

# ---------- 4. 前端(nginx) ----------
deploy_frontend() {
  local web_home="/opt/orbitcloud/web"
  local nginx_conf="/etc/nginx/conf.d/orbitcloud.conf"
  [[ -d "$REPO_ROOT/build/dist/web" ]] || { warn "缺少前端产物 build/dist/web,跳过(先运行 build/build.sh --frontend)"; return; }
  command -v nginx >/dev/null 2>&1 || { warn "未安装 nginx(apt install -y nginx),跳过"; return; }

  step "部署前端"
  rm -rf "$web_home"
  cp -r "$REPO_ROOT/build/dist/web" "$web_home"
  ok "$web_home"

  cat > "$nginx_conf" <<EOF
server {
    listen 80;
    server_name _;

    root $web_home;
    index index.html;

    location / { try_files \$uri \$uri/ /index.html; }

    location /api/ {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        proxy_buffering off;
        proxy_read_timeout 600s;
        client_max_body_size 0;
    }
}
EOF
  nginx -t >/dev/null 2>&1 && systemctl reload nginx || { warn "nginx 配置校验失败,未 reload"; }
  ok "nginx 已配置($nginx_conf)"
}
if [[ "$ONLY_BACKEND" == false && "$ONLY_SMB" == false ]]; then
  deploy_frontend
fi

# ---------- 5. 汇总 ----------
step "部署完成"
ok "后端   : http://<服务器IP>:8080(API)  systemd: orbitcloud"
ok "SMB 网关: \\\\<服务器IP>\\<桶名> 端口 445   systemd: smb-gateway"
ok "前端   : http://<服务器IP>(Nginx 托管)"
warn "首次使用: 浏览器访问 API 或用命令创建桶; SMB 共享名 = 桶名"
warn "对象存储: 若未部署 RustFS/MinIO, 后端配置 storage.driver 需为 local(见 OrbitCloud.yaml)"
