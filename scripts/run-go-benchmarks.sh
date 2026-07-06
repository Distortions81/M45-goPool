#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

pools_csv="${GO_BENCH_POOLS:-gopool,pogolo,ckpool}"
miners_csv="${GO_BENCH_MINERS:-100,1000,10000}"
pipeline="${GO_BENCH_SUBMIT_PIPELINE:-1}"
warmup="${GO_BENCH_SUBMIT_WARMUP:-3s}"
duration="${GO_BENCH_SUBMIT_DURATION:-8s}"
notify_rounds="${GO_BENCH_NOTIFY_ROUNDS:-5}"
batch="${GO_BENCH_BATCH:-500}"
timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
out_dir="${GO_BENCH_OUT_DIR:-reports/go-benchmarks}"
jsonl="${GO_BENCH_JSONL:-${out_dir}/go-benchmarks-${timestamp}.jsonl}"
log="${GO_BENCH_LOG:-${out_dir}/go-benchmarks-${timestamp}.log}"
svg="${GO_BENCH_SVG:-benchmarks/go/heatmap.svg}"
render_svg="${GO_BENCH_RENDER_SVG:-1}"
bin_dir="${GO_BENCH_BIN_DIR:-.benchmarks/go-benchmark-bin}"
probe_image="${GO_BENCH_PROBE_IMAGE:-openbench-probes:latest}"
network="${GO_BENCH_NETWORK:-openbench-regtest_default}"
address="${GO_BENCH_ADDRESS:-bcrt1qlk935ze2fsu86zjp395uvtegztrkaezawxx0wf}"

