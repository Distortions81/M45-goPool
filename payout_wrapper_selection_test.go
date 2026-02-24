package main

import (
	"bytes"
	"testing"
	"time"
)

func testSubmitTask(job *Job, worker string) submissionTask {
	return submissionTask{
		job:              job,
		jobID:            job.JobID,
		workerName:       worker,
		extranonce2:      "00000000",
		extranonce2Large: []byte{0x00, 0x00, 0x00, 0x00},
		ntime:            "6553f100",
		ntimeVal:         uint32(job.Template.CurTime),
		nonce:            "00000001",
		nonceVal:         1,
		useVersion:       uint32(job.Template.Version),
		scriptTime:       job.ScriptTime,
		receivedAt:       time.Now(),
	}
}

func testWrapperJob(poolScript []byte, total int64) *Job {
	return &Job{
		JobID: "wrapper-selection-job",
		Template: GetBlockTemplateResult{
			Height:        101,
			CurTime:       1700000000,
			Bits:          "1d00ffff",
			Version:       0x20000000,
			Previous:      "0000000000000000000000000000000000000000000000000000000000000000",
			CoinbaseValue: total,
		},
		Extranonce2Size:         4,
		TemplateExtraNonce2Size: 8,
		PayoutScript:            poolScript,
		CoinbaseValue:           total,
		CoinbaseMsg:             "goPool-wrapper-selection-test",
		ScriptTime:              1700000000,
	}
}

func TestPrepareShareContextStrict_SelectsSingleDualTripleWrappers(t *testing.T) {
	prevDebug := debugLogging
	debugLogging = true
	defer func() { debugLogging = prevDebug }()

	poolAddr, poolScript := generateTestWallet(t)
	workerName, workerAddr, workerScript := generateTestWorker(t)
	_, donationScript := generateTestWallet(t)

	tests := []struct {
		name             string
		poolFeePercent   float64
		payoutAddress    string
		withWorkerWallet bool
		donationPercent  float64
		withDonation     bool
		workerName       string
		wantOutCount     int
		wantScripts      [][]byte
	}{
		{
			name:             "single_wrapper_when_pool_fee_zero",
			poolFeePercent:   0,
			payoutAddress:    poolAddr,
			withWorkerWallet: true,
			workerName:       workerName,
			wantOutCount:     1,
			wantScripts:      [][]byte{workerScript},
		},
		{
			name:             "dual_wrapper_when_pool_fee_positive",
			poolFeePercent:   2.0,
			payoutAddress:    poolAddr,
			withWorkerWallet: true,
			workerName:       workerName,
			wantOutCount:     2,
			wantScripts:      [][]byte{poolScript, workerScript},
		},
		{
			name:             "triple_wrapper_when_donation_enabled",
			poolFeePercent:   2.0,
			payoutAddress:    poolAddr,
			withWorkerWallet: true,
			workerName:       workerName,
			donationPercent:  12.5,
			withDonation:     true,
			wantOutCount:     3,
			wantScripts:      [][]byte{poolScript, donationScript, workerScript},
		},
		{
			name:             "fallback_to_single_when_worker_equals_pool_wallet",
			poolFeePercent:   2.0,
			payoutAddress:    poolAddr,
			withWorkerWallet: true,
			workerName:       poolAddr + ".worker",
			wantOutCount:     1,
			wantScripts:      [][]byte{poolScript},
		},
		{
			name:             "fallback_to_single_when_worker_wallet_missing",
			poolFeePercent:   2.0,
			payoutAddress:    poolAddr,
			withWorkerWallet: false,
			workerName:       "missing.worker",
			wantOutCount:     1,
			wantScripts:      [][]byte{poolScript},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job := testWrapperJob(poolScript, 50*1e8)
			if tt.withDonation {
				job.OperatorDonationPercent = tt.donationPercent
				job.DonationScript = donationScript
			}

			mc := &MinerConn{
				cfg: Config{
					PoolFeePercent: tt.poolFeePercent,
					PayoutAddress:  tt.payoutAddress,
				},
				stratumV1: minerConnStratumV1State{
					extranonce1: []byte{0x01, 0x02, 0x03, 0x04},
				},
			}

			if tt.withWorkerWallet {
				addr := workerAddr
				script := workerScript
				if tt.workerName == poolAddr+".worker" {
					addr = poolAddr
					script = poolScript
				}
				mc.setWorkerWallet(tt.workerName, addr, script)
			}

			task := testSubmitTask(job, tt.workerName)
			ctx, ok := mc.prepareShareContextStrict(task)
			if !ok {
				t.Fatalf("prepareShareContextStrict returned not ok")
			}
			if len(ctx.cbTx) == 0 {
				t.Fatalf("expected cbTx in debug context")
			}

			outs := parseCoinbaseOutputs(t, ctx.cbTx)
			if len(outs) != tt.wantOutCount {
				t.Fatalf("output count mismatch: got %d, want %d", len(outs), tt.wantOutCount)
			}

			for _, expectedScript := range tt.wantScripts {
				found := false
				for _, out := range outs {
					if bytes.Equal(out.PkScript, expectedScript) {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("expected script %x not present in outputs", expectedScript)
				}
			}
		})
	}
}
