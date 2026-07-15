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
