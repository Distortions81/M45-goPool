package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcutil"
)

type countingSubmitRPC struct {
	submitCalls atomic.Int64
	blockHex    string
}

func (c *countingSubmitRPC) call(method string, params any, out any) error {
	return c.callCtx(context.Background(), method, params, out)
}

func (c *countingSubmitRPC) callCtx(_ context.Context, method string, params any, out any) error {
	if method == "submitblock" {
		c.submitCalls.Add(1)
		if p, ok := params.([]any); ok && len(p) > 0 {
			if blockHex, ok := p[0].(string); ok {
				c.blockHex = blockHex
			}
		}
	}
	return nil
}

type rejectingSubmitRPC struct {
	submitCalls atomic.Int64
	result      any
}

func (r *rejectingSubmitRPC) callCtx(_ context.Context, method string, _ any, out any) error {
	switch method {
	case "submitblock":
		r.submitCalls.Add(1)
		if dst, ok := out.(*any); ok {
			*dst = r.result
		}
		return nil
	case "getblockheader":
		return errors.New("block not found")
	default:
		return nil
	}
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

func TestSubmitBlockResultStringIsRejection(t *testing.T) {
	rpc := &rejectingSubmitRPC{result: "bad-cb-amount"}
	mc := &MinerConn{
		id:  "submitblock-result-test",
		rpc: rpc,
	}

	var submitRes any
	err := mc.submitBlockWithFastRetry(&Job{Template: GetBlockTemplateResult{Height: 1}}, "worker", strings.Repeat("0", 64), "deadbeef", &submitRes)
	if err == nil {
		t.Fatalf("expected submitblock BIP22 result string to be treated as rejection")
	}
	if !strings.Contains(err.Error(), "bad-cb-amount") {
		t.Fatalf("expected rejection reason in error, got %v", err)
	}
	if got := rpc.submitCalls.Load(); got != 1 {
		t.Fatalf("expected no retry for validation rejection, got %d calls", got)
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
			mc.extranonce1,
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
			mc.extranonce1,
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
	// Simulate a clean re-notify for the same underlying template after the
	// share was parsed. Block submission must still use task.scriptTime.
	mc.jobScriptTime[jobID] = job.ScriptTime

	mc.conn = nopConn{}
	mc.processSubmissionTask(task)
	flushFoundBlockLog(t)

	rpc := mc.rpc.(*countingSubmitRPC)
	if got := rpc.submitCalls.Load(); got != 1 {
		t.Fatalf("expected submitblock to be called once, got %d", got)
	}
	expectedBlockHex, _, _, _, err := buildBlockWithScriptTime(job, mc.extranonce1, ex2, ntimeHex, chosenNonce, int32(useVersion), payoutScript, notifiedScriptTime)
	if err != nil {
		t.Fatalf("build expected notified block: %v", err)
	}
	fallbackBlockHex, _, _, _, err := buildBlockWithScriptTime(job, mc.extranonce1, ex2, ntimeHex, chosenNonce, int32(useVersion), payoutScript, job.ScriptTime)
	if err != nil {
		t.Fatalf("build fallback block: %v", err)
	}
	if rpc.blockHex != expectedBlockHex {
		t.Fatalf("submitted block did not use notified scriptTime")
	}
	if rpc.blockHex == fallbackBlockHex {
		t.Fatalf("submitted block used fallback scriptTime")
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
		policyReject: submitPolicyReject{reason: rejectInvalidNTime, errCode: stratumErrCodeInvalidRequest, errMsg: "invalid ntime"},
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

func TestBannedMinerBlockBypassesBanPolicy(t *testing.T) {
	mc, job := newSubmitReadyMinerConnForModesTest(t)
	mc.cfg.DataDir = t.TempDir()
	mc.cfg.ShareCheckDuplicate = false
	rpc := &countingSubmitRPC{}
	mc.rpc = rpc
	job.Target = new(big.Int).Set(maxUint256)
	wallet, payoutScript := generateTestWallet(t)
	mc.setWorkerWallet(mc.currentWorker(), wallet, payoutScript)
	if script := mc.singlePayoutScript(job, mc.currentWorker()); len(script) == 0 {
		t.Fatalf("missing payout script for current worker")
	}

	now := time.Unix(job.Template.CurTime, 0)
	mc.stateMu.Lock()
	mc.banUntil = now.Add(time.Hour)
	mc.banReason = "test ban"
	mc.invalidSubs = 3
	mc.stateMu.Unlock()

	conn := &recordConn{}
	mc.conn = conn
	task, ok := mc.prepareSubmissionTask(testSubmitRequestForJob(job, mc.currentWorker()), now)
	if !ok {
		t.Fatalf("banned miner's advertised work was rejected before PoW")
	}
	if task.banPolicy == nil {
		t.Fatalf("submission task did not preserve receipt-time ban state")
	}
	if out := conn.String(); out != "" {
		t.Fatalf("banned block candidate was rejected before PoW: %q", out)
	}

	mc.processSubmissionTask(task)
	flushFoundBlockLog(t)

	if got := rpc.submitCalls.Load(); got != 1 {
		t.Fatalf("submitblock calls = %d, want 1", got)
	}
	mc.metrics.mu.Lock()
	shareErrors := mc.metrics.shareErrorCount
	mc.metrics.mu.Unlock()
	if shareErrors != 0 {
		t.Fatalf("found block recorded %d banned submit errors, want 0", shareErrors)
	}
}

func TestBannedMinerNonBlockRejectedWithoutPenalty(t *testing.T) {
	mc, job := newSubmitReadyMinerConnForModesTest(t)
	mc.cfg.ShareCheckDuplicate = false
	rpc := &countingSubmitRPC{}
	mc.rpc = rpc
	job.Target = new(big.Int)
	wallet, payoutScript := generateTestWallet(t)
	mc.setWorkerWallet(mc.currentWorker(), wallet, payoutScript)
	if script := mc.singlePayoutScript(job, mc.currentWorker()); len(script) == 0 {
		t.Fatalf("missing payout script for current worker")
	}

	now := time.Unix(job.Template.CurTime, 0)
	const initialInvalidSubs = 3
	const initialValidSubs = 7
	mc.stateMu.Lock()
	mc.banUntil = now.Add(time.Hour)
	mc.banReason = "test ban"
	mc.invalidSubs = initialInvalidSubs
	mc.validSubsForBan = initialValidSubs
	mc.stateMu.Unlock()

	conn := &recordConn{}
	mc.conn = conn
	task, ok := mc.prepareSubmissionTask(testSubmitRequestForJob(job, mc.currentWorker()), now)
	if !ok {
		t.Fatalf("banned miner's advertised work was rejected before PoW")
	}
	mc.processSubmissionTask(task)

	if got := rpc.submitCalls.Load(); got != 0 {
		t.Fatalf("submitblock calls = %d, want 0", got)
	}
	if out := conn.String(); !strings.Contains(out, "banned") {
		t.Fatalf("expected banned response, got %q", out)
	}
	mc.stateMu.Lock()
	invalidSubs := mc.invalidSubs
	validSubs := mc.validSubsForBan
	mc.stateMu.Unlock()
	if invalidSubs != initialInvalidSubs {
		t.Fatalf("invalid submission count = %d, want %d", invalidSubs, initialInvalidSubs)
	}
	if validSubs != initialValidSubs {
		t.Fatalf("valid submission count = %d, want %d", validSubs, initialValidSubs)
	}
	mc.metrics.mu.Lock()
	shareErrors := mc.metrics.shareErrorCount
	mc.metrics.mu.Unlock()
	if shareErrors != 1 {
		t.Fatalf("banned non-block recorded %d submit errors, want 1", shareErrors)
	}
}

func TestSubmitBlockMatchesNotifyPayload(t *testing.T) {
	mc, notifyConn := minerConnForNotifyTest(t)
	mc.maxRecentJobs = 1
	mc.cfg.DataDir = t.TempDir()
	setupTestStateDB(t, mc.cfg.DataDir)
	mc.cfg.SubmitProcessInline = true
	mc.cfg.ShareRequireAuthorizedConnection = true
	mc.cfg.ShareRequireWorkerMatch = true
	mc.cfg.ShareJobFreshnessMode = shareJobFreshnessJobID
	mc.cfg.ShareCheckParamFormat = true
	rpc := &countingSubmitRPC{}
	mc.rpc = rpc
	workerA := mc.currentWorker()
	walletA, _, ok := mc.workerWalletData(workerA)
	if !ok {
		t.Fatalf("missing wallet A data")
	}

	job := benchmarkSubmitJobForTest(t)
	job.Generation = 1
	job.Target = new(big.Int).Set(maxUint256)
	const rawTxHex = "0100000001" +
		"0000000000000000000000000000000000000000000000000000000000000000" +
		"ffffffff00ffffffff0101000000000000000000000000"
	rawTx, err := hex.DecodeString(rawTxHex)
	if err != nil {
		t.Fatalf("decode raw tx: %v", err)
	}
	parsedTx, err := btcutil.NewTxFromBytes(rawTx)
	if err != nil {
		t.Fatalf("parse raw tx: %v", err)
	}
	job.MerkleBranches = buildMerkleBranches([]*btcutil.Tx{parsedTx})
	job.Transactions = []GBTTransaction{{Data: rawTxHex, Txid: parsedTx.Hash().String()}}

	mc.sendNotifyFor(job, true)
	notifies := notifyMessagesFromOutput(t, notifyConn.String())
	if len(notifies) != 1 {
		t.Fatalf("expected one notify, got %d", len(notifies))
	}
	params := notifies[0].Params
	if len(params) < 9 {
		t.Fatalf("notify params too short: %#v", params)
	}
	stratumJobID := params[0].(string)
	prevhashLE := params[1].(string)
	coinb1 := params[2].(string)
	coinb2 := params[3].(string)
	branches := params[4].([]any)
	versionHex := params[5].(string)
	bitsHex := params[6].(string)
	ntimeHex := params[7].(string)
	if len(branches) != len(job.MerkleBranches) || branches[0] != job.MerkleBranches[0] {
		t.Fatalf("notify merkle branches got %#v want %#v", branches, job.MerkleBranches)
	}
	if prevhashLE != hexToLEHex(job.PrevHash) {
		t.Fatalf("notify prevhash got %q want %q", prevhashLE, hexToLEHex(job.PrevHash))
	}
	if bitsHex != job.Template.Bits {
		t.Fatalf("notify bits got %q want %q", bitsHex, job.Template.Bits)
	}
	version, err := parseUint32BEHex(versionHex)
	if err != nil {
		t.Fatalf("parse notify version: %v", err)
	}

	en2Hex := "00000000"
	coinbaseHex := coinb1 + hex.EncodeToString(mc.extranonce1) + en2Hex + coinb2
	coinbaseBytes, err := hex.DecodeString(coinbaseHex)
	if err != nil {
		t.Fatalf("decode notify coinbase: %v", err)
	}
	coinbaseTxID := doubleSHA256(coinbaseBytes)
	merkleRoot := computeMerkleRootFromBranches(coinbaseTxID, job.MerkleBranches)
	nonceHex := "00000000"
	expectedHeader, err := job.buildBlockHeader(merkleRoot, ntimeHex, nonceHex, int32(version))
	if err != nil {
		t.Fatalf("build expected header: %v", err)
	}

	// Reauthorize the connection to wallet B after wallet A's work was sent.
	// Also overwrite A's live cache entry so only the immutable advertised
	// coinbase can reproduce the solved block.
	workerB, walletB, scriptB := generateTestWorker(t)
	mc.setWorkerWallet(workerB, walletB, scriptB)
	mc.handleAuthorizeID(2, workerB, "")
	mc.setWorkerWallet(workerA, walletA, scriptB)

	// Publish a newer competing branch after reauthorization. With a one-job
	// active window this retires wallet A's exact advertised binding; a winning
	// block on that displaced branch must still be reconstructed from A's notify.
	reorg := *job
	reorg.JobID = "wallet-b-reorg"
	reorg.Generation = 2
	reorg.Template.Previous = strings.Repeat("22", 32)
	reorg.PrevHash = reorg.Template.Previous
	reorg.Template.Height--
	reorg.Target = new(big.Int)
	reorg.targetBE = [32]byte{}
	mc.sendNotifyFor(&reorg, true)
	mc.jobMu.Lock()
	_, oldActive := mc.activeJobs[stratumJobID]
	retired, oldRetired := mc.retiredJobs[stratumJobID]
	mc.jobMu.Unlock()
	if oldActive || !oldRetired || retired.job != job || retired.worker != workerA {
		t.Fatalf("old wallet binding state: active=%v retired=%v job_match=%v worker=%q",
			oldActive, oldRetired, retired.job == job, retired.worker)
	}

	mc.handleSubmit(&StratumRequest{
		ID:     1,
		Method: "mining.submit",
		// The label says B, but this retained job was advertised for A. Strict
		// worker policy must not suppress a network-target block, and payout and
		// attribution must remain bound to A.
		Params: []any{workerB, stratumJobID, en2Hex, ntimeHex, nonceHex},
	})
	flushFoundBlockLog(t)

	if got := rpc.submitCalls.Load(); got != 1 {
		t.Fatalf("expected submitblock to be called once, got %d", got)
	}
	blockBytes, err := hex.DecodeString(rpc.blockHex)
	if err != nil {
		t.Fatalf("decode submitted block: %v", err)
	}
	if len(blockBytes) <= 81 {
		t.Fatalf("submitted block too short: %d bytes", len(blockBytes))
	}
	if !bytes.Equal(blockBytes[:80], expectedHeader) {
		t.Fatalf("submitted block header does not match notify payload")
	}
	if blockBytes[80] != 2 {
		t.Fatalf("expected two transactions in submitted block, got varint byte %#x", blockBytes[80])
	}
	var expectedPayload bytes.Buffer
	expectedPayload.WriteByte(2)
	expectedPayload.Write(coinbaseBytes)
	expectedPayload.Write(rawTx)
	if !bytes.Equal(blockBytes[80:], expectedPayload.Bytes()) {
		t.Fatalf("submitted block payload does not match notify coinbase plus job transactions")
	}
	if got := mc.currentWorker(); got != workerB {
		t.Fatalf("late wallet A block changed current worker to %q, want wallet B %q", got, workerB)
	}
	found := readLastFoundBlockRecord(t, mc.cfg.DataDir)
	if got, _ := found["worker"].(string); got != workerA {
		t.Fatalf("found block worker = %q, want advertised wallet A %q", got, workerA)
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
