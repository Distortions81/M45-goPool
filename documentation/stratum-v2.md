# Stratum V2 (Early Support)

goPool includes an early Stratum V2 mining listener path on a separate TCP port.

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

## Notes

- SV2 currently uses a separate listener/port from Stratum v1 (`server.pool_listen` / `server.stratum_tls_listen`).
- The status webpage "How to connect your miner" panel will show the SV2 endpoint when `stratum_v2_listen` is configured.
- Configure your miner for Stratum V2 mode and point it to the SV2 port.

## Current Scope

The SV2 implementation is still in-progress. The current codebase includes:

- setup connection negotiation
- mining channel open (standard + extended)
- mining job/update framing (`SetTarget`, `SetExtranoncePrefix`, `NewMiningJob`, `SetNewPrevHash`)
- share submit decode/encode and bridge into the shared mining share core

