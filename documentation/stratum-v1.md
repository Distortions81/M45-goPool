# Stratum v1 compatibility (goPool)

This document summarizes **Stratum v1 JSON-RPC methods** goPool currently recognizes on the Stratum listener, and notes messages that are **acknowledged but not fully implemented** (or not implemented at all).

Code pointers:

- Dispatch / method routing: `miner_conn.go`
- Subscribe / authorize / configure / notify: `miner_auth.go`
- Encode helpers + subscribe response shape: `miner_io.go`
- Difficulty / version mask / extranonce notifications: `miner_rejects.go`
- Submit parsing / policy: `miner_submit_parse.go`
- Fast-path method sniffing (decode): `stratum_sniff.go`

## Supported (client → pool)

- `mining.subscribe`
  - Accepts a best-effort **client identifier** in `params[0]` (used for UI aggregation).
  - Accepts a best-effort **session/resume token** in `params[1]` and uses it as the per-connection session ID (also returned in the subscribe response).
  - Subscribe response shape is controlled by the runtime startup flag `-ckpool-emulate` (not `config.toml`):
    - `true` (default): CKPool-style tuple list with `mining.notify` only.
    - `false`: extended tuple list includes `mining.set_difficulty`, `mining.notify`, `mining.set_extranonce`, and `mining.set_version_mask`.
- `mining.authorize`
  - Usually follows `mining.subscribe`, but goPool also accepts authorize-before-subscribe and will begin sending work only after subscribe completes.
  - Optional shared password enforcement via config.
- `mining.auth`
  - Alias for `mining.authorize` (CKPool compatibility).
- `mining.submit`
  - Standard 5 params plus an optional 6th `version` field (used for version-rolling support).
- `mining.ping` (and `client.ping`)
  - Replies with `"pong"`.
- `client.get_version`
  - Returns a `goPool/<version>` string.
- `mining.configure`
  - Implements a subset of common extension negotiation:
    - `version-rolling` (BIP310-style negotiation; server may follow with `mining.set_version_mask`)
    - `suggest-difficulty` / `suggestdifficulty` (advertised as supported)
    - `minimum-difficulty` / `minimumdifficulty` (optionally used as a per-connection difficulty floor)
    - `subscribe-extranonce` / `subscribeextranonce` (treated as opt-in for `mining.set_extranonce`)
- `mining.extranonce.subscribe`
  - Opt-in for `mining.set_extranonce` notifications.
- `mining.suggest_difficulty`
  - Supported as a client hint; only the first suggest per connection is applied.
- `mining.suggest_target`
  - Supported as a client hint; converted to difficulty and applied similarly to `mining.suggest_difficulty`.
- `mining.set_difficulty` / `mining.set_target`
  - Non-standard pool→miner messages that some proxies/miners accidentally send to the pool.
  - goPool tolerates these by treating them like `mining.suggest_difficulty` / `mining.suggest_target`.

## Supported (pool → client notifications)

- `mining.notify`
- `mining.set_difficulty`
- `client.show_message` (used for bans/warnings)
- `mining.set_extranonce`
  - Sent only after opt-in via `mining.extranonce.subscribe` or `mining.configure` (`subscribe-extranonce`).
- `mining.set_version_mask`
  - Sent only after version-rolling negotiation (and when a mask is available).

## Acknowledged for compatibility (but not fully supported)

- `mining.get_transactions`
  - Returns a list of transaction IDs (`txid`) for the requested job (or the most recent/last job when called without params).
  - For bandwidth/safety, this returns txids only (not raw transaction hex).
- `mining.capabilities`
  - Returns `true` but does not currently act on advertised capabilities.
- `client.show_message` / `client.reconnect`
  - If received as a *request* (client → pool), goPool acknowledges with `true` to avoid breaking certain proxies.

## Not implemented / notable deviations

- `client.reconnect` (pool → client notification)
  - goPool does not currently initiate reconnects via a `client.reconnect` notification.
- `mining.set_target` (pool → client notification)
  - goPool does not send `mining.set_target` (it uses `mining.set_difficulty`).
- Unknown methods
  - Unknown `method` values are **replied to with a JSON-RPC error** (`-32601 method not found`) when an `id` is present; when `id` is missing or `null`, they are treated as notifications and ignored.

## Handshake timing / gating

- Pool→miner notifications are not sent until the connection is subscribed; work (`mining.notify`) is only started once the connection is both subscribed and authorized.
- Initial work is intentionally delayed very briefly after authorize (`defaultInitialDifficultyDelay`, currently 250ms) to give miners a chance to send `mining.configure` / `mining.suggest_*` first; if the miner sends `mining.configure`, goPool will send the initial work immediately after the configure response is written.
- When the job feed is degraded (no template, RPC errors, or node syncing/indexing state), goPool refuses new connections and disconnects existing miners until updates recover.
