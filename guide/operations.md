# goPool Operations Guide

> **Quick start reminder:** the [main README](../README.md) gives a concise walk-through; this guide expands every section into the operational context that running goPool day-to-day requires.

goPool ships as a self-contained pool daemon that connects directly to Bitcoin Core (JSON-RPC + ZMQ), hosts a Stratum v1 endpoint, and exposes a status UI with JSON APIs. This guide covers every step operators usually repeat in production, from building binaries to tuning performance; refer to the sibling documents (`guide/performance.md`, `guide/RELEASES.md`, `guide/TESTING.md`) for capacity planning, release bundles, and testing recipes.

## Building

Requirements:
* **Go 1.24.11+** — install from https://go.dev/dl/ for matching ABI guarantees.
* **ZeroMQ headers** (`libzmq3-dev`, `zeromq`, etc.) to satisfy `github.com/pebbe/zmq4`. On Debian/Ubuntu run `sudo apt install -y libzmq3-dev`; other distros follow their package manager.

Clone and build:

```bash
git clone https://github.com/Distortions81/M45-Core-goPool.git
cd M45-Core-goPool
go build -o goPool
```

Use `GOOS`/`GOARCH` for cross-compilation and avoid `go install` unless populating `GOBIN` intentionally. Hardware acceleration flags (`noavx`, `nojsonsimd`) remain the only build tags you usually need; logging verbosity no longer relies on a build tag but on `[logging].level`/`-log-level`.

### Build metadata

Release builds embed two fields via `-ldflags`:

- `main.buildTime`: the UTC timestamp recorded when the binary was compiled. The status UI exposes it as `build_time`.
- `main.buildVersion`: the version label (e.g., `v1.2.3`) and shows up under `build_version`.

GitHub Actions sets both automatically per run. If you build manually and want consistent metadata, pass the same flags yourself:

```
go build -ldflags="-X main.buildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ) -X main.buildVersion=vX.Y.Z" ./...
```

Both values appear on the status page and JSON endpoints so you can verify the exact build at runtime.

## Initial configuration

1. Run `./goPool`; it generates `data/config/examples/` and exits.
2. Copy the base example to `data/config/config.toml` and edit required values (especially `node.payout_address`, `node.rpc_url`, and ZMQ addresses: `node.zmq_hashblock_addr`/`node.zmq_rawblock_addr`—leave blank to fall back to RPC/longpoll).
3. Optional: copy `data/config/examples/secrets.toml.example` and `data/config/examples/tuning.toml.example` to `data/config/` for sensitive credentials or advanced tuning.
4. Re-run `./goPool`; it may regenerate `pool_entropy` and normalized listener ports if you later invoke `./goPool -rewrite-config`.

## Runtime overrides

| Flag | Description |
|------|-------------|
| `-network <mainnet|testnet|signet|regtest>` | Temporarily sets default RPC/ZMQ ports and ensures only one network is active. |
| `-bind <ip>` | Replace the bind IP of every listener (Stratum, status HTTP/HTTPS). |
| `-rpc-url <url>` | Override `node.rpc_url` for this run—useful for temporary test nodes. |
| `-rpc-cookie <path>` | Override `node.rpc_cookie_path` when testing alternate cookie locations. |
| `-secrets <path>` | Point to an alternate `secrets.toml`; the file is not rewritten. |
| `-rewrite-config` | Persist derived values like `pool_entropy` back into `config.toml`. |
| `-stdout` | Mirror every structured log entry to stdout (nice when running under systemd/journal). |
| `-profile` | Capture a 60-second `default.pgo` CPU profile. |
| `-flood` | Force both `min_difficulty` and `max_difficulty` to a low value for stress testing. |
| `-log-level <debug|info|warn|error>` | Override `[logging].level` for this run only (not persisted). |
| `-no-json` | Disable the JSON status endpoints while keeping the HTML UI active. |
| `-allow-rpc-creds` | Force username/password auth from `secrets.toml`; logs a warning and is deprecated. |

Flags only override values for the running instance; nothing is written back to `config.toml` (except `node.rpc_cookie_path` when auto-detected). Use configuration files for durable behavior.

## Launching goPool

### Initial run

1. Run `./goPool` once without a config. The daemon stops after generating `data/config/examples/`.
2. Copy `data/config/examples/config.toml.example` to `data/config/config.toml`.
3. Provide the required values (payout address, RPC/ZMQ endpoints, any branding overrides) and restart the pool.
4. Optional: copy `data/config/examples/secrets.toml.example` and `data/config/examples/tuning.toml.example` to `data/config/` and edit as needed. The tuning file sits beside the base config and can be deleted to revert to defaults.
5. If you prefer reproducible derived settings, rerun `./goPool -rewrite-config` once after editing. This writes derived fields such as `pool_entropy` and normalized listener ports back to `config.toml`.

