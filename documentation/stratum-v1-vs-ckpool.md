# goPool Stratum v1 vs CKPool (differences)

This is a practical compatibility-oriented list of ways goPool’s Stratum v1 behavior differs from CKPool.

CKPool reference implementation used for this comparison is `koga73/ckpool` (a GitHub mirror of CKPool) and, specifically, `src/stratifier.c` / `src/ckpool.h`.

## Method names / surface area

- **Authorization method name**
  - CKPool uses `mining.auth`.
  - goPool uses `mining.authorize` and also accepts `mining.auth` as an alias.
- **CKPool-specific deployment/mode methods**
  - CKPool supports additional methods related to passthrough / nodes / remote servers such as `mining.passthrough`, `mining.node`, and `mining.remote`.
  - goPool does not implement those deployment modes or methods.
- **Terminate**
  - CKPool supports `mining.term` (disconnect).
  - goPool does not implement `mining.term`.

## Subscribe behavior / subscribe result shape

- **Subscribe response subscription list**
  - CKPool’s subscribe `result` includes only `mining.notify` with a subscription id string, plus `extranonce1` and `extranonce2_size`.
  - goPool’s subscribe `result` advertises multiple subscriptions (`mining.set_difficulty`, `mining.notify`, `mining.set_extranonce`, `mining.set_version_mask`), plus `extranonce1` and `extranonce2_size`.
- **Session resumption token (`mining.subscribe` params[1])**
  - CKPool parses `params[1]` as a reconnect/session token and attempts to match a previous disconnected session (primarily to preserve `extranonce1`).
  - goPool treats `params[1]` as an optional session id and uses it for its per-connection session tracking (and also returns a session id in the subscribe response).

## Ordering / gating (subscribe vs auth vs work)

- **Authorize-before-subscribe**
  - CKPool explicitly tolerates `mining.auth` before `mining.subscribe`.
  - goPool tolerates authorize-before-subscribe (but does not start sending work until subscribe).
- **Message gating**
  - CKPool drops many requests unless `subscribed` and `authorised` flags are set.
  - goPool is more permissive for some non-standard methods (e.g. it acks some `client.*` / draft methods for compatibility).

## `mining.configure` / version rolling

- **Negotiation depth**
  - CKPool’s `mining.configure` response always enables `"version-rolling": true` and returns `"version-rolling.mask": "<mask>"` (it does not parse the miner’s option map in the way many BIP310-style implementations do).
  - goPool parses extensions + option aliases, supports version-rolling negotiation (including requested masks and min-bit-count), and may send `mining.set_version_mask` as a follow-up notification.
- **What miners submit**
  - CKPool treats the optional 6th `mining.submit` param as a **version mask / version-rolling field** (named `version_mask` in the CKPool code) and validates it against the allowed mask.
  - goPool treats the optional 6th `mining.submit` param as a **version value** (rolled version), and applies BIP310/BIP320-style policy around the negotiated mask.

## Difficulty / target related messages

- **`mining.set_target`**
  - CKPool primarily uses `mining.set_difficulty` to communicate difficulty changes.
  - goPool uses `mining.set_difficulty` and does not send `mining.set_target`.
- **Suggested difficulty**
  - CKPool implements a `mining.suggest*` family (matched broadly via `mining.suggest`).
  - goPool implements `mining.suggest_difficulty` and `mining.suggest_target` explicitly and applies only the first suggestion per connection.

## Transaction requests

- **`mining.get_transactions` / `mining.get_txnhashes`**
  - CKPool supports `mining.get*` requests and routes them through its transaction request path.
  - goPool supports `mining.get_transactions` but returns txids only (not raw tx hex).

## Error formatting

- **JSON-RPC `error` shape**
  - CKPool commonly uses `error` as a **string** (e.g. share errors), and uses its own internal error string formats for some protocol errors.
  - goPool uses the more common Stratum v1 pattern `error: [code, message, null]` (and `error: null` on success).

## `client.show_message` policy

- **Whitelisting**
  - CKPool only sends `client.show_message` to a subset of clients (based on useragent heuristics).
  - goPool uses `client.show_message` for bans/warnings without a useragent allowlist.
