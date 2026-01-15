package main

import (
	"bufio"
	"context"
	"encoding/hex"
	"math/big"
	"sync/atomic"
	"testing"
	"time"
)

type countingSubmitRPC struct {
	submitCalls atomic.Int64
}

func (c *countingSubmitRPC) call(method string, params interface{}, out interface{}) error {
	return c.callCtx(context.Background(), method, params, out)
}

func (c *countingSubmitRPC) callCtx(_ context.Context, method string, params interface{}, out interface{}) error {
	if method == "submitblock" {
		c.submitCalls.Add(1)
	}
	return nil
}

func TestWinningBlockNotRejectedAsDuplicate(t *testing.T) {
	metrics := NewPoolMetrics()
	mc := benchmarkMinerConnForSubmit(metrics)
	mc.cfg.CheckDuplicateShares = true
	mc.cfg.DataDir = t.TempDir()
	mc.rpc = &countingSubmitRPC{}

	// Minimal job: make Target huge so the share is always treated as a block,
	// regardless of the computed difficulty.
	job := benchmarkSubmitJobForTest(t)
	job.Target = new(big.Int).Set(maxUint256)
	jobID := job.JobID
	mc.jobDifficulty[jobID] = 1e-12

	ntimeHex := "6553f100" // 1700000000
	task := submissionTask{
		mc:               mc,
		reqID:            1,
		job:              job,
		jobID:            jobID,
		workerName:       "worker1",
		extranonce2:      "00000000",
		extranonce2Bytes: []byte{0, 0, 0, 0},
		ntime:            ntimeHex,
		nonce:            "00000000",
		versionHex:       "00000001",
		useVersion:       1,
		receivedAt:       time.Unix(1700000000, 0),
	}

	// Seed the duplicate cache with the exact share key. If duplicate detection
	// were applied to winning blocks, this would cause an incorrect rejection.
	if dup := mc.isDuplicateShare(jobID, task.extranonce2, task.ntime, task.nonce, task.versionHex); dup {
		t.Fatalf("unexpected duplicate when seeding cache")
	}

	mc.conn = nopConn{}
	mc.writer = bufio.NewWriterSize(mc.conn, 256)
	mc.processSubmissionTask(task)

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
