# goPool Operations Documentation

> **Quick start reminder:** the [main README](../README.md) gives a concise walk-through; this document expands each section into the operational context needed to run goPool day-to-day.

goPool ships as a self-contained pool daemon that connects directly to Bitcoin Core (JSON-RPC + ZMQ), hosts a Stratum v1 endpoint, and exposes a status UI with JSON APIs. This documentation covers the operational steps teams repeat in production; refer to sibling documents (especially `documentation/TESTING.md`) for testing recipes.

Operational Stratum notes:

- Stratum is gated (at startup and during runtime) only when the job feed reports errors or the node is in a non-usable syncing/indexing state. While gated, new miner connections are refused and existing miners are disconnected to avoid idling on stale/no work during node/bootstrap issues.
- When the node/job feed is stale, the main status page (`/`) displays a dedicated "node unavailable" page instead of the normal overview.
- A background heartbeat (`stratumHeartbeatInterval`) performs periodic non-longpoll template refreshes so "quiet mempool / no template churn" does not look like a dead node.
- When updates are degraded but basic node RPC calls still work, the node-unavailable page will also show common sync/indexing indicators (IBD flag and blocks/headers) to help diagnose "node indexing" situations.

## Building

Requirements:
* **Go 1.26.0+** — install from https://go.dev/dl/ for matching ABI guarantees.
* **ZeroMQ headers** (`libzmq3-dev`, `zeromq`, etc.) to satisfy `github.com/pebbe/zmq4`. On Debian/Ubuntu run `sudo apt install -y libzmq3-dev`; other distros follow their package manager.

Clone and build:

```bash
git clone https://github.com/Distortions81/M45-Core-goPool.git
cd M45-Core-goPool
go build -o goPool
```

Use `GOOS`/`GOARCH` for cross-compilation and avoid `go install` unless populating `GOBIN` intentionally. Hardware acceleration flags (`noavx`, `nojsonsimd`) remain the only build tags you usually need; runtime logging/tracing is controlled by `[logging].debug` and `[logging].net_debug` (or `-debug` / `-net-debug`).

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
3. Optional: copy `data/config/examples/secrets.toml.example`, `data/config/examples/services.toml.example`, `data/config/examples/policy.toml.example`, and `data/config/examples/tuning.toml.example` to `data/config/` for sensitive credentials or advanced tuning.
4. Re-run `./goPool`; it may regenerate `pool_entropy` and normalized listener ports if you later invoke `./goPool -rewrite-config`.

## Runtime overrides

| Flag | Description |
|------|-------------|
| `-network <mainnet|testnet|signet|regtest>` | Temporarily sets default RPC/ZMQ ports and ensures only one network is active. |
| `-bind <ip>` | Replace the bind IP of every listener (Stratum, status HTTP/HTTPS). |
| `-listen <addr>` | Override Stratum TCP listen address for this run (for example `:3333`). |
| `-status <addr>` | Override status HTTP listen address for this run (for example `:80`). |
| `-status-tls <addr>` | Override status HTTPS listen address for this run (for example `:443`). |
| `-stratum-tls <addr>` | Override Stratum TLS listen address for this run (for example `:24333`). |
| `-rpc-url <url>` | Override `node.rpc_url` for this run—useful for temporary test nodes. |
| `-rpc-cookie <path>` | Override `node.rpc_cookie_path` when testing alternate cookie locations. |
| `-data-dir <path>` | Override the data directory (logs/state/config/examples) for this run. |
| `-max-conns <n>` | Override max concurrent miner connections (`-1` keeps configured value). |
| `-safe-mode <true|false>` | Force conservative compatibility/safety settings (can disable fast-path tuning and automatic bans). |
| `-secrets <path>` | Point to an alternate `secrets.toml`; the file is not rewritten. |
| `-rewrite-config` | Persist derived values like `pool_entropy` back into `config.toml`. |
| `-stdout` | Mirror every structured log entry to stdout (nice when running under systemd/journal). |
| `-profile` | Write a CPU profile to `default.pgo` for offline `pprof` analysis. |
| `-flood` | Force both `min_difficulty` and `max_difficulty` to a low value for stress testing. |
| `-debug` / `-net-debug` | Force debug logging and raw network tracing at startup. |
| `-no-json` | Disable the JSON status endpoints while keeping the HTML UI active. |
| `-allow-public-rpc` | Allow connecting to an unauthenticated RPC endpoint (testing only). |
| `-allow-rpc-creds` | Force username/password auth from `secrets.toml`; logs a warning and is deprecated. |
| `-backup-on-boot` | Run one forced database backup pass at startup (best-effort). |
| `-saved-workers-local-noauth` | Allow saved-worker pages without Clerk auth (local single-user mode). |

