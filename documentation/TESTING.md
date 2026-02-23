# Testing goPool

> **See also:** [Main README](../README.md) for setup and configuration.

goPool includes comprehensive test coverage:
- **Unit tests** for core pool logic
- **Compatibility tests** against btcd/pogolo behavior
- **End-to-end tests** for block construction and share validation
- **Fuzzing tests** for input validation

All tests run with the standard Go toolchain without external dependencies.

## Running Tests

### Full Test Suite

```bash
# Run all tests
go test ./...

# Run with verbose output
go test -v ./...

# Using the helper script
./scripts/run-tests.sh
```

### Individual Tests

```bash
# Run specific test
go test -v ./... -run TestBuildBlock_ParsesWithBtcdAndHasValidMerkle

# Run tests in the root package (this repo is a single Go package)
go test -v .

# Run with race detector
go test -race ./...
```

## Test Categories

### Core Pool Logic
- **`block_test.go`** - Block assembly, header construction, merkle tree building
- **`coinbase_test.go`** - Coinbase script layout and BIP34 height encoding
- **`difficulty_test.go`** - Bits/target/difficulty conversions
- **`pending_submissions_test.go`** - Pending submitblock replay and JSONL handling

### Status / API / Security
- **`path_traversal_test.go`** - Static file serving and path traversal hardening
- **`status_server_json_test.go`** - JSON endpoint behavior and response shaping
- **`status_workers_*_test.go`** - Worker page routing, saved-worker flows, and status helpers

### Compatibility Tests (btcd/pogolo)
- **`cross_impl_sanity_test.go`** - Address parsing, merkle trees, compact difficulty bits, script encoding
- **`pogolo_compat_test.go`** - Extranonce handling, difficulty/target mapping, version masks, coinbase construction, ntime behavior
- **`found_block_test.go`** - End-to-end block construction including dual-payout coinbases and pogolo-style layouts
- **`pow_compat_test.go`** - Header construction validated against btcd's `blockchain.CheckProofOfWork`
- **`difficulty_compat_test.go`** - `difficultyFromHash` validation against btcd's difficulty/work calculations
- **`witness_strip_compat_test.go`** - `stripWitnessData` validation comparing txid/wtxid against btcd's `TxHash`/`WitnessHash`
- **`block_validity_compat_test.go`** - Full-block validity via btcd `ProcessBlock` on regtest chain

### Wallet / Address Validation
- **`wallet_validation_test.go`** - Payout address validation and network-specific handling
- **`wallet_fuzz_test.go`** - Fuzz testing for address validation

### Accounting / Payouts
- **`payout_test.go`** - Payout accounting logic
- **`payout_debug_test.go`** - Debugging helpers for payout calculations
- **`worker_list_store_*_test.go`** - Saved-worker storage, migrations, and worker tracking persistence

### Performance / Timing
- **`submit_timing_test.go`** - Measures latency from `handleBlockShare` entry to `submitblock` invocation
- Benchmark suites live alongside the code as `*_bench_test.go` files; run them with `go test -run '^$' -bench . -benchmem ./...`.
- **`miner_decode_bench_test.go`** - Stratum decode microbenchmarks comparing full JSON unmarshal vs fast/manual sniffing for `ping`, `subscribe`, `authorize`, and `submit`.
- **`stratum_fastpath_bench_test.go`** - Stratum encode microbenchmarks comparing normal vs fast-path response encoding (`true`, `pong`, subscribe response in CKPool and expanded modes).

### Stratum Fast-Path Benchmarks

Use these commands to compare normal vs fast decode/encode paths without running unit tests:

```bash
# Decode comparison (full JSON unmarshal vs fast/manual sniff path)
go test -run '^$' -bench 'BenchmarkStratumDecode(FastJSON|Manual)' -benchmem .

# Encode comparison (normal vs fast response encoding)
go test -run '^$' -bench 'BenchmarkStratumEncode' -benchmem .

# Run both together
go test -run '^$' -bench 'BenchmarkStratum(Decode(FastJSON|Manual)|Encode)' -benchmem .
```

For more stable comparisons across changes/machines, run multiple samples and (optionally) compare with `benchstat`:

