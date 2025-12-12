package main

import (
	"strings"
	"testing"
)

func TestAggregateCoinbaseSplitMatchesOutputs(t *testing.T) {
	poolScript := strings.Repeat("aa", 10)
	workerScript := strings.Repeat("bb", 10)

	dbg := &ShareDebug{
		CoinbaseOutputs: []CoinbaseOutputDebug{
			{ValueSats: 1_000_000, ScriptHex: poolScript},
			{ValueSats: 9_000_000, ScriptHex: workerScript},
			{ValueSats: 500_000, ScriptHex: "cc"},
		},
	}

	aggregateCoinbaseSplit(poolScript, workerScript, dbg)

	if dbg.WorkerValueSats != 9_000_000 {
		t.Fatalf("worker value sats = %d, want %d", dbg.WorkerValueSats, 9_000_000)
	}
	if dbg.PoolValueSats != 1_000_000 {
		t.Fatalf("pool value sats = %d, want %d", dbg.PoolValueSats, 1_000_000)
	}

	total := dbg.WorkerValueSats + dbg.PoolValueSats
	if total == 0 {
		t.Fatalf("total value sats is zero")
	}

	expectedWorkerPct := float64(dbg.WorkerValueSats) * 100 / float64(total)
	expectedPoolPct := float64(dbg.PoolValueSats) * 100 / float64(total)

	if dbg.WorkerPercent != expectedWorkerPct {
		t.Fatalf("worker percent = %f, want %f", dbg.WorkerPercent, expectedWorkerPct)
	}
	if dbg.PoolPercent != expectedPoolPct {
		t.Fatalf("pool percent = %f, want %f", dbg.PoolPercent, expectedPoolPct)
	}
}