Flags only override values for the running instance; nothing is written back to `config.toml` (except `node.rpc_cookie_path` when auto-detected). Use configuration files for durable behavior.

## Launching goPool

### Initial run

1. Run `./goPool` once without a config. The daemon stops after generating `data/config/examples/`.
2. Copy `data/config/examples/config.toml.example` to `data/config/config.toml`.
3. Provide the required values (payout address, RPC/ZMQ endpoints, any branding overrides) and restart the pool.
4. Optional: copy `data/config/examples/secrets.toml.example`, `data/config/examples/services.toml.example`, `data/config/examples/policy.toml.example`, and `data/config/examples/tuning.toml.example` to `data/config/` and edit as needed.
5. If you prefer reproducible derived settings, rerun `./goPool -rewrite-config` once after editing. This writes derived fields such as `pool_entropy` and normalized listener ports back to `config.toml`.

### Common runtime flags

| Flag | Description |
|------|-------------|
| `-network <mainnet|testnet|signet|regtest>` | Force network defaults for RPC/ZMQ ports and version mask adjustments. Only one mode is accepted. |
| `-bind <ip>` | Override the bind IP for all listeners (Stratum, status UI). Ports remain as configured. |
| `-listen <addr>` | Override Stratum TCP listen address for this run. |
| `-status <addr>` | Override status HTTP listen address for this run. |
| `-status-tls <addr>` | Override status HTTPS listen address for this run. |
| `-stratum-tls <addr>` | Override Stratum TLS listen address for this run. |
| `-rpc-url <url>` | Override the RPC URL defined in `config.toml`. |
| `-rpc-cookie <path>` | Override the RPC cookie path; useful for temporary deployments while keeping `config.toml` untouched. |
| `-data-dir <path>` | Override the data directory used for config/state/logs for this run. |
| `-max-conns <n>` | Override maximum concurrent miner connections (`-1` keeps config value). |
| `-secrets <path>` | Use an alternative `secrets.toml` location (defaults to `data/config/secrets.toml`). |
| `-stdout` | Mirror structured logs to stdout in addition to the rolling files. |
| `-profile` | Write a CPU profile to `default.pgo` for offline `pprof` analysis. |
| `-rewrite-config` | Rewrite `config.toml` after applying runtime overrides (reorders sections and fills derived values). |
| `-flood` | Force `min_difficulty`/`max_difficulty` to the same low value for stress testing. |
| `-safe-mode <true|false>` | Toggle conservative compatibility/safety mode for this run. |
| `-debug` / `-net-debug` | Force debug logging and raw network tracing at startup. |
| `-no-json` | Disable the JSON status endpoints (you still get the HTML status UI). |
| `-allow-public-rpc` | Allow connecting to an unauthenticated RPC endpoint (testing only). |
| `-allow-rpc-creds` | Force RPC auth to come from `secrets.toml` `rpc_user`/`rpc_pass`. Deprecated and insecure; prefer cookie auth. |
| `-backup-on-boot` | Force one startup backup run (best-effort). |
| `-saved-workers-local-noauth` | Disable Clerk auth requirement for saved-worker pages (local single-user mode). |

Additional runtime knobs exist in `config.toml` plus optional `services.toml`/`policy.toml`/`tuning.toml`, but the flags above let you temporarily override them without editing files.
Flags such as `-network`, `-rpc-url`, `-rpc-cookie`, `-allow-public-rpc`, and `-secrets` only affect the current invocation; they override the values from `config.toml` or `secrets.toml` at runtime but are not persisted back to the files.

## Configuration files

### config.toml

The required `data/config/config.toml` is the primary interface for pool behavior. Key sections include:

