# Version Bits Configuration

This document explains `data/config/version_bits.toml`, how goPool applies it, and the currently known bit usage in goPool.

## Purpose

`version_bits.toml` lets you force specific block header version bits on or off without changing code.

Use this when you want strict operator control of individual bits (for example forcing a bit off across all jobs).

## Important warning

- Do not flip version bits unless you have a very specific, validated reason.
- In normal operation, you should leave node-provided version bits unchanged.
- `version_bits.toml` modifies bits from the node's `getblocktemplate` version.
- Changing these bits incorrectly can make signaling invalid for your node/network policy.
- Flipping bits improperly can cause a found (winning) block to be rejected by the network/node.

## File behavior

- File path: `data/config/version_bits.toml`
- Optional: if the file does not exist, goPool uses normal behavior
- Read-only from goPool's perspective:
  - goPool reads this file
  - goPool never rewrites this file
- Example template: `data/config/examples/version_bits.toml.example`

## Format

```toml
[[bits]]
  bit = 4
  enabled = true

[[bits]]
  bit = 1
  enabled = true
```

Rules:

- `bit` must be between `0` and `31`
- `enabled = true` sets the bit
- `enabled = false` clears the bit
- use one `[[bits]]` block per bit override
- entries are applied in order; if the same bit appears multiple times, the last entry wins

## Apply order

goPool applies version changes in this order:

1. Base template version from node (`getblocktemplate`)
2. `version_bits.toml` overrides

`version_bits.toml` has final authority for any bit you list.

## Miner submit compatibility

`policy.toml [version].share_allow_out_of_mask_version_bits` controls whether
goPool rejects miner-submitted version changes that were not negotiated or are
outside the negotiated version-rolling mask:

- `true` (default): allow unnegotiated or out-of-mask submits for legacy miner compatibility.
- `false`: strict mask enforcement (`invalid version` or `invalid version mask` policy reject on
  non-block shares).

### How goPool interprets submitted `version`

goPool prefers BIP310 replacement-bit semantics for submit `version` values.

- Values fully inside the negotiated mask are treated as the miner-selected
  replacement bits for that mask.
- Full rolled versions are accepted when they differ from the job version only
  inside the negotiated mask.
- Ambiguous in-mask values also keep a legacy XOR-delta fallback while checking
  the resulting share, so older firmware still has a chance to submit valid work.
- When `share_allow_out_of_mask_version_bits = true`, out-of-mask values are
  treated as legacy XOR deltas for compatibility.

## Bitcoin version bits reference

| Bit Range | Value/Use | Description |
|-----------|-----------|-------------|
| 31-29 | `001` (binary) | Required prefix: signals the block uses BIP 9 version bits. |
| 0 | `0x20000001` | CSV upgrade: signal for CHECKSEQUENCEVERIFY (BIP 112). |
| 1 | `0x20000002` | SegWit: signal for Segregated Witness (BIP 141). |
| 2 | `0x20000004` | Taproot: signal for Schnorr signatures and MAST (BIP 341). |
| 3-28 | Available | Reserved for future soft fork proposals or experimental signaling. |

## Known bit usage in goPool (current)

| Bit | Known use in goPool | Default behavior |
|-----|----------------------|------------------|
| 0..31 | No goPool-specific meaning currently assigned | Passed through from template unless overridden |

Notes:

- "No goPool-specific meaning" means goPool does not attach internal logic to those bits today.
- You can still force any of those bits with `version_bits.toml`.
- If you force bits, validate with your node/network policy before production rollout.

## References

- Bitcoin BIPs repository: <https://github.com/bitcoin/bips>
- BIP 9 (version bits signaling): <https://github.com/bitcoin/bips/blob/master/bip-0009.mediawiki>
- BIP 112 (CHECKSEQUENCEVERIFY): <https://github.com/bitcoin/bips/blob/master/bip-0112.mediawiki>
- BIP 141 (SegWit): <https://github.com/bitcoin/bips/blob/master/bip-0141.mediawiki>
- BIP 341 (Taproot): <https://github.com/bitcoin/bips/blob/master/bip-0341.mediawiki>
