# M45-Core-goPool

[![Go CI](https://github.com/Distortions81/M45-Core-goPool/actions/workflows/ci.yml/badge.svg)](https://github.com/Distortions81/M45-Core-goPool/actions/workflows/ci.yml)
[![Go Vulncheck](https://github.com/Distortions81/M45-Core-goPool/actions/workflows/govulncheck.yml/badge.svg)](https://github.com/Distortions81/M45-Core-goPool/actions/workflows/govulncheck.yml)
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
- The codebase currently spans ~16,000 lines of code, backed by ~7,000 lines of tests.

Continuous integration runs on GitHub Actions to verify `go test ./...` and ensure code quality, plus a dedicated `Go Vulncheck` workflow runs `govulncheck ./...` to keep dependency vulnerabilities in check.

## Getting started

1. Build and run:
   ```bash
   go run main.go
   # or
   go build -o goPool && ./goPool
   ```
2. Edit configuration under `data/config/config.toml` (and `data/config/secrets.toml` only if you plan to force RPC credentials via `-allow-rpc-credentials`, otherwise point `node.rpc_cookie_path` at bitcoind's `.cookie` or let goPool auto-detect it through `$BITCOIN_DATADIR`, btcd's default `AppDataDir("btcd", false)/data` layout (per `btcsuite/btcd/rpcclient`), and the standard Linux locations). If you just want to override the auth cookie location for a single launch, use the `-rpc-cookie-path` flag instead of editing config files. To test against an open RPC endpoint such as `https://bitcoin-rpc.publicnode.com`, set `node.allow_public_rpc = true` alongside the desired `node.rpc_url`. See `operations.md` for deployment and tuning guidance.
3. Configure ZMQ block notifications so goPool can react to new tips without polling:
   ```toml
   node.zmq_block_addr = "tcp://<bitcoind-host>:28332"
   ```
   Bitcoind writes blocks to ZMQ from the default port `28332`; point `node.zmq_block_addr` at the same interface (use `127.0.0.1` when the node is local). Restart bitcoind with `zmqpubrawblock=tcp://0.0.0.0:28332` (or whichever interface you prefer) if that option is missing from your `bitcoin.conf`. goPool logs `watching ZMQ block notifications` when the connection succeeds and tracks disconnect/reconnect counts in the status endpoints, so check those logs if the feed ever degrades.
4. Point your miner at the Stratum listen address.
3. Point your miner at the Stratum listen address.
