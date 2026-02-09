package main

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"testing"
	"time"
)

// timingRPC is a lightweight rpcCaller used to measure how long it takes
// for handleBlockShare to reach the submitblock RPC. It records the elapsed
// time from a test-provided start timestamp until call is invoked.
type timingRPC struct {
	start   time.Time
	elapsed time.Duration
	method  string
}

func (t *timingRPC) call(method string, params interface{}, out interface{}) error {
	t.method = method
	if !t.start.IsZero() {
		t.elapsed = time.Since(t.start)
	}
	// No-op RPC: return success immediately so timing reflects only the
	// block construction work before the RPC call.
	return nil
}

func (t *timingRPC) callCtx(_ context.Context, method string, params interface{}, out interface{}) error {
	return t.call(method, params, out)
}

// nopConn is a minimal net.Conn implementation used to satisfy MinerConn's
// write path during tests without performing any real network I/O.
type nopConn struct{}

func (nopConn) Read(b []byte) (int, error)       { return 0, nil }
func (nopConn) Write(b []byte) (int, error)      { return len(b), nil }
func (nopConn) Close() error                     { return nil }
func (nopConn) LocalAddr() net.Addr              { return &net.IPAddr{} }
func (nopConn) RemoteAddr() net.Addr             { return &net.IPAddr{} }
func (nopConn) SetDeadline(time.Time) error      { return nil }
func (nopConn) SetReadDeadline(time.Time) error  { return nil }
func (nopConn) SetWriteDeadline(time.Time) error { return nil }

// TestHandleBlockShareSubmitLatency measures the time from entering
// handleBlockShare to the point where submitblock is invoked. It uses a
// minimal Job and a timingRPC so the test does not depend on network I/O.
func TestHandleBlockShareSubmitLatency(t *testing.T) {
	workerName, workerWallet, workerScript := generateTestWorker(t)

	// Minimal job mirroring the single-coinbase case from block_test.go.
	job := &Job{
		JobID: "timing-test-job",
		Template: GetBlockTemplateResult{
			Height:        101,
			CurTime:       1700000000,
			Mintime:       0,
			Bits:          "1d00ffff",
			Previous:      "0000000000000000000000000000000000000000000000000000000000000000",
			CoinbaseValue: 50 * 1e8,
		},
		Extranonce2Size:         4,
		TemplateExtraNonce2Size: 8,
		PayoutScript:            []byte{0x51}, // OP_TRUE for structure test
		WitnessCommitment:       "",
		CoinbaseMsg:             "goPool-timing-test",
		ScriptTime:              0,
		Transactions:            nil,
		MerkleBranches:          nil,
		CoinbaseValue:           50 * 1e8,
	}

	// Configure a MinerConn with a no-fee pool so dual-payout is disabled
	// and handleBlockShare uses the single-output block builder path.
	trpc := &timingRPC{}
	mc := &MinerConn{
		id:          "timing-test-miner",
		rpc:         trpc,
		cfg:         Config{PoolFeePercent: 0},
		extranonce1: []byte{0x01, 0x02, 0x03, 0x04},
	}
	mc.setWorkerWallet(workerName, workerWallet, workerScript)
	// Ensure handleBlockShare uses the scriptTime that matches what was notified.
	mc.jobScriptTime = map[string]int64{job.JobID: job.ScriptTime}
	// Provide a no-op conn/writer so handleBlockShare can emit responses
	// without panicking on a nil connection.
	mc.conn = nopConn{}
	mc.writer = bufio.NewWriter(mc.conn)

	// Parameters matching the job template.
	en2 := []byte{0xaa, 0xbb, 0xcc, 0xdd}
	ntimeHex := fmt.Sprintf("%08x", job.Template.CurTime)
	nonceHex := "00000001"
	useVersion := uint32(1)
	now := time.Now()

	req := &StratumRequest{ID: 1}

	trpc.start = time.Now()
	mc.handleBlockShare(req.ID, job, workerName, en2, ntimeHex, nonceHex, useVersion, "dummyhash", 1.0, now)

	if trpc.method != "submitblock" {
		t.Fatalf("expected submitblock RPC, got %q", trpc.method)
	}
	if trpc.elapsed <= 0 {
		t.Fatalf("expected positive elapsed time, got %s", trpc.elapsed)
	}

	t.Logf("handleBlockShare to submitblock took %s", trpc.elapsed)

	// This threshold is intentionally generous so the test does not flap
	// on slower CI machines; block construction for a single-coinbase
	// block should be comfortably below this under normal conditions.
	const maxAllowed = time.Millisecond * 2
	if trpc.elapsed > maxAllowed {
		t.Fatalf("submit path took too long: %s (max %s)", trpc.elapsed, maxAllowed)
	}
}
