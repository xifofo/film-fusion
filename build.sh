#!/usr/bin/env bash
# 一键打包 film-fusion 镜像：
#   1) 构建唯一的新版前端（/），组装到后端 dist
#   2) docker build 多平台镜像（默认推送到 registry）
#
# 用法:
#   ./build.sh v3.0.8              # 默认：构建 + 推送 v3.0.8 和 latest
#   ./build.sh v3.0.8 --no-push    # 仅构建，不推送（本地校验用）
#
# 可通过环境变量覆盖默认行为:
#   IMAGE_REPO=kumayi/film-fusion             # 镜像仓库
#   ADMIN_FRONTEND_DIR=../film-fusion-admin   # 新版前端代码目录
#   PLATFORMS=linux/amd64,linux/arm64         # 目标平台
#   DOCKER_BUILD_ARGS="--no-cache"            # 额外透传给 docker build
#   TAG_LATEST=0                              # 关闭 latest tag

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
PLATFORMS="${PLATFORMS:-linux/amd64,linux/arm64}"
DOCKER_BUILD_ARGS="${DOCKER_BUILD_ARGS:-}"
# 是否同时打 latest tag（默认开启，可用 TAG_LATEST=0 关闭）
TAG_LATEST="${TAG_LATEST:-1}"

BACKEND_DIR="$(cd "$(dirname "$0")" && pwd)"
command -v docker >/dev/null 2>&1 || { echo "[build] 缺少 docker" >&2; exit 1; }

echo "==> [1/2] 构建并组装新版前端"
"$BACKEND_DIR/build-frontends.sh"

TAG_ARGS=( -t "$IMAGE" )
IMAGE_TAGS_DESC="$IMAGE"
if [[ "$TAG_LATEST" == "1" ]]; then
  TAG_ARGS+=( -t "$IMAGE_LATEST" )
  IMAGE_TAGS_DESC="$IMAGE, $IMAGE_LATEST"
fi

if [[ -n "$PUSH_FLAG" ]]; then
  echo "==> [2/2] docker build: $IMAGE_TAGS_DESC  (platforms=$PLATFORMS, 推送到 registry)"
else
  echo "==> [2/2] docker build: $IMAGE_TAGS_DESC  (platforms=$PLATFORMS, 不推送)"
fi
# shellcheck disable=SC2086
docker build \
  --platform="$PLATFORMS" \
  "${TAG_ARGS[@]}" \
  $PUSH_FLAG \
  $DOCKER_BUILD_ARGS \
  "$BACKEND_DIR"
echo "==> 完成: $IMAGE_TAGS_DESC"
