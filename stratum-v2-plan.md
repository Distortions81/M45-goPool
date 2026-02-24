# Stratum V2 Support Plan for goPool

## Goal

Add Stratum V2 support for miners while preserving the current Stratum V1 implementation and operational behavior.

## Guiding Approach

- Keep Stratum V1 fully working during the rollout.
- Avoid rewriting share validation/job logic; extract and reuse existing protocol-agnostic paths.
- Ship in phases behind feature flags.
- Prefer incremental interoperability (basic mining channels first) over broad spec coverage on day one.

## Scope (Initial)

- Downstream Stratum V2 support for miners connecting to goPool.
- Pool-generated jobs from existing `JobManager` / `getblocktemplate` flow (no upstream SV2 template provider integration initially).
- Basic mining functionality:
  - connection + handshake
  - setup connection negotiation
  - open mining channel
  - set target / new job distribution
  - submit shares
- Keep Stratum V1 TCP/TLS listeners and UI connect instructions unchanged unless SV2 is explicitly enabled.

## Non-Goals (Initial)

- Full Job Negotiator role implementation.
- Upstream SV2 Template Distribution integration.
- Translator proxy mode for other pools.
- Every optional extension in the SV2 specs.

## Current Codebase Touchpoints (Observed)

- `miner_conn.go`: v1 read loop, dispatch, fast-path decode, per-connection lifecycle/state.
- `miner_auth.go`: subscribe/authorize/configure handshake, miner metadata, worker registration.
- `miner_submit_parse.go` + `miner_submit_process.go`: submit parsing and share validation/processing.
- `job_subscribe.go` + `job_manager.go`: job fanout and subscriber lifecycle.
- `miner_types.go`: `MinerConn`, Stratum v1 request/response types, shared connection state.
- `config_types.go`, `config_load.go`, `config_build.go`: runtime config and flags.
- `status_*` + templates: status/UI exposure of stratum listeners/counters.

## Proposed Architecture

### 1. Introduce a protocol-neutral mining session core

Create an internal session/service layer that owns:

- worker identity + auth result
- extranonce/channel assignment state
- difficulty/target state (vardiff hooks)
- active job tracking
- share submission validation entrypoint
- metrics/accounting hooks

This layer should be callable from:

- existing Stratum V1 handlers (gradual migration)
- new Stratum V2 connection handlers

Practical outcome: v2 implementation reuses the existing mining logic instead of duplicating `mining.submit` validation and accounting.

### 2. Add a separate Stratum V2 listener stack (parallel to v1)

New components (suggested files/packages):

- `sv2_listener.go` (accept loop / lifecycle)
- `sv2_conn.go` (connection state machine)
- `sv2_codec.go` (framing, encode/decode)
- `sv2_handshake.go` (noise + setup messages)
- `sv2_mining.go` (mining-channel message handling)
- `sv2_types.go` (message structs/constants)

Keep v2 isolated from the v1 fast-path code in `miner_conn.go`.

### 3. Bridge `JobManager` jobs into SV2 mining messages

Add a translator layer from existing `Job` to SV2 job messages:

- map pool `Job` fields to SV2 job declarations / new mining job messages
- manage per-channel job IDs
- preserve clean-job semantics
- maintain version rolling policy compatibility with existing config (`VersionMask`, `MinVersionBits`)

### 4. Bridge SV2 share submissions into existing validation

Implement a conversion path from SV2 submit messages to an internal submit structure used by current validation:

- worker/channel -> worker identity
- SV2 nonce/extranonce fields -> coinbase/header reconstruction inputs
- version rolling bits -> current version checks
- target/difficulty accounting -> existing share processing

## Delivery Phases

## Phase 0: Design + Spec Pinning

- Pick exact SV2 spec version/compatibility target and document it.
- Decide first supported role set (likely downstream mining device -> pool server, standard channels only).
- Document cryptography dependencies (Noise implementation choice, audited library selection).
- Define a minimal interoperability matrix (devices/proxies to test).

Exit criteria:

- Written protocol support matrix and unsupported features list.
- Message set for MVP enumerated.

## Phase 1: Internal Refactor for Reuse (No Behavior Change)

- Extract protocol-agnostic submit processing entrypoint from v1 handlers.
- Extract reusable connection/session state where possible (auth outcome, difficulty, job tracking).
- Keep existing v1 tests green.
- Add tests for the extracted core using synthetic inputs (not wire protocol JSON).

Likely edits:

- `miner_submit_parse.go`
- `miner_submit_process.go`
- `miner_auth.go`
- `miner_types.go`

Exit criteria:

- v1 behavior unchanged.
- Shared submit/auth/job APIs available for v2 handlers.

