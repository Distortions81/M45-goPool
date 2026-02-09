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
