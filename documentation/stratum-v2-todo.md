# Stratum V2 To-Do

This document tracks the work needed to move goPool's Stratum V2 listener from "experimental submit-path interop" toward protocol-correct and testable behavior.

Reference baseline used for comparison:
- `documentation/sv2-apps/pool-apps/pool`
- `documentation/sv2-apps/miner-apps/jd-client`
- `documentation/sv2-apps/miner-apps/translator`

## Goals

- Be protocol-correct for supported message flows.
- Fail unsupported flows with correct SV2 error messages.
- Interoperate with reference pool/miner apps for standard channels first.
- Add extended-channel support only when complete and safe.

## Priority 0 (Protocol Correctness / Safety)

### 1. Reject miner-sent server-only mining messages

Problem:
- Pool-side `sv2Conn` currently accepts inbound `SetExtranoncePrefix`, `SetNewPrevHash`, and `NewMiningJob` and mutates local state.

Why it matters:
- These are server-originated mining messages in the pool->miner direction.
- Accepting them lets a client corrupt submit validation state.

Files:
- `sv2_conn.go`

Tasks:
- Reject inbound server-only messages on miner connections.
- Return a protocol error or disconnect cleanly (decide behavior and document it).
- Add tests proving miner-sent server-only messages do not alter channel/job state.

### 2. Implement `OpenMiningChannelError` and use it for channel-open failures

Problem:
- Channel-open failures currently use `SetupConnection.Error` in some paths or return internal errors without SV2 responses.

Why it matters:
- Wrong message type for the failed request breaks interop and spec expectations.

Files:
- `stratum_v2_types.go`
- `stratum_v2_codec.go`
- `sv2_conn.go`

Tasks:
- Add `OpenMiningChannelError` wire type + codec encode/decode.
- Use it for:
  - open before setup
  - invalid worker identity
  - wallet validation failures
  - unsupported channel mode/capability combinations
  - invalid request parameters
- Standardize error codes to match reference app conventions where applicable.

### 3. Stop advertising incomplete extended channel support (or complete it)

Problem:
- `OpenExtendedMiningChannel` succeeds, but current work delivery path emits standard `NewMiningJob` flow.

Why it matters:
- Extended-channel clients (for example translator/JDC flows) expect `NewExtendedMiningJob`.
- Current behavior is not protocol-correct and can fail interop.

Files:
- `sv2_conn.go`
- `stratum_v2_types.go`
- `stratum_v2_codec.go`
- `documentation/stratum-v2.md`

Tasks (choose one path first):
- Short-term safe path (recommended):
  - Reject `OpenExtendedMiningChannel` with `OpenMiningChannelError` until extended flow is implemented.
- Full support path:
  - Add `NewExtendedMiningJob` wire type + codec.
  - Emit correct extended job messages and activation sequence.
  - Ensure submit mapping supports extended jobs without v1-only assumptions.

## Priority 1 (Handshake / Capability Negotiation)

### 4. Enforce `SetupConnection.flags` negotiation

Problem:
- `SetupConnection.flags` are stored but not enforced; server replies with `flags=0`.

Why it matters:
- Capability negotiation affects allowed channel types and work-selection behavior.
- Reference pool sets response flags and enforces downstream constraints.

Files:
- `sv2_conn.go`

Tasks:
- Parse and enforce relevant mining flags (at least:
  - work-selection/custom-work requirements
  - requires-standard-jobs / requires-extended-channels behavior)
- Set `SetupConnection.Success.flags` accordingly.
- Add compatibility notes in `documentation/stratum-v2.md`.

### 5. Validate `SetupConnection` message sequencing and duplicate setup behavior

Problem:
- The current code gates open-channel on setup, but duplicate/late setup handling is undefined.

Tasks:
- Define and implement behavior for duplicate `SetupConnection`.
- Add tests for out-of-order messages and repeat handshake attempts.

## Priority 2 (Open Channel Validation)

### 6. Validate `Open*MiningChannel` request parameters

Problem:
- Current handlers do not enforce `NominalHashRate`, `MaxTarget`, and (for extended) extranonce constraints.

Why it matters:
- Reference implementations reject invalid values with specific error codes.

Files:
- `sv2_conn.go`

Tasks:
- Validate `NominalHashRate`.
- Validate `MaxTarget` against pool policy / allowed range.
- Validate `MinExtranonceSize` for extended channels.
- Return reference-compatible error codes such as:
  - `invalid-nominal-hashrate`
  - `max-target-out-of-range`
  - `min-extranonce-size-too-large`

### 7. Ensure channel/group semantics are correct

Problem:
- `GroupChannelID` is currently set equal to `ChannelID` without a full group-channel model.

