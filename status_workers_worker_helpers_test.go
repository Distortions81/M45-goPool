package main

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func TestBuildCurrentJobCoinbaseDetail_UsesExactSinglePayoutPath(t *testing.T) {
	_, poolScript := generateTestWallet(t)
	workerName, workerAddr, workerScript := generateTestWorker(t)

	job := &Job{
		Template: GetBlockTemplateResult{
			Height: 120000,
		},
		JobID:                   "job-1",
		Extranonce2Size:         4,
		TemplateExtraNonce2Size: 4,
		CoinbaseValue:           50 * 1e8,
		PayoutScript:            poolScript,
		ScriptTime:              1000,
	}
	mc := &MinerConn{
		cfg: Config{PoolFeePercent: 0},
		stratumV1: minerConnStratumV1State{
			extranonce1: []byte{0xaa, 0xbb, 0xcc, 0xdd},
			notify: minerConnStratumV1NotifyState{
				jobScriptTime: map[string]int64{
					"job-1": 2000,
				},
			},
		},
	}
	mc.updateWorker(workerName)
	mc.setWorkerWallet(workerName, workerAddr, workerScript)
	coinb1, coinb2, err := buildCoinbaseParts(
		job.Template.Height,
		mc.stratumV1.extranonce1,
		job.Extranonce2Size,
		job.TemplateExtraNonce2Size,
		workerScript,
		job.CoinbaseValue,
		job.WitnessCommitment,
		job.Template.CoinbaseAux.Flags,
		job.CoinbaseMsg,
		2000,
	)
	if err != nil {
		t.Fatalf("buildCoinbaseParts: %v", err)
	}
	mc.stratumV1.notify.jobNotifyCoinbase = map[string]notifiedCoinbaseParts{
		job.JobID: {coinb1: coinb1, coinb2: coinb2},
	}

	detail := mc.buildCurrentJobCoinbaseDetail(job)
	if detail == nil || detail.Coinbase == "" {
		t.Fatalf("buildCurrentJobCoinbaseDetail failed")
	}

	gotRaw, err := hex.DecodeString(detail.Coinbase)
	if err != nil {
		t.Fatalf("decode detail coinbase: %v", err)
	}
	expectedRaw, _, err := serializeCoinbaseTx(
		job.Template.Height,
		mc.stratumV1.extranonce1,
		[]byte{0, 0, 0, 0},
		job.TemplateExtraNonce2Size,
		workerScript,
		job.CoinbaseValue,
		job.WitnessCommitment,
		job.Template.CoinbaseAux.Flags,
		job.CoinbaseMsg,
		2000,
	)
	if err != nil {
		t.Fatalf("serialize expected coinbase: %v", err)
	}
	if !bytes.Equal(gotRaw, expectedRaw) {
		t.Fatalf("coinbase mismatch between display path and miner build path")
	}
}
