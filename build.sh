#!/usr/bin/env bash
# 一键打包 film-fusion 镜像：
#   1) 进入前端目录构建（pnpm build）
#   2) 拷贝前端 dist 到后端 dist
#   3) docker build 多平台镜像（默认推送到 registry）
#
# 用法:
#   ./build.sh v2.1.6              # 默认：构建 + 推送
#   ./build.sh v2.1.6 --no-push    # 仅构建，不推送（本地校验用）
#
# 可通过环境变量覆盖默认行为:
#   IMAGE_REPO=kumayi/film-fusion              # 镜像仓库
#   FRONTEND_DIR=../film-fusion-frontend       # 前端代码目录
#   PLATFORMS=linux/amd64,linux/arm64          # 目标平台
#   DOCKER_BUILD_ARGS="--no-cache"             # 额外透传给 docker build 的参数

set -euo pipefail

VERSION="${1:-}"
MODE="${2:-}"
if [[ -z "$VERSION" ]]; then
  echo "Usage: $0 <version> [--no-push]" >&2
  echo "  e.g. $0 v2.1.6" >&2
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
PLATFORMS="${PLATFORMS:-linux/amd64,linux/arm64}"
DOCKER_BUILD_ARGS="${DOCKER_BUILD_ARGS:-}"

BACKEND_DIR="$(cd "$(dirname "$0")" && pwd)"
FRONTEND_DIR_DEFAULT="$BACKEND_DIR/../film-fusion-frontend"
FRONTEND_DIR="${FRONTEND_DIR:-$FRONTEND_DIR_DEFAULT}"

if [[ ! -d "$FRONTEND_DIR" ]]; then
  echo "[build] 前端目录不存在: $FRONTEND_DIR" >&2
  echo "[build] 可通过 FRONTEND_DIR=/path/to/frontend 覆盖" >&2
  exit 1
fi
FRONTEND_DIR="$(cd "$FRONTEND_DIR" && pwd)"

command -v pnpm >/dev/null 2>&1 || { echo "[build] 缺少 pnpm，请先安装" >&2; exit 1; }
command -v docker >/dev/null 2>&1 || { echo "[build] 缺少 docker" >&2; exit 1; }

echo "==> [1/3] 构建前端: $FRONTEND_DIR"
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

echo "==> [2/3] 同步 dist 到 $BACKEND_DIR/dist"
rm -rf "$BACKEND_DIR/dist"
mkdir -p "$BACKEND_DIR/dist"
# 拷贝前端 dist 内容（含隐藏文件）到后端 dist 下
cp -R "$FRONTEND_DIR/dist/." "$BACKEND_DIR/dist/"

if [[ -n "$PUSH_FLAG" ]]; then
  echo "==> [3/3] docker build: $IMAGE  (platforms=$PLATFORMS, 推送到 registry)"
else
  echo "==> [3/3] docker build: $IMAGE  (platforms=$PLATFORMS, 不推送)"
fi
# shellcheck disable=SC2086
docker build \
  --platform="$PLATFORMS" \
  -t "$IMAGE" \
  $PUSH_FLAG \
  $DOCKER_BUILD_ARGS \
  "$BACKEND_DIR"

echo "==> 完成: $IMAGE"
