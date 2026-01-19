# Testing goPool

> **See also:** [Main README](README.md) for setup and configuration.

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

# Run tests in specific package
go test -v ./stratum

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
- **`worker_status_test.go`** - Worker status view and privacy redaction

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
- **`worker_status_test.go`** - Worker accounting and best-share tracking

### Performance / Timing
- **`submit_timing_test.go`** - Measures latency from `handleBlockShare` entry to `submitblock` invocation
- See [performance.md](performance.md) for detailed benchmark notes and capacity planning

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

As of the latest changes, overall coverage is approximately **24.6% of statements**. Highest coverage areas:

- **Share validation and block construction** - `block_test.go`, `coinbase_test.go`, `found_block_test.go`
- **Accounting and payout logic** - `payout_test.go`, `payout_debug_test.go`, `worker_status_test.go`
- **Compatibility layers** - `cross_impl_sanity_test.go`, `pogolo_compat_test.go`, `*_compat_test.go`

## Helper Scripts

### Run Tests Script

The [scripts/run-tests.sh](scripts/run-tests.sh) script runs the full test suite with verbose output:

```bash
# Run all tests
./scripts/run-tests.sh

# Pass additional arguments to go test
./scripts/run-tests.sh -race
./scripts/run-tests.sh -run TestSpecificTest
```