### Common runtime flags

| Flag | Description |
|------|-------------|
| `-network <mainnet|testnet|signet|regtest>` | Force network defaults for RPC/ZMQ ports and version mask adjustments. Only one mode is accepted. |
| `-bind <ip>` | Override the bind IP for all listeners (Stratum, status UI). Ports remain as configured. |
| `-rpc-url <url>` | Override the RPC URL defined in `config.toml`. |
| `-rpc-cookie <path>` | Override the RPC cookie path; useful for temporary deployments while keeping `config.toml` untouched. |
| `-secrets <path>` | Use an alternative `secrets.toml` location (defaults to `data/config/secrets.toml`). |
| `-stdout` | Mirror structured logs to stdout in addition to the rolling files. |
| `-profile` | Capture a 60‑second CPU profile in `default.pgo`. |
| `-rewrite-config` | Rewrite `config.toml` after applying runtime overrides (reorders sections and fills derived values). |
| `-flood` | Force `min_difficulty`/`max_difficulty` to the same low value for stress testing. |
| `-log-level <debug|info|warn|error>` | Override the `[logging].level` setting for the current run (not persisted back to config). |
| `-no-json` | Disable the JSON status endpoints (you still get the HTML status UI). |
| `-allow-rpc-creds` | Force RPC auth to come from `secrets.toml` `rpc_user`/`rpc_pass`. Deprecated and insecure; prefer cookie auth. |

Additional runtime knobs exist in `config.toml`/`tuning.toml`, but the flags above let you temporarily override them without editing files.
Flags such as `-network`, `-rpc-url`, `-rpc-cookie`, and `-secrets` only affect the current invocation; they override the values from `config.toml` or `secrets.toml` at runtime but are not persisted back to the files.

## Configuration files

### config.toml

The required `data/config/config.toml` is the primary interface for pool behavior. Key sections include:

- `[server]`: `pool_listen`, `status_listen`, `status_tls_listen`, and `status_public_url`. Set `status_tls_listen = ""` to disable HTTPS and rely on `status_listen` only. Leaving `status_listen` empty disables HTTP entirely (e.g., TLS-only deployments). `status_public_url` feeds redirects and Clerk cookie domains. When both HTTP and HTTPS are enabled, the HTTP listener now issues a temporary (307) redirect to the HTTPS endpoint so the public UI and JSON APIs stay behind TLS.
- `[branding]`: Styling and branding options shown in the status UI (tagline, pool donation link, GitHub link, location string).
- `[stratum]`: `stratum_tls_listen` for TLS-enabled Stratum (leave blank to disable secure Stratum).
- `[auth]`: Clerk URLs and session cookies used for the status UI.
- `[node]`: `rpc_url`, `rpc_cookie_path`, ZMQ addresses (`zmq_hashblock_addr`/`zmq_rawblock_addr`), and `allow_public_rpc`.
- `[mining]`: Pool fee, donation settings, `extranonce2_size`, `template_extra_nonce2_size`, `job_entropy`, `pooltag_prefix`, and flags that control solo-mode shortcuts.
- `[backblaze_backup]`: Cloud backup toggle, bucket name, prefix, and upload interval.
- `[logging]`: `level` sets the default log verbosity (`debug`, `info`, `warn`, or `error`). It controls the structured log output and whether `net-debug.log` is enabled.

Set numeric values explicitly (do not rely on automation), and trim whitespace (goPool trims internally but a clean config is easier to audit). After editing, restart goPool or send `SIGUSR2` (see below).

### tuning.toml

The `data/config/tuning.toml` file overrides fine-grained limits without touching the main config. Sections include:

