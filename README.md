# <a href="https://github.com/Distortions81/M45-Core-goPool/blob/main/data/www/logo.png"><img src="https://raw.githubusercontent.com/Distortions81/M45-Core-goPool/main/data/www/logo.png" alt="goPool logo" width="32" height="32" style="vertical-align: middle;"></a> M45-goPool

[![Go CI](https://github.com/Distortions81/M45-Core-goPool/actions/workflows/ci.yml/badge.svg)](https://github.com/Distortions81/M45-Core-goPool/actions/workflows/ci.yml)
[![Go Vulncheck](https://github.com/Distortions81/M45-Core-goPool/actions/workflows/govulncheck.yml/badge.svg)](https://github.com/Distortions81/M45-Core-goPool/actions/workflows/govulncheck.yml)
[![Coverage](https://codecov.io/github/Distortions81/M45-goPool/graph/badge.svg)](https://app.codecov.io/github/Distortions81/M45-goPool)
[![Go Report Card](https://goreportcard.com/badge/github.com/Distortions81/M45-Core-goPool)](https://go.dev)
[![License](https://img.shields.io/github/license/Distortions81/M45-Core-goPool)](https://github.com/Distortions81/M45-Core-goPool/blob/main/LICENSE)

goPool is a **from-scratch Golang solo Bitcoin mining pool**. It connects directly to Bitcoin Core over JSON-RPC and ZMQ, exposes Stratum v1 (with optional TLS), and ships with a status UI + JSON APIs for monitoring.

## What this project is

- A solo Bitcoin pool server written in Go from the ground up for this project.
- A direct integration with Bitcoin Core (no external pool engine dependency).
- A self-hosted stack: Stratum endpoint, web status UI, and JSON APIs.

## Feature highlights

- **Simple live dashboard** to quickly see pool health and mining activity.
- **Worker pages** so you can check each rig's speed and recent performance.
- **Online/offline status** so you can quickly see which miners are up or down.
- **Saved workers** so your favorite rigs are easy to revisit anytime.
- **24-hour hashrate charts** to spot drops, spikes, and stability trends.
- **Best share tracking** so you can see standout shares over time.
- **Discord pings** (optional): alerts when a saved worker goes offline, when it has been back online long enough to count as recovered, and when a saved worker finds a block.
- **Easy connect details** shown on the site for miner setup.

- **Solo mining core**: builds and submits Bitcoin blocks directly against Bitcoin Core (`getblocktemplate` + `submitblock`) with JSON-RPC and optional ZMQ acceleration.
- **Stratum v1 server**: supports `mining.subscribe`, `mining.authorize`, `mining.submit`, `mining.configure`, CKPool-style `mining.auth`, and optional Stratum TLS.
- **Compatibility controls**: CKPool subscribe-response emulation, version-rolling support, suggest-difficulty handling, optional `mining.set_extranonce` and `mining.set_version_mask` notifications.
- **Share-policy controls**: duplicate checks, nTime and version-rolling checks, worker-match enforcement, stale-job freshness modes, and optional inline submit processing.
- **VarDiff + hashrate telemetry**: configurable difficulty clamps/targets, EMA smoothing, worker/pool hashrate history, and best-share tracking.
- **Safety and resilience**: node/job-feed health gating (disconnect/refuse when unsafe), reconnect/invalid-submit banning, pending submission replay, and stale-feed safeguards.
- **Web status UI + JSON APIs**: live overview, node/pool/server pages, worker pages, and `/api/*` endpoints for monitoring/automation.
- **Operator controls**: optional admin panel for live settings updates, persist-to-disk controls, log tooling, and guarded reboot action.
- **Storage and backups**: SQLite state store with atomic snapshots and optional Backblaze B2 upload workflow.
- **Auth/integrations**: optional Clerk auth flows, saved-worker pages, Discord notification toggles, and one-time worker linking codes.
- **Performance options**: Stratum socket buffer tuning, optional SIMD JSON/hash paths, and built-in profiling hooks.

## Direct Go libraries and licenses

The table below lists direct Go module dependencies from `go.mod` and the license declared in each dependency's upstream `LICENSE*` file.

| Library | Version | License |
|---|---:|---|
| github.com/Backblaze/blazer | v0.7.2 | Apache-2.0 |
| github.com/btcsuite/btcd | v0.25.0 | ISC |
| github.com/btcsuite/btcd/btcec/v2 | v2.3.6 | ISC |
| github.com/btcsuite/btcd/btcutil | v1.1.6 | ISC |
| github.com/btcsuite/btcd/chaincfg/chainhash | v1.1.0 | ISC |
| github.com/bwmarrin/discordgo | v0.29.0 | BSD-3-Clause |
| github.com/bytedance/sonic | v1.15.0 | Apache-2.0 |
| github.com/clerk/clerk-sdk-go/v2 | v2.5.1 | MIT |
| github.com/golang-jwt/jwt/v5 | v5.3.0 | MIT |
| github.com/hako/durafmt | v0.0.0-20210608085754-5c1018a4e16b | MIT |
| github.com/martinhoefling/goxkcdpwgen | v0.1.1 | MIT |
| github.com/minio/sha256-simd | v1.0.1 | Apache-2.0 |
| github.com/pebbe/zmq4 | v1.4.0 | BSD-3-Clause |
| github.com/pelletier/go-toml | v1.9.5 | Apache-2.0 |
| github.com/remeh/sizedwaitgroup | v1.0.0 | MIT |
| golang.org/x/sys | v0.40.0 | BSD-3-Clause |
| modernc.org/sqlite | v1.44.1 | BSD-3-Clause |

Additional third-party asset notices are in `THIRD_PARTY_NOTICES.md`.

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
  - `data/config/version_bits.toml` for explicit per-bit block-version overrides (read-only; never rewritten by goPool). `data/config/policy.toml` `[version].bip110_enabled` toggles BIP-110 signaling (bit 4), `[version].share_allow_version_mask_mismatch` allows out-of-mask miner submit versions for compatibility (default `false`), and `version_bits.toml` can still force bit-level overrides afterward. BIP-110 reference: https://github.com/bitcoin/bips/blob/master/bip-0110.mediawiki
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
- **`documentation/version-bits.md`** – Version-bit override file format and known bit usage in goPool.
- **`documentation/json-apis.md`** – HTTP JSON API reference for the `/api/*` status endpoints.
- **`documentation/TESTING.md`** – Test suite instructions and how to add or run existing tests.
- **`LICENSE`** – Legal terms for using goPool.

Need help? Open an issue on GitHub or refer to the documentation in `documentation/` before asking for assistance.
