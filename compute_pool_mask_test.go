package main

import "testing"

func TestComputePoolMaskExcludesPoolControlledBits(t *testing.T) {
	const configuredMask = uint32(0x0000f071)
	tpl := GetBlockTemplateResult{
		// Required bits and all versionbits deployments belong to the node/pool,
		// not to downstream ASIC rolling.
		VbRequired: 0x00000001,
		VbAvailable: map[string]int{
			"started":      12,
			"locked_in":    13,
			"invalid-low":  -1,
			"invalid-high": 32,
		},
		// Bitcoin Core commonly omits version mutability. Safety exclusions must
		// not depend on this field being present.
		Mutable: nil,
		Rules:   []string{"segwit", "locked_in"},
	}
	cfg := Config{
		VersionMaskConfigured: true,
		VersionMask:           configuredMask,
		VersionBitOverrides: map[uint32]bool{
			4:  false,
			5:  true,
			6:  false,
			32: true,
		},
	}

	got := computePoolMask(tpl, cfg)
	want := configuredMask &^
		uint32(tpl.VbRequired) &^
		(uint32(1) << 12) &^
		(uint32(1) << 13) &^
		(uint32(1) << 4) &^
		(uint32(1) << 5) &^
		(uint32(1) << 6)
	if got != want {
		t.Fatalf("computePoolMask()=%#08x want %#08x", got, want)
	}
}

func TestComputePoolMaskSelectsConfiguredBase(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want uint32
	}{
		{
			name: "unconfigured zero uses default",
			cfg:  Config{},
			want: defaultVersionMask,
		},
		{
			name: "configured zero stays zero",
			cfg: Config{
				VersionMaskConfigured: true,
			},
			want: 0,
		},
		{
			name: "nonzero programmatic mask is honored",
			cfg: Config{
				VersionMask: 0x00006000,
			},
			want: 0x00006000,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := computePoolMask(GetBlockTemplateResult{}, tc.cfg); got != tc.want {
				t.Fatalf("computePoolMask()=%#08x want %#08x", got, tc.want)
			}
		})
	}
}

func TestComputePoolMaskDoesNotRestoreFullyPrunedMask(t *testing.T) {
	tpl := GetBlockTemplateResult{
		VbRequired:  0x00000001,
		VbAvailable: map[string]int{"deployment": 1},
	}
	cfg := Config{
		VersionMaskConfigured: true,
		VersionMask:           0x00000003,
	}

	if got := computePoolMask(tpl, cfg); got != 0 {
		t.Fatalf("computePoolMask()=%#08x want zero", got)
	}
}