- `[rate_limits]`: `max_conns`, burst windows, steady-state rates, `stratum_messages_per_minute` (messages/min before disconnect + 1h ban), and whether to auto-calculate throttles from `max_conns`.
- `[timeouts]`: `connection_timeout_seconds`.
- `[difficulty]`: `default_difficulty` fallback when no suggestion arrives, `max_difficulty`/`min_difficulty` clamps (0 disables a clamp), whether to lock miner-suggested difficulty, and whether to enforce min/max on suggested difficulty (ban/disconnect when outside limits). The first `mining.suggest_*` is honored once per connection, triggers a clean notify, and subsequent suggests are ignored.
- `[mining]`: `disable_pool_job_entropy` to remove the `<pool_entropy>-<job_entropy>` suffix, and `vardiff_fine` to enable half-step VarDiff adjustments without power-of-two snapping.
- `[hashrate]`: `hashrate_ema_tau_seconds`, `hashrate_ema_min_shares`, `ntime_forward_slack_seconds`.
- `[discord]`: Worker notification thresholds for Discord alerts.
- `[status]`: `mempool_address_url` controls the external explorer link prefix used by the worker status UI.
- `[peer_cleaning]`: Enable/disable peer cleanup and tune thresholds.
- `[bans]`: Ban thresholds/durations, `banned_miner_types` (disconnect miners by client ID on subscribe), and `clean_expired_on_startup` (defaults to `true`). Prefer `data/config/miner_blacklist.json` for client ID blacklist management; it overrides `banned_miner_types` when present. Set `clean_expired_on_startup = false` if you want to keep expired bans for inspection.
- `[version]`: `min_version_bits` and `ignore_min_version_bits`.

Delete `tuning.toml` to revert to built-in defaults. The first run creates `data/config/examples/tuning.toml.example`.

### secrets.toml

Keep sensitive data out of `config.toml`:

- `rpc_user`/`rpc_pass`: Only used when `-allow-rpc-creds` is supplied (deprecated). The preferred path is `node.rpc_cookie_path`.
- `discord_token`, `clerk_secret_key`, `clerk_publishable_key`, `backblaze_account_id`, `backblaze_application_key`.

`secrets.toml` is gitignored and should live under `data/config`. The example is re-generated on each restart for reference.

## Node, RPC, and ZMQ

goPool expects a Bitcoin Core node with RPC enabled. Configure:

- `node.rpc_url`: The RPC endpoint for `getblocktemplate` and `submitblock`.
- `node.rpc_cookie_path`: Point this to `~/.bitcoin/.cookie` (or equivalent). When empty, goPool auto-detects common locations and, when successful, writes the discovered path back into `config.toml`.
- `node.allow_public_rpc`: Set to `true` only when intentionally connecting to an unauthenticated endpoint.
- `-rpc-cookie`/`-rpc-url`: Use these overrides for temporary testing (e.g., a local regtest instance).
- `-allow-rpc-creds`: Forces `rpc_user`/`rpc_pass` from `secrets.toml`. goPool logs a warning every run and you lose the security of the cookie file workflow.

To change network defaults, use the `-network` flag:

- `mainnet`, `testnet`, `signet`, `regtest` — only one may be set per run. goPool applies RPC and ZMQ port defaults, RPC URL overrides, and sets `cfg.mainnet/testnet/...` booleans used for validation.

### ZMQ block updates

goPool can use Bitcoin Core's ZMQ publisher to learn about new blocks quickly, but it still uses RPC (including longpoll) to fetch the actual `getblocktemplate` payload and keep templates current.

`node.zmq_hashblock_addr` and `node.zmq_rawblock_addr` control the ZMQ subscriber connections. When both are empty goPool disables ZMQ and logs a warning that you are running RPC/longpoll-only; this lets regtest or longpoll-only pools skip configuring a publisher. When a network flag (`-network`) is set and both are blank, goPool auto-fills the default `tcp://127.0.0.1:28332` for that network.

If you publish `hashblock` and `rawblock` on different ports, configure:

- `node.zmq_hashblock_addr` for `hashblock`
- `node.zmq_rawblock_addr` for `rawblock`

If both are set to the same `tcp://IP:port`, goPool will share a single ZMQ connection.

#### What goPool subscribes to

goPool subscribes to these Bitcoin Core ZMQ topics:

- `hashblock`: triggers an immediate template refresh (new block).
- `rawblock`: records block-tip telemetry (height/time/difficulty + payload size) and triggers an immediate template refresh (new block).

Only `hashblock` and `rawblock` affect job freshness.

#### Minimal topics (without affecting mining correctness)

To avoid losing anything that affects mining/job freshness:

- Publish/subscribe **at least one** of `hashblock` or `rawblock` so goPool refreshes immediately on new blocks.

Common choices:

- **Lowest bandwidth:** enable only `hashblock`.
- **More block-tip telemetry without extra RPC:** enable `rawblock` (and optionally also `hashblock`).

#### Why longpoll still matters