```bash
# Baseline / candidate example
go test -run '^$' -bench 'BenchmarkStratum(Decode(FastJSON|Manual)|Encode)' -benchmem -count=5 . > before.txt
go test -run '^$' -bench 'BenchmarkStratum(Decode(FastJSON|Manual)|Encode)' -benchmem -count=5 . > after.txt

# Optional (if benchstat is installed)
benchstat before.txt after.txt
```

### Stratum Fast-Path Benchmark Snapshot (example)

Example local run command:

```bash
go test -run '^$' -bench 'BenchmarkStratum(Decode(FastJSON|Manual)|Encode)' -benchmem -benchtime=100ms .
```

Environment for the sample numbers below:

- `goos`: `linux`
- `goarch`: `amd64`
- `cpu`: `AMD Ryzen 9 7950X 16-Core Processor`
- `pkg`: `goPool`

Key results (microbenchmarks):

- **Decode (`mining.submit`)**
  - Full decode (`fastJSONUnmarshal`): `366.6 ns/op`, `461 B/op`, `11 allocs/op`
  - Fast/manual sniff path: `107.3 ns/op`, `0 B/op`, `0 allocs/op`
  - Roughly **3.4x faster** with the fast path in this benchmark
- **Decode (`mining.ping`)**
  - Full decode: `129.8 ns/op`, `106 B/op`, `3 allocs/op`
  - Fast/manual sniff path: `39.22 ns/op`, `0 B/op`, `0 allocs/op`
  - Roughly **3.3x faster**
- **Encode (`true` response)**
  - Normal encode: `157.6 ns/op`, `204 B/op`, `4 allocs/op`
  - Fast encode: `48.34 ns/op`, `0 B/op`, `0 allocs/op`
  - Roughly **3.3x faster**
- **Encode (`pong` response)**
  - Normal encode: `168.9 ns/op`, `205 B/op`, `4 allocs/op`
  - Fast encode: `45.13 ns/op`, `0 B/op`, `0 allocs/op`
  - Roughly **3.7x faster**
- **Encode (`mining.subscribe`, CKPool mode)**
  - Normal encode: `346.7 ns/op`, `501 B/op`, `11 allocs/op`
  - Fast encode: `62.73 ns/op`, `0 B/op`, `0 allocs/op`
  - Roughly **5.5x faster**
- **Encode (`mining.subscribe`, expanded mode)**
  - Normal encode: `630.7 ns/op`, `1063 B/op`, `17 allocs/op`
  - Fast encode: `105.9 ns/op`, `0 B/op`, `0 allocs/op`
  - Roughly **6.0x faster**

Notes:

- These are **microbenchmarks** of parsing/encoding paths, not full end-to-end pool throughput benchmarks.
- Re-run on your target hardware and compare with `benchstat` before using the numbers for capacity planning.

### Hex Fast-Path Benchmarks

Hex encode/decode microbenchmarks live in `job_utils_hex_bench_test.go` and compare LUT-based helpers vs stdlib (`encoding/hex`) and alternate implementations.

Example focused command (decode + encode + uint32 hex parse):

```bash
go test -run '^$' -bench 'Benchmark(DecodeHexToFixedBytesBytes_(32_(PoolPairLUT|Std)|4_(PoolPairLUT|Std))|ParseUint32BEHexBytes_(LUT|Switch)|Encode(BytesToFixedHex_32_Std|32ToHex64Lower_(Unrolled|2ByteLUTLoop|LUTLoop)|ToString_32_(Std|StdStackBuf|Unrolled)))' -benchmem -benchtime=100ms .
```

Environment for the sample numbers below:

- `goos`: `linux`
- `goarch`: `amd64`
- `cpu`: `AMD Ryzen 9 7950X 16-Core Processor`
- `pkg`: `goPool`

Key results (microbenchmarks):

- **Decode 32-byte hex into fixed bytes**
  - stdlib `hex.Decode`: `20.64 ns/op`, `0 allocs/op`
  - goPool pair-LUT helper (`decodeHexToFixedBytesBytes`): `16.37 ns/op`, `0 allocs/op`
  - Roughly **1.26x faster** in this benchmark
