#!/usr/bin/env bash
# ============================================================
# OrbitCloud 构建脚本(Linux / macOS / WSL)
# 用法:
#   ./build/build.sh                     # 编译 linux/amd64 后端 + Rust SMB 网关
#   ./build/build.sh --all               # 三平台(win/linux/darwin amd64/arm64)交叉编译
#   ./build/build.sh --frontend          # 附带构建前端
#   ./build/build.sh --check             # 仅环境检查
# 产物:
#   build/dist/orbitcloud                后端二进制(部署用)
#   build/dist/smb-gateway               SMB 网关二进制(部署用)
#   build/dist/web/                      前端产物(可选)
# ============================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(dirname "$SCRIPT_DIR")"
DIST_ROOT="$SCRIPT_DIR/dist"
LOG_FILE="$DIST_ROOT/build.log"
VERSION="dev"
ALL=false
FRONTEND=false
CHECK=false
OSES=(linux)
ARCHS=(amd64)

while [[ $# -gt 0 ]]; do
  case "$1" in
    --all)       ALL=true ;;
    --frontend)  FRONTEND=true ;;
    --check)     CHECK=true ;;
    --version)   VERSION="$2"; shift ;;
    --os)        IFS=',' read -r -a OSES <<< "$2"; shift ;;
    --arch)      IFS=',' read -r -a ARCHS <<< "$2"; shift ;;
    -h|--help)
      sed -n '2,11p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) echo "未知参数: $1"; exit 1 ;;
  esac
  shift
done

[[ "$ALL" == true ]] && { OSES=(windows linux darwin); ARCHS=(amd64 arm64); }

step() { printf "\n==> %s\n" "$1"; }
info() { printf "    %s\n" "$1"; }
ok()   { printf "    [OK] %s\n" "$1"; }
die()  { printf "    [FAIL] %s\n" "$1" >&2; exit 1; }

mkdir -p "$DIST_ROOT"
: > "$LOG_FILE"
log() { printf '[%s] %s\n' "$(date +%H:%M:%S)" "$1" >> "$LOG_FILE"; }

# ---------- 环境检查 ----------
step "环境检查"
command -v go >/dev/null 2>&1 || die "未找到 Go(需 1.25+): https://go.dev/dl/"
info "$(go version)"
command -v cargo >/dev/null 2>&1 || die "未找到 cargo(需 Rust 1.95+): https://rustup.rs/"
info "$(cargo --version)"
if [[ "$FRONTEND" == true ]]; then
  command -v node >/dev/null 2>&1 || die "未找到 Node.js(需 ≥18): https://nodejs.org/"
  command -v npm  >/dev/null 2>&1 || die "未找到 npm"
  info "node $(node --version) / npm $(npm --version)"
fi
info "目标: ${OSES[*]} × ${ARCHS[*]} | 前端:$FRONTEND | 版本:$VERSION"
[[ "$CHECK" == true ]] && { ok "环境检查通过"; exit 0; }

if [[ "$VERSION" == "dev" ]]; then
  TAG="$(git -C "$REPO_ROOT" describe --tags --abbrev=0 2>/dev/null || true)"
  [[ -n "$TAG" ]] && VERSION="${TAG#v}"
fi

# ---------- 1. 前端(可选) ----------
if [[ "$FRONTEND" == true ]]; then
  step "构建前端(frontend/)"
  [[ -d "$REPO_ROOT/frontend/node_modules" ]] || (cd "$REPO_ROOT/frontend" && npm install)
  (cd "$REPO_ROOT/frontend" && npm run build) | tee -a "$LOG_FILE"
  rm -rf "$DIST_ROOT/web"
  cp -r "$REPO_ROOT/frontend/dist" "$DIST_ROOT/web"
  ok "前端产物: $DIST_ROOT/web"
fi

# ---------- 2. 后端编译 ----------
for os in "${OSES[@]}"; do
  for arch in "${ARCHS[@]}"; do
    step "编译后端 $os/$arch"
    exe="orbitcloud"; [[ "$os" == "windows" ]] && exe="orbitcloud.exe"
    pkg_dir="orbitcloud-${os}-${arch}-${VERSION}"
    out_dir="$DIST_ROOT/$pkg_dir"
    mkdir -p "$out_dir"
    (cd "$REPO_ROOT" && GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 \
      go build -trimpath -ldflags "-s -w" -o "$out_dir/$exe" .) | tee -a "$LOG_FILE"
    if [[ -d "$DIST_ROOT/web" ]]; then
      cp -r "$DIST_ROOT/web" "$out_dir/web"
    fi
    HASH="$(cd "$out_dir" && shasum -a 256 "$exe" | awk '{print $1}')"
    echo "$HASH  $exe" >> "$out_dir/sha256sums.txt"
    ok "$out_dir/$exe ($(du -h "$out_dir/$exe" | awk '{print $1}'))"
    log "built $os/$arch -> $out_dir/$exe ($HASH)"
  done
done

# 部署用后端产物(linux/amd64)
if [[ "${OSES[*]}" =~ linux && "${ARCHS[*]}" =~ amd64 ]]; then
  cp "$DIST_ROOT/orbitcloud-linux-amd64-${VERSION}/orbitcloud" "$DIST_ROOT/orbitcloud"
  ok "$DIST_ROOT/orbitcloud(部署用)"
fi

# ---------- 3. Rust SMB 网关 ----------
step "编译 SMB 网关(Rust, release)"
(cd "$REPO_ROOT/smb_server/rust" && cargo build --release) | tee -a "$LOG_FILE"
cp "$REPO_ROOT/smb_server/rust/target/release/smb-gateway" "$DIST_ROOT/smb-gateway"
ok "$DIST_ROOT/smb-gateway ($(du -h "$DIST_ROOT/smb-gateway" | awk '{print $1}'))"

# ---------- 4. 汇总 ----------
step "构建完成"
info "产物目录: $DIST_ROOT"
info "构建日志: $LOG_FILE"