Even with ZMQ enabled, goPool still uses RPC longpoll to keep templates current when the mempool/tx set changes. ZMQ tx topics are not used to refresh templates today, so if you disable longpoll you may stop picking up transaction-only template updates (fees/txs) between blocks.

## Status UI, TLS, and listeners

The status UI uses two listeners:

- `server.status_listen` (default `:80`) — serves HTTP, static files, and JSON endpoints.
- `server.status_tls_listen` (default `:443`) — serves HTTPS with auto-generated certificates (stored in `data/tls_cert.pem` and `data/tls_key.pem`).

Set `status_tls_listen = ""` to disable HTTPS and keep only the HTTP listener. Set `status_listen = ""` to disable HTTP entirely and rely solely on TLS. The CLI no longer provides an `-http-only` toggle.

goPool also auto-creates `/app/`, `/stats/`, and `/api/*` handlers plus optional TLS/cert reloading. Run `systemctl kill -s SIGUSR1 <service>` to reload the templates (the previous template set is kept when parsing fails) and `SIGUSR2` to reload the configuration files without stopping the daemon.

## Admin Control Panel

`data/config/admin.toml` is created automatically the first time goPool runs. The generated file documents the panel, defaults to `enabled = false`, and ships with `username = "admin"` plus a random password (check the file to copy the generated secret). Update the file to enable the UI, pick a unique username/password, and keep it out of version control. The `session_expiration_seconds` value controls how long the admin session remains valid (default 900 seconds).

goPool now stores a `password_sha256` alongside the plaintext password. On startup, if `password` is set, goPool verifies/refreshes `password_sha256` to match it. After the first successful admin login, the plaintext `password` is cleared from `admin.toml` and only the hash remains; subsequent logins use the hash.

When enabled, visit `/admin` (deliberately absent from the main navigation) and log in with the credentials stored in `admin.toml`. The panel exposes:

* **Live settings** – a field-based UI that updates goPool's in-memory configuration immediately. Some settings still require a reboot to fully apply across all subsystems.
* **Save to disk** – optionally force-write the current in-memory settings to `config.toml` and `tuning.toml`.
* **Reboot** – a button that sends SIGTERM to goPool. It requires re-entering the admin password and typing `REBOOT` to confirm the action so your pool does not restart accidentally.

Because the admin login is intentionally simple, bind this UI to trusted networks only (e.g., keep `server.status_listen` local-domain, use firewall rules, or run behind an authenticated proxy) and rotate credentials whenever you rotate administrators.

## Mining specifics

- `mining.pool_fee_percent`, `operator_donation_percent`, and `operator_donation_address` determine how rewards are split.
- `pooltag_prefix` customizes the `/goPool/` coinbase tag (only letters/digits).
- `job_entropy` and `pool_entropy` help make each template unique; disable the suffix with `[tuning.mining] disable_pool_job_entropy = true`.
- `solo_mode` defaults to `true` (lighter validation). Set to `false` to enforce stricter duplicate detection and low-difficulty checks.
- `check_duplicate_shares` enables duplicate share detection when `solo_mode = true`; set it to `true` to apply the same checks used in multi-worker pools.
- `direct_submit_processing` lets each stratum connection process `mining.submit` inline instead of via the worker queue; useful for low-latency environments but eases backpressure.
- `solo_mode` skips several policy guards that multi-worker pools still perform:
  - worker-mismatch validation (the connection’s authorized worker name is trusted once you authenticate),
  - strict stale-job/prevhash checks, tight `ntime` window enforcement, and BIP320 version/mask requirements,
  - duplicate-share filtering and low-difficulty rejection.

## Logging and diagnostics

Log files live under `data/logs/`:

- `pool.log` – structured log of pool events.
- `errors.log` – captures `ERROR` events for quick troubleshooting.
- `net-debug.log` – recorded when `[logging].level` or `-log-level` is set to `debug`; contains raw requests/responses and raw RPC/ZMQ traffic.

Use `-stdout` to mirror every entry to stdout. Pair that with `journalctl` or container logs for live debugging.

The internal `simpleLogger` writes a daily rolling file per log type, rotating after three days (configurable via `const logRetentionDays`).

## Backups and bans

