# goPool Performance Report

Date: 2026-03-02

## Environment

- Host CPU: AMD Ryzen 9 7950X 16-Core Processor
- Logical CPUs: 32 (`nproc`)
- Go: go1.26.0 linux/amd64
- Kernel: Linux 6.8.0-101-generic

## Commands Run

```bash
go test -run '^$' -bench . -benchmem -count=1 ./... | tee benchmark_results.txt
lscpu > cpu_info.txt
```

Benchmark suite completed successfully:

- `PASS`
- `ok   goPool   111.738s`

## Key Benchmark Metrics

### Share Processing Throughput

- `BenchmarkProcessSubmissionTaskAcceptedShare-32`
  - `909.3 ns/share`
  - `1,099,772 shares/s`
  - `4,399,088 workers@15spm`
  - `3,079,361 workers@15spm_70pct`

- `BenchmarkHandleSubmitAndProcessAcceptedShare-32`
  - `550.5 ns/share`
  - `1,816,373 shares/s`
  - `7,265,493 workers@15spm`
  - `5,085,845 workers@15spm_70pct`

- `BenchmarkHandleSubmitAndProcessAcceptedShare_DupCheckEnabled-32`
  - `536.3 ns/share`
  - `1,864,528 shares/s`
  - `7,458,113 workers@15spm`
  - `5,220,679 workers@15spm_70pct`

### Status Build Capacity

- `BenchmarkBuildStatusData/100000_conns-32`
  - `2137 ns/conn`
  - `4679 conns@10ms`
  - `7019 conns@15ms`
  - `14038 conns@30ms`
  - `28077 conns@60ms`

### Memory Footprint (Workers)

From `BenchmarkEstimateMemory500kWorkers-32` logs:

- Estimated memory at 500k workers:
  - `heapAlloc=2.34 GiB`
  - `heapInuse=2.36 GiB`
  - `alloc≈4.90 KiB/worker`
  - `inuse≈4.96 KiB/worker`

## Estimated Max Miners on This CPU

Using the benchmark's built-in conversion at **15 shares/min per miner**:

- Conservative (70% headroom, lower measured path): **~3.08 million miners**
- Practical target from full submit+process path (70% headroom): **~5.09 million miners**
- Peak benchmark-only ceiling (no headroom): **~7.46 million miners**

## Recommendation

For capacity planning on this host CPU, use **~5.0 million miners @ 15 shares/min** as a practical CPU estimate, and **~3.0 million** as a stricter conservative floor when reserving more operational safety margin.

## Notes

- These are microbenchmarks and synthetic in-process paths.
- Real deployments will be lower due to network I/O, TLS, kernel scheduling, RPC/back-end dependencies, DB/fs activity, GC pressure, and mixed traffic patterns.
- If needed, rerun with `-count=5` and compare via `benchstat` for tighter confidence intervals.
