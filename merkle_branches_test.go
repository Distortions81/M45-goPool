package main

import (
	"bytes"
	"math/big"
	"testing"
	"time"
)

func TestMerkleRootDecodedBranchesMatchesStringBranches(t *testing.T) {
	coinbaseHash := bytes.Repeat([]byte{0xaa}, 32)
	branches := []string{
		"1111111111111111111111111111111111111111111111111111111111111111",
		"2222222222222222222222222222222222222222222222222222222222222222",
		"3333333333333333333333333333333333333333333333333333333333333333",
	}

	decoded, err := decodeMerkleBranchesBytes(branches)
	if err != nil {
		t.Fatalf("decodeMerkleBranchesBytes: %v", err)
	}

	rootStr, ok := computeMerkleRootFromBranches32(coinbaseHash, branches)
	if !ok {
		t.Fatalf("computeMerkleRootFromBranches32 failed")
	}
	rootBytes, ok := computeMerkleRootFromBranchesBytes32(coinbaseHash, decoded)
	if !ok {
		t.Fatalf("computeMerkleRootFromBranchesBytes32 failed")
	}
	if rootStr != rootBytes {
		t.Fatalf("merkle roots differ: string=%x bytes=%x", rootStr, rootBytes)
	}

	legacy := computeMerkleRootFromBranches(coinbaseHash, branches)
	if legacy == nil || len(legacy) != 32 {
		t.Fatalf("computeMerkleRootFromBranches returned invalid root")
	}
	if !bytes.Equal(legacy, rootBytes[:]) {
		t.Fatalf("legacy merkle root differs: legacy=%x bytes=%x", legacy, rootBytes)
	}
}

func TestPrepareShareContextMerkleBytesEquivalent(t *testing.T) {
	metrics := NewPoolMetrics()
	mc := benchmarkMinerConnForSubmit(metrics)
	mc.cfg.ShareCheckDuplicate = false

	job := benchmarkSubmitJobForTest(t)
	job.Target = new(big.Int).Set(maxUint256)
	job.targetBE = uint256BEFromBigInt(job.Target)
	job.ScriptTime = job.Template.CurTime

	job.MerkleBranches = []string{
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}

	decoded, err := decodeMerkleBranchesBytes(job.MerkleBranches)
	if err != nil {
		t.Fatalf("decode merkle branches: %v", err)
	}

	jobBytes := *job
	jobBytes.merkleBranchesBytes = decoded
	jobStr := *job
	jobStr.merkleBranchesBytes = nil

	now := time.Unix(1700000000, 0)
	task := submissionTask{
		mc:               mc,
		reqID:            1,
		jobID:            job.JobID,
		workerName:       mc.currentWorker(),
		extranonce2:      "00000000",
		extranonce2Large: []byte{0, 0, 0, 0},
		ntime:            "6553f100",
		ntimeVal:         0x6553f100,
		nonce:            "00000000",
		nonceVal:         0,
		useVersion:       1,
		scriptTime:       job.ScriptTime,
		receivedAt:       now,
	}

	task.job = &jobStr
	ctxStr, ok := mc.prepareShareContext(task)
	if !ok {
		t.Fatalf("prepareShareContext (string branches) rejected share")
	}

	task.job = &jobBytes
	ctxBytes, ok := mc.prepareShareContext(task)
	if !ok {
		t.Fatalf("prepareShareContext (decoded branches) rejected share")
	}

	if ctxStr.hashHex != ctxBytes.hashHex || ctxStr.isBlock != ctxBytes.isBlock {
		t.Fatalf("share context differs: string=%+v bytes=%+v", ctxStr, ctxBytes)
	}
}
