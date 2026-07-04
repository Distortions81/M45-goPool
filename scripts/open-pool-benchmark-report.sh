#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
bench_dir="${OPEN_POOL_BENCHMARK_DIR:-${repo_root}/.benchmarks/open-pool-benchmark}"
timestamp="$(date -u +%Y%m%dT%H%M%SZ)"

pools="${OPEN_POOL_REPORT_POOLS:-gopool,pogolo,ckpool}"
conns="${OPEN_POOL_REPORT_CONNS:-1 4 16 64}"
workers="${OPEN_POOL_REPORT_WORKERS:-4}"
pipeline="${OPEN_POOL_REPORT_PIPELINE:-16}"
warmup="${OPEN_POOL_REPORT_WARMUP:-2}"
duration="${OPEN_POOL_REPORT_DURATION:-8}"
profile="${OPEN_POOL_REPORT_PROFILE:-validation}"
out_dir="${OPEN_POOL_REPORT_DIR:-${repo_root}/reports/open-pool-benchmark}"
label="${OPEN_POOL_REPORT_LABEL:-open-pool-sweep-${timestamp}}"

mkdir -p "$out_dir"

csv_path="${out_dir}/${label}.csv"
report_path="${out_dir}/${label}.txt"
workspace_csv_dir="report-output"
workspace_csv="${workspace_csv_dir}/${label}.csv"

mkdir -p "${bench_dir}/${workspace_csv_dir}"

"${repo_root}/scripts/open-pool-benchmark.sh" sweep \
  --pools "$pools" \
  --profile "$profile" \
  --conns "$conns" \
  --workers "$workers" \
  --pipeline "$pipeline" \
  --warmup "$warmup" \
  --duration "$duration" \
  --csv "$workspace_csv" \
  --out results \
  --label "$label"

cp "${bench_dir}/${workspace_csv}" "$csv_path"

python3 - "$csv_path" "$report_path" <<'PY'
from __future__ import annotations

import csv
import pathlib
import sys

csv_path = pathlib.Path(sys.argv[1])
report_path = pathlib.Path(sys.argv[2])

with csv_path.open(newline="", encoding="utf-8") as handle:
    rows = list(csv.reader(handle))

if not rows:
    raise SystemExit(f"empty CSV: {csv_path}")

headers, data = rows[0], rows[1:]
widths = [len(header) for header in headers]
for row in data:
    for index, cell in enumerate(row):
        widths[index] = max(widths[index], len(cell))

lines = []
for row in [headers, *data]:
    lines.append("  ".join(cell.rjust(widths[index]) for index, cell in enumerate(row)))

report_path.write_text("\n".join(lines) + "\n", encoding="utf-8")
print(report_path)
PY
