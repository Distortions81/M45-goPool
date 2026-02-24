# Stratum V2 (WIP Checklist)

goPool includes an in-progress Stratum V2 mining listener on a separate TCP port.

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

## Current Testing Guidance

- Use a separate SV2 port (no same-port autodetect yet).
- Prefer **standard channel** mode for now.
- Treat pool logs/status as source of truth while miner UI counters are unstable during reconnects.

## Done

- [x] Separate-port SV2 listener (`stratum_v2_listen`, `-stratum-v2`)
- [x] `SetupConnection` negotiation (basic)
- [x] `OpenStandardMiningChannel` handling
- [x] `OpenExtendedMiningChannel` handling (open only; see TODO)
- [x] SV2 frame codec (core mining submit/open/job-update subset)
- [x] Noise transport support (initial `Noise_NX` responder implementation)
- [x] Share submit bridge into shared mining share core
- [x] `SubmitShares{Standard,Extended}` decode + `SubmitShares.{Success,Error}` responses
- [x] `SetTarget`, `SetNewPrevHash`, `SetExtranoncePrefix`, `NewMiningJob` framing
- [x] Initial job push on channel open
- [x] Status webpage shows SV2 endpoint when configured

## In Progress / Debugging

- [ ] SV2 + Noise interoperability stability (frequent reconnects / connection resets)
- [ ] Miner UI counters not matching pool accepted-share logs in some sessions
- [ ] SV2 field byte-order validation with real miners (U256/header-field interoperability)

## TODO (High Priority)

- [ ] Implement `NewExtendedMiningJob` (server -> client)
- [ ] Emit correct extended jobs on extended channels (currently unsafe/incomplete)
- [ ] Add `OpenMiningChannel.Error` responses and reject unsupported modes cleanly
- [ ] Finish `SetupConnection.flags` capability negotiation/gating
- [ ] Add end-to-end SV2+Noise integration tests (channel open -> job -> submit)

## TODO (Next)

- [ ] Channel close/reset messages and state cleanup
- [ ] `UpdateChannel` / `SetGroupChannel`
- [ ] Better SV2-specific error mapping/diagnostics in logs
- [ ] Authority-based Noise certificate verification (non-TOFU)
- [ ] Same-port protocol autodetect (optional)

## Known Limitation (Important)

- Extended channel is not complete yet because `NewExtendedMiningJob` is not implemented.
- An extended-channel miner may connect/open successfully but receive incompatible work messages.
- If testing today, use **standard channel mode**.