goPool maintains its state in `data/state/workers.db`. For Backblaze uploads, it takes a consistent SQLite snapshot first (using SQLite's backup API). If you enable a local snapshot (`keep_local_copy = true` or set `snapshot_path`), goPool also writes a persistent snapshot you can back up safely (for example `data/state/workers.db.bak`).

### Backblaze B2

Configure the `[backblaze_backup]` section:

```toml
[backblaze_backup]
enabled = true
bucket = "my-bucket"
prefix = "gopool/"
interval_seconds = 43200
keep_local_copy = true
snapshot_path = ""
```

Store credentials in `secrets.toml` and keep them secure.

If Backblaze is temporarily unavailable at startup (network outage, transient auth failure), goPool keeps writing local snapshots (when enabled) and will retry connecting to B2 on later backup runs without requiring a restart.

### Ban cleanup

Expired bans are rewritten on every startup by default. Control this via `[tuning.bans].clean_expired_on_startup` (defaults to `true`). Set it to `false` to inspect expired entries without clearing them.

Clean bans happen inside `NewAccountStore` as it opens the shared state DB; when disabled, you still get bans loaded from disk, but expired entries remain visible via the status UI.

## State database and snapshots

If you need a “safe to copy while goPool is running” database file, enable a local snapshot via `[backblaze_backup].keep_local_copy` (defaults the snapshot to `data/state/workers.db.bak`) or `[backblaze_backup].snapshot_path`. That snapshot is written atomically during each backup run.

When `[backblaze_backup].enabled = true`, goPool always writes a local snapshot (defaulting to `data/state/workers.db.bak` when `snapshot_path` is empty) so you still have a reliable local backup even if B2 is temporarily unavailable.

If you do not have a snapshot configured, stop the pool before copying `data/state/workers.db`. Avoid opening the live DB with external tools while goPool is running.

The `data/state/` directory also holds ban metadata, saved workers snapshots, and any auto-generated JSON caches—keep it alongside your main `data/` backup strategy.

## Tuning limits

Auto-configured accept rate limits calculate `max_accept_burst`/`max_accepts_per_second` based on `max_conns` unless `tuning.toml` overrides them. Recent defaults aim to allow all miners to reconnect within `accept_reconnect_window` seconds.

Key tuning knobs:

- `accept_burst_window` / `accept_reconnect_window` / `accept_steady_state_*` – windows that shape burst vs sustained behavior.
- `hashrate_ema_tau_seconds` / `hashrate_ema_min_shares` – adjust EMA smoothing for per-worker hashrate.
- `ntime_forward_slack_seconds` – tolerated future timestamps on shares (default 7000 seconds).
- `peer_cleaning` – enable/disable and tune thresholds for cleaning stalled miners.
- `difficulty` – clamp advertised difficulty, optionally enforce min/max on miner-suggested difficulty, and optionally lock miner suggestions.

Each tuning value logs when set, so goPool operators can audit what changed via `pool.log`.

## Runtime operations

- **SIGUSR1** reloads the HTML templates under `data/templates/`. Errors (parse failures, missing files) are logged but the previous template set remains active so the site keeps serving—check `pool.log` if pages look odd after a reload.
- **SIGUSR2** reloads `config.toml`, `secrets.toml`, and `tuning.toml`, reapplies overrides, and updates the status server with the new config.
- **Shutdown** occurs on `SIGINT`/`SIGTERM`. goPool stops the status servers, Stratum listener, and pending replayers gracefully.
- **TLS cert reloading** uses `certReloader` to monitor `data/tls_cert.pem`/`tls_key.pem` hourly. Certificate renewals (e.g., via certbot) are picked up without restarts.

## Monitoring APIs

- `/api/overview`, `/api/pool-page`, `/api/server`, etc., provide JSON snapshots consumed by the UI. Disable them with `-no-json`.
- `/stats/` and `/app/` serve the saved-worker dashboards, including per-worker graphing data.
- The status UI exposes worker-level metrics (hashrate, bans, accepted shares) and automatically lists Discord/Clerk states if configured.

## Profiling and debugging

- `-profile` writes `default.pgo`; use `go tool pprof` or `./scripts/profile-graph.sh default.pgo profile.svg` to inspect the profile and generate SVGs.
- Watch `metrics` JSON endpoints for bumps in share handling latency (`SubmitState` exposures).
- `net-debug.log` records RPC/ZMQ traffic when log level is `debug` (set via `[logging].level` or `-log-level debug`); tail the file when you need detailed traces.

## Related guides

- **`guide/performance.md`** – Capacity planning, CPU/latency breakdowns, and network bandwidth ballparks.
- **`guide/RELEASES.md`** – Packaging, verifying release checksums, upgrade steps, and release workflow details.
- **`guide/TESTING.md`** – How to run and extend the test suite, including fuzz targets and benchmarks.

Refer back to the concise [main README](../README.md) for quick start instructions, and keep this guide nearby for reference while you tune your deployment.
