# Performance

This document summarizes the pool’s main CPU cost centers and the benchmarks we
use to keep them in check. Numbers vary by hardware and Go version; always run
the benchmarks on your target machine for capacity planning.

## What matters (CPU-only)

At a high level, pool CPU is dominated by:

- **Share submission verification**: per-share coinbase/header build, SHA256d,
  difficulty math, stats/accounting, and response serialization.
- **Status snapshot rebuild**: iterating over connections to rebuild the cached
  status snapshot used by the web UI and JSON endpoints.
- **JSON payload assembly**: per-endpoint censoring and slice copying for the
  specific fields that are actually returned.

This doc is focused on CPU. Network bandwidth, socket limits, and miner/network
latency will usually become constraints *before* pure CPU, depending on your
deployment.

## Status/UI architecture (why it scales)

- Status endpoints read a cached snapshot via `(*StatusServer).statusDataView()`
  and **only copy/censor what they serialize**, instead of deep-cloning the
  entire `StatusData`.
- Pool-wide `SharesPerSecond/SharesPerMinute` are tracked in `PoolMetrics` as a
  rolling 60s window so reads are **O(1)** (not a scan of all workers).
- Pool-wide `PoolHashrate` is tracked as an aggregate of per-connection rolling
  hashrate updates, so reads are **O(1)** (not a scan of all connections).
- The status snapshot itself (`buildStatusData`) is rebuilt at most once per
  `defaultRefreshInterval` (currently 10s) and then served from cache.

## Benchmarks

### Benchmark environment

Example numbers in this doc were captured on:

- CPU: AMD Ryzen 9 7950X 16-Core Processor
- OS/Arch: linux/amd64
- Go: go1.24.11

### Share processing throughput (CPU per accepted share)

Benchmark:

- `miner_submit_bench_test.go` → `BenchmarkHandleSubmitAndProcessAcceptedShare`

What it measures:

- The CPU cost of `handleSubmit` parsing/validation + duplicate-share check,
  plus `(*MinerConn).processSubmissionTask(...)` for an accepted (non-block)
  share, including stats/accounting + response JSON serialization.
- It uses a no-op `net.Conn`, so it excludes real kernel/network I/O.

Run:

```bash
go test -run '^$' -bench 'BenchmarkHandleSubmitAndProcessAcceptedShare$' -benchmem
```

Example (Ryzen 9 7950X, from a local run):

- ~`~0.85 µs/share` ⇒ ~`~1.18M shares/s` (CPU-only, no real network I/O)
- Converted to “workers” at **15 shares/min per worker**:
  `workers ≈ shares/s * 60 / 15` (≈ `shares/s * 4`)

Profiling:

```bash
go test -run '^$' -bench 'BenchmarkHandleSubmitAndProcessAcceptedShare$' -cpuprofile cpu_submit.out
go tool pprof -top ./goPool.test cpu_submit.out
```

### Status snapshot rebuild (CPU per connection)

Benchmark:

- `status_bench_test.go` → `BenchmarkBuildStatusData`

What it measures:

- The CPU cost of rebuilding the cached status snapshot (`buildStatusData`)
  as a function of live connection count.
- It reports `ns/conn` and a derived `conns@10ms` (how many connections could be
  processed within a 10ms CPU budget for a single rebuild). It also reports
  `conns@5ms`, `conns@15ms`, `conns@30ms`, and `conns@60ms`.

Run:

```bash
go test -run '^$' -bench 'BenchmarkBuildStatusData$' -benchmem
```

### Overview payload assembly (CPU per rendered worker)

Benchmark:

- `status_bench_test.go` → `BenchmarkOverviewPagePayload`

What it measures:

- The CPU cost of assembling the overview JSON response from the cached view,
  copying/censoring only the small slices the page returns.
- It reports `ns/recent_worker` (based on `RecentWork`, not total workers).

Run:

```bash
go test -run '^$' -bench 'BenchmarkOverviewPagePayload$' -benchmem
```

## Ballpark “max workers” (low-latency, CPU-only, network ignored)

For “web UI feels snappy”, the status snapshot rebuild time is a good
first-order bound because the first request after the cache TTL pays the rebuild
cost.

Treat these as rough CPU-only guidelines for “connected workers” on the example
benchmark machine above:

- ~`~5.2k workers @ 10ms` rebuild budget
- ~`~7.9k workers @ 15ms` rebuild budget
- ~`~15.7k workers @ 30ms` rebuild budget
- ~`~31.4k workers @ 60ms` rebuild budget

At **15 shares/min per worker**, share-processing CPU is typically not the
limiting factor at those sizes (e.g. `10k workers` ≈ `2.5k shares/s`).

## Interpreting “max workers” (CPU-only)

There is no single “max workers” number without assumptions. Capacity depends
on:

- **Shares per worker per minute** (VarDiff target): more shares/min means more
  work per worker.
- **Refresh interval + snapshot rebuild cost**: `buildStatusData` runs at most
  once per refresh interval, but it’s still O(connections).
- **UI payload size**: endpoints that render “all workers” will always be more
  expensive than “recent/top N”.

For CPU planning, treat these as separate budgets:

- A **share-processing budget** (shares/sec sustained).
- A **status rebuild budget** (one rebuild per refresh interval).
- A **UI payload budget** (per request).
