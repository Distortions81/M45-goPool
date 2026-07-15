package main

// applyConfiguredVersionBits applies explicit operator overrides to the block
// version supplied by the node. Entries in version_bits.toml are the final
// authority for the bits they name.
func applyConfiguredVersionBits(version int32, cfg Config) int32 {
	u := uint32(version)
	for bit, enabled := range cfg.VersionBitOverrides {
		if bit > 31 {
			continue
		}
		mask := uint32(1) << bit
		if enabled {
			u |= mask
		} else {
			u &^= mask
		}
	}
	return int32(u)
}
