#!/usr/bin/env bash
# ============================================================
# OrbitCloud 一键构建脚本(Linux / macOS)
# 用法:
#   ./build/build.sh                     # 三平台(win/linux/darwin)后端交叉编译
#   ./build/build.sh --frontend          # 同步构建前端
#   ./build/build.sh --package           # 打 zip/tar.gz 包
#   ./build/build.sh --os linux --arch amd64   # 指定目标
#   ./build/build.sh --check             # 仅环境检查
# ============================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(dirname "$SCRIPT_DIR")"
DIST_ROOT="$SCRIPT_DIR/dist"
LOG_FILE="$DIST_ROOT/build.log"
VERSION="dev"
FRONTEND=false
PACKAGE=false
CHECK=false
OSES=(windows linux darwin)
ARCHS=(amd64 arm64)

while [[ $# -gt 0 ]]; do
  case "$1" in
    --frontend)  FRONTEND=true ;;
    --package)   PACKAGE=true ;;
    --check)     CHECK=true ;;
    --version)   VERSION="$2"; shift ;;
    --os)        IFS=',' read -r -a OSES <<< "$2"; shift ;;
    --arch)      IFS=',' read -r -a ARCHS <<< "$2"; shift ;;
    -h|--help)
      sed -n '2,10p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) echo "未知参数: $1"; exit 1 ;;
  esac
  shift
done

step() { printf "\n==> %s\n" "$1"; }
info() { printf "    %s\n" "$1"; }
ok()   { printf "    [OK] %s\n" "$1"; }

mkdir -p "$DIST_ROOT"
: > "$LOG_FILE"
log() { printf '[%s] %s\n' "$(date +%H:%M:%S)" "$1" >> "$LOG_FILE"; }

# ---------- 环境检查 ----------
step "环境检查"
command -v go >/dev/null 2>&1 || { echo "未找到 Go(需 1.25+): https://go.dev/dl/"; exit 1; }
info "$(go version)"
log "$(go version)"
if [[ "$FRONTEND" == true ]]; then
  command -v node >/dev/null 2>&1 || { echo "未找到 Node.js(需 ≥18): https://nodejs.org/"; exit 1; }
  command -v npm  >/dev/null 2>&1 || { echo "未找到 npm"; exit 1; }
  info "node $(node --version) / npm $(npm --version)"
fi
info "目标: ${OSES[*]} × ${ARCHS[*]} | 前端:$FRONTEND | 打包:$PACKAGE | 版本:$VERSION"
[[ "$CHECK" == true ]] && { ok "环境检查通过"; exit 0; }

if [[ "$VERSION" == "dev" ]]; then
  TAG="$(git -C "$REPO_ROOT" describe --tags --abbrev=0 2>/dev/null || true)"
  [[ -n "$TAG" ]] && VERSION="${TAG#v}"
fi

# ---------- 1. 前端 ----------
if [[ "$FRONTEND" == true ]]; then
  step "构建前端(frontend/)"
  if [[ ! -d "$REPO_ROOT/frontend/node_modules" ]]; then
    info "先执行 npm install ..."
    (cd "$REPO_ROOT/frontend" && npm install)
  fi
  (cd "$REPO_ROOT/frontend" && npm run build) | tee -a "$LOG_FILE"
  ok "前端产物: frontend/dist"
fi

# ---------- 2. 后端交叉编译 ----------
declare -a PKG_DIRS=()
for os in "${OSES[@]}"; do
  for arch in "${ARCHS[@]}"; do
    step "编译 $os/$arch"
    exe="orbitcloud"; [[ "$os" == "windows" ]] && exe="orbitcloud.exe"
    pkg_dir="orbitcloud-${os}-${arch}-${VERSION}"
    out_dir="$DIST_ROOT/$pkg_dir"
    mkdir -p "$out_dir"

    (cd "$REPO_ROOT" && \
     GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 \
     go build -trimpath -ldflags "-s -w" -o "$out_dir/$exe" .) | tee -a "$LOG_FILE"

    if [[ "$FRONTEND" == true && -d "$REPO_ROOT/frontend/dist" ]]; then
      cp -r "$REPO_ROOT/frontend/dist" "$out_dir/web"
      info "已附带前端产物 → $pkg_dir/web/"
    fi

    HASH="$(cd "$out_dir" && shasum -a 256 "$exe" | awk '{print $1}')"
    echo "$HASH  $exe" >> "$out_dir/sha256sums.txt"
    ok "$out_dir/$exe ($(du -h "$out_dir/$exe" | awk '{print $1}'))"
    log "built $os/$arch -> $out_dir/$exe ($HASH)"
    PKG_DIRS+=("$pkg_dir")
  done
done

# ---------- 3. 打包 ----------
if [[ "$PACKAGE" == true ]]; then
  step "打包"
  for pkg_dir in "${PKG_DIRS[@]}"; do
    (cd "$DIST_ROOT" && tar -czf "$pkg_dir.tar.gz" "$pkg_dir")
    ok "$DIST_ROOT/$pkg_dir.tar.gz"
    log "packed $pkg_dir.tar.gz"
  done
fi

# ---------- 4. 汇总 ----------
step "构建完成"
info "产物目录: $DIST_ROOT"
info "构建日志: $LOG_FILE"
info "提示: 各平台运行前先 <binary> -initConfig 生成配置, 再 <binary> --add-superadmin <user> <pass> 创建管理员"
