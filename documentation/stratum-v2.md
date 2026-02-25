# Stratum V2 (Experimental)

goPool includes an experimental Stratum V2 mining listener on a dedicated TCP port.

This is usable for early interoperability testing and share-submit validation, but it is not full SV2 mining-protocol coverage yet.

## Enable

Set a dedicated listener in `data/config/config.toml`:

```toml
[stratum]
stratum_v2_listen = ":3334"
```

Or override at runtime:

```bash
./goPool -stratum-v2 :3334
```

## Transport Behavior

- SV2 runs on a **separate port** (`stratum_v2_listen` / `-stratum-v2`).
- goPool does **not** multiplex SV1 and SV2 on the same listener.
- On the SV2 port, goPool auto-detects:
  - plaintext SV2 framing (first message looks like `SetupConnection`)
  - SV2 + Noise (`Noise_NX_Secp256k1+EllSwift_ChaChaPoly_SHA256`)
- Noise support is responder-side and still experimental (see limitations below).

## Current Support (Implemented)

- `SetupConnection` basic validation + `SetupConnection.Success/Error`
  - Mining protocol (`protocol=0`) only
  - Version negotiation currently targets SV2 version `2`
- Channel open:
  - `OpenStandardMiningChannel` + `.Success`
  - `OpenExtendedMiningChannel` + `.Success` (open succeeds, but extended job flow is incomplete)
- Mining updates / framing support:
  - `SetTarget`
  - `SetNewPrevHash`
  - `SetExtranoncePrefix` (codec/state support)
  - `NewMiningJob` (standard-path job announcements used by current bridge)
- Share submit path:
  - `SubmitSharesStandard`
  - `SubmitSharesExtended`
  - `SubmitShares.Success`
  - `SubmitShares.Error`
- Initial job bundle is pushed immediately after channel open when a current pool job is available.
- Status UI / status JSON expose SV2 listener configuration and active SV2 miner counts.

## Practical Testing Guidance (Recommended)

- Use a dedicated SV2 port (for example `:3334`).
- Prefer **standard channel mode** for real miner testing right now.
- If the miner supports both plaintext and Noise, test plaintext first to isolate protocol issues from transport issues.
- Treat goPool logs and goPool status pages/APIs as source of truth when miner UI counters look inconsistent during reconnects.
- Re-test after difficulty changes and new block/job events to verify `SetTarget` and `SetNewPrevHash` handling on your miner.

## Known Limitations

- `NewExtendedMiningJob` is not implemented yet.
- Extended channels can open, but work delivery for extended-channel mining is incomplete/unsafe.
- `SetupConnection.flags` capability negotiation is not fully enforced yet (basic handshake only).
- `OpenMiningChannel.Error` handling/rejection paths are not complete for all unsupported cases.
- Noise certificate authority verification is not implemented yet (current path is effectively TOFU-style responder behavior).
- Interoperability with real miners is still being validated; expect reconnects / edge-case framing mismatches.

## Message Coverage (At a Glance)

Supported now (decode/encode path present and used by the current server flow):

- `SetupConnection`, `SetupConnection.Success`, `SetupConnection.Error`
- `OpenStandardMiningChannel`, `OpenStandardMiningChannel.Success`
- `OpenExtendedMiningChannel`, `OpenExtendedMiningChannel.Success`
- `SetTarget`, `SetNewPrevHash`, `NewMiningJob`
- `SubmitSharesStandard`, `SubmitSharesExtended`
- `SubmitShares.Success`, `SubmitShares.Error`

Partially implemented / not production-ready:

- Extended-channel work flow (`NewExtendedMiningJob` missing)
- Capability negotiation flags
- Full channel lifecycle management (`UpdateChannel`, reset/close, group-channel management)

## Near-Term Priorities

- Implement `NewExtendedMiningJob` and complete extended-channel work emission.
- Add stronger `Open*MiningChannel` error responses and unsupported-mode rejection behavior.
- Expand end-to-end SV2+Noise integration tests (open -> job -> submit).
- Improve SV2-specific logging/diagnostics for interoperability debugging.
- Replace TOFU-style Noise cert handling with authority-based verification.
