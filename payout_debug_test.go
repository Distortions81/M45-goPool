//go:build debug

package main

import (
	"encoding/hex"
	"testing"
	"time"
)

func buildTestJob(t *testing.T, poolAddr string, coinbaseValue int64) *Job {
	t.Helper()
	poolScript, err := scriptForAddress(poolAddr, ChainParams())
	if err != nil {
		t.Fatalf("scriptForAddress(pool) error: %v", err)
	}
	return &Job{
		Template: GetBlockTemplateResult{
			Height: 926364,
		},
		Extranonce2Size:         4,
		CoinbaseValue:           coinbaseValue,
		WitnessCommitment:       "",
		CoinbaseMsg:             "goPool",
		PayoutScript:            poolScript,
		TemplateExtraNonce2Size: 4,
		ScriptTime:              time.Now().Unix(),
	}
}

func buildBlockCoinbase(t *testing.T, mc *MinerConn, job *Job, worker string, en2 []byte) []byte {
	t.Helper()
	var cbTx []byte
	var err error
	if poolScript, workerScript, totalValue, feePercent, ok := mc.dualPayoutParams(job, worker); ok {
		cbTx, _, err = serializeDualCoinbaseTx(
			job.Template.Height,
			mc.extranonce1,
			en2,
			job.TemplateExtraNonce2Size,
			poolScript,
			workerScript,
			totalValue,
			feePercent,
			job.WitnessCommitment,
			job.Template.CoinbaseAux.Flags,
			job.CoinbaseMsg,
			job.ScriptTime,
		)
		if err != nil {
			t.Fatalf("serializeDualCoinbaseTx error: %v", err)
		}
	}
	if len(cbTx) == 0 {
		cbTx, _, err = serializeCoinbaseTx(
			job.Template.Height,
			mc.extranonce1,
			en2,
			job.TemplateExtraNonce2Size,
			job.PayoutScript,
			job.CoinbaseValue,
			job.WitnessCommitment,
			job.Template.CoinbaseAux.Flags,
			job.CoinbaseMsg,
			job.ScriptTime,
		)
		if err != nil {
			t.Fatalf("serializeCoinbaseTx fallback error: %v", err)
		}
	}
	return cbTx
}

func newTestAccountStore(t *testing.T) *AccountStore {
	t.Helper()
	cfg := Config{DataDir: t.TempDir()}
	s, err := NewAccountStore(cfg, false, true)
	if err != nil {
		t.Fatalf("NewAccountStore error: %v", err)
	}
	return s
}

func TestShareDetailCoinbaseMatchesBlockCoinbase(t *testing.T) {
	const (
		workerName = "bc1qagc0l2cvx0c0mx23rkjpwhe7klelynj98h82tj.bitaxe"
		workerAddr = "bc1qagc0l2cvx0c0mx23rkjpwhe7klelynj98h82tj"
		poolAddr   = "3B86bWqfjdQeLEr8nkeeWU6ygksc2K7MoL"
	)

	tests := []struct {
		name             string
		dualEnabled      bool
		poolFeePercent   float64
		poolAddress      string
		workerHasScript  bool
		expectDualPayout bool
	}{
		{
			name:             "single_output_pool_only",
			dualEnabled:      false,
			poolFeePercent:   0,
			poolAddress:      poolAddr,
			workerHasScript:  true,
			expectDualPayout: false,
		},
		{
			name:             "dual_payout_pool_and_worker",
			dualEnabled:      true,
			poolFeePercent:   2.0,
			poolAddress:      poolAddr,
			workerHasScript:  true,
			expectDualPayout: true,
		},
		{
			name:             "dual_enabled_missing_worker_script_falls_back",
			dualEnabled:      true,
			poolFeePercent:   2.0,
			poolAddress:      poolAddr,
			workerHasScript:  false,
			expectDualPayout: false,
		},
		{
			name:             "dual_enabled_same_pool_and_worker_address_uses_single_output",
			dualEnabled:      true,
			poolFeePercent:   2.0,
			poolAddress:      workerAddr,
			workerHasScript:  true,
			expectDualPayout: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			acct := newTestAccountStore(t)
			job := buildTestJob(t, tt.poolAddress, 50*1e8)

			mc := &MinerConn{
				cfg: Config{
					PayoutAddress:  tt.poolAddress,
					PoolFeePercent: tt.poolFeePercent,
				},
				accounting:  acct,
				extranonce1: []byte{0xaa, 0xbb, 0xcc, 0xdd},
			}

			if tt.workerHasScript {
				script, err := scriptForAddress(workerAddr, ChainParams())
				if err != nil {
					t.Fatalf("scriptForAddress(worker) error: %v", err)
				}
				mc.setWorkerWallet(workerName, workerAddr, script)
			}

			en2 := []byte{0x01, 0x02, 0x03, 0x04}
			cbBlock := buildBlockCoinbase(t, mc, job, workerName, en2)

			header := []byte("header")
			hash := []byte("hash")
			extraHex := hex.EncodeToString(en2)
			debug := mc.buildShareDetail(job, workerName, header, hash, nil, extraHex, nil)
			if debug == nil || debug.Coinbase == "" {
				t.Fatalf("buildShareDetail returned nil or empty coinbase")
			}
			cbPanel, err := hex.DecodeString(debug.Coinbase)
			if err != nil {
				t.Fatalf("decode debug coinbase: %v", err)
			}

			if !hexEqual(cbBlock, cbPanel) {
				t.Fatalf("coinbase mismatch between block and panel paths\nblock:  %x\npanel:  %x", cbBlock, cbPanel)
			}
		})
	}
}

func hexEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
