# M45-Core-goPool

[![Go CI](https://github.com/Distortions81/M45-Core-goPool/actions/workflows/ci.yml/badge.svg)](https://github.com/Distortions81/M45-Core-goPool/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/Distortions81/M45-Core-goPool)](https://goreportcard.com/report/github.com/Distortions81/M45-Core-goPool)
[![License](https://img.shields.io/github/license/Distortions81/M45-Core-goPool)](https://github.com/Distortions81/M45-Core-goPool/blob/main/LICENSE)
[![Go Version](https://img.shields.io/github/go-mod/go-version/Distortions81/M45-Core-goPool)](https://go.dev)

Solo Bitcoin pool uses `bitcoind` over JSON-RPC + ZMQ and offers Stratum v1, optionally with TLS.

## Why goPool

- Split coinbase.
- Highly threaded for performance.
- Memory safety.
- Extensive tests in [TESTING.md](https://github.com/Distortions81/M45-Core-goPool/blob/main/TESTING.md) (`go test ./...`, scripts, `go test -cover`, etc.), ensuring regressions are caught before release.
- Compatibility tests run against [btcd](https://github.com/btcsuite/btcd) and [pogolo](https://github.com/pogodev/pogolo) (see `cross_impl_sanity_test.go`) to sanity check everything.
- The codebase currently spans ~13,000 lines of code, backed by ~5,500 lines of tests.

Continuous integration runs on GitHub Actions to verify `go test ./...` and publishes coverage to Codecov; check the coverage widget above or visit [codecov.io/gh/Distortions81/M45-Core-goPool](https://codecov.io/gh/Distortions81/M45-Core-goPool) for the latest report.

## Getting started

1. Build and run:
   ```bash
   go run main.go
   # or
   go build -o goPool && ./goPool
   ```
2. Edit configuration under `data/state/config.toml`/`secrets.toml` (see `operations.md` for the deployment and tuning guidance).
3. Point your miner at the Stratum listen address.
