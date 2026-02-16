#!/usr/bin/env bash
set -euo pipefail

UMBREL_REPO_URL="${UMBREL_REPO_URL:-https://github.com/getumbrel/umbrel.git}"
UMBREL_BRANCH="${UMBREL_BRANCH:-master}"
UMBREL_DIR="${UMBREL_DIR:-$HOME/umbrel-dev}"
NO_UPDATE=0

usage() {
  cat <<'EOF'
Run Umbrel's Linux development environment (umbrel-dev).

Usage:
  scripts/run-umbrel-linux.sh [options] [command] [command-args...]

Options:
  --dir PATH       Local checkout path (default: $HOME/umbrel-dev)
  --branch NAME    Git branch/tag to use (default: master)
  --repo URL       Umbrel git repo URL
  --no-update      Skip git fetch/pull when repo already exists
  -h, --help       Show this help

Common commands:
  start (default), logs, shell, stop, restart, reset, destroy
  client -- <rpc> [args]

Examples:
  scripts/run-umbrel-linux.sh
  scripts/run-umbrel-linux.sh logs
  scripts/run-umbrel-linux.sh client -- apps.install.mutate -- --appId gopool
EOF
}

require_cmd() {
  local cmd="$1"
  if ! command -v "${cmd}" >/dev/null 2>&1; then
    echo "missing required command: ${cmd}"
    exit 1
  fi
}

if [[ "$(uname -s)" != "Linux" ]]; then
  echo "this script is for Linux only"
  exit 1
fi

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dir)
      UMBREL_DIR="$2"
      shift 2
      ;;
    --branch)
      UMBREL_BRANCH="$2"
      shift 2
      ;;
    --repo)
      UMBREL_REPO_URL="$2"
      shift 2
      ;;
    --no-update)
      NO_UPDATE=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      break
      ;;
  esac
done

ACTION="${1:-start}"
if [[ $# -gt 0 ]]; then
  shift
fi
ACTION_ARGS=("$@")

require_cmd git
require_cmd docker

if ! docker info >/dev/null 2>&1; then
  echo "docker daemon is not available"
  exit 1
fi

if [[ -d "${UMBREL_DIR}/.git" ]]; then
  if [[ "${NO_UPDATE}" -eq 0 ]]; then
    git -C "${UMBREL_DIR}" fetch --prune origin
    git -C "${UMBREL_DIR}" checkout "${UMBREL_BRANCH}"
    if git -C "${UMBREL_DIR}" show-ref --quiet "refs/remotes/origin/${UMBREL_BRANCH}"; then
      git -C "${UMBREL_DIR}" pull --ff-only origin "${UMBREL_BRANCH}"
    fi
  fi
elif [[ -e "${UMBREL_DIR}" ]]; then
  echo "path exists but is not a git checkout: ${UMBREL_DIR}"
  exit 1
else
  git clone --branch "${UMBREL_BRANCH}" "${UMBREL_REPO_URL}" "${UMBREL_DIR}"
fi

cd "${UMBREL_DIR}"

if [[ ! -x "./scripts/umbrel-dev" ]]; then
  echo "umbrel-dev script not found at ${UMBREL_DIR}/scripts/umbrel-dev"
  exit 1
fi

./scripts/umbrel-dev "${ACTION}" "${ACTION_ARGS[@]}"
