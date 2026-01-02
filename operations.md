# Configuration & operations

## Configuration files

- `data/config/config.toml` (**required**): primary, user-facing options such as ports, branding, payout address, RPC URL, and basic difficulty/fee settings.
- `data/config/secrets.toml` (**required** only when you run with `-allow-rpc-credentials`): store `rpc_user` / `rpc_pass` and optional Clerk secrets for the pool. Without that flag goPool refuses to use RPC credentials so you must configure `node.rpc_cookie_path` (or rely on the automatic detection below) to point at bitcoind's auth cookie.
- `data/config/tuning.toml` (optional): advanced tuning and limits. Deleting this file reverts to the built-in defaults. See `data/config/examples/tuning.toml.example` for the current list.
- Branding options in `config.toml` include `discord_url` and `github_url`, which control the header and About-page links.

## Backblaze database backups

- Backblaze B2 integration is configured via a `[backblaze_backup]` table in `data/config/config.toml`. The service stays disabled unless `enabled = true` and the required fields are populated.
  - `account_id`, `bucket`, and optional `prefix` belong in the base config so you can keep namespace settings under version control. Backblaze requires the bucket name to be lowercase.
  - The sensitive `application_key` (and optionally `backblaze_account_id`) must live in `data/config/secrets.toml` so it never shows up in the checked-in config (`backblaze_application_key`, `backblaze_account_id`). Use the master application key (not the key’s ID); if you don’t already have one, generate a new application key with write permissions for your bucket. The `backblaze_account_id` in the secrets file should match the Key ID associated with that master application key.
- `interval_seconds` controls how often goPool snapshots `state/workers.db` and uploads that file (default `43200`, i.e. every 12 hours); the configured prefix is prepended so you can namespace the upload, and Backblaze keeps every version unless you add lifecycle rules.
  - Backups only succeed when the configured B2 bucket already exists and the credentials have permissions to write objects; errors are logged but do not stop the pool from running.
  - `keep_local_copy` (default `true`) stores a local copy of the last snapshot in `data/state/` even if Backblaze uploads are disabled.

## Tuning highlights

- `hashrate_ema_tau_seconds` – time constant (seconds) for the per-connection hashrate EMA used in worker stats. Larger values smooth the reports but react more slowly; default `600` (~10 minutes).
- `ntime_forward_slack_seconds` – how far miners may roll `ntime` beyond the template’s `curtime` / `mintime`; default `7000`.

## Launch flags

- `-sha256-no-avx` (default `false`): disables the AVX-accelerated `sha256-simd` backend so the pool falls back to the platform-independent `crypto/sha256`.
- `-allow-rpc-credentials` (default `false`): force goPool to use `rpc_user`/`rpc_pass` from `data/config/secrets.toml` instead of the auth cookie; this is deprecated and insecure, so avoid it whenever possible. The flag logs a warning each launch and is the only way to load credentials from secrets.toml.
- If `node.rpc_cookie_path` is empty, goPool attempts to mimic `btcsuite/btcd/rpcclient`'s cookie autodetection: it first checks `$BITCOIN_DATADIR`, then btcd's `AppDataDir("btcd", false)/data` layout, and finally a collection of common Linux cookie locations (`~/.bitcoin/.cookie`, `/var/lib/bitcoin/.cookie`, `/home/bitcoin/.bitcoin/.cookie`, `/etc/bitcoin/.cookie`, plus the regtest/testnet3/testnet4/signet/simnet variants) before failing.
- When a working RPC cookie is detected (either through autodetection or a manual `node.rpc_cookie_path` override), goPool rewrites `data/config/config.toml` so the discovered path is persisted for subsequent restarts.
- `-rpc-cookie-path` (default empty): explicitly set the RPC cookie path at launch and skip autodetection. This is handy for temporary overrides or debugging when the cookie lives somewhere unusual.
- `node.rpc_cookie_path` may point at a directory (for example, your Bitcoin datadir); goPool will look for a `.cookie` file inside that directory so you can stop worrying about the exact filename.
- `node.allow_public_rpc` (default `false`): set this to `true` when connecting to intentionally unauthenticated RPC endpoints (only recommended for public/testing nodes such as `https://bitcoin-rpc.publicnode.com`). When enabled and `node.rpc_cookie_path` remains empty, goPool skips credential loading and connects without Basic auth, which lets you test the pool against services that offer open RPC access.

## Bitcoin Core ZMQ topics

goPool reads Bitcoin Core's ZMQ notifications from `node.zmq_block_addr` and subscribes to these topics:

- `rawblock` (required): used to trigger job/template refresh on new blocks (mining still works with only this).
- `hashblock` (optional): redundant refresh trigger; goPool can use either.
- `rawtx` / `hashtx` (optional): used for status/metrics only.

Bitcoin Core allows publishing each topic to a different ZMQ address/port (e.g. `zmqpubrawblock=...`, `zmqpubrawtx=...`). goPool currently expects all topics it subscribes to to be available on the single configured `node.zmq_block_addr`, so publish the optional tx topics to the same endpoint if you want those UI stats to populate.
goPool also uses RPC longpoll for frequent `getblocktemplate` updates (including `coinbasevalue` / transaction fees); ZMQ is used for fast new-block detection and status metrics.

## Status pages & API

- HTML status pages are served on `status_listen` (default `:80`) from Go `html/template` files in `data/templates/`.
  - `overview.tmpl` – dashboard
  - `worker_status.tmpl` – per-worker view
  - `server.tmpl` – server stats page
  - `about.tmpl` – about page
  - `pool.tmpl` – pool info page
  - `node.tmpl` – node info page
  - `layout.tmpl` – shared layout
- The main status page exposes per-worker statistics (rolling hashrate, recent share window, ban status) and a pool-wide hashrate graph based on the EMA, which is smoothed client-side for a stable curve.
- APIs such as `/api/overview` and `/api/pool-hashrate` return JSON suitable for monitoring or dashboards.

### Live template reloading

Templates can be reloaded without restarting the pool by sending a `SIGUSR1` signal to the process:

```bash
# Find the pool process ID
ps aux | grep goPool

# Send SIGUSR1 to reload templates
kill -SIGUSR1 <pid>

# Or if using systemd
systemctl kill -s SIGUSR1 gopool.service
```

This is useful for updating the UI while the pool is running. If template parsing fails, the error is logged and the old templates remain active.

### Live config reloading

Status pages and API responses reflect the current configuration, so editing `data/config/*.toml` and sending `SIGUSR2` tells goPool to re-read the base config, tuning overrides, and secrets without restarting the whole pool. The reload refreshes the branding, payout, difficulty, and rate-limit summaries shown on the UI, but listeners or Clerk/Clerk callback paths stay tied to the values that were active at startup, so restart after making those kinds of changes.

```bash
# Signal goPool to reload the config files
kill -SIGUSR2 <pid>

# Or via systemd
systemctl kill -s SIGUSR2 gopool.service
```

## Logging

- Logs live under `data_dir/logs` (e.g. `data/logs`):
  - `pool.log`: structured pool log. By default only errors are logged; build with `-tags debug` or `-tags verbose` for more output.
  - `net-debug.log`: network traffic log emitted only when building with `-tags debug`.
  - `errors.log`: error-only log for easier troubleshooting.
