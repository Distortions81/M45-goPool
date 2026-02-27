# goPool JSON APIs

goPool’s status UI is backed by a small set of HTTP JSON endpoints intended for monitoring, dashboards, and the built-in web UI.

This document describes the **HTTP `/api/*` endpoints** exposed by the status webserver (not Bitcoin Core JSON-RPC, and not Stratum’s JSON-RPC messages).

## Quick facts

- **Base URL:** same host/port as the status UI (`server.status_listen` / `server.status_tls_listen`).
- **Disable APIs:** start the daemon with `-no-json` to disable all `/api/*` handlers.
- **Schema versioning:** responses include `api_version` (currently `"1"`). There is no version prefix in the URL path.
- **Encoding:** JSON (snake_case keys) via `sonic`.

## Transport and redirects

If `server.status_tls_listen` is enabled, the HTTP listener (if configured) redirects to HTTPS with a **307 Temporary Redirect**. This applies to both the HTML UI and JSON APIs.

## Conventions

### Content-Type

Successful responses set `Content-Type: application/json`.

### Error responses are plain text

Most errors are returned using `http.Error(...)` and therefore:

- use a relevant HTTP status code (e.g., `400`, `401`, `403`, `404`, `405`, `500`)
- return a **plain-text** body (not a JSON error envelope)

### Timestamps

- `time.Time` fields are encoded as **RFC3339** JSON strings (example: `"2026-01-31T19:55:02Z"`).
- Many endpoints also expose timestamp strings explicitly (usually RFC3339).

### Durations

Some responses include `time.Duration` values (for example `uptime` or `render_duration`).

In JSON, these are encoded as **integers** representing **nanoseconds** (because `time.Duration` is an `int64`).

### Caching headers

The “public snapshot” endpoints are served from an in-memory cache and include:

- `Cache-Control: no-cache, no-store, must-revalidate`
- `X-JSON-Updated-At`: RFC3339 timestamp of the cached payload
- `X-JSON-Next-Update-At`: RFC3339 timestamp when the cache will be refreshed

Note: despite the name, these responses are not intended to be browser-cacheable; the cache is server-side.

### Privacy / redaction

Some public endpoints censor sensitive user data (worker identifiers and hashes) so they can be safely embedded in a public status page.

In particular:

- worker names may be shortened
- wallet addresses may be shortened
- share hashes may be shortened
- some fields (like `wallet_script` / `last_share_detail`) may be cleared

## Endpoint catalog

  - `GET /api/overview` for headline miner/hashrate values
  - `GET /api/pool-page` for pool RPC/share diagnostics
  - `GET /api/server` for process/system diagnostics
  - `GET /api/pool-hashrate` for fast hashrate + block timer telemetry
  - `GET /api/blocks` for recent found blocks

Public (no auth):

- `GET /api/overview` — overview page snapshot (default refresh ~10s)
- `GET /api/pool-page` — pool diagnostics snapshot (default refresh ~10s)
- `GET /api/node` — node info snapshot (default refresh ~10s)
- `GET /api/server` — server diagnostics snapshot (default refresh ~10s)
- `GET /api/pool-hashrate` — fast pool hashrate/block timer snapshot (default refresh ~5s)
- `GET /api/blocks` — recent blocks list (default refresh ~3s; supports `?limit=`)

## Endpoints

### GET /api/overview

Overview snapshot used by the home page.

Response object: `OverviewPageData`

- `api_version` (string)
- `active_miners` (int)
- `active_tls_miners` (int)
- `shares_per_minute` (number, optional)
- `pool_hashrate` (number, optional)
- `pool_tag` (string, optional)
- `btc_price_fiat` (number, optional)
- `btc_price_updated_at` (string, optional; RFC3339)
- `fiat_currency` (string, optional)
- `render_duration` (int; duration nanoseconds)
- `workers` (array of `RecentWorkView`, censored)
- `banned_workers` (array of `WorkerView`, censored and truncated)
- `best_shares` (array of `BestShare`, censored)
- `miner_types` (array of `MinerTypeView`; optional)

Types referenced:

- `RecentWorkView`
  - `name` (string; censored)
  - `display_name` (string; censored)
  - `rolling_hashrate` (number)
  - `hashrate_accuracy` (string; optional; `"~"` while warming up, `"≈"` while settling; omitted when stable)
  - `difficulty` (number)
  - `vardiff` (number)
  - `share_rate` (number)
  - `accepted` (integer)
  - `connection_id` (string)

- `WorkerView` (used for `banned_workers`; values are censored/truncated)
  - `name` (string; censored)
  - `display_name` (string; censored)
  - `banned` (boolean)
  - `banned_until` (string; RFC3339; optional)
  - `ban_reason` (string; optional; brief)

- `BestShare`
  - `worker` (string; censored)
  - `difficulty` (number)
  - `timestamp` (string; RFC3339)
  - `hash` (string; censored; optional)
  - `display_worker` (string; optional)
  - `display_hash` (string; optional)

Example:

```bash
curl -sS https://STATUS_HOST/api/overview | jq .
```

### GET /api/pool-page

Pool diagnostics snapshot used by `/pool`.

Response object: `PoolPageData`

