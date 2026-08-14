#!/usr/bin/env bash
# 一键打包 film-fusion 主程序与 RSS Generator Worker 镜像：
#   1) 进入前端目录构建（pnpm build）
#   2) 拷贝前端 dist 到后端 dist
#   3) docker build 主程序多平台镜像（默认推送到 registry）
#   4) docker build Worker 多平台镜像（默认推送到 registry）
#
# 用法:
#   ./build.sh v3.0.8              # 默认：构建 + 推送两张镜像，各包含 v3.0.8 和 latest
#   ./build.sh v3.0.8 --no-push    # 构建两张镜像但不推送（本地校验用）
#
# 可通过环境变量覆盖默认行为:
#   IMAGE_REPO=kumayi/film-fusion                              # 主程序镜像仓库
#   WORKER_IMAGE_REPO=kumayi/film-fusion-rss-generator-worker # Worker 镜像仓库
#   FRONTEND_DIR=../film-fusion-frontend                       # 前端代码目录
#   WORKER_DIR=./rss-generator-worker                          # Worker 构建上下文
#   PLATFORMS=linux/amd64,linux/arm64                          # 目标平台
#   DOCKER_BUILD_ARGS="--no-cache"                             # 额外透传给两次 docker build
#   TAG_LATEST=0                                               # 关闭两个镜像的 latest tag

set -euo pipefail

VERSION="${1:-}"
MODE="${2:-}"
if [[ -z "$VERSION" ]]; then
  echo "Usage: $0 <version> [--no-push]" >&2
  echo "  e.g. $0 v3.0.8" >&2
  exit 1
fi

PUSH_FLAG="--push"
case "$MODE" in
  ""|--push) PUSH_FLAG="--push" ;;
  --no-push) PUSH_FLAG="" ;;
  *) echo "[build] 未知模式: $MODE（支持: --push | --no-push）" >&2; exit 1 ;;
esac

IMAGE_REPO="${IMAGE_REPO:-kumayi/film-fusion}"
IMAGE="${IMAGE_REPO}:${VERSION}"
IMAGE_LATEST="${IMAGE_REPO}:latest"
WORKER_IMAGE_REPO="${WORKER_IMAGE_REPO:-kumayi/film-fusion-rss-generator-worker}"
WORKER_IMAGE="${WORKER_IMAGE_REPO}:${VERSION}"
WORKER_IMAGE_LATEST="${WORKER_IMAGE_REPO}:latest"
PLATFORMS="${PLATFORMS:-linux/amd64,linux/arm64}"
DOCKER_BUILD_ARGS="${DOCKER_BUILD_ARGS:-}"
# 是否同时打 latest tag（默认开启，可用 TAG_LATEST=0 关闭）
TAG_LATEST="${TAG_LATEST:-1}"

BACKEND_DIR="$(cd "$(dirname "$0")" && pwd)"
FRONTEND_DIR_DEFAULT="$BACKEND_DIR/../film-fusion-frontend"
FRONTEND_DIR="${FRONTEND_DIR:-$FRONTEND_DIR_DEFAULT}"
WORKER_DIR="${WORKER_DIR:-$BACKEND_DIR/rss-generator-worker}"

if [[ ! -d "$FRONTEND_DIR" ]]; then
  echo "[build] 前端目录不存在: $FRONTEND_DIR" >&2
  echo "[build] 可通过 FRONTEND_DIR=/path/to/frontend 覆盖" >&2
  exit 1
fi
FRONTEND_DIR="$(cd "$FRONTEND_DIR" && pwd)"

if [[ ! -f "$WORKER_DIR/Dockerfile" ]]; then
  echo "[build] Worker Dockerfile 不存在: $WORKER_DIR/Dockerfile" >&2
  echo "[build] 可通过 WORKER_DIR=/path/to/rss-generator-worker 覆盖" >&2
  exit 1
fi
WORKER_DIR="$(cd "$WORKER_DIR" && pwd)"