- **Decode 4-byte hex into fixed bytes**
  - stdlib `hex.Decode`: `3.450 ns/op`, `0 allocs/op`
  - goPool pair-LUT helper (`decodeHexToFixedBytesBytes`): `3.360 ns/op`, `0 allocs/op`
  - Essentially **similar** performance in this benchmark
- **Parse 8-char uint32 hex (`parseUint32BEHexBytes`)**
  - LUT parser: `2.018 ns/op` (lower), `2.000 ns/op` (upper), `0 allocs/op`
  - switch parser: `4.042 ns/op` (lower), `4.489 ns/op` (upper), `0 allocs/op`
  - LUT path is roughly **2x faster**
- **Encode 32 bytes -> 64 hex bytes (byte buffer output)**
  - stdlib `hex.Encode`: `17.97 ns/op`, `0 allocs/op`
  - LUT loop: `15.03 ns/op`, `0 allocs/op`
  - 2-byte LUT loop: `18.73 ns/op`, `0 allocs/op`
  - Unrolled LUT encode: `8.139 ns/op`, `0 allocs/op`
  - Unrolled path is roughly **2.2x faster** than stdlib in this benchmark
- **Encode 32 bytes -> hex string**
  - `hex.EncodeToString`: `55.35 ns/op`, `128 B/op`, `2 allocs/op`
  - stdlib with stack buffer + `string(out[:])`: `33.65 ns/op`, `64 B/op`, `1 alloc/op`
  - unrolled encode + `string(out[:])`: `20.63 ns/op`, `64 B/op`, `1 alloc/op`
  - Fast path significantly reduces CPU time and cuts one allocation

Notes:

- These are **microbenchmarks** of helper functions (not end-to-end share processing).
- For change comparisons, use `-count` and `benchstat` as shown in the Stratum benchmark section above.

## CPU Profiling with Simulated Miners

Generate CPU profiles using the `TestGenerateGoProfileWithSimulatedMiners` test. This test is gated behind environment variables and doesn't run in the normal test suite.

### Generate Profile

```bash
GO_PROFILE_SIMULATED_MINERS=1 \
GO_PROFILE_MINER_COUNT=32 \
GO_PROFILE_DURATION=10s \
GO_PROFILE_OUTPUT=default.pgo \
go test -run TestGenerateGoProfileWithSimulatedMiners ./...
```

### Configuration Variables

- **`GO_PROFILE_SIMULATED_MINERS`** - Set to `1` to enable profiling
- **`GO_PROFILE_MINER_COUNT`** - Number of simulated miner goroutines
- **`GO_PROFILE_DURATION`** - Profile capture duration
- **`GO_PROFILE_OUTPUT`** - Output filename for the profile

### Analyze Profile

```bash
# Interactive analysis
go tool pprof default.pgo ./goPool

# Generate visualization
go tool pprof -http=:8080 default.pgo ./goPool

# Or use the helper script
./scripts/profile-graph.sh default.pgo profile.svg
```

## Code Coverage

### Quick Coverage Check

```bash
# Overall coverage summary
go test ./... -cover
```

### Detailed Coverage Analysis

```bash
# Generate coverage profile
go test ./... -coverprofile=coverage.out

# View function-by-function coverage
go tool cover -func=coverage.out

# Open visual coverage report in browser
go tool cover -html=coverage.out
```

### Current Coverage

As of February 23, 2026, `go test ./... -cover` reports approximately **36.0% of statements** covered. Highest coverage areas:

- **Share validation and block construction** - `block_test.go`, `coinbase_test.go`, `found_block_test.go`
- **Accounting and payout logic** - `payout_test.go`, `payout_debug_test.go`, `payout_wrapper_selection_test.go`
- **Compatibility layers** - `cross_impl_sanity_test.go`, `pogolo_compat_test.go`, `*_compat_test.go`

## Helper Scripts

### Run Tests Script

The [scripts/run-tests.sh](../scripts/run-tests.sh) script runs the full test suite with verbose output:

```bash
# Run all tests
./scripts/run-tests.sh

# Pass additional arguments to go test
./scripts/run-tests.sh -race
./scripts/run-tests.sh -run TestSpecificTest
```