case "$bin_dir" in
  /*) bin_path="$bin_dir" ;;
  *) bin_path="$repo_root/$bin_dir" ;;
esac

IFS=',' read -r -a pools <<< "$pools_csv"
IFS=',' read -r -a miners <<< "$miners_csv"

mkdir -p "$out_dir" "$bin_path"

log_msg() {
  printf '%s\n' "$*" | tee -a "$log"
}

append_metadata() {
  python3 - "$jsonl" <<'PY'
import datetime as dt
import json
import os
import sys

path = sys.argv[1]
row = {
    "kind": "metadata",
    "timestamp": dt.datetime.now(dt.UTC).replace(microsecond=0).isoformat().replace("+00:00", "Z"),
    "date": dt.datetime.now(dt.UTC).date().isoformat(),
    "host_cpus": os.cpu_count(),
    "pools": os.environ["GO_BENCH_POOLS_EFFECTIVE"].split(","),
    "miners": [int(v) for v in os.environ["GO_BENCH_MINERS_EFFECTIVE"].split(",")],
    "pinning": "disabled",
}
with open(path, "a", encoding="utf-8") as fh:
    fh.write(json.dumps(row, sort_keys=True) + "\n")
PY
}

pool_port() {
  case "$1" in
    gopool|ckpool) printf '3333' ;;
    pogolo) printf '5661' ;;
    *) echo "unknown pool: $1" >&2; exit 2 ;;
  esac
}

pool_extra_flags() {
  case "$1" in
    ckpool) printf '%s\n' "--worker-suffix=false" "--ordered-handshake" ;;
    *) ;;
  esac
}

cleanup_env() {
  docker rm -f openbench-pool >/dev/null 2>&1 || true
  ./scripts/go-benchmark-env.sh down >>"$log" 2>&1 || true
}

start_pool() {
  local pool="$1"
  local no_zmq="$2"
  log_msg "=== start pool=${pool} no_zmq=${no_zmq} ==="
  cleanup_env
  if [ "$no_zmq" = "1" ]; then
    GO_BENCHMARK_NO_ZMQ=1 ./scripts/go-benchmark-env.sh notify-fanout \
      --pools "$pool" \
      --conns 1 \
      --rounds 1 \
      --shards 1 \
      --no-pin \
      --out '' \
      --keep >>"$log" 2>&1 || true
  else
    ./scripts/go-benchmark-env.sh notify-fanout \
      --pools "$pool" \
      --conns 1 \
      --rounds 1 \
      --shards 1 \
      --no-pin \
      --out '' \
      --keep >>"$log" 2>&1 || true
  fi
  if ! docker ps --format '{{.Names}}' | grep -qx 'openbench-pool'; then
    echo "openbench-pool did not start for ${pool}; see ${log}" >&2
    exit 1
  fi
}

run_probe() {
  docker run --rm \
    --network "$network" \
    --ulimit nofile=65535 \
    -v "$bin_path:/bench:ro" \
    "$probe_image" "$@"
}

record_submit() {
  local pool="$1"
  local miner_count="$2"
  local port
  mapfile -t extra < <(pool_extra_flags "$pool")
  port="$(pool_port "$pool")"
  log_msg "--- submit pool=${pool} miners=${miner_count} ---"
  local output
  output="$(
    run_probe /bench/go-submit-bench \
      --host openbench-pool \
      --port "$port" \
      --address "$address" \
      --connections "$miner_count" \
      --pipeline "$pipeline" \
      --warmup "$warmup" \
      --duration "$duration" \
      --batch "$batch" \
      "${extra[@]}"
  )"
  printf '%s\n' "$output" | tee -a "$log"
  local payload
  payload="$(printf '%s\n' "$output" | tail -n 1)"
  python3 - "$jsonl" "$pool" "$miner_count" "$payload" <<'PY'
import json
import sys

path, pool, miners, payload = sys.argv[1:5]
data = json.loads(payload)
lat = data["latency_ms"]
row = {
    "kind": "submit",
    "pool": pool,
    "miners": int(miners),
    "validated_per_sec": data["validated_per_sec"],
    "p50": lat["p50"],
    "p95": lat["p95"],
    "p99": lat["p99"],
    "max": lat["max"],
    "connections": data["connections"],
    "pipeline": data["pipeline"],
    "duration_s": data["duration_s"],
    "submits": data["submits"],
    "accepts": data["accepts"],
    "rejects": data["rejects"],
    "errors": data["errors"],
}
with open(path, "a", encoding="utf-8") as fh:
    fh.write(json.dumps(row, sort_keys=True) + "\n")
PY
}

record_notify() {
  local pool="$1"
  local miner_count="$2"
  local mode="$3"
  local port
  mapfile -t extra < <(pool_extra_flags "$pool")
  port="$(pool_port "$pool")"
  log_msg "--- notify mode=${mode} pool=${pool} miners=${miner_count} ---"
  local output
  output="$(
    run_probe /bench/go-notify-fanout \
      --host openbench-pool \
      --port "$port" \
      --address "$address" \
      --rpc http://bitcoind:18443 \
      --rpc-user openbench \
      --rpc-pass openbenchpass \
      --connections "$miner_count" \
      --rounds "$notify_rounds" \
      --batch "$batch" \
      "${extra[@]}"
  )"
  printf '%s\n' "$output" | tee -a "$log"
  local result
  result="$(printf '%s\n' "$output" | awk '/^RESULT /{line=$0} END{print line}')"
  python3 - "$jsonl" "$pool" "$miner_count" "$mode" "$result" <<'PY'
import json
import re
import sys

path, pool, miners, mode, line = sys.argv[1:6]
pattern = re.compile(
    r"RESULT conns=(?P<conns>\d+) established=(?P<established>\d+) rounds=(?P<rounds>\d+) "
    r"best_round=(?P<best_round>\d+) received=(?P<received>\d+) avg=(?P<avg>[0-9.]+) "
    r"p50=(?P<p50>[0-9.]+) p95=(?P<p95>[0-9.]+) p99=(?P<p99>[0-9.]+) max=(?P<max>[0-9.]+) ms"
)
match = pattern.fullmatch(line.strip())
if not match:
    raise SystemExit(f"could not parse notify RESULT line: {line!r}")
row = {
    "kind": "notify",
    "mode": mode,
    "pool": pool,
    "miners": int(miners),
}
for key, value in match.groupdict().items():
    row[key] = int(value) if key in {"conns", "established", "rounds", "best_round", "received"} else float(value)
with open(path, "a", encoding="utf-8") as fh:
    fh.write(json.dumps(row, sort_keys=True) + "\n")
PY
}

: >"$log"
: >"$jsonl"
export GO_BENCH_POOLS_EFFECTIVE="$pools_csv"
export GO_BENCH_MINERS_EFFECTIVE="$miners_csv"
append_metadata

log_msg "go benchmark suite"
log_msg "jsonl=${jsonl}"
log_msg "log=${log}"
log_msg "svg=${svg}"
log_msg "pools=${pools_csv} miners=${miners_csv} pinning=disabled render_svg=${render_svg}"

CGO_ENABLED=0 go build -o "$bin_path/go-submit-bench" ./benchmarks/go/submit
CGO_ENABLED=0 go build -o "$bin_path/go-notify-fanout" ./benchmarks/go/notify-fanout

for pool in "${pools[@]}"; do
  start_pool "$pool" 0
  for miner_count in "${miners[@]}"; do
    record_submit "$pool" "$miner_count"
  done
  for miner_count in "${miners[@]}"; do
    record_notify "$pool" "$miner_count" "zmq"
  done
  cleanup_env
done

for pool in "${pools[@]}"; do
  start_pool "$pool" 1
  for miner_count in "${miners[@]}"; do
    record_notify "$pool" "$miner_count" "no-zmq"
  done
  cleanup_env
done

if [ "$render_svg" = "1" ]; then
  python3 scripts/render-go-benchmark-heatmap.py "$jsonl" -o "$svg"
  log_msg "wrote ${svg}"
fi
log_msg "wrote ${jsonl}"
