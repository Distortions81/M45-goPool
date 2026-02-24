package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"sync/atomic"
	"testing"
	"time"
)

type countingSubmitRPC struct {
	submitCalls atomic.Int64
}

func (c *countingSubmitRPC) call(method string, params any, out any) error {
	return c.callCtx(context.Background(), method, params, out)
}

func (c *countingSubmitRPC) callCtx(_ context.Context, method string, params any, out any) error {
	if method == "submitblock" {
		c.submitCalls.Add(1)
	}
	return nil
}

func flushFoundBlockLog(t *testing.T) {
	t.Helper()
	done := make(chan struct{})
	select {
	case foundBlockLogCh <- foundBlockLogEntry{Done: done}:
	case <-time.After(250 * time.Millisecond):
		t.Fatalf("timed out enqueueing found block log flush")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for found block log flush")
	}
}

func TestWinningBlockNotRejectedAsDuplicate(t *testing.T) {
	metrics := NewPoolMetrics()
	mc := benchmarkMinerConnForSubmit(metrics)
	mc.cfg.ShareCheckDuplicate = true
	mc.cfg.DataDir = t.TempDir()
	mc.rpc = &countingSubmitRPC{}

	// Minimal job: make Target huge so the share is always treated as a block,
	// regardless of the computed difficulty.
	job := benchmarkSubmitJobForTest(t)
	job.Target = new(big.Int).Set(maxUint256)
	jobID := job.JobID
	mc.jobDifficulty[jobID] = 1e-12
	mc.jobScriptTime = map[string]int64{jobID: job.ScriptTime}

	ntimeHex := "6553f100" // 1700000000
	task := submissionTask{
		mc:               mc,
		reqID:            1,
		job:              job,
		jobID:            jobID,
		workerName:       mc.currentWorker(),
		extranonce2:      "00000000",
		extranonce2Large: []byte{0, 0, 0, 0},
		ntime:            ntimeHex,
		ntimeVal:         0x6553f100,
		nonce:            "00000000",
		nonceVal:         0x00000000,
		versionHex:       "00000001",
		useVersion:       1,
		scriptTime:       job.ScriptTime,
		receivedAt:       time.Unix(1700000000, 0),
	}

	// Seed the duplicate cache with the exact share key. If duplicate detection
	// were applied to winning blocks, this would cause an incorrect rejection.
	if dup := mc.isDuplicateShare(jobID, (&task).extranonce2Decoded(), task.ntimeVal, task.nonceVal, task.useVersion); dup {
		t.Fatalf("unexpected duplicate when seeding cache")
	}

	mc.conn = nopConn{}
	mc.processSubmissionTask(task)
	flushFoundBlockLog(t)

	rpc := mc.rpc.(*countingSubmitRPC)
	if got := rpc.submitCalls.Load(); got != 1 {
		t.Fatalf("expected submitblock to be called once, got %d", got)
	}
}

func TestWinningBlockUsesNotifiedScriptTime(t *testing.T) {
	metrics := NewPoolMetrics()
	mc := benchmarkMinerConnForSubmit(metrics)
	mc.cfg.ShareCheckDuplicate = false
	mc.cfg.DataDir = t.TempDir()
	mc.rpc = &countingSubmitRPC{}

	job := benchmarkSubmitJobForTest(t)
	job.MerkleBranches = nil
	job.Transactions = nil
	job.Target = new(big.Int).SetBytes([]byte{
		0x00, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
		0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
		0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
		0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
	})

	jobID := job.JobID
	ex2 := []byte{0, 0, 0, 0}
	ntimeHex := "6553f100" // 1700000000
	useVersion := uint32(1)

	// Simulate that we notified this miner using a unique scriptTime.
	notifiedScriptTime := job.ScriptTime + 1
	mc.jobScriptTime = map[string]int64{jobID: notifiedScriptTime}

	// Find a nonce such that the block condition is true for notifiedScriptTime,
	// but false for the fallback job.ScriptTime. This ensures we submit the block
	// only if we rebuild with the notified coinbase.
	payoutScript := mc.singlePayoutScript(job, mc.currentWorker())
	if len(payoutScript) == 0 {
		t.Fatalf("missing payout script for current worker")
	}
	var chosenNonce string
	for i := range uint32(500000) {
		nonceHex := fmt.Sprintf("%08x", i)

		cbTx, cbTxid, err := serializeCoinbaseTxPredecoded(
			job.Template.Height,
			mc.stratumV1.extranonce1,
			ex2,
			job.TemplateExtraNonce2Size,
			payoutScript,
			job.CoinbaseValue,
			job.witnessCommitScript,
			job.coinbaseFlagsBytes,
			job.CoinbaseMsg,
			notifiedScriptTime,
		)
		if err != nil || len(cbTxid) != 32 || len(cbTx) == 0 {
			t.Fatalf("coinbase build: %v", err)
		}
		merkle := computeMerkleRootFromBranches(cbTxid, job.MerkleBranches)
		hdr, err := job.buildBlockHeader(merkle, ntimeHex, nonceHex, int32(useVersion))
		if err != nil {
			t.Fatalf("build header: %v", err)
		}
		hh := doubleSHA256Array(hdr)
		var hhLE [32]byte
		copy(hhLE[:], hh[:])
		reverseBytes32(&hhLE)
		if new(big.Int).SetBytes(hhLE[:]).Cmp(job.Target) > 0 {
			continue
		}

		// Same nonce with fallback scriptTime should not be a block (high probability).
		cbTx2, cbTxid2, err := serializeCoinbaseTxPredecoded(
			job.Template.Height,
			mc.stratumV1.extranonce1,
			ex2,
			job.TemplateExtraNonce2Size,
			payoutScript,
			job.CoinbaseValue,
			job.witnessCommitScript,
			job.coinbaseFlagsBytes,
			job.CoinbaseMsg,
			job.ScriptTime,
		)
		if err != nil || len(cbTxid2) != 32 || len(cbTx2) == 0 {
			t.Fatalf("coinbase build fallback: %v", err)
		}
		merkle2 := computeMerkleRootFromBranches(cbTxid2, job.MerkleBranches)
		hdr2, err := job.buildBlockHeader(merkle2, ntimeHex, nonceHex, int32(useVersion))
		if err != nil {
			t.Fatalf("build header fallback: %v", err)
		}
		hh2 := doubleSHA256Array(hdr2)
		var hh2LE [32]byte
		copy(hh2LE[:], hh2[:])
		reverseBytes32(&hh2LE)
		if new(big.Int).SetBytes(hh2LE[:]).Cmp(job.Target) <= 0 {
			continue
		}

		chosenNonce = nonceHex
		break
	}
	if chosenNonce == "" {
		t.Fatalf("failed to find nonce for notified vs fallback scriptTime")
	}
	chosenNonceVal, err := parseUint32BEHex(chosenNonce)
	if err != nil {
		t.Fatalf("parse chosen nonce: %v", err)
	}

	task := submissionTask{
		mc:               mc,
		reqID:            1,
		job:              job,
		jobID:            jobID,
		workerName:       mc.currentWorker(),
		extranonce2:      "00000000",
		extranonce2Large: ex2,
		ntime:            ntimeHex,
		ntimeVal:         0x6553f100,
		nonce:            chosenNonce,
		nonceVal:         chosenNonceVal,
		versionHex:       "00000001",
		useVersion:       useVersion,
		scriptTime:       notifiedScriptTime,
		receivedAt:       time.Unix(1700000000, 0),
	}

	mc.conn = nopConn{}
	mc.processSubmissionTask(task)
	flushFoundBlockLog(t)

	rpc := mc.rpc.(*countingSubmitRPC)
	if got := rpc.submitCalls.Load(); got != 1 {
		t.Fatalf("expected submitblock to be called once, got %d", got)
	}
}

