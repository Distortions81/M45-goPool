## Experimental vs `main`

- **Scope:** `92 files`, `+7442/-1563`.

### Runtime / VarDiff
- Hashrate confidence is now 4-tier in UI: `~`, `≈`, `≈+`, `✓`.
- Settling logic tightened; fewer false stable flips and less warmup jitter.
- `shares/min` now stays visible during settling/reset windows (less `---/0` flicker).
- VarDiff retuned for faster convergence with stronger noise guards:
- defaults: target `10 shares/min`, window `60s` (adaptive `30s..4m`), step `2`, damping `0.7`, delay `30s`.
- new safety/cooldown caps reduce oscillation and bad large jumps.

### Saved Workers / Storage / Privacy
- Saved-worker identity is now hash-based.
- `worker_hash = SHA256(full worker string)`.
- `worker` column now stores the same hash identity.
- `worker_display` stores censored label.
- Migration/backfill rewrites legacy rows, dedupes by `(user_id, worker_hash)`, preserves notify + best difficulty.
- Added unique index on `(user_id, worker_hash)`.
- Privacy/terms disclosures updated to match this storage model (Last Updated: **Feb 14, 2026**).

### Saved Workers Runtime
- Added manual force reset: `POST /worker/reconnect`.
- Applies `30s` reset ban, then disconnects for clean reconnect.
- Saved Workers now shows best recent **Work Start** latency and RTT-based ping stats.

### Overview / API / Caching
- Overview graph first load primes with history via `/api/pool-hashrate?include_history=1`.
- Live graph updates now use `/api/pool-hashrate` only.
- `/api/pool-hashrate` now supports optional `pool_hashrate_history`.
- Added short-lived server-side HTML/JSON response cache (`5s` TTL).

### Admin / Ops
- New pages: `/admin/operator`, `/admin/logs` (live tail + password-protected log-level control).
- New optional flag: `-miner-profile-json <path>`.
- Added admin controls for `disable_connect_rate_limits` and peer-cleanup tuning.
- Reduced ZMQ/longpoll reconnect log spam.
- Tightened admin/runtime safety bounds for outage-prone settings.

### Docs / Tests
- JSON API docs synchronized with current responses.
- Coverage expanded for vardiff, hashrate confidence, saved-worker migration, admin, and JSON method/query behavior.
- Flaky/redundant tests removed and helpers consolidated.
