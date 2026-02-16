#!/usr/bin/env bash
set -euo pipefail

IMAGE_REPO="${IMAGE_REPO:-ghcr.io/distortions81/gopool-umbrel}"
IMAGE_TAG="${1:-v0.1.0}"
BUILD_VERSION="${BUILD_VERSION:-${IMAGE_TAG}}"
BUILD_TIME="${BUILD_TIME:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"
BUILDER_NAME="${BUILDER_NAME:-gopool-builder}"

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
APP_COMPOSE_PATH="${ROOT_DIR}/apps/gopool/docker-compose.yml"
DOCKERFILE_PATH="${ROOT_DIR}/apps/gopool/Dockerfile"
IMAGE_REF="${IMAGE_REPO}:${IMAGE_TAG}"

if ! command -v docker >/dev/null 2>&1; then
  echo "docker is required"
  exit 1
fi

if ! docker info >/dev/null 2>&1; then
  echo "docker daemon is not available"
  exit 1
fi

if ! docker buildx inspect "${BUILDER_NAME}" >/dev/null 2>&1; then
  docker buildx create --name "${BUILDER_NAME}" --driver docker-container --use >/dev/null
else
  docker buildx use "${BUILDER_NAME}" >/dev/null
fi

docker run --privileged --rm tonistiigi/binfmt --install arm64 >/dev/null
docker buildx inspect --bootstrap >/dev/null

echo "Building and pushing ${IMAGE_REF} for linux/amd64,linux/arm64..."
docker buildx build \
  --builder "${BUILDER_NAME}" \
  --platform linux/amd64,linux/arm64 \
  --file "${DOCKERFILE_PATH}" \
  --build-arg "BUILD_TIME=${BUILD_TIME}" \
  --build-arg "BUILD_VERSION=${BUILD_VERSION}" \
  --tag "${IMAGE_REF}" \
  --push \
  "${ROOT_DIR}"

INSPECT_OUTPUT="$(docker buildx imagetools inspect "${IMAGE_REF}")"
DIGEST="$(printf '%s\n' "${INSPECT_OUTPUT}" | sed -nE 's/^Digest:[[:space:]]+(sha256:[0-9a-f]{64})$/\1/p' | head -n1)"
if [[ -z "${DIGEST}" ]]; then
  echo "failed to resolve digest for ${IMAGE_REF}"
  exit 1
fi

PINNED_IMAGE="${IMAGE_REF}@${DIGEST}"
TMP_FILE="$(mktemp)"
awk -v pinned="${PINNED_IMAGE}" '
  {
    if (!done && $1 == "image:") {
      print "    image: " pinned
      done = 1
      next
    }
    print
  }
  END {
    if (!done) {
      exit 1
    }
  }
' "${APP_COMPOSE_PATH}" > "${TMP_FILE}"
mv "${TMP_FILE}" "${APP_COMPOSE_PATH}"

echo "Pinned image:"
echo "  ${PINNED_IMAGE}"
echo "Updated compose:"
echo "  ${APP_COMPOSE_PATH}"
