# <a href="https://github.com/Distortions81/M45-Core-goPool/blob/main/data/www/logo.png"><img src="https://raw.githubusercontent.com/Distortions81/M45-Core-goPool/main/data/www/logo.png" alt="goPool logo" width="32" height="32" style="vertical-align: middle;"></a> M45-Core-goPool (goPool)

[![Go CI](https://github.com/Distortions81/M45-Core-goPool/actions/workflows/ci.yml/badge.svg)](https://github.com/Distortions81/M45-Core-goPool/actions/workflows/ci.yml)
[![Go Vulncheck](https://github.com/Distortions81/M45-Core-goPool/actions/workflows/govulncheck.yml/badge.svg)](https://github.com/Distortions81/M45-Core-goPool/actions/workflows/govulncheck.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/Distortions81/M45-Core-goPool)](https://goreportcard.com/report/github.com/Distortions81/M45-Core-goPool)
[![License](https://img.shields.io/github/license/Distortions81/M45-Core-goPool)](https://github.com/Distortions81/M45-Core-goPool/blob/main/LICENSE)
[![Go Version](https://img.shields.io/github/go-mod/go-version/Distortions81/M45-Core-goPool)](https://go.dev)

Solo Bitcoin pool that connects to Bitcoin Core (`bitcoind`) over JSON-RPC + ZMQ and serves Stratum v1 (optionally with TLS), plus a status UI and JSON APIs.

## Features

- Stratum v1 over TCP (`server.pool_listen`) with optional TLS (`stratum.stratum_tls_listen`).
- Bitcoin Core integration via JSON-RPC (`node.rpc_url`) + ZMQ block feed (`node.zmq_block_addr`), with RPC longpoll for template updates.
- Split coinbase outputs: pool fee, optional operator donation, and miner payout.
- Status UI (HTML templates in `data/templates/`) + JSON endpoints under `/api/*`.
- HTTPS-first status UI with auto-generated self-signed cert by default (writes `data/tls_cert.pem` / `data/tls_key.pem`).
- Periodic local snapshots of `data/state/workers.db` for safe system backups, with optional Backblaze B2 uploads (configured under `[backblaze_backup]`; uploads are not required for local snapshots).
- Defensive controls: rate limiting, invalid-submission bans, reconnect churn bans.
- Extensive test suite and compatibility checks (see `TESTING.md`).

## Requirements

- Bitcoin Core with RPC enabled, and ZMQ enabled if you want tip updates without polling.
- Go toolchain compatible with `go.mod` (currently `go 1.24.11`).
- ZeroMQ library + headers (required to build `github.com/pebbe/zmq4`).

## Quick start

1. Build and run:
   ```bash
   go run .
   # or
   go build -o goPool && ./goPool
   ```
2. Configure the pool:
   - Primary config: `data/config/config.toml`
   - Optional secrets: `data/config/secrets.toml` (Discord bot token, Clerk dev keys, and/or RPC user/pass when using `-allow-rpc-credentials`)
   - Optional tuning overlay: `data/config/tuning.toml`

   If `data/config/config.toml` is missing, goPool exits with instructions and generates fresh examples under `data/config/examples/`.
3. Configure Bitcoin Core ZMQ (recommended):
   - Set `node.zmq_block_addr = "tcp://127.0.0.1:28332"` in `data/config/config.toml`
   - Add ZMQ publishers to your `bitcoin.conf` (single endpoint for all topics goPool subscribes to):
     ```conf
     zmqpubrawblock=tcp://127.0.0.1:28332
     zmqpubhashblock=tcp://127.0.0.1:28332
     zmqpubrawtx=tcp://127.0.0.1:28332
     zmqpubhashtx=tcp://127.0.0.1:28332
     ```
4. Point your miner at Stratum:
   - Plain: `stratum+tcp://<pool-host>:3333` (default)
   - TLS: `stratum+ssl://<pool-host>:4333` (when `stratum.stratum_tls_listen` is set)

## Configuration notes

- RPC auth prefers `node.rpc_cookie_path` (can be a file path or a directory containing `.cookie`). If it’s empty, goPool tries autodetection via `$BITCOIN_DATADIR`, btcd’s default `AppDataDir("btcd", false)/data` layout, and a list of common Linux locations. When a working cookie is found, goPool persists it back into `data/config/config.toml`.
- To override cookie path for a single launch, use `-rpc-cookie-path`. To override the RPC URL, use `-rpc-url`.
- `-allow-rpc-credentials` allows `rpc_user`/`rpc_pass` from `data/config/secrets.toml`, but is deprecated/insecure compared to the cookie.
- To connect to intentionally unauthenticated public RPC endpoints, set `node.allow_public_rpc = true` (and still understand the security implications).
- ZMQ can be disabled with `-no-zmq` (pool will rely on RPC/longpoll only).
- goPool always uses RPC longpoll for frequent `getblocktemplate` updates (including `coinbasevalue` / transaction fees); ZMQ is used for fast new-block detection and status metrics.

## Status UI and TLS

- Default listeners in `data/config/config.toml`:
  - Stratum: `:3333`
  - Status UI (HTTP): `:8080`
  - Status UI (HTTPS): `:4443`
  - Stratum TLS: `:4333`
- Status UI defaults to HTTPS-first (`-https-only=true`): when HTTP and HTTPS ports differ, HTTP serves a small safe subset and redirects other routes to HTTPS.
- Certificates:
  - On first run, goPool generates a self-signed cert at `data/tls_cert.pem` / `data/tls_key.pem`.
  - Replacing those files (e.g. with certbot output) is supported; goPool auto-reloads the cert hourly.
- Live reload:
  - `SIGUSR1` reloads HTML templates.
  - `SIGUSR2` reloads config/secrets/tuning for status pages (restart for listener/auth-path changes).

## Documentation (in this repo)

- [`operations.md`](https://github.com/Distortions81/M45-Core-goPool/blob/main/operations.md) – configuration + operations notes (flags, ZMQ topics, status API, reload signals, logging).
- [`performance.md`](https://github.com/Distortions81/M45-Core-goPool/blob/main/performance.md) – operator capacity planning notes and benchmarks.
- [`TESTING.md`](https://github.com/Distortions81/M45-Core-goPool/blob/main/TESTING.md) – test suite overview and how to run it.
- [`data/config/examples/autogen.md`](https://github.com/Distortions81/M45-Core-goPool/blob/main/data/config/examples/autogen.md) – how config examples are generated/used.

## Useful scripts and artifacts

- [`scripts/install-bitcoind.sh`](https://github.com/Distortions81/M45-Core-goPool/blob/main/scripts/install-bitcoind.sh) – bootstrap a local bitcoind + ZMQ config.
- [`scripts/run-tests.sh`](https://github.com/Distortions81/M45-Core-goPool/blob/main/scripts/run-tests.sh) – run the full Go test suite with helpful defaults.
- [`scripts/profile-graph.sh`](https://github.com/Distortions81/M45-Core-goPool/blob/main/scripts/profile-graph.sh) – render a CPU profile into `profile.svg`.
- [`default.pgo`](https://github.com/Distortions81/M45-Core-goPool/blob/main/default.pgo) / [`profile.svg`](https://github.com/Distortions81/M45-Core-goPool/blob/main/profile.svg) – example CPU profile + render (see `performance.md`).
