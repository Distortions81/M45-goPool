#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
bench_dir="${GO_BENCHMARK_ENV_DIR:-${repo_root}/.benchmarks/go-benchmark-env}"
upstream="${GO_BENCHMARK_ENV_REPO:-https://github.com/eandersson/open-pool-benchmark.git}"
ref="${GO_BENCHMARK_ENV_REF:-main}"
overlay="${repo_root}/benchmarks/go/openbench"
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
  git -C "$bench_dir" reset --hard FETCH_HEAD >/dev/null
fi

cp "${overlay}/pools.yml" "${bench_dir}/pools.yml"
mkdir -p "${bench_dir}/pools/gopool"
cp "${overlay}/pools/gopool/bench.toml" "${bench_dir}/pools/gopool/bench.toml"
cp "${overlay}/pools/gopool/Dockerfile" "${bench_dir}/pools/gopool/Dockerfile"
cp "${overlay}/pools/gopool/entrypoint.sh" "${bench_dir}/pools/gopool/entrypoint.sh"

if [ "${GO_BENCHMARK_NO_ZMQ:-0}" = "1" ]; then
  sed -i \
    -e 's#^  zmq_hashblock_addr = .*#  zmq_hashblock_addr = ""#' \
    -e 's#^  zmq_rawblock_addr = .*#  zmq_rawblock_addr = ""#' \
    "${bench_dir}/pools/gopool/bench.toml"
  sed -i \
    -e "s#^zmq_host = .*#zmq_host = 'tcp://127.0.0.1:1'#" \
    "${bench_dir}/pools/pogolo/bench.toml"
  sed -i \
    -e 's#"zmqblock": .*#"zmqblock": "tcp://127.0.0.1:1",#' \
    "${bench_dir}/pools/ckpool/bench.conf"
fi

# Keep hashblock and rawblock on separate ZMQ sockets. ckpool's ZMQ block
# watcher expects the hashblock stream and logs size errors if rawblock frames
# are published on the same endpoint.
if [ -f "${bench_dir}/regtest/bitcoin.conf" ]; then
  sed -i \
    -e 's#^zmqpubhashblock=.*#zmqpubhashblock=tcp://0.0.0.0:28332#' \
    -e 's#^zmqpubrawblock=.*#zmqpubrawblock=tcp://0.0.0.0:28333#' \
    "${bench_dir}/regtest/bitcoin.conf"
fi
if [ -f "${bench_dir}/regtest/Dockerfile.bitcoind" ]; then
  sed -i \
    -e 's/# 18443 = regtest RPC, 28332 = zmq hashblock.*/# 18443 = regtest RPC, 28332 = zmq hashblock, 28333 = zmq rawblock/' \
    -e 's/^EXPOSE 18443 28332.*/EXPOSE 18443 28332 28333/' \
    "${bench_dir}/regtest/Dockerfile.bitcoind"
fi

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
  set -- list
fi

exec docker compose run --rm openbench "$@"
