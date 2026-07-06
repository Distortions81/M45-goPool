# Go benchmarks

Benchmarks for `mining.submit` and `mining.notify`. Use
`scripts/run-go-benchmarks.sh` to reproduce the full local regtest benchmark
suite and regenerate the SVG.

## Current results

Unpinned rerun from July 6, 2026 on a 32-logical-CPU host.

The goPool benchmark profile keeps normal submit-validation checks and vardiff
enabled. Connection-rate limits and invalid-submit bans are relaxed so synthetic
`10000`-miner reject-load runs can complete.

![Benchmark heat map](heatmap.svg)

Every numeric value is a colored tile. Each metric column is scaled
independently; green is better and red is worse.

The SVG includes `mining.submit`, `mining.notify` with ZMQ/default pool
configuration, and `mining.notify` with pool-side ZMQ disabled.

## Reproduce

Run the full benchmark suite from the repository root:

```bash
./scripts/run-go-benchmarks.sh
```

The script builds the Go probes, prepares the open-pool-benchmark regtest
environment, starts one pool at a time, runs `100`, `1000`, and `10000` miner
cases, writes raw JSONL/log output under `reports/go-benchmarks/`, and
regenerates `benchmarks/go/heatmap.svg`. CPU pinning is disabled by default.

Useful knobs:

```bash
GO_BENCH_POOLS=gopool,pogolo,ckpool
GO_BENCH_MINERS=100,1000,10000
GO_BENCH_SUBMIT_DURATION=8s
GO_BENCH_NOTIFY_ROUNDS=5
GO_BENCH_OUT_DIR=reports/go-benchmarks
GO_BENCH_SVG=benchmarks/go/heatmap.svg
GO_BENCH_RENDER_SVG=1
```

For a quick wiring check without replacing the committed SVG:

```bash
GO_BENCH_POOLS=gopool GO_BENCH_MINERS=100 GO_BENCH_SUBMIT_WARMUP=1s \
GO_BENCH_SUBMIT_DURATION=1s GO_BENCH_NOTIFY_ROUNDS=1 GO_BENCH_RENDER_SVG=0 \
./scripts/run-go-benchmarks.sh
```

To render an SVG again from a saved JSONL run:

```bash
./scripts/render-go-benchmark-heatmap.py reports/go-benchmarks/<run>.jsonl \
  -o benchmarks/go/heatmap.svg
```

The lower-level environment wrapper is still available for manual inspection:

```bash
./scripts/go-benchmark-env.sh --help
```
