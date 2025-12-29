# Testing goPool

> **See also:** [Main README](https://github.com/Distortions81/M45-Core-goPool/blob/main/README.md) for setup, configuration, and usage.

This project includes a mix of unit tests, compatibility tests against btcd/pogolo
behaviour, and end-to-end style tests that exercise block construction and share
validation. All tests run with the standard Go toolchain; no external services
are required.

## Running the tests

To run the full test suite:

```bash
go test ./...
```

To run with verbose output:

```bash
go test -v ./...
```

You can also run individual tests or files using the usual `-run` and package
selectors, for example:

```bash
go test -v ./... -run TestBuildBlock_ParsesWithBtcdAndHasValidMerkle
```

## Test categories

- **Core pool logic**
  - `block_test.go` - block assembly and header/merkle construction.
  - `coinbase_test.go` - coinbase script layout and BIP34 height encoding.
  - `difficulty_test.go` - bits/target/difficulty conversions.
  - `pending_submissions_test.go` - pending submitblock replay and JSONL handling.

- **Status / API / security**
  - `path_traversal_test.go` - static file serving and path traversal hardening.
  - `worker_status_test.go` - worker status view and privacy redaction behaviour.

- **Compatibility tests**
  - `cross_impl_sanity_test.go` - compatibility with btcd address parsing,
    merkle trees, compact difficulty bits, and script encoding.
  - `pogolo_compat_test.go` - behavioural compatibility with the pogolo pool
    around extranonce handling, difficulty/target mapping, version masks,
    coinbase construction, and ntime behaviour.
  - `found_block_test.go` - end-to-end block construction tests, including
    dual-payout coinbases and pogolo-style block layouts.
  - `pow_compat_test.go` - builds headers via goPool and asks btcd
    `blockchain.CheckProofOfWork` to validate our PoW view.
  - `difficulty_compat_test.go` - checks `difficultyFromHash` against btcd's
    difficulty/work calculations for known targets.
  - `witness_strip_compat_test.go` - validates `stripWitnessData` by comparing
    txid/wtxid against btcd's `TxHash` / `WitnessHash` for SegWit and legacy
    transactions.
  - `block_validity_compat_test.go` - builds a block with goPool's share path
    and feeds it into btcd `ProcessBlock` on a regtest chain (with PoW checks
    disabled) to confirm full-block validity, not just serialization.

- **Wallet / address validation**
  - `wallet_validation_test.go`, `wallet_fuzz_test.go` - payout address
    validation, fuzz tests, and network-specific address handling.

- **Accounting / payouts**
  - `payout_test.go`, `payout_debug_test.go` - payout accounting logic and
    debugging helpers.
  - `worker_status_test.go` - worker accounting and best-share tracking.

- **Performance / timing**
  - `submit_timing_test.go` - measures the time from entering the
    `handleBlockShare` path to the point where `submitblock` is invoked, using
    a fake `rpcCaller` so the test does not depend on network latency.
  - `performance.md` - benchmark notes and capacity planning guidance.

## Profiling with simulated miners

The `TestGenerateGoProfileWithSimulatedMiners` helper can capture a CPU
profile while a configurable number of simulated miners exercise hashing and
big-int work. It is gated behind an environment variable so it never runs as
part of the normal suite. To emit a profile:

```bash
GO_PROFILE_SIMULATED_MINERS=1 \
GO_PROFILE_MINER_COUNT=32 \
GO_PROFILE_DURATION=10s \
GO_PROFILE_OUTPUT=default.pgo \
go test -run TestGenerateGoProfileWithSimulatedMiners ./...
```

Use `GO_PROFILE_MINER_COUNT` to adjust how many goroutines run, `GO_PROFILE_DURATION`
to shorten or lengthen the capture window, and `GO_PROFILE_OUTPUT` to change the
file name written by `runtime/pprof`. After the test completes, feed the output
via `go tool pprof default.pgo ./gopool` (or whichever binary you profiled) to
inspect or convert the profile.

## Rough code coverage

For a quick, overall coverage number:

```bash
go test ./... -cover
```

To generate a coverage profile and inspect it in detail:

```bash
go test ./... -coverprofile=coverage.out
go tool cover -func=coverage.out   # function-by-function coverage
go tool cover -html=coverage.out   # open in a browser for a visual view
```

As of the latest changes, running:

```bash
go test ./... -cover
```

locally reports coverage of about **24.6% of statements** (this will drift as
tests are added), with the highest coverage concentrated in:

- share validation and block/coinbase construction (`block_test.go`,
  `coinbase_test.go`, `found_block_test.go`)
- accounting and payout logic (`payout_test.go`, `payout_debug_test.go`,
  `worker_status_test.go`)
- btcd/pogolo compatibility shims (`cross_impl_sanity_test.go`,
  `pogolo_compat_test.go`, the *_compat_test.go files above)

## Scripted test run

For convenience there is a helper script under `scripts/` that runs the full
test suite with verbose output and forwards any additional arguments to
`go test`. See `scripts/run-tests.sh` for details.
