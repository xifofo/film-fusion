#!/usr/bin/env bash
# 本地构建唯一的 AdminBase 前端并组装 dist；不构建镜像、不推送、不启动后端。
# ADMIN_FRONTEND_DIR 可覆盖默认的兄弟仓库位置。

set -euo pipefail

BACKEND_DIR="$(cd "$(dirname "$0")" && pwd)"
ADMIN_FRONTEND_DIR="${ADMIN_FRONTEND_DIR:-$BACKEND_DIR/../film-fusion-admin}"

if [[ ! -f "$ADMIN_FRONTEND_DIR/apps/web/package.json" ]]; then
  echo "[build] 新版前端目录不存在或缺少 apps/web/package.json: $ADMIN_FRONTEND_DIR" >&2
  echo "[build] 请准备 film-fusion-admin 源码，或设置 ADMIN_FRONTEND_DIR" >&2
  exit 1
fi

ADMIN_FRONTEND_DIR="$(cd "$ADMIN_FRONTEND_DIR" && pwd)"

# 使用前端要求的 Node 版本；仅在当前版本不兼容时尝试已有 nvm。
select_frontend_node() {
  local node_compatible
  node_compatible="$(node -p 'const [major, minor] = process.versions.node.split(".").map(Number); Number(major > 22 || (major === 22 && minor >= 12))' 2>/dev/null || echo 0)"
  if [[ "$node_compatible" != "1" ]]; then
    local nvm_dir="${NVM_DIR:-$HOME/.nvm}"
    if [[ -s "$nvm_dir/nvm.sh" ]]; then
      # shellcheck source=/dev/null
      . "$nvm_dir/nvm.sh"
      nvm use 22 >/dev/null 2>&1 || true
    fi
  fi

  node_compatible="$(node -p 'const [major, minor] = process.versions.node.split(".").map(Number); Number(major > 22 || (major === 22 && minor >= 12))' 2>/dev/null || echo 0)"
  if [[ "$node_compatible" != "1" ]]; then
    echo "[build] 前端需要 Node.js 22.12+，请切换版本后重试" >&2
    exit 1
  fi
  echo "==> 使用 Node: $(node -v) ($(command -v node))"
}

# 沿用工程自身的构建命令，新版工程会同时执行 TypeScript 检查。
build_frontend() {
  (
    cd "$1"
    if [[ ! -d node_modules ]]; then
      pnpm install --frozen-lockfile
    fi
    pnpm build
  )
}

select_frontend_node
command -v pnpm >/dev/null 2>&1 || { echo "[build] 缺少 pnpm，请先安装 pnpm 10+" >&2; exit 1; }

echo "==> [1/2] 构建新版（/）: $ADMIN_FRONTEND_DIR"
# 同一个镜像使用同源 API，不将开发环境的跨域地址带入新版发布产物。
VITE_API_BASE_URL=/api build_frontend "$ADMIN_FRONTEND_DIR"

if [[ ! -f "$ADMIN_FRONTEND_DIR/apps/web/dist/index.html" ]]; then
  echo "[build] 新版前端缺少 index.html，保留现有 dist" >&2
  exit 1
fi

# 先完整组装产物，再替换现有 dist；任一构建或拷贝失败均保留上次的可用产物。
staging_dir="$(mktemp -d "$BACKEND_DIR/.frontend-dist.XXXXXX")"
cleanup() {
  if [[ -d "$staging_dir/previous" && ! -e "$BACKEND_DIR/dist" ]]; then
    mv "$staging_dir/previous" "$BACKEND_DIR/dist" || return
  fi
  rm -rf "$staging_dir"
}
trap cleanup EXIT

mkdir -p "$staging_dir/dist"
cp -R "$ADMIN_FRONTEND_DIR/apps/web/dist/." "$staging_dir/dist/"

echo "==> [2/2] 更新前端产物: $BACKEND_DIR/dist"
if [[ -e "$BACKEND_DIR/dist" ]]; then
  mv "$BACKEND_DIR/dist" "$staging_dir/previous"
fi
mv "$staging_dir/dist" "$BACKEND_DIR/dist"
echo "==> 完成：新版 /，API /api，不包含旧版前端"