## Phase 2: Config + Feature Flags + Status Surface

- Add config flags (disabled by default), e.g.:
  - `StratumV2Listen`
  - `StratumV2TLSListen` (optional, depending on transport decision)
  - `StratumV2Enabled`
  - `StratumV2RequireAuth` / worker auth mapping policy
- Wire config load/build/defaults/examples.
- Add status/UI visibility for v2 listener state and connection counts.

Likely edits:

- `config_types.go`
- `config_load.go`
- `config_build.go`
- `defaults.go`
- `status_data_*`
- `data/config/examples/*.toml.example`
- `data/templates/overview.tmpl` / admin templates

Exit criteria:

- v2 listener can be configured independently and is off by default.

## Phase 3: SV2 Transport, Framing, and Handshake

- Implement binary framing codec with strict bounds checks.
- Implement handshake/state machine (including Noise if required by chosen mode).
- Add setup connection negotiation handling.
- Add robust protocol error handling and disconnect policy (similar hardening level as v1).

Testing:

- frame decode/encode unit tests
- malformed message fuzz tests
- handshake state transition tests

Exit criteria:

- Can establish an SV2 session and complete setup negotiation in tests.

## Phase 4: Basic Mining Channels (Job Delivery + Share Submit)

- Implement open channel flow (start with standard channels).
- Assign channel IDs and per-channel extranonce state.
- Emit target updates and new jobs from `JobManager`.
- Accept submit shares and route them through shared validation/accounting.
- Map success/error outcomes into SV2 responses.

Testing:

- end-to-end simulated SV2 miner session
- parity tests vs v1 for share acceptance/rejection behavior
- clean-job transition and stale share handling

Exit criteria:

- A test SV2 client can connect, receive jobs, submit valid shares, and receive correct responses.

## Phase 5: Operational Hardening

- Rate limiting / flood control for v2 messages (reuse or adapt `StratumMessagesPerMinute` semantics).
- Metrics and observability:
  - v2 connections
  - handshake failures
  - channel opens
  - share accepts/rejects by reason
- Logging with protocol labels (`stratum_v1`, `stratum_v2`).
- Timeout and idle connection handling parity with v1.

Exit criteria:

- v2 has production-grade visibility and abuse protection.

## Phase 6: Interop and Rollout

- Test with at least one real SV2 proxy/miner stack.
- Run staged rollout behind config flag.
- Publish operator docs:
  - how to enable
  - known limitations
  - miner/proxy compatibility notes

Exit criteria:

- Documented and stable enough for opt-in production use.

## Key Technical Decisions to Resolve Early

- Which SV2 role subset is implemented first (standard channels only vs extended/custom).
- How worker identity/auth maps into SV2 (username-style worker names, tokens, or explicit fields).
- Whether v2 uses a separate port only (recommended for initial rollout).
- Whether to support plaintext v2 for lab/testing or require encrypted transport only.
- How to represent vardiff in SV2 channel target updates without regressing existing behavior.
- How to preserve version-rolling policy enforcement in SV2 submit validation.

## Risks and Mitigations

- Spec complexity / handshake errors:
  - Mitigation: isolate codec/handshake code, add strict state-machine tests and fuzzing.
- Duplicate mining logic across v1/v2:
  - Mitigation: Phase 1 extraction before v2 wire handling.
- Behavior drift in share validation:
  - Mitigation: parity tests that compare v1 and v2 internal submission outcomes.
- Operational blind spots:
  - Mitigation: add v2-specific metrics/log labels before rollout.

## Test Plan (MVP)

- Unit tests:
  - SV2 frame codec
  - message encode/decode
  - handshake transitions
  - channel state validation
- Reuse/core tests:
  - share validation path with protocol-neutral submit structs
- Integration tests:
  - `JobManager` -> SV2 job broadcast
  - valid/invalid/stale/duplicate share submits
  - reconnect/session cleanup
- Fuzz tests:
  - frame decoder
  - message parser

## Suggested Implementation Order (Pragmatic)

1. Extract shared submit/auth/session core from v1 paths.
2. Add config flags + disabled v2 listener skeleton.
3. Implement codec + handshake tests.
4. Implement standard channel open + target/job send.
5. Implement submit share bridge to existing processing.
6. Add metrics/rate limits/logging.
7. Interop test with a real client/proxy.

## Definition of Done (Initial SV2 Support)

- Stratum V2 is optional and disabled by default.
- Existing Stratum V1 tests pass unchanged.
- SV2 MVP tests cover handshake, channel open, job delivery, and share submission.
- Real-world interoperability verified with at least one SV2 implementation.
- Operator docs/config examples include clear enablement and limitations.