- `[server]`: `pool_listen`, `status_listen`, `status_tls_listen`, and `status_public_url`. Set `status_tls_listen = ""` to disable HTTPS and rely on `status_listen` only. Leaving `status_listen` empty disables HTTP entirely (e.g., TLS-only deployments). `status_public_url` feeds redirects and Clerk cookie domains. When both HTTP and HTTPS are enabled, the HTTP listener now issues a temporary (307) redirect to the HTTPS endpoint so the public UI and JSON APIs stay behind TLS.
- `[branding]`: Styling and branding options shown in the status UI (tagline, pool donation link, location string).
- `[stratum]`: `stratum_tls_listen` for TLS-enabled Stratum (leave blank to disable secure Stratum), plus `stratum_password_enabled`/`stratum_password` to require a shared password on `mining.authorize`, and `stratum_password_public` to show the password on the public connect panel.
- `policy.toml [stratum]`: `ckpool_emulate` controls CKPool-style subscribe response compatibility.
- `tuning.toml [stratum]`: `fast_decode_enabled`, `fast_encode_enabled`, `tcp_read_buffer_bytes`, and `tcp_write_buffer_bytes` control Stratum fast-path and socket buffer tuning.
- Optional runtime overrides (temporary): `-ckpool-emulate`, `-stratum-fast-decode`, `-stratum-fast-encode`, `-stratum-tcp-read-buffer`, and `-stratum-tcp-write-buffer`.
- `[node]`: `rpc_url`, `rpc_cookie_path`, and ZMQ addresses (`zmq_hashblock_addr`/`zmq_rawblock_addr`).
- `[mining]`: Pool fee, donation settings, and `pooltag_prefix`.
- `[logging]`: `debug` enables verbose runtime logging, and `net_debug` enables raw network tracing (`net-debug.log`) when debug logging is active.

Set numeric values explicitly (do not rely on automation), and trim whitespace (goPool trims internally but a clean config is easier to audit). After editing, restart goPool or send `SIGUSR2` (see below).

### Split Override Files

Optional split override files can layer advanced settings without touching the main config:

- `services.toml`: service/integration settings:
  `auth` (Clerk URLs/session cookie), `backblaze_backup` (backup service settings), `discord` (Discord URLs/channels + worker notify threshold), `status` (`mempool_address_url`, `github_url` links).
- `[rate_limits]`: `max_conns`, burst windows, steady-state rates, `stratum_messages_per_minute` (messages/min before disconnect + 1h ban), and whether to auto-calculate throttles from `max_conns`.
- `[timeouts]`: `connection_timeout_seconds`.
- `[mining]` in `policy.toml`: share-validation policy toggles (`share_*` settings) plus `submit_process_inline`.
- `[difficulty]`: `default_difficulty` fallback when no suggestion arrives, `max_difficulty`/`min_difficulty` clamps (0 disables a clamp), whether to lock miner-suggested difficulty, and whether to enforce min/max on suggested difficulty (ban/disconnect when outside limits). The first `mining.suggest_*` is honored once per connection, triggers a clean notify, and subsequent suggests are ignored.
- `[mining]`: `extranonce2_size`, `template_extra_nonce2_size`, `job_entropy`, `coinbase_scriptsig_max_bytes`, `disable_pool_job_entropy` to remove the `<pool_entropy>-<job_entropy>` suffix, and `difficulty_step_granularity` to control difficulty quantization precision (`1` power-of-two, `2` half-step, `3` third-step, `4` quarter-step default).
- `[hashrate]`: `hashrate_ema_tau_seconds`, `share_ntime_max_forward_seconds`.
- `[peer_cleaning]`: Enable/disable peer cleanup and tune thresholds.
- `[bans]`: Ban thresholds/durations, `banned_miner_types` (disconnect miners by client ID on subscribe), and `clean_expired_on_startup` (defaults to `true`). Prefer `data/config/miner_blacklist.json` for client ID blacklist management; it overrides `banned_miner_types` when present. Set `clean_expired_on_startup = false` if you want to keep expired bans for inspection.
- `[version]`: `min_version_bits` and `share_allow_degraded_version_bits`.

Keep these files absent to use built-in defaults. The first run creates examples under `data/config/examples/`.

### secrets.toml

Keep sensitive data out of `config.toml`:

- `rpc_user`/`rpc_pass`: Only used when `-allow-rpc-creds` is supplied (deprecated). The preferred path is `node.rpc_cookie_path`.
- `discord_token`, `clerk_secret_key`, `clerk_publishable_key`, `backblaze_account_id`, `backblaze_application_key`.

`secrets.toml` is gitignored and should live under `data/config`. The example is re-generated on each restart for reference.

## Node, RPC, and ZMQ

