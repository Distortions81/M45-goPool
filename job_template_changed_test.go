package main

import "testing"

func TestTemplateChangedIncludesMiningPayloadFields(t *testing.T) {
	base := GetBlockTemplateResult{
		Bits:                     "1d00ffff",
		Target:                   "00000000ffff0000000000000000000000000000000000000000000000000000",
		Height:                   100,
		Version:                  0x20000000,
		Previous:                 "prev",
		CoinbaseValue:            50 * 1e8,
		DefaultWitnessCommitment: "commitment-a",
		VbAvailable:              map[string]int{"deployment": 12},
		VbRequired:               1,
		Mutable:                  []string{"time"},
		Rules:                    []string{"segwit"},
		Transactions: []GBTTransaction{{
			Txid: "txid",
			Hash: "wtxid-a",
			Data: "data-a",
		}},
	}
	base.CoinbaseAux.Flags = "flags-a"

	tests := []struct {
		name      string
		mutate    func(*GetBlockTemplateResult)
		wantClean bool
	}{
		{"version", func(tpl *GetBlockTemplateResult) { tpl.Version++ }, true},
		{"target", func(tpl *GetBlockTemplateResult) { tpl.Target = "different" }, true},
		{"coinbase value", func(tpl *GetBlockTemplateResult) { tpl.CoinbaseValue-- }, true},
		{"witness commitment", func(tpl *GetBlockTemplateResult) { tpl.DefaultWitnessCommitment = "commitment-b" }, true},
		{"coinbase flags", func(tpl *GetBlockTemplateResult) { tpl.CoinbaseAux.Flags = "flags-b" }, true},
		{"vbavailable", func(tpl *GetBlockTemplateResult) { tpl.VbAvailable["deployment"] = 13 }, true},
		{"vbrequired", func(tpl *GetBlockTemplateResult) { tpl.VbRequired++ }, true},
		{"mutable", func(tpl *GetBlockTemplateResult) { tpl.Mutable = append(tpl.Mutable, "transactions") }, true},
		{"rules", func(tpl *GetBlockTemplateResult) { tpl.Rules = append(tpl.Rules, "new-rule") }, true},
		{"transaction witness hash", func(tpl *GetBlockTemplateResult) { tpl.Transactions[0].Hash = "wtxid-b" }, false},
		{"transaction data", func(tpl *GetBlockTemplateResult) { tpl.Transactions[0].Data = "data-b" }, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			current := base
			current.VbAvailable = map[string]int{"deployment": 12}
			current.Mutable = append([]string(nil), base.Mutable...)
			current.Rules = append([]string(nil), base.Rules...)
			current.Transactions = append([]GBTTransaction(nil), base.Transactions...)
			next := current
			next.VbAvailable = map[string]int{"deployment": 12}
			next.Mutable = append([]string(nil), current.Mutable...)
			next.Rules = append([]string(nil), current.Rules...)
			next.Transactions = append([]GBTTransaction(nil), current.Transactions...)
			tc.mutate(&next)

			jm := &JobManager{curJob: &Job{Template: current}}
			changed, clean := jm.templateChanged(next)
			if !changed || clean != tc.wantClean {
				t.Fatalf("templateChanged = (%v, %v), want (true, %v)", changed, clean, tc.wantClean)
			}
		})
	}
}

func TestTemplateChangedComparesEffectiveVersion(t *testing.T) {
	const baseVersion = int32(0x20000000)

	tests := []struct {
		name        string
		currentCfg  Config
		nextCfg     Config
		currentRaw  int32
		nextRaw     int32
		wantChanged bool
		wantClean   bool
	}{
		{
			name:       "BIP110 forced on does not churn identical raw template",
			currentCfg: Config{BIP110Enabled: true},
			nextCfg:    Config{BIP110Enabled: true},
			currentRaw: baseVersion,
			nextRaw:    baseVersion,
		},
		{
			name: "forced off node bit does not churn",
			currentCfg: Config{VersionBitOverrides: map[uint32]bool{
				5: false,
			}},
			nextCfg: Config{VersionBitOverrides: map[uint32]bool{
				5: false,
			}},
			currentRaw: baseVersion | 1<<5,
			nextRaw:    baseVersion | 1<<5,
		},
		{
			name: "explicit override remains final across BIP110 toggle",
			currentCfg: Config{VersionBitOverrides: map[uint32]bool{
				bip110VersionBit: false,
			}},
			nextCfg: Config{
				BIP110Enabled: true,
				VersionBitOverrides: map[uint32]bool{
					bip110VersionBit: false,
				},
			},
			currentRaw: baseVersion,
			nextRaw:    baseVersion,
		},
		{
			name:        "non-overridden node version change remains clean",
			currentCfg:  Config{BIP110Enabled: true},
			nextCfg:     Config{BIP110Enabled: true},
			currentRaw:  baseVersion,
			nextRaw:     baseVersion | 1<<6,
			wantChanged: true,
			wantClean:   true,
		},
		{
			name:        "runtime version policy change creates clean job",
			currentCfg:  Config{},
			nextCfg:     Config{BIP110Enabled: true},
			currentRaw:  baseVersion,
			nextRaw:     baseVersion,
			wantChanged: true,
			wantClean:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			current := GetBlockTemplateResult{
				Previous: "prev",
				Height:   100,
				Bits:     "1d00ffff",
				Target:   "target",
				Version:  applyConfiguredVersionBits(tc.currentRaw, tc.currentCfg),
			}
			next := current
			next.Version = tc.nextRaw
			jm := &JobManager{
				cfg:    tc.nextCfg,
				curJob: &Job{Template: current},
			}

			changed, clean := jm.templateChanged(next)
			if changed != tc.wantChanged || clean != tc.wantClean {
				t.Fatalf("templateChanged = (%v, %v), want (%v, %v)", changed, clean, tc.wantChanged, tc.wantClean)
			}
		})
	}
}