- `api_version` (string)
- `blocks_accepted` (integer)
- `blocks_errored` (integer)
- `rpc_gbt_last_sec` (number)
- `rpc_gbt_max_sec` (number)
- `rpc_gbt_count` (integer)
- `rpc_submit_last_sec` (number)
- `rpc_submit_max_sec` (number)
- `rpc_submit_count` (integer)
- `rpc_errors` (integer)
- `share_errors` (integer)
- `rpc_gbt_min_1h_sec` (number)
- `rpc_gbt_avg_1h_sec` (number)
- `rpc_gbt_max_1h_sec` (number)
- `error_history` (array of `PoolErrorEvent`; optional)

`PoolErrorEvent`:

- `at` (string; RFC3339; optional)
- `type` (string)
- `message` (string)

Example:

```bash
curl -sS https://STATUS_HOST/api/pool-page | jq .
```

### GET /api/node

Bitcoin node info snapshot used by `/node`.

Response object: `NodePageData`

- `api_version` (string)
- `node_network` (string; optional)
- `node_subversion` (string; optional)
- `node_blocks` (integer)
- `node_headers` (integer)
- `node_initial_block_download` (boolean)
- `node_connections` (integer)
- `node_connections_in` (integer)
- `node_connections_out` (integer)
- `node_peers` (array of `NodePeerInfo`; optional)
- `node_pruned` (boolean)
- `node_size_on_disk_bytes` (integer)
- `node_peer_cleanup_enabled` (boolean)
- `node_peer_cleanup_max_ping_ms` (number)
- `node_peer_cleanup_min_peers` (integer)
- `genesis_hash` (string; optional)
- `genesis_expected` (string; optional)
- `genesis_match` (boolean)
- `best_block_hash` (string; optional)

`NodePeerInfo`:

- `display` (string)
- `ping_ms` (number)
- `connected_at` (integer; Unix seconds)

Example:

```bash
curl -sS https://STATUS_HOST/api/node | jq .
```

### GET /api/server

Server diagnostics snapshot used by `/server`.

Response object: `ServerPageData`

- `api_version` (string)
- `uptime` (int; duration nanoseconds)
- `rpc_error` (string; optional)
- `rpc_healthy` (boolean)
- `rpc_disconnects` (integer)
- `rpc_reconnects` (integer)
- `accounting_error` (string; optional)
- `job_feed` (object `ServerPageJobFeed`)
- `process_goroutines` (integer)
- `process_cpu_percent` (number)
- `go_mem_alloc_bytes` (integer)
- `go_mem_sys_bytes` (integer)
- `process_rss_bytes` (integer)
- `system_mem_total_bytes` (integer)
- `system_mem_free_bytes` (integer)
- `system_mem_used_bytes` (integer)
- `system_load1` (number)
- `system_load5` (number)
- `system_load15` (number)

`ServerPageJobFeed`:

- `last_error` (string; optional)
- `last_error_at` (string; optional; RFC3339)
- `error_history` (array of string; optional)
- `zmq_healthy` (boolean)
- `zmq_disconnects` (integer)
- `zmq_reconnects` (integer)
- `last_raw_block_at` (string; optional; RFC3339)
- `last_raw_block_bytes` (integer; optional)
- `block_hash` (string; optional)
- `block_height` (integer; optional)
- `block_time` (string; optional; RFC3339)
- `block_bits` (string; optional)
- `block_difficulty` (number; optional)

Example:

```bash
curl -sS https://STATUS_HOST/api/server | jq .
```

### GET /api/pool-hashrate

Fast “headline stats” endpoint used for the hashrate UI and block timer.

Query parameters:

- `include_history` (optional; set to `1` to include `pool_hashrate_history` for initial chart priming)

Response object:

- `api_version` (string)
- `pool_hashrate` (number)
- `pool_hashrate_history` (array of `PoolHashrateHistoryPoint`; optional; returned when `include_history=1`)
- `block_height` (integer)
- `block_difficulty` (number)
- `block_time_left_sec` (integer; signed seconds)
  - `-1` means “block timer not started yet”
  - `<0` (other values) means the target interval has been exceeded (overdue)
- `recent_block_times` (array of string; RFC3339)
- `next_difficulty_retarget` (object; optional)
  - `height` (integer)
  - `blocks_away` (integer)
  - `duration_estimate` (string; optional; human-readable)
- `template_tx_fees_sats` (integer; optional)
- `template_updated_at` (string; optional; RFC3339)
- `updated_at` (string; RFC3339)

`PoolHashrateHistoryPoint`:

- `at` (string; RFC3339)
- `hashrate` (number)
- `block_height` (integer; optional)

Example:

```bash
curl -sS https://STATUS_HOST/api/pool-hashrate | jq .
```

### GET /api/blocks

Recent block list.

Query parameters:

- `limit` (optional int; `1..100`; default `10`)

Response: JSON array of `FoundBlockView` objects. Values are censored for safe display.

`FoundBlockView`:

- `height` (integer)
- `hash` (string; censored)
- `display_hash` (string; censored)
- `worker` (string; censored)
- `display_worker` (string; censored)
- `timestamp` (string; RFC3339)
- `share_diff` (number)
- `pool_fee_sats` (integer; optional)
- `worker_payout_sats` (integer; optional)
- `confirmations` (integer; optional)
- `result` (string; optional; `"possible"`, `"winning"`, or `"stale"`)

Example:

```bash
curl -sS 'https://STATUS_HOST/api/blocks?limit=25' | jq .
```
