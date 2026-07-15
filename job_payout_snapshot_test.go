package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestJobPayoutScriptsRemainImmutableAcrossRuntimeConfig(t *testing.T) {
	const bestHash = "0000000000000000000000000000000000000000000000000000000000000001"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode RPC request: %v", err)
			return
		}
		result, _ := json.Marshal(bestHash)
		_ = json.NewEncoder(w).Encode(rpcResponse{ID: req.ID, Result: result})
	}))
	t.Cleanup(srv.Close)

	rpc := &RPCClient{url: srv.URL, client: srv.Client(), lp: srv.Client()}
	oldPoolScript := []byte{0x51, 0x21, 0x01}
	oldDonationScript := []byte{0x51, 0x21, 0x02}
	oldCfg := Config{
		PayoutAddress:           "old-payout",
		PoolFeePercent:          2,
		OperatorDonationPercent: 1,
		Extranonce2Size:         4,
		TemplateExtraNonce2Size: 8,
	}
	jm := NewJobManager(rpc, oldCfg, nil, oldPoolScript, oldDonationScript)

	// The manager owns constructor inputs; caller mutation must not leak in.
	oldPoolScript[0] = 0xff
	oldDonationScript[0] = 0xff

	tpl := GetBlockTemplateResult{
		Bits:                     "1d00ffff",
		Target:                   "00000000ffff0000000000000000000000000000000000000000000000000000",
		CurTime:                  1_700_000_000,
		Height:                   101,
		Version:                  0x20000000,
		Previous:                 bestHash,
		CoinbaseValue:            50 * 1e8,
		DefaultWitnessCommitment: "00",
	}
	oldJob, err := jm.buildJob(context.Background(), tpl)
	if err != nil {
		t.Fatalf("build old job: %v", err)
	}

	newPoolScript := []byte{0x52, 0x21, 0x03}
	newDonationScript := []byte{0x52, 0x21, 0x04}
	newCfg := oldCfg
	newCfg.PayoutAddress = "new-payout"
	newCfg.PoolFeePercent = 3
	newCfg.OperatorDonationPercent = 1.5
	jm.ApplyRuntimeConfig(newCfg, newPoolScript, newDonationScript)

	// Runtime apply owns its inputs too.
	newPoolScript[0] = 0xff
	newDonationScript[0] = 0xff

	if !bytes.Equal(oldJob.PayoutScript, []byte{0x51, 0x21, 0x01}) {
		t.Fatalf("old payout script mutated: %x", oldJob.PayoutScript)
	}
	if !bytes.Equal(oldJob.DonationScript, []byte{0x51, 0x21, 0x02}) {
		t.Fatalf("old donation script mutated: %x", oldJob.DonationScript)
	}
	if oldJob.PayoutAddress != oldCfg.PayoutAddress || oldJob.PoolFeePercent != oldCfg.PoolFeePercent ||
		oldJob.OperatorDonationPercent != oldCfg.OperatorDonationPercent {
		t.Fatalf("old payout policy mutated: address=%q fee=%v donation=%v",
			oldJob.PayoutAddress, oldJob.PoolFeePercent, oldJob.OperatorDonationPercent)
	}

	newJob, err := jm.buildJob(context.Background(), tpl)
	if err != nil {
		t.Fatalf("build new job: %v", err)
	}
	if !bytes.Equal(newJob.PayoutScript, []byte{0x52, 0x21, 0x03}) {
		t.Fatalf("new payout script = %x", newJob.PayoutScript)
	}
	if !bytes.Equal(newJob.DonationScript, []byte{0x52, 0x21, 0x04}) {
		t.Fatalf("new donation script = %x", newJob.DonationScript)
	}
	if newJob.PayoutAddress != newCfg.PayoutAddress || newJob.PoolFeePercent != newCfg.PoolFeePercent ||
		newJob.OperatorDonationPercent != newCfg.OperatorDonationPercent {
		t.Fatalf("new payout policy mismatch: address=%q fee=%v donation=%v",
			newJob.PayoutAddress, newJob.PoolFeePercent, newJob.OperatorDonationPercent)
	}
}
