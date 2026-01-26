# <a href="https://github.com/Distortions81/M45-Core-goPool/blob/main/data/www/logo.png"><img src="https://raw.githubusercontent.com/Distortions81/M45-Core-goPool/main/data/www/logo.png" alt="goPool logo" width="32" height="32" style="vertical-align: middle;"></a> M45-Core-goPool (goPool)

[![Go CI](https://github.com/Distortions81/M45-Core-goPool/actions/workflows/ci.yml/badge.svg)](https://github.com/Distortions81/M45-Core-goPool/actions/workflows/ci.yml)
[![Go Vulncheck](https://github.com/Distortions81/M45-Core-goPool/actions/workflows/govulncheck.yml/badge.svg)](https://github.com/Distortions81/M45-Core-goPool/actions/workflows/govulncheck.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/Distortions81/M45-Core-goPool)](https://go.dev)
[![License](https://img.shields.io/github/license/Distortions81/M45-Core-goPool)](https://github.com/Distortions81/M45-Core-goPool/blob/main/LICENSE)

goPool is a solo Bitcoin mining pool that connects directly to Bitcoin Core over JSON-RPC and ZMQ, exposes Stratum v1 (with optional TLS), and ships with a status UI + JSON APIs for monitoring.

> **Downloads:** Pre-built binaries are available on GitHub Releases (see [guide/RELEASES.md](guide/RELEASES.md)).

## Quick start

1. Install Go 1.24 or later and ZeroMQ (`libzmq3-dev` or equivalent depending on your platform).
2. Clone the repo and build the pool.
    ```bash
    git clone https://github.com/Distortions81/M45-Core-goPool.git
    cd M45-Core-goPool
    go build -o goPool
    ```
3. Run `./goPool` once to generate example config files under `data/config/examples/`, then copy the base example into `data/config/config.toml` and edit it.
4. Set the required `node.payout_address`, `node.rpc_url`, and `node.zmq_block_addr` (leave it empty to run RPC/longpoll-only) before restarting the pool.

## Configuration overview

- `data/config/config.toml` controls listener ports, branding, RPC credentials, fee percentages, and most runtime behavior.
- TLS on the status UI is driven by `server.status_tls_listen` (default `:443`). Leave it empty (`""`) to disable HTTPS and rely solely on `server.status_listen` for HTTP; leaving `server.status_listen` empty disables HTTP entirely.
- `data/config/config.toml` also covers bitcoind settings such as `node.rpc_url`, `node.rpc_cookie_path`, and `node.zmq_block_addr` (leave it empty to disable ZMQ and rely on RPC/longpoll). The first run writes helper examples to `data/config/examples/`.
- `data/config/tuning.toml` lets you override advanced limits (rate limits, bans, EMA windows, etc.) while `data/config/secrets.toml` holds sensitive credentials (RPC user/pass, Discord/Clerk secrets, Backblaze keys).
- `[logging].level` controls runtime verbosity (`debug`, `info`, `warn`, `error`) and gates features like `net-debug.log`; override it temporarily with `-log-level <level>`.
- Enable `[mining].check_duplicate_shares = true` to pull shared duplication checks into solo-mode (this setting lives only in the config).

Flags like `-network`, `-rpc-url`, `-rpc-cookie`, and `-secrets` override the corresponding config file values for a single run—they are not written back to `config.toml`.

## Building & releases

- Build directly with `go build -o goPool`. Use hardware-acceleration tags such as `noavx` or `nojsonsimd` only when necessary; see [guide/operations.md](guide/operations.md) for guidance.
- Release builds already embed `build_time`/`build_version` via `-ldflags`. For a local build, pass the same metadata manually:

  ```bash
  go build -ldflags="-X main.buildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ) -X main.buildVersion=v0.0.0-dev" -o goPool
  ```
- Downloaded releases bundle everything under `data/` plus `guide/` documentation—see [guide/RELEASES.md](guide/RELEASES.md) for upgrade guidance, checksums, and release mechanics.

## Documentation & resources

- **`guide/operations.md`** – Main reference for configuration options, CLI flags, logging, backup policies, and runtime procedures.
- **`guide/performance.md`** – Capacity planning, benchmark data, and CPU/network ballparks.
- **`guide/TESTING.md`** – Test suite instructions and how to add or run existing tests.
- **`guide/RELEASES.md`** – Release package contents, verification, and upgrade steps.
- **`LICENSE`** – Legal terms for using goPool.

Need help? Open an issue on GitHub or refer to the documentation in `guide/` before asking for assistance.
