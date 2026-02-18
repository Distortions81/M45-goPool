# Discord Changelog (operator copy/paste)

This file is intentionally written in Discord-friendly Markdown blocks so pool operators can copy/paste updates.

## Last 24h (as of 2026-02-18)

```md
**goPool (last 24h) — operator notes**

**Stratum v1 compatibility & correctness**
- Unknown JSON-RPC methods now return `-32601` when an `id` is present (prevents strict miners from waiting forever on a response).
- Accept `mining.auth` as an alias for `mining.authorize` (miner-compat).
- Tolerate `authorize`/`auth` arriving before `subscribe`; we still don’t send work/notify until the miner is subscribed.
- `mining.get_transactions` now returns txids for a job id (or the most recent job) instead of always returning `[]`.

**Handshake / notification sequencing**
- Pool → miner notifications (`mining.notify`, `mining.set_difficulty`, `mining.set_extranonce`, `mining.set_version_mask`) are gated until after `mining.subscribe` is processed.
- Startup gating has a `60s` grace window so boot-up jitter doesn’t immediately kick miners or show “node down”.

**Node health, gating, and miner safety**
- The pool treats the node as “degraded” only when the node is unusable (RPC errors) or is in a non-usable state like syncing/indexing (not based on “feed lag” timers).
- When degraded persists past a short grace window, the pool disconnects miners (with a Stratum `client.show_message`) and refuses new Stratum connections until the node recovers.
- Job template fetching includes a periodic heartbeat so “still working” is tracked even when templates don’t change frequently.

**Website UX during node issues**
- Main page (`/`) shows a dedicated “node down / updates degraded” template (logo + last success/error + syncing/indexing hints) instead of silently looking “stale”.
- When the node is connected-but-degraded, the UI surfaces the node’s current message/state (e.g. indexing/syncing) rather than just a generic warning.

**UI routing (net final state)**
- Keep plaintext worker lookup routes: `/user/<worker>`, `/users/<worker>`, `/stats/<worker>`, `/app/<worker>`.

**Submission pipeline optimizations (share processing hot path)**
- Faster duplicate-share checks and decoded-share key building.
- Cache job fields used during submit validation (e.g., prevhash/height, ntime window bounds).
- Reduce allocs in extranonce2 handling and in some version-rolling / freshness checks.

**Hex / encoding micro-opts**
- Added LUT-based fixed-width hex helpers for common 2/4/32-byte cases; avoid `fmt.Sprintf` for fixed-width hex formatting.

**Performance notes (microbench, `fd1cf98` → `3330e26`, Ryzen 9 7950X, `-count=7 -benchtime=200ms`)**
- Stratum manual decode: `-49.9%` time (`45.81ns → 22.95ns`) and `-100%` allocs (`2 → 0 allocs/op`).
- Stratum manual `mining.submit` decode: `-42.5%` time (`179.7ns → 103.4ns`) and `-100%` allocs (`7 → 0 allocs/op`).
- Share-prep task: `-2.3% to -2.7%` time and `-10%` allocs (`10 → 9 allocs/op`).
- Merkle branch compute (1000 tx): `-20.5%` time (`204.9µs → 162.9µs`).
```