goPool expects a Bitcoin Core node with RPC enabled. Configure:

- `node.rpc_url`: The RPC endpoint for `getblocktemplate` and `submitblock`.
- `node.rpc_cookie_path`: Point this to `~/.bitcoin/.cookie` (or equivalent). When empty, goPool auto-detects common locations and, when successful, writes the discovered path back into `config.toml`.
- `-allow-public-rpc`: Allow connecting to an unauthenticated RPC endpoint (testing only).
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

goPool also auto-creates `/stats/` and `/api/*` handlers plus optional TLS/cert reloading. Run `systemctl kill -s SIGUSR1 <service>` to reload the templates (the previous template set is kept when parsing fails) and `SIGUSR2` to reload the configuration files without stopping the daemon.

## Admin Control Panel

`data/config/admin.toml` is created automatically the first time goPool runs. The generated file documents the panel, defaults to `enabled = false`, and ships with `username = "admin"` plus a random password (check the file to copy the generated secret). Update the file to enable the UI, pick a unique username/password, and keep it out of version control. The `session_expiration_seconds` value controls how long the admin session remains valid (default 900 seconds).

goPool now stores a `password_sha256` alongside the plaintext password. On startup, if `password` is set, goPool verifies/refreshes `password_sha256` to match it. After the first successful admin login, the plaintext `password` is cleared from `admin.toml` and only the hash remains; subsequent logins use the hash.

When enabled, visit `/admin` (deliberately absent from the main navigation) and log in with the credentials stored in `admin.toml`. The panel exposes:

* **Live settings** – a field-based UI that updates goPool's in-memory configuration immediately. Some settings still require a reboot to fully apply across all subsystems.
* **Save to disk** – optionally force-write the current in-memory settings to `config.toml`, `services.toml`, `policy.toml`, and `tuning.toml`.
* **Reboot** – a button that sends SIGTERM to goPool. It requires re-entering the admin password and typing `REBOOT` to confirm the action so your pool does not restart accidentally.

Because the admin login is intentionally simple, bind this UI to trusted networks only (e.g., keep `server.status_listen` local-domain, use firewall rules, or run behind an authenticated proxy) and rotate credentials whenever you rotate administrators.

## Mining specifics

- `mining.pool_fee_percent`, `operator_donation_percent`, and `operator_donation_address` determine how rewards are split.
- `pooltag_prefix` customizes the `/goPool/` coinbase tag (only letters/digits).
- `job_entropy` and `pool_entropy` help make each template unique; disable the suffix with `tuning.toml` `[mining] disable_pool_job_entropy = true`.
- Share validation checks are explicit toggles in `policy.toml` `[mining]`:
  - `share_require_authorized_connection` defaults to `true`.
  - `share_job_freshness_mode` defaults to `1` (options: `0=off`, `1=job_id`, `2=job_id+prevhash`).
  - `share_check_param_format` defaults to `true`.
  - `share_check_ntime_window` and `share_check_version_rolling` default to `true`.
- `share_check_duplicate` defaults to `true` and enables duplicate-share detection (same job/extranonce2/ntime/nonce/version on one connection).
- `share_require_worker_match` defaults to `false`; enable it if you want strict submit/authorize worker-name matching.
- `submit_process_inline` defaults to `false`. Enabling it can reduce submit latency by processing `mining.submit` inline instead of queueing work.
- `vardiff_enabled` defaults to `true`; set it to `false` to keep connection difficulty static unless explicitly changed.

## Logging and diagnostics

Log files live under `data/logs/`:

- `pool.log` – structured log of pool events.
- `errors.log` – captures `ERROR` events for quick troubleshooting.
- `net-debug.log` – recorded when debug logging + network tracing are enabled (`[logging].debug=true` and `[logging].net_debug=true`, or `-debug -net-debug`); contains raw requests/responses and raw RPC/ZMQ traffic.

Use `-stdout` to mirror every entry to stdout. Pair that with `journalctl` or container logs for live debugging.

The internal `simpleLogger` writes a daily rolling file per log type, rotating after three days (configurable via `const logRetentionDays`).

## Backups and bans

