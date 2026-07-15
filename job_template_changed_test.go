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
		VbAvailable:              map[string]int{"deployment": 13},
		VbRequired:               1,
		Mutable:                  []string{"time", "version/force"},
		Rules:                    []string{"segwit"},
		Transactions: []GBTTransaction{{
			Txid: "txid",
			Hash: "wtxid-a",
			Data: "data-a",
		}},
	}
	base.CoinbaseAux.Flags = "flags-a"

	tests := []struct {
		name        string
		mutate      func(*GetBlockTemplateResult)
		wantChanged bool
		wantClean   bool
	}{
		{"version", func(tpl *GetBlockTemplateResult) { tpl.Version++ }, true, true},
		{"mintime", func(tpl *GetBlockTemplateResult) { tpl.Mintime++ }, true, true},
		{"target", func(tpl *GetBlockTemplateResult) { tpl.Target = "different" }, true, true},
		{"coinbase value", func(tpl *GetBlockTemplateResult) { tpl.CoinbaseValue-- }, true, false},
		{"witness commitment", func(tpl *GetBlockTemplateResult) { tpl.DefaultWitnessCommitment = "commitment-b" }, true, false},
		{"coinbase flags", func(tpl *GetBlockTemplateResult) { tpl.CoinbaseAux.Flags = "flags-b" }, true, false},
		{"vbavailable mask policy", func(tpl *GetBlockTemplateResult) { tpl.VbAvailable["deployment"] = 14 }, true, true},
		{"vbrequired", func(tpl *GetBlockTemplateResult) { tpl.VbRequired++ }, true, true},
		{"irrelevant mutable metadata", func(tpl *GetBlockTemplateResult) { tpl.Mutable = append(tpl.Mutable, "transactions") }, false, false},
		{"irrelevant rule metadata", func(tpl *GetBlockTemplateResult) { tpl.Rules = append(tpl.Rules, "new-rule") }, false, false},
		{"active rules metadata", func(tpl *GetBlockTemplateResult) { tpl.Rules = append(tpl.Rules, "deployment") }, false, false},
		{"transaction witness hash", func(tpl *GetBlockTemplateResult) { tpl.Transactions[0].Hash = "wtxid-b" }, true, false},
		{"transaction data", func(tpl *GetBlockTemplateResult) { tpl.Transactions[0].Data = "data-b" }, true, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			current := base
			current.VbAvailable = map[string]int{"deployment": 13}
			current.Mutable = append([]string(nil), base.Mutable...)
			current.Rules = append([]string(nil), base.Rules...)
			current.Transactions = append([]GBTTransaction(nil), base.Transactions...)
			next := current
			next.VbAvailable = map[string]int{"deployment": 13}
			next.Mutable = append([]string(nil), current.Mutable...)
			next.Rules = append([]string(nil), current.Rules...)
			next.Transactions = append([]GBTTransaction(nil), current.Transactions...)
			tc.mutate(&next)

			cfg := Config{}
			jm := &JobManager{cfg: cfg, curJob: &Job{
				Template:    current,
				VersionMask: computePoolMask(current, cfg),
			}}
			changed, clean := jm.templateChanged(next)
			if changed != tc.wantChanged || clean != tc.wantClean {
				t.Fatalf("templateChanged = (%v, %v), want (%v, %v)", changed, clean, tc.wantChanged, tc.wantClean)
			}
		})
	}
}

func TestTemplateChangedKeepsCoinbaseTransactionBundleNonClean(t *testing.T) {
	current := GetBlockTemplateResult{
		Previous:                 "prev",
		Height:                   100,
		Bits:                     "1d00ffff",
		Target:                   "target",
		Version:                  0x20000000,
		CoinbaseValue:            50 * 1e8,
		DefaultWitnessCommitment: "commitment-a",
		Transactions: []GBTTransaction{{
			Txid: "tx-a",
			Hash: "wtx-a",
			Data: "data-a",
		}},
	}
	current.CoinbaseAux.Flags = "flags-a"
	next := current
	next.CoinbaseValue--
	next.DefaultWitnessCommitment = "commitment-b"
	next.CoinbaseAux.Flags = "flags-b"
	next.Transactions = []GBTTransaction{{Txid: "tx-b", Hash: "wtx-b", Data: "data-b"}}
	cfg := Config{}
	jm := &JobManager{cfg: cfg, curJob: &Job{
		Template:    current,
		VersionMask: computePoolMask(current, cfg),
	}}

	changed, clean := jm.templateChanged(next)
	if !changed || clean {
		t.Fatalf("templateChanged = (%v, %v), want (true, false)", changed, clean)
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
			name: "forced on bit does not churn identical raw template",
			currentCfg: Config{VersionBitOverrides: map[uint32]bool{
				5: true,
			}},
			nextCfg: Config{VersionBitOverrides: map[uint32]bool{
				5: true,
			}},
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
			name: "non-overridden node version change remains clean",
			currentCfg: Config{VersionBitOverrides: map[uint32]bool{
				5: true,
			}},
			nextCfg: Config{VersionBitOverrides: map[uint32]bool{
				5: true,
			}},
			currentRaw:  baseVersion,
			nextRaw:     baseVersion | 1<<6,
			wantChanged: true,
			wantClean:   true,
		},
		{
			name:       "runtime version override creates clean job",
			currentCfg: Config{},
			nextCfg: Config{VersionBitOverrides: map[uint32]bool{
				5: true,
			}},
			currentRaw:  baseVersion,
			nextRaw:     baseVersion,
			wantChanged: true,
			wantClean:   true,
		},
		{
			name: "runtime override polarity change creates clean job",
			currentCfg: Config{VersionBitOverrides: map[uint32]bool{
				5: true,
			}},
			nextCfg: Config{VersionBitOverrides: map[uint32]bool{
				5: false,
			}},
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
				cfg: tc.nextCfg,
				curJob: &Job{
					Template:    current,
					VersionMask: computePoolMask(current, tc.currentCfg),
				},
			}

			changed, clean := jm.templateChanged(next)
			if changed != tc.wantChanged || clean != tc.wantClean {
				t.Fatalf("templateChanged = (%v, %v), want (%v, %v)", changed, clean, tc.wantChanged, tc.wantClean)
			}
		})
	}
}
