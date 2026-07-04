#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
bench_dir="${OPEN_POOL_BENCHMARK_DIR:-${repo_root}/.benchmarks/open-pool-benchmark}"
upstream="${OPEN_POOL_BENCHMARK_REPO:-https://github.com/eandersson/open-pool-benchmark.git}"
ref="${OPEN_POOL_BENCHMARK_REF:-main}"
overlay="${repo_root}/benchmarks/open-pool-benchmark"
src_dir="${bench_dir}/pools/gopool/src"

if ! command -v git >/dev/null 2>&1; then
  echo "git is required" >&2
  exit 1
fi

if ! docker compose version >/dev/null 2>&1; then
  echo "Docker with the compose plugin is required" >&2
  exit 1
fi

if [ ! -d "${bench_dir}/.git" ]; then
  mkdir -p "$(dirname "$bench_dir")"
  git clone --depth 1 --branch "$ref" "$upstream" "$bench_dir"
else
  git -C "$bench_dir" fetch --depth 1 origin "$ref"
  git -C "$bench_dir" checkout --detach FETCH_HEAD >/dev/null
fi

cp "${overlay}/pools.yml" "${bench_dir}/pools.yml"
mkdir -p "${bench_dir}/pools/gopool"
cp "${overlay}/pools/gopool/bench.toml" "${bench_dir}/pools/gopool/bench.toml"
cp "${overlay}/pools/gopool/Dockerfile" "${bench_dir}/pools/gopool/Dockerfile"
cp "${overlay}/pools/gopool/entrypoint.sh" "${bench_dir}/pools/gopool/entrypoint.sh"

rm -rf "$src_dir"
mkdir -p "$src_dir"
tar -C "$repo_root" \
  --exclude='./.git' \
  --exclude='./.benchmarks' \
  --exclude='./data/logs' \
  --exclude='./data/state' \
  --exclude='./data/config/*.toml' \
  --exclude='./data/config/*.toml.bak' \
  --exclude='./goPool' \
  --exclude='./gopool' \
  --exclude='./*.test' \
  --exclude='./*.out' \
  -cf - . | tar -C "$src_dir" -xf -

cd "$bench_dir"

if [ "$#" -eq 0 ]; then
  set -- suite --pools gopool,pogolo,ckpool
fi

exec docker compose run --rm openbench "$@"
