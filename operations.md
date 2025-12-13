# Configuration & operations

## Configuration files

- `data/config/config.toml` (**required**): primary, user-facing options such as ports, branding, payout address, RPC URL, and basic difficulty/fee settings.
- `data/config/secrets.toml` (**required**): sensitive values like `rpc_user` / `rpc_pass` needed to connect to bitcoind.
- `data/config/tuning.toml` (optional): advanced tuning and limits. Deleting this file reverts to the built-in defaults. See `data/config/examples/tuning.toml.example` for the current list.

## Tuning highlights

- `hashrate_ema_tau_seconds` – time constant (seconds) for the per-connection hashrate EMA used in worker stats. Larger values smooth the reports but react more slowly; default `600` (~10 minutes).
- `ntime_forward_slack_seconds` – how far miners may roll `ntime` beyond the template’s `curtime` / `mintime`; default `7000`.

## Launch flags

- `-sha256-no-avx` (default `false`): disables the AVX-accelerated `sha256-simd` backend so the pool falls back to the platform-independent `crypto/sha256`.

## Status pages & API

- HTML status pages are served on `status_listen` (default `:80`) from Go `html/template` files in `data/www`.
  - `status.tmpl` – dashboard
  - `worker_status.tmpl` – per-worker view
  - `server_stats.tmpl` – server stats page
- The main status page exposes per-worker statistics (rolling hashrate, recent share window, ban status) and a pool-wide hashrate graph based on the EMA, which is smoothed client-side for a stable curve.
- APIs such as `/api/status-page` and `/api/pool-hashrate` return JSON suitable for monitoring or dashboards.

## Logging

- Logs live under `data_dir/logs` (e.g. `data/logs`):
  - `pool.log`: structured pool log. By default only errors are logged; build with `-tags debug` or `-tags verbose` for more output.
  - `net-debug.log`: network traffic log emitted only when building with `-tags debug`.
  - `errors.log`: error-only log for easier troubleshooting.
