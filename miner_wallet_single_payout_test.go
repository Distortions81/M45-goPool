package main

import (
	"bytes"
	"testing"
)

func TestSinglePayoutScript_UsesWorkerScriptWhenPoolFeeIsZero(t *testing.T) {
	_, poolScript := generateTestWallet(t)
	workerName, workerAddr, workerScript := generateTestWorker(t)

	job := &Job{PayoutScript: poolScript}
	mc := &MinerConn{cfg: Config{PoolFeePercent: 0}}
	mc.setWorkerWallet(workerName, workerAddr, workerScript)

	got := mc.singlePayoutScript(job, workerName)
	if !bytes.Equal(got, workerScript) {
		t.Fatalf("singlePayoutScript returned pool script at 0 fee; expected worker script")
	}
}

func TestSinglePayoutScript_UsesPoolScriptWhenPoolFeeIsPositive(t *testing.T) {
	_, poolScript := generateTestWallet(t)
	workerName, workerAddr, workerScript := generateTestWorker(t)

	job := &Job{PayoutScript: poolScript}
	mc := &MinerConn{cfg: Config{PoolFeePercent: 2}}
	mc.setWorkerWallet(workerName, workerAddr, workerScript)

	got := mc.singlePayoutScript(job, workerName)
	if !bytes.Equal(got, poolScript) {
		t.Fatalf("singlePayoutScript should use pool script when fee is positive")
	}
}

func TestSinglePayoutScript_ZeroPoolFeeWithoutWorkerScriptReturnsNil(t *testing.T) {
	_, poolScript := generateTestWallet(t)
	job := &Job{PayoutScript: poolScript}
	mc := &MinerConn{cfg: Config{PoolFeePercent: 0}}

	got := mc.singlePayoutScript(job, "missing.worker")
	if got != nil {
		t.Fatalf("singlePayoutScript should return nil when worker wallet is unresolved at 0 fee")
	}
}

func TestJobPayoutPolicy_RemainsStableAfterRuntimeConfigChange(t *testing.T) {
	_, poolScript := generateTestWallet(t)
	workerName, workerAddr, workerScript := generateTestWorker(t)

	job := &Job{
		PayoutScript:         poolScript,
		PayoutAddress:        "captured-pool-address",
		PoolFeePercent:       2,
		PayoutPolicyCaptured: true,
		CoinbaseValue:        50 * 1e8,
	}
	mc := &MinerConn{cfg: Config{PoolFeePercent: 0, PayoutAddress: workerAddr}}
	mc.setWorkerWallet(workerName, workerAddr, workerScript)

	gotPool, gotWorker, _, gotFee, ok := mc.dualPayoutParams(job, workerName)
	if !ok {
		t.Fatal("captured positive-fee job unexpectedly changed to single-payout mode")
	}
	if !bytes.Equal(gotPool, poolScript) || !bytes.Equal(gotWorker, workerScript) {
		t.Fatal("captured payout scripts changed after runtime config update")
	}
	if gotFee != 2 {
		t.Fatalf("captured fee = %v, want 2", gotFee)
	}
}

func TestJobPayoutPolicy_CapturedZeroFeeIgnoresCurrentPositiveFee(t *testing.T) {
	_, poolScript := generateTestWallet(t)
	workerName, workerAddr, workerScript := generateTestWorker(t)

	job := &Job{
		PayoutScript:         poolScript,
		PoolFeePercent:       0,
		PayoutPolicyCaptured: true,
	}
	mc := &MinerConn{cfg: Config{PoolFeePercent: 2}}
	mc.setWorkerWallet(workerName, workerAddr, workerScript)

	if got := mc.singlePayoutScript(job, workerName); !bytes.Equal(got, workerScript) {
		t.Fatal("captured zero-fee job did not retain worker-only payout")
	}
}
