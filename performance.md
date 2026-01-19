# Performance & Capacity Planning

> **Quick Start:** See the [main README](README.md) for setup instructions.

This document provides practical capacity planning guidance for goPool operators. The benchmarks focus primarily on **CPU** with additional **network bandwidth** estimates for gigabit and 10 gigabit deployments.

**Important:** These numbers are ballpark estimates. Real-world deployments encounter additional limits including file descriptors, memory, kernel/network overhead, TLS processing, and disk I/O.

## Reference Hardware

All benchmarks below were conducted on:

- **CPU:** AMD Ryzen 9 7950X 16-Core Processor
- **OS/Arch:** linux/amd64
- **Go Version:** go1.24.11

## Executive Summary (CPU Only)

Assuming **15 shares/min per worker** (0.25 shares/sec):

- **Share processing:** ~1.16M shares/sec throughput = ~4.6M workers at 100% CPU (theoretical maximum)
- **Practical limit:** Status dashboard rebuilding, not share validation
- **Planning guideline:** Design around dashboard refresh performance for a responsive UI, not share hashing capacity

**Key takeaway:** Share validation has massive headroom. The status UI rebuild scales with connected miners and runs periodically, making it the primary bottleneck for "snappy dashboard" experience.

## CPU Consumption Breakdown

### Per-Share Processing
For each miner submission:
1. Parse Stratum message
2. Validate share format
3. Perform proof-of-work checks
4. Update worker statistics
5. Send response

**Impact:** Higher shares/min per worker = proportionally higher CPU usage

### Status Dashboard Rebuilding
Periodic snapshot generation by scanning all active connections:
- Runs every ~10 seconds (configurable via `defaultRefreshInterval`)
- Scales linearly with connected miner count
- Usually the limiting factor for UI responsiveness

### API/Web Serving
Converting status snapshots to JSON/HTML responses:
- Generally lower cost than snapshot rebuild
- Cached snapshots minimize repeated work

## Dashboard Rebuild Latency Targets

For a responsive UI, the key question is: **"How many workers can we scan/rebuild in X milliseconds?"**

### Benchmark Results (Reference 7950X)

| Target Latency | Max Workers (100% CPU) | Max Workers (~70% CPU) |
|---------------|------------------------|------------------------|
| 5ms           | ~2,700                 | ~1,900                 |
| 10ms          | ~5,300                 | ~3,700                 |
| 15ms          | ~8,000                 | ~5,600                 |
| 30ms          | ~16,000                | ~11,200                |
| 60ms          | ~32,100                | ~22,500                |

### Important Notes

- **Caching:** Status snapshots are cached and only rebuilt once per `defaultRefreshInterval` (~10s by default)
- **Most requests are fast:** Cached reads are inexpensive; rebuild latency spikes occur periodically
- **Share processing not the bottleneck:** At 15 shares/min/worker, even 10,000 workers only generate ~2,500 shares/sec, well below measured share-processing capacity (~1.16M shares/sec)

## Putting it together (realistic CPU-only ballparks)

There are two different “max worker” stories:

- **Average CPU load** (amortized over time): combines shares + periodic status
  rebuilds using their refresh intervals.
- **Worst-case latency** (spikes): how long a status rebuild takes when it runs.

### Average CPU load (70% CPU target)

Using the reference benchmarks:

- Share handling (15 shares/min/worker) costs ~`858 ns` per share.
  - Per worker: `0.25 shares/sec * 858 ns ≈ 215 ns/sec` of CPU time.
  - At 70% CPU: ballpark **~3.2M workers** for share processing alone.
- Status rebuild cost is ~`~1.9 µs/worker` per rebuild and happens every ~`10s`.
  - Per worker: `1.9 µs / 10s ≈ 190 ns/sec` of CPU time.
  - At 70% CPU: ballpark **~3.7M connected workers** for rebuild CPU alone.

If you combine those two costs (shares + rebuild CPU), the CPU-only “math max”
lands around **~1.7M connected workers at ~70% CPU** on this 7950X.

This number is intentionally conservative and still ignores real-world limits
like memory, goroutines, open sockets, TLS, and the kernel/network stack.

### Worst-case latency (UI “snappiness”)

Even if the *average* CPU is fine, very large worker counts can cause the
dashboard rebuild to take tens of milliseconds when it runs. For a UI that
“feels instant”, the rebuild budgets in the section above are the more useful
guide (e.g. ~`5k` workers @ `10ms`).

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

If you want a portable CPU profile suitable for PGO builds (and an easy SVG you
can open in a browser):

- `default.pgo` (CPU profile output): [default.pgo](default.pgo)
- `profile.svg` (Graphviz render): [profile.svg](profile.svg)

Generate `default.pgo` with the `-profile` flag (writes a 60s CPU profile), then
render it with:

```bash
./scripts/profile-graph.sh default.pgo profile.svg
```

## Saved workers dashboard (how many people can watch?)

The saved workers page refreshes every **5 seconds** and (usually) checks a
small list of saved workers. In the UI and DB we cap this at **64 saved workers
per user**; the common case is much smaller (e.g. 15).

On the reference 7950X, with a realistic “15 saved workers” list (10 online / 5
offline), the pool can serve roughly:

- ~`26k` saved-workers refreshes per second (CPU-only)
- That’s ~`130k` concurrent viewers refreshing every 5 seconds
- At ~70% CPU: ~`91k` concurrent viewers (CPU-only)

In practice, network/TLS overhead and whatever else the machine is doing will
reduce this, but the main takeaway is that “saved workers page viewers” are not
usually a CPU bottleneck compared to managing the miners themselves.

## Network ballparks (gigabit vs 10 gig)

These are **bandwidth-only** estimates (not CPU), assuming:

- **15 shares/min per worker** (0.25 shares/sec)
- Stratum traffic is “typical” (shares + responses + occasional `mining.notify`)
- We aim to use ~**70%** of link capacity to avoid living at the edge

As a conservative rule of thumb, plan for **~1 KB/sec per worker** of total
traffic (up + down). On this assumption:

- **1 Gbit**: ~`87k` workers (70% of 1 Gbit)
- **10 Gbit**: ~`875k` workers (70% of 10 Gbit)

If your miners/pool send more frequent or larger `mining.notify` messages, or
you’re using TLS everywhere, a safer “heavy traffic” assumption is **~2 KB/sec
per worker**, which halves the numbers:

- **1 Gbit (heavy)**: ~`44k` workers
- **10 Gbit (heavy)**: ~`438k` workers

In practice, long before you hit these bandwidth limits you may hit other real
world limits: file descriptors, kernel packet-per-second overhead, memory, and
the CPU/UI limits earlier in this document.