select_frontend_node() {
  command -v node >/dev/null 2>&1 || { echo "[build] 缺少 node，请先安装 Node.js 20.19+ 或 22.12+" >&2; exit 1; }

  local node_compatible
  node_compatible="$(node -p 'const [major, minor] = process.versions.node.split(".").map(Number); Number((major === 20 && minor >= 19) || major >= 22)' 2>/dev/null || echo 0)"

  if [[ "$node_compatible" != "1" ]]; then
    local nvm_dir="${NVM_DIR:-$HOME/.nvm}"
    if [[ -s "$nvm_dir/nvm.sh" ]]; then
      # shellcheck source=/dev/null
      . "$nvm_dir/nvm.sh"
      nvm use 22 >/dev/null 2>&1 || nvm use 20 >/dev/null 2>&1 || true
    fi
  fi

  node_compatible="$(node -p 'const [major, minor] = process.versions.node.split(".").map(Number); Number((major === 20 && minor >= 19) || major >= 22)' 2>/dev/null || echo 0)"
  if [[ "$node_compatible" != "1" ]]; then
    echo "[build] 当前 Node $(node -v) 与 Vite 不兼容，请切换到 Node 20.19+ 或 22.12+ 后重试" >&2
    echo "[build] 示例: export NVM_DIR=\"\$HOME/.nvm\" && . \"\$NVM_DIR/nvm.sh\" && nvm use 22" >&2
    exit 1
  fi

  echo "==> 使用 Node: $(node -v) ($(command -v node))"
}

select_frontend_node
command -v pnpm >/dev/null 2>&1 || { echo "[build] 缺少 pnpm，请先安装" >&2; exit 1; }
command -v docker >/dev/null 2>&1 || { echo "[build] 缺少 docker" >&2; exit 1; }

echo "==> [1/4] 构建前端: $FRONTEND_DIR"
(
  cd "$FRONTEND_DIR"
  if [[ ! -d node_modules ]]; then
    if [[ -f pnpm-lock.yaml ]]; then
      pnpm install --frozen-lockfile
    else
      pnpm install
    fi
  fi
  pnpm build
)

if [[ ! -d "$FRONTEND_DIR/dist" ]]; then
  echo "[build] 前端 dist 不存在，构建可能失败" >&2
  exit 1
fi

echo "==> [2/4] 同步 dist 到 $BACKEND_DIR/dist"
rm -rf "$BACKEND_DIR/dist"
mkdir -p "$BACKEND_DIR/dist"
# 拷贝前端 dist 内容（含隐藏文件）到后端 dist 下
cp -R "$FRONTEND_DIR/dist/." "$BACKEND_DIR/dist/"

TAG_ARGS=( -t "$IMAGE" )
IMAGE_TAGS_DESC="$IMAGE"
WORKER_TAG_ARGS=( -t "$WORKER_IMAGE" )
WORKER_IMAGE_TAGS_DESC="$WORKER_IMAGE"
if [[ "$TAG_LATEST" == "1" ]]; then
  TAG_ARGS+=( -t "$IMAGE_LATEST" )
  IMAGE_TAGS_DESC="$IMAGE, $IMAGE_LATEST"
  WORKER_TAG_ARGS+=( -t "$WORKER_IMAGE_LATEST" )
  WORKER_IMAGE_TAGS_DESC="$WORKER_IMAGE, $WORKER_IMAGE_LATEST"
fi

if [[ -n "$PUSH_FLAG" ]]; then
  echo "==> [3/4] docker build 主程序: $IMAGE_TAGS_DESC  (platforms=$PLATFORMS, 推送到 registry)"
else
  echo "==> [3/4] docker build 主程序: $IMAGE_TAGS_DESC  (platforms=$PLATFORMS, 不推送)"
fi
# shellcheck disable=SC2086
docker build \
  --platform="$PLATFORMS" \
  "${TAG_ARGS[@]}" \
  $PUSH_FLAG \
  $DOCKER_BUILD_ARGS \
  "$BACKEND_DIR"

if [[ -n "$PUSH_FLAG" ]]; then
  echo "==> [4/4] docker build Worker: $WORKER_IMAGE_TAGS_DESC  (platforms=$PLATFORMS, 推送到 registry)"
else
  echo "==> [4/4] docker build Worker: $WORKER_IMAGE_TAGS_DESC  (platforms=$PLATFORMS, 不推送)"
fi
# shellcheck disable=SC2086
docker build \
  --platform="$PLATFORMS" \
  "${WORKER_TAG_ARGS[@]}" \
  $PUSH_FLAG \
  $DOCKER_BUILD_ARGS \
  "$WORKER_DIR"

echo "==> 完成主程序: $IMAGE_TAGS_DESC"
echo "==> 完成 Worker: $WORKER_IMAGE_TAGS_DESC"