Tasks:
- Confirm whether current single-channel behavior is acceptable for supported flows.
- If not, implement minimal group-channel bookkeeping or document constraints.

## Priority 3 (Job / Target / Share Semantics)

### 8. Make standard-channel job flow spec-correct and explicit

Problem:
- Job bundle logic currently reuses v1-era internals heavily and relies on inferred mappings.

Tasks:
- Audit standard `NewMiningJob` field semantics against reference apps/spec.
- Ensure job IDs and active/future job transitions are handled explicitly.
- Add tests around stale-share classification after `SetNewPrevHash`.

### 9. Make `SubmitShares.Success` accounting values accurate

Problem:
- `new_shares_sum` is currently hardcoded to `1`.

Why it matters:
- Breaks miner accounting/telemetry expectations.

Files:
- `sv2_conn.go`
- shared submit/accounting plumbing

Tasks:
- Plumb actual share work/credit into SV2 submit success responses.
- Verify `last_sequence_number`, `new_submits_accepted_count`, and `new_shares_sum` semantics.

### 10. Tighten submit error mapping

Problem:
- Error mapping is mostly string-based and may misclassify protocol-level validation failures.

Tasks:
- Replace heuristic string matching with structured error categories where possible.
- Add tests for:
  - invalid channel id
  - invalid job id
  - stale share
  - duplicate share
  - invalid timestamp/version/extranonce

## Priority 4 (Missing Mining Messages / Lifecycle)

### 11. Add missing mining-protocol lifecycle messages (as needed)

Current gaps (documented and/or implied):
- `NewExtendedMiningJob`
- `OpenMiningChannelError`
- `UpdateChannel`
- `CloseChannel`
- `SetGroupChannel` / group management
- reset/close lifecycle handling

Tasks:
- Implement only the subset needed for the targeted interop matrix first.
- Reject unsupported messages explicitly rather than silently ignoring if feasible.

### 12. Review inbound handling for unknown/unsupported messages

Problem:
- Some unsupported messages are currently ignored in the default branch.

Tasks:
- Decide per-message policy: ignore, error, or disconnect.
- Document behavior and add tests.

## Transport / Noise (Security + Interop)

### 13. Replace TOFU-style Noise behavior with proper authority verification

Problem:
- Current docs state Noise authority verification is not implemented.

Files:
- `sv2_noise*.go`
- `documentation/stratum-v2.md`

Tasks:
- Implement certificate/authority verification model aligned with intended deployment.
- Add integration tests for successful and failed auth cases.

### 14. Expand plaintext vs Noise parity tests

Tasks:
- Verify identical behavior for:
  - setup
  - open channel
  - initial job bundle
  - submit success/error
- Add regression tests for framing edge cases.

## Testing Plan (Use `documentation/sv2-apps` References)

### A. Standard-channel interoperability (first milestone)

Use:
- `documentation/sv2-apps/miner-apps/mining-device`
- `documentation/sv2-apps/miner-apps/translator` (only if standard path is supported in chosen mode)

Scenarios:
- `SetupConnection` success/failure
- `OpenStandardMiningChannel` success
- initial `SetTarget` + `NewMiningJob` + `SetNewPrevHash`
- valid submit -> `SubmitSharesSuccess`
- stale submit after new prevhash -> `SubmitSharesError(stale-share)`
- invalid channel/job submit -> correct error code

### B. Extended-channel interoperability (only after implementation)

Use:
- `documentation/sv2-apps/miner-apps/translator`
- `documentation/sv2-apps/miner-apps/jd-client`

Scenarios:
- `OpenExtendedMiningChannel` success + `NewExtendedMiningJob`
- `SetNewPrevHash` activation
- extended submits accepted/rejected correctly
- capability-flag-dependent behavior

### C. Negative protocol tests

Scenarios:
- miner sends pool-only messages
- open before setup
- duplicate setup
- unsupported message types
- malformed frame lengths / wrong channel bit usage

## Documentation Updates (Keep in Sync)

Update `documentation/stratum-v2.md` whenever behavior changes:
- supported messages
- known limitations
- error behavior / unsupported flow behavior
- Noise verification status

## Suggested Milestones

### Milestone 1 (Safe experimental standard channels)
- Reject server-only inbound messages
- Add `OpenMiningChannelError`
- Enforce basic setup/open sequencing
- Validate open params
- Correct submit success accounting
- Standard-channel interop test with reference mining device

### Milestone 2 (Capability-correct standard channels)
- Implement/setup flags enforcement
- Improve error mapping
- Add negative protocol tests
- Plaintext/Noise parity coverage

### Milestone 3 (Extended channels)
- Add `NewExtendedMiningJob`
- Complete extended job lifecycle + submit path
- Interop with reference translator/JDC
- Re-enable extended channels by default (if stable)
