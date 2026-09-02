#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/release.sh vMAJOR.MINOR.PATCH

Runs the Go test suite, validates UMBREL_VERSION, creates an annotated source
tag, and pushes main plus the tag. Set RELEASE_REMOTE to override origin.
EOF
}

if [[ $# -ne 1 ]]; then
  usage >&2
  exit 2
fi

release_tag="$1"
release_remote="${RELEASE_REMOTE:-origin}"

if [[ ! "${release_tag}" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z][0-9A-Za-z.-]*)?$ ]]; then
  echo "Invalid release tag: ${release_tag}" >&2
  exit 2
fi

repo_root="$(git rev-parse --show-toplevel)"
cd "${repo_root}"

if [[ -n "$(git status --porcelain)" ]]; then
  echo "The worktree must be clean before releasing." >&2
  exit 1
fi

umbrel_version="$(tr -d '[:space:]' < UMBREL_VERSION)"
if [[ ! "${umbrel_version}" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z][0-9A-Za-z.-]*)?$ ]]; then
  echo "UMBREL_VERSION must contain a semantic version such as 0.1.0." >&2
  exit 1
fi

if git rev-parse --verify --quiet "refs/tags/${release_tag}" >/dev/null; then
  echo "Tag already exists locally: ${release_tag}" >&2
  exit 1
fi
if git ls-remote --exit-code --tags "${release_remote}" "refs/tags/${release_tag}" >/dev/null 2>&1; then
  echo "Tag already exists on ${release_remote}: ${release_tag}" >&2
  exit 1
fi

go test ./...
git tag -a "${release_tag}" -m "M45 goPool ${release_tag} (Umbrel ${umbrel_version})"
git push "${release_remote}" main "refs/tags/${release_tag}"

echo "Released ${release_tag}; Umbrel package version ${umbrel_version}."
