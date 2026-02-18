# Stratum v1 compatibility (goPool)

This document summarizes **Stratum v1 JSON-RPC methods** goPool currently recognizes on the Stratum listener, and notes messages that are **acknowledged but not fully implemented** (or not implemented at all).

Code pointers:

- Dispatch / method routing: `miner_conn.go`
- Subscribe / authorize / configure / notify: `miner_auth.go`
- Encode helpers + subscribe response shape: `miner_io.go`
- Difficulty / version mask / extranonce notifications: `miner_rejects.go`
- Submit parsing / policy: `miner_submit_parse.go`

## Supported (client → pool)

- `mining.subscribe`
  - Accepts a best-effort **client identifier** in `params[0]` (used for UI aggregation).
  - Accepts a best-effort **session/resume token** in `params[1]` and uses it as the subscription ID in the subscribe response.
- `mining.authorize`
  - Usually follows `mining.subscribe`, but goPool also accepts authorize-before-subscribe and will begin sending work only after subscribe completes.
  - Optional shared password enforcement via config.
- `mining.auth`
  - Alias for `mining.authorize` (CKPool compatibility).
- `mining.submit`
  - Standard 5 params plus an optional 6th `version` field (used for version-rolling support).
- `mining.ping` (and `client.ping`)
  - Replies with `"pong"`.
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
  - Returns a list of transaction IDs (`txid`) for the requested job (or the last/current job when called without params).
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
  - Unknown `method` values are **replied to with a JSON-RPC error** when an `id` is present; when `id` is missing or `null`, they are treated as notifications and ignored.

## Handshake timing / gating

- Pool→miner notifications (`mining.notify`, `mining.set_difficulty`, `mining.set_extranonce`, `mining.set_version_mask`) are only sent after `mining.subscribe` has completed.
- Initial work is normally sent shortly after authorize to allow miners a brief negotiation window; if the miner sends `mining.configure`, goPool will send the initial work immediately after configure is handled.
- When the job feed is degraded (no template, errors, or updates stalled beyond `stratumMaxFeedLag`), goPool refuses new connections and disconnects existing miners until updates recover.
