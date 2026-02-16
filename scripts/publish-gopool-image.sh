#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TOKEN_FILE="${ROOT_DIR}/scripts/token.txt"
PUBLISH_SCRIPT="${ROOT_DIR}/apps/gopool/publish-image.sh"
GHCR_USER="${GHCR_USER:-distortions81}"
IMAGE_TAG="${1:-v0.1.0}"

if ! command -v docker >/dev/null 2>&1; then
  echo "docker is required"
  exit 1
fi

if [[ ! -f "${TOKEN_FILE}" ]]; then
  echo "missing token file: ${TOKEN_FILE}"
  echo "create it with your GitHub PAT (classic), one line only"
  exit 1
fi

GHCR_PAT="$(tr -d '\r\n' < "${TOKEN_FILE}")"
if [[ -z "${GHCR_PAT}" ]]; then
  echo "token file is empty: ${TOKEN_FILE}"
  exit 1
fi

if [[ ! -x "${PUBLISH_SCRIPT}" ]]; then
  echo "publish script not found or not executable: ${PUBLISH_SCRIPT}"
  exit 1
fi

echo "${GHCR_PAT}" | docker login ghcr.io -u "${GHCR_USER}" --password-stdin
"${PUBLISH_SCRIPT}" "${IMAGE_TAG}"