goPool maintains its state in `data/state/workers.db`. For Backblaze uploads, it takes a consistent SQLite snapshot first (using SQLite's backup API). If you enable a local snapshot (`keep_local_copy = true` or set `snapshot_path`), goPool also writes a persistent snapshot you can back up safely (for example `data/state/workers.db.bak`).

### Backblaze B2

Configure `services.toml` `[backblaze_backup]`:

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

Expired bans are rewritten on every startup by default. Control this via `policy.toml` `[bans].clean_expired_on_startup` (defaults to `true`). Set it to `false` to inspect expired entries without clearing them.

Clean bans happen inside `NewAccountStore` as it opens the shared state DB; when disabled, you still get bans loaded from disk, but expired entries remain visible via the status UI.

## State database and snapshots

If you need a “safe to copy while goPool is running” database file, enable a local snapshot via `[backblaze_backup].keep_local_copy` (defaults the snapshot to `data/state/workers.db.bak`) or `[backblaze_backup].snapshot_path`. That snapshot is written atomically during each backup run.

When `[backblaze_backup].enabled = true`, goPool always writes a local snapshot (defaulting to `data/state/workers.db.bak` when `snapshot_path` is empty) so you still have a reliable local backup even if B2 is temporarily unavailable.

If you do not have a snapshot configured, stop the pool before copying `data/state/workers.db`. Avoid opening the live DB with external tools while goPool is running.

The `data/state/` directory also holds ban metadata, saved workers snapshots, and any auto-generated JSON caches—keep it alongside your main `data/` backup strategy.

## Tuning limits

Auto-configured accept rate limits calculate `max_accept_burst`/`max_accepts_per_second` based on `max_conns` unless `tuning.toml` overrides them. Recent defaults aim to allow all miners to reconnect within `accept_reconnect_window` seconds.

Key runtime knobs:

- `accept_burst_window` / `accept_reconnect_window` / `accept_steady_state_*` – windows that shape burst vs sustained behavior.
- `hashrate_ema_tau_seconds` – tune EMA smoothing for per-worker hashrate.
- `share_ntime_max_forward_seconds` – tolerated future timestamps on shares (default 7000 seconds).
- `peer_cleaning` – enable/disable and tune thresholds for cleaning stalled miners.
- `difficulty` – clamp advertised difficulty, optionally enforce min/max on miner-suggested difficulty, and optionally lock miner suggestions.

Each override value logs when set, so goPool operators can audit what changed via `pool.log`.

## Runtime operations

- **SIGUSR1** reloads the HTML templates under `data/templates/`. Errors (parse failures, missing files) are logged but the previous template set remains active so the site keeps serving—check `pool.log` if pages look odd after a reload.
- **SIGUSR2** reloads `config.toml`, `secrets.toml`, `services.toml`, `policy.toml`, and `tuning.toml`, reapplies overrides, and updates the status server with the new config.
- **Shutdown** occurs on `SIGINT`/`SIGTERM`. goPool stops the status servers, Stratum listener, and pending replayers gracefully.
- **TLS cert reloading** uses `certReloader` to monitor `data/tls_cert.pem`/`tls_key.pem` hourly. Certificate renewals (e.g., via certbot) are picked up without restarts.

## Monitoring APIs

- `/api/overview`, `/api/pool-page`, `/api/server`, `/api/node`, `/api/pool-hashrate`, and `/api/blocks` provide the public JSON snapshots consumed by the UI. Disable all JSON APIs with `-no-json`.
- `/user/<wallet>` and `/stats/<wallet>` are standard wallet lookup routes.
- `/users/<wallet_sha256>` is the privacy variant of wallet lookup (keeps raw wallet values out of links/bookmarks).
- `/stats/` serves the saved-worker dashboards, including per-worker graphing data.
- Authenticated JSON routes (`/api/saved-workers*`, `/api/discord/notify-enabled`, `/api/auth/session-refresh`) back saved-worker and Clerk/Discord flows when enabled.

## Profiling and debugging

- `-profile` writes `default.pgo`; use `go tool pprof` or `./scripts/profile-graph.sh default.pgo profile.svg` to inspect the profile and generate SVGs.
- Watch `/api/pool-page` and `/api/server` for RPC/share error counters and feed-health drift.
- `net-debug.log` records RPC/ZMQ traffic when debug logging + network tracing are enabled (`[logging].debug=true` and `[logging].net_debug=true`, or `-debug -net-debug`).

## Related guides

- **`documentation/TESTING.md`** – How to run and extend the test suite, including fuzz targets and benchmarks.

Refer back to the concise [main README](../README.md) for quick start instructions, and keep this document nearby while you tune your deployment.
