package main

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func flushFoundBlockLogger(t *testing.T) {
	t.Helper()
	done := make(chan struct{})
	select {
	case foundBlockLogCh <- foundBlockLogEntry{Done: done}:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out sending found block flush marker")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for found block flush marker")
	}
}

// readLastFoundBlockRecord polls the sqlite state DB in dir and returns the
// last JSON object written. It is used to verify logFoundBlock behaviour.
func readLastFoundBlockRecord(t *testing.T, dir string) map[string]interface{} {
	t.Helper()
	flushFoundBlockLogger(t)
	dbPath := stateDBPathFromDataDir(dir)
	deadline := time.Now().Add(2 * time.Second)
	for {
		db, err := openStateDB(dbPath)
		if err == nil {
			var rec map[string]interface{}
			var line string
			if err := db.QueryRow("SELECT json FROM found_blocks_log ORDER BY id DESC LIMIT 1").Scan(&line); err == nil && line != "" {
				_ = db.Close()
				if err := json.Unmarshal([]byte(line), &rec); err != nil {
					t.Fatalf("unmarshal found block record: %v", err)
				}
				return rec
			}
			_ = db.Close()
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for found block sqlite row in %s", dbPath)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestLogFoundBlockFallback_NoWorkerScript verifies that when there is no
// cached worker payout script, logFoundBlock treats the full reward as a
// pool-only payout and sets dual_payout_fallback=true.
func TestLogFoundBlockFallback_NoWorkerScript(t *testing.T) {
	dir := t.TempDir()
	job := &Job{
		JobID: "log-test",
		Template: GetBlockTemplateResult{
			Height:        100,
			CurTime:       1700000000,
			Bits:          "1d00ffff",
			Previous:      "00",
			CoinbaseValue: 50 * 1e8,
		},
	}
	cfg := Config{
		DataDir:        dir,
		PayoutAddress:  "1PoolAddrExample111111111111111111",
		PoolFeePercent: 2.0,
	}
	mc := &MinerConn{cfg: cfg}

	mc.logFoundBlock(job, "worker1", "deadbeef", 1.0)

	rec := readLastFoundBlockRecord(t, dir)

	total := int64(rec["coinbase_value_sats"].(float64))
	poolFee := int64(rec["pool_fee_sats"].(float64))
	workerAmt := int64(rec["worker_payout_sats"].(float64))
	fallback, ok := rec["dual_payout_fallback"].(bool)
	if !ok {
		t.Fatalf("dual_payout_fallback missing or not bool")
	}

	if total != job.Template.CoinbaseValue {
		t.Fatalf("coinbase_value_sats mismatch: got %d, want %d", total, job.Template.CoinbaseValue)
	}
	if poolFee != total || workerAmt != 0 {
		t.Fatalf("expected full reward to pool in fallback: pool_fee=%d worker=%d total=%d", poolFee, workerAmt, total)
	}
	if !fallback {
		t.Fatalf("expected dual_payout_fallback=true when worker script is missing")
	}
}

// TestLogFoundBlockFallback_SameWorkerAddress verifies that when the worker
// address matches the pool payout address, logFoundBlock treats the reward as
// pool-only and sets dual_payout_fallback=true even when a worker payout
// script is cached.
func TestLogFoundBlockFallback_SameWorkerAddress(t *testing.T) {
	dir := t.TempDir()
	addr := "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa" // valid mainnet address

	job := &Job{
		JobID: "log-test-same-addr",
		Template: GetBlockTemplateResult{
			Height:        101,
			CurTime:       1700000100,
			Bits:          "1d00ffff",
			Previous:      "00",
			CoinbaseValue: 25 * 1e8,
		},
	}
	cfg := Config{
		DataDir:        dir,
		PayoutAddress:  addr,
		PoolFeePercent: 1.5,
	}
	mc := &MinerConn{cfg: cfg}

	// Cache a valid worker payout script for the worker so the fallback
	// path is triggered by address equality rather than missing script.
	script, err := scriptForAddress(addr, ChainParams())
	if err != nil {
		t.Fatalf("scriptForAddress error: %v", err)
	}
	mc.setWorkerWallet(addr, addr, script)

	mc.logFoundBlock(job, addr, "deadbeef", 1.0)

	rec := readLastFoundBlockRecord(t, dir)

	total := int64(rec["coinbase_value_sats"].(float64))
	poolFee := int64(rec["pool_fee_sats"].(float64))
	workerAmt := int64(rec["worker_payout_sats"].(float64))
	fallback, ok := rec["dual_payout_fallback"].(bool)
	if !ok {
		t.Fatalf("dual_payout_fallback missing or not bool")
	}

	if total != job.Template.CoinbaseValue {
		t.Fatalf("coinbase_value_sats mismatch: got %d, want %d", total, job.Template.CoinbaseValue)
	}
	if poolFee != total || workerAmt != 0 {
		t.Fatalf("expected full reward to pool when worker address equals payout: pool_fee=%d worker=%d total=%d", poolFee, workerAmt, total)
	}
	if !fallback {
		t.Fatalf("expected dual_payout_fallback=true when worker address equals pool payout address")
	}
}

// TestLogFoundBlock_NoFallbackDifferentAddress verifies that when the worker
// address differs from the pool payout address and a worker payout script is
// cached, logFoundBlock records a pool/worker split based on PoolFeePercent
// and dual_payout_fallback=false.
func TestLogFoundBlock_NoFallbackDifferentAddress(t *testing.T) {
	dir := t.TempDir()
	poolAddr := "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa"   // valid mainnet
	workerAddr := "1BoatSLRHtKNngkdXEeobR76b53LETtpyT" // another valid mainnet

	job := &Job{
		JobID: "log-test-no-fallback",
		Template: GetBlockTemplateResult{
			Height:        102,
			CurTime:       1700000200,
			Bits:          "1d00ffff",
			Previous:      "00",
			CoinbaseValue: 10 * 1e8,
		},
	}
	cfg := Config{
		DataDir:        dir,
		PayoutAddress:  poolAddr,
		PoolFeePercent: 2.5,
	}
	mc := &MinerConn{cfg: cfg}

	script, err := scriptForAddress(workerAddr, ChainParams())
	if err != nil {
		t.Fatalf("scriptForAddress error: %v", err)
	}
	mc.setWorkerWallet(workerAddr, workerAddr, script)

	mc.logFoundBlock(job, workerAddr, "deadbeef", 1.0)

	rec := readLastFoundBlockRecord(t, dir)

	total := int64(rec["coinbase_value_sats"].(float64))
	poolFee := int64(rec["pool_fee_sats"].(float64))
	workerAmt := int64(rec["worker_payout_sats"].(float64))
	fallback, ok := rec["dual_payout_fallback"].(bool)
	if !ok {
		t.Fatalf("dual_payout_fallback missing or not bool")
	}

	if total != job.Template.CoinbaseValue {
		t.Fatalf("coinbase_value_sats mismatch: got %d, want %d", total, job.Template.CoinbaseValue)
	}

	// Mirror the pool's split logic.
	feePct := cfg.PoolFeePercent
	if feePct < 0 {
		feePct = 0
	}
	if feePct > 99.99 {
		feePct = 99.99
	}
	expectedPoolFee := int64(math.Round(float64(total) * feePct / 100.0))
	if expectedPoolFee < 0 {
		expectedPoolFee = 0
	}
	if expectedPoolFee > total {
		expectedPoolFee = total
	}
	expectedWorker := total - expectedPoolFee

	if poolFee != expectedPoolFee || workerAmt != expectedWorker {
		t.Fatalf("unexpected split: pool_fee=%d worker=%d total=%d expected_pool=%d expected_worker=%d",
			poolFee, workerAmt, total, expectedPoolFee, expectedWorker)
	}
	if fallback {
		t.Fatalf("expected dual_payout_fallback=false when worker address differs from pool payout")
	}
}