func TestBlockBypassesPolicyRejects(t *testing.T) {
	metrics := NewPoolMetrics()
	mc := benchmarkMinerConnForSubmit(metrics)
	mc.cfg.ShareCheckDuplicate = false
	mc.cfg.DataDir = t.TempDir()
	mc.rpc = &countingSubmitRPC{}

	job := benchmarkSubmitJobForTest(t)
	job.Target = new(big.Int).Set(maxUint256)
	jobID := job.JobID

	// Use a notified scriptTime to avoid coinbase mismatch issues.
	mc.jobScriptTime = map[string]int64{jobID: job.ScriptTime + 1}

	task := submissionTask{
		mc:               mc,
		reqID:            1,
		job:              job,
		jobID:            jobID,
		workerName:       mc.currentWorker(),
		extranonce2:      "00000000",
		extranonce2Large: []byte{0, 0, 0, 0},
		ntime:            "6553f100",
		ntimeVal:         0x6553f100,
		nonce:            "00000000",
		nonceVal:         0x00000000,
		versionHex:       "00000001",
		useVersion:       1,
		scriptTime:       job.ScriptTime + 1,
		// Simulate a policy rejection (e.g. strict ntime/version rules) that
		// should not prevent submitting a real block.
		policyReject: sharePolicyReject{reason: rejectInvalidNTime, errCode: stratumErrCodeInvalidRequest, errMsg: "invalid ntime"},
		receivedAt:   time.Unix(1700000000, 0),
	}

	mc.conn = nopConn{}
	mc.processSubmissionTask(task)
	flushFoundBlockLog(t)

	rpc := mc.rpc.(*countingSubmitRPC)
	if got := rpc.submitCalls.Load(); got != 1 {
		t.Fatalf("expected submitblock to be called once, got %d", got)
	}
}

func benchmarkSubmitJobForTest(t *testing.T) *Job {
	t.Helper()
	// Reuse the benchmark job shape but without testing.B dependency.
	job := &Job{
		JobID:                   "test-submit-job",
		Template:                GetBlockTemplateResult{Height: 101, CurTime: 1700000000, Mintime: 1700000000, Bits: "1d00ffff", Previous: "0000000000000000000000000000000000000000000000000000000000000000", CoinbaseValue: 50 * 1e8, Version: 1},
		Target:                  new(big.Int),
		Extranonce2Size:         4,
		TemplateExtraNonce2Size: 8,
		PayoutScript:            []byte{0x51},
		WitnessCommitment:       "",
		CoinbaseMsg:             "goPool-test",
		ScriptTime:              0,
		MerkleBranches:          nil,
		Transactions:            nil,
		CoinbaseValue:           50 * 1e8,
		PrevHash:                "0000000000000000000000000000000000000000000000000000000000000000",
	}

	var prevBytes [32]byte
	if n, err := hex.Decode(prevBytes[:], []byte(job.Template.Previous)); err != nil || n != 32 {
		t.Fatalf("decode prevhash: %v", err)
	}
	job.prevHashBytes = prevBytes

	var bitsBytes [4]byte
	if n, err := hex.Decode(bitsBytes[:], []byte(job.Template.Bits)); err != nil || n != 4 {
		t.Fatalf("decode bits: %v", err)
	}
	job.bitsBytes = bitsBytes

	return job
}
