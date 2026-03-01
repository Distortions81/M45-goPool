# <a href="https://github.com/Distortions81/M45-Core-goPool/blob/main/data/www/logo.png"><img src="https://raw.githubusercontent.com/Distortions81/M45-Core-goPool/main/data/www/logo.png" alt="goPool logo" width="32" height="32" style="vertical-align: middle;"></a> M45-goPool (goPool)

[![Go CI](https://github.com/Distortions81/M45-Core-goPool/actions/workflows/ci.yml/badge.svg)](https://github.com/Distortions81/M45-Core-goPool/actions/workflows/ci.yml)
[![Go Vulncheck](https://github.com/Distortions81/M45-Core-goPool/actions/workflows/govulncheck.yml/badge.svg)](https://github.com/Distortions81/M45-Core-goPool/actions/workflows/govulncheck.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/Distortions81/M45-Core-goPool)](https://go.dev)
[![License](https://img.shields.io/github/license/Distortions81/M45-Core-goPool)](https://github.com/Distortions81/M45-Core-goPool/blob/main/LICENSE)

goPool is a solo Bitcoin mining pool that connects directly to Bitcoin Core over JSON-RPC and ZMQ, exposes Stratum v1 (with optional TLS), and ships with a status UI + JSON APIs for monitoring.

> **Downloads:** Pre-built binaries are available on GitHub Releases.

Stratum notes:

- goPool accepts both `mining.authorize` and CKPool-style `mining.auth`, and tolerates authorize-before-subscribe (work starts after subscribe completes).
- On startup and during runtime, Stratum is gated only when the node/job feed reports errors or the node is in a non-usable syncing/indexing state: new connections are refused and existing miners are disconnected to avoid wasted hashing.

<p align="center">
  <img src="Screenshot_20260215_055225.png" alt="goPool status dashboard" width="720">
</p>

## Quick start

1. Install Go 1.26 or later and ZeroMQ (`libzmq3-dev` or equivalent depending on your platform).
2. Clone the repo and build the pool.
    ```bash
    git clone https://github.com/Distortions81/M45-Core-goPool.git
    cd M45-Core-goPool
    go build -o goPool
    ```
3. Run `./goPool` once to generate example config files under `data/config/examples/`, then copy the base example into `data/config/config.toml` and edit it.
4. Set the required `node.payout_address`, `node.rpc_url`, and ZMQ addresses (`node.zmq_hashblock_addr`/`node.zmq_rawblock_addr`; leave empty to run RPC/longpoll-only) before restarting the pool.

## Codebase size

- **Go source (excluding tests):** 35,438 lines across 147 non-test `.go` files.
- **Go tests:** 17,518 lines across 107 `*_test.go` files.
- **Go source (total):** 52,956 lines across 254 `.go` files.

Counts above were collected on March 1, 2026.

## Configuration overview

- `data/config/config.toml` controls listener ports, core branding, node endpoints, fee percentages, and most runtime behavior.
- TLS on the status UI is driven by `server.status_tls_listen` (default `:443`). Leave it empty (`""`) to disable HTTPS and rely solely on `server.status_listen` for HTTP; leaving `server.status_listen` empty disables HTTP entirely.
- `data/config/config.toml` also covers bitcoind settings such as `node.rpc_url`, `node.rpc_cookie_path`, and ZMQ addresses (`node.zmq_hashblock_addr`/`node.zmq_rawblock_addr`; leave empty to disable ZMQ and rely on RPC/longpoll). First run writes helper examples to `data/config/examples/`.
- Optional split files:
  - `data/config/services.toml` for service/integration settings (`auth`, `backblaze_backup`, `discord`, `status` links).
  - `data/config/policy.toml` for submit-policy/version/bans/timeouts.
  - `data/config/tuning.toml` for rate limits, vardiff, EMA tuning, and peer-cleaning controls.
  - `data/config/secrets.toml` for sensitive credentials (RPC user/pass, Discord/Clerk secrets, Backblaze keys).
- `data/config/admin.toml` controls the optional admin UI at `/admin`. The file is auto-generated on first run with `enabled = false` and a random password (read the file to see the generated secret). Update it to enable the panel, pick fresh credentials, and keep the file private. goPool writes `password_sha256` on startup and clears the plaintext password after the first successful login; subsequent logins use the hash. The admin UI provides a field-based editor for the in-memory config, can force-write `config.toml` + split override files, and includes a reboot control; reboot requests require typing `REBOOT` and resubmitting the admin password.
- `[logging]` uses boolean toggles: `debug` enables verbose runtime logs, and `net_debug` enables raw network tracing (`net-debug.log`). You can also force these at startup with `-debug` and `-net-debug`.
- `share_*` validation toggles live in `data/config/policy.toml` `[mining]` (for example `share_check_duplicate`).

Flags like `-network`, `-rpc-url`, `-rpc-cookie`, and `-secrets` override the corresponding config file values for a single run—they are not written back to `config.toml`.

## Building & releases

- Build directly with `go build -o goPool`. Use hardware-acceleration tags such as `noavx` or `nojsonsimd` only when necessary; see [documentation/operations.md](documentation/operations.md) for guidance.
- Release builds already embed `build_time`/`build_version` via `-ldflags`. For a local build, pass the same metadata manually:

  ```bash
  go build -ldflags="-X main.buildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ) -X main.buildVersion=v0.0.0-dev" -o goPool
  ```
- Downloaded releases bundle everything under `data/` plus `documentation/` docs.

## Documentation & resources

- **`documentation/README.md`** - Documentation index.
- **`documentation/operations.md`** – Main reference for configuration options, CLI flags, logging, backup policies, and runtime procedures.
- **`documentation/json-apis.md`** – HTTP JSON API reference for the `/api/*` status endpoints.
- **`documentation/TESTING.md`** – Test suite instructions and how to add or run existing tests.
- **`LICENSE`** – Legal terms for using goPool.

Need help? Open an issue on GitHub or refer to the documentation in `documentation/` before asking for assistance.
