# Configuration & operations

## Configuration files

- `data/config/config.toml` (**required**): primary, user-facing options such as ports, branding, payout address, RPC URL, and basic difficulty/fee settings.
- `data/config/secrets.toml` (**required**): sensitive values like `rpc_user` / `rpc_pass` needed to connect to bitcoind.
- `data/config/tuning.toml` (optional): advanced tuning and limits. Deleting this file reverts to the built-in defaults. See `data/config/examples/tuning.toml.example` for the current list.
- Branding options in `config.toml` include `discord_url` and `github_url`, which control the header and About-page links.

## Tuning highlights

- `hashrate_ema_tau_seconds` – time constant (seconds) for the per-connection hashrate EMA used in worker stats. Larger values smooth the reports but react more slowly; default `600` (~10 minutes).
- `ntime_forward_slack_seconds` – how far miners may roll `ntime` beyond the template’s `curtime` / `mintime`; default `7000`.

## Launch flags

- `-sha256-no-avx` (default `false`): disables the AVX-accelerated `sha256-simd` backend so the pool falls back to the platform-independent `crypto/sha256`.

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
