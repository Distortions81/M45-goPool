# Stratum V2 Implementation Status (goPool)

This document tracks what is currently implemented, partially implemented, and not yet implemented in goPool's Stratum V2 (SV2) support.

Status reflects the current codebase state on this branch/worktree (including active in-progress fixes).

## Summary

- SV2 listener exists and can accept real miner connections on a separate port.
- `Noise_NX` transport support exists and can complete handshake with ESP-Miner-class devices.
- Standard SV2 submit plumbing is implemented (frame codec, submit decode, response encode, share-core bridge).
- Extended-channel support is **not complete** and is currently unsafe for real mining because `NewExtendedMiningJob` is not implemented.

## Implemented

### Listener / Session

- Separate-port SV2 listener via `stratum_v2_listen` config and `-stratum-v2` CLI override
- `MinerConn.serveSV2()` entrypoint and `sv2Conn` lifecycle attachment
- SV2 frame read loop skeleton with message dispatch
- Setup gating before channel-open handling

### Transport / Framing

- SV2 frame header encode/decode (`extension_type`, `msg_type`, `msg_length`)
- Channel-message bit handling
- Plaintext SV2 transport path
- Noise transport auto-detection (plaintext vs likely `SV2+Noise`)
- Responder-side `Noise_NX` handshake implementation (first-pass)
- Encrypted frame read/write transport for SV2 frames
- Serialized SV2 writes (`writeMu`) to avoid encrypted stream corruption from concurrent writes

### Mining Protocol (Current Scope)

- `SetupConnection` request/success/error codec + handler
- `OpenStandardMiningChannel` request + success codec + handler
- `OpenExtendedMiningChannel` request + success codec + handler (see "Known Gaps")
- `SetTarget` codec + outbound writer + responder integration
- `SetNewPrevHash` codec + inbound/outbound state tracking
- `SetExtranoncePrefix` codec + mapper-state sync
- `NewMiningJob` codec + inbound/outbound state tracking
- `SubmitSharesStandard` codec + handler
- `SubmitSharesExtended` codec + handler (submit path only; depends on valid extended job flow)
- `SubmitShares.Success` / `SubmitShares.Error` codec + outbound responses

### Share Processing Integration

- Protocol-neutral share submit core (shared with v1)
- SV2 submit bridge into shared `submissionTask`/share-processing path
- SV2 hook adapter for submit success/error + target updates
- SV2 mapper state (`channel_id`/wire `job_id` -> local job ID)
- Active-job stale-share gating using `SetNewPrevHash`
- Improved SV2 error-code mapping (`stale-share`, `difficulty-too-low`, `duplicate-share`, etc.)

### Job/Event Integration (Incremental)

- `MinerConn` protocol dispatch for work send (`sendWorkForProtocol`)
- SV2 outbound job bundle helper (`SetTarget -> NewMiningJob -> SetNewPrevHash`)
- Initial job bundle send on channel open (when current job is available)

### Web / Ops

- Status page "How to connect your miner" shows SV2 endpoint when configured
- Basic SV2 setup docs (`documentation/stratum-v2.md`)

## Partially Implemented / Experimental

### Noise Interop

- ESP-Miner-class `SV2+Noise` handshake can complete.
- Transport is still under active interoperability debugging; disconnect/reconnect churn is still observed with some miners.
- Certificate verification is currently TOFU-style / permissive (no configured authority verification path yet).

### Standard Job Emission Correctness

- SV2 `U256` byte order fixes are in progress (target/prevhash/merkle-root handling).
- Recent fixes addressed:
  - `SetTarget.maximum_target` endianness
  - `SetNewPrevHash.prev_hash` endianness
  - `NewMiningJob.merkle_root` generation + endianness
- These areas should be treated as interoperability-sensitive until broader real-miner validation is complete.

### Extended Submit Path

- `SubmitSharesExtended` codec + handler exists.
- However, it is not production-ready because the server does **not** yet send `NewExtendedMiningJob`.
- This means an extended-channel miner may connect and open a channel, but receive incompatible job messages.

## Not Implemented (Major Gaps)

### Critical Mining Protocol Gaps

- `NewExtendedMiningJob` codec + outbound server implementation
- Proper extended job construction from pool job/template:
  - `coinbase_tx_prefix`
  - `coinbase_tx_suffix`
  - `merkle_path`
  - `version_rolling_allowed`
- `OpenMiningChannel.Error` responses (standard/extended)
- Channel close / teardown messages and state cleanup beyond connection teardown
- `UpdateChannel` / `SetGroupChannel`

### Protocol Correctness / Negotiation

- Full `SetupConnection.flags` negotiation semantics (capabilities/requirements)
- Robust protocol feature gating by negotiated flags
- Same-port protocol autodetect (currently separate-port only)

### Broader SV2 Coverage

- Template Distribution Protocol support
- Job Declaration Protocol support
- Custom job flow (`SetCustomMiningJob*`)
- Full proxy/group-channel workflows

### Security / Hardening

- Authority-based Noise certificate verification / trust configuration
- Full handshake/transport test vectors and interoperability fixtures
- Connection limits/abuse controls specific to SV2 channels/messages

## Known Incompatibilities / Warnings

### Extended Channel Warning (Current)

Extended channel open is currently accepted, but the server does not yet emit `NewExtendedMiningJob`.

This can cause malformed work on miners expecting extended jobs (for example, receiving `NewMiningJob` on an extended channel), resulting in bogus local "found block" events, invalid shares, or reconnect loops.

Recommendation:

- Prefer **standard SV2 channel mode** for current testing, or
- Temporarily reject extended channel opens until `NewExtendedMiningJob` is implemented

### Miner UI Counters vs Pool Truth

Some miners may show `0 shares` or stale counters while the pool is actually accepting shares, especially during reconnect churn. Use pool logs/status as the source of truth while SV2 support is still stabilizing.

## Recommended Next Work

1. Implement `NewExtendedMiningJob` (codec + outbound generation) and stop sending standard jobs on extended channels.
2. Finish SV2 `U256`/header-field byte-order interoperability validation with real miners.
3. Stabilize Noise transport interoperability (disconnect/reconnect churn).
4. Add explicit `OpenMiningChannel.Error` and reject unsupported modes/features cleanly.
5. Add end-to-end TCP smoke tests for `SV2+Noise` and channel open -> job -> submit.
