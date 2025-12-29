# Performance (Operator Notes)

This is a practical, operator-friendly reference for “how much can this box
handle?” using simple benchmarks. It focuses on **CPU only** (network ignored).

These numbers are meant as a *ballpark*, not a guarantee. Real deployments will
hit other limits too (file descriptors, memory, kernel/network overhead, TLS,
disk logging, etc.).

## Reference machine (for the numbers below)

- CPU: AMD Ryzen 9 7950X 16-Core Processor
- OS/Arch: linux/amd64
- Go: go1.24.11

## Quick capacity picture (CPU-only, network ignored)

We assume **15 shares/min per worker** for rough planning.

- **Share handling headroom is huge on this CPU.** Even including submit
  parsing/validation, we measured about **~1.16M shares/sec** of CPU throughput.
  At 15 shares/min/worker (0.25 shares/sec/worker), that’s a *theoretical*
  **~4.6M workers at 100% CPU** for share checking alone.
- **What will limit “snappy dashboard” first is status rebuilding**, because it
  scales with the number of connected miners and is paid periodically (and on
  the first request after the cache expires).

In other words: for “web UI feels fast”, plan around the dashboard/status work,
not around share hashing.

## What uses CPU (and what it means)

- **Every share submitted by miners**: parse the message, validate it, do the
  proof-of-work checks, update stats, and send a response. More shares/min per
  worker means more CPU per worker.
- **Keeping the dashboard fresh**: the server rebuilds a status snapshot by
  scanning connections. With more connected miners, this takes longer.
- **Serving the web/API**: turning that snapshot into JSON responses costs some
  CPU, but it’s usually smaller than the snapshot rebuild itself.

## “Low-latency max workers” (dashboard rebuild budget)

If you want the UI to feel responsive, a simple rule of thumb is:

“How many connected workers can we scan/rebuild in **X ms**?”

On the reference 7950X, measured rebuild budgets are roughly:

- ~`2.7k` workers @ `5ms`
- ~`5.3k` workers @ `10ms`
- ~`8.0k` workers @ `15ms`
- ~`16.0k` workers @ `30ms`
- ~`32.1k` workers @ `60ms`

Notes:

- The status snapshot is cached and only rebuilt about once per
  `defaultRefreshInterval` (currently 10s), so most requests are “cheap reads”.
  The “spike” happens on rebuild.
- At **15 shares/min per worker**, share CPU is not the limiting factor at these
  worker counts (e.g. `10k workers` ≈ `2.5k shares/sec`, far below the measured
  share-processing throughput).

## Re-running these numbers on your hardware

Run the two benchmarks:

```bash
go test -run '^$' -bench 'BenchmarkHandleSubmitAndProcessAcceptedShare$' -benchmem
go test -run '^$' -bench 'BenchmarkBuildStatusData$' -benchmem
```

If you want a CPU profile (to see what’s taking time):

```bash
go test -run '^$' -bench 'BenchmarkHandleSubmitAndProcessAcceptedShare$' -cpuprofile cpu_submit.out
go tool pprof -top ./goPool.test cpu_submit.out
```
