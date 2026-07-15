package main

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"
)

func installSaturatedSubmissionPool(t *testing.T) *submissionWorkerPool {
	t.Helper()
	ensureSubmissionWorkerPool()
	old := submissionWorkers
	pool := &submissionWorkerPool{tasks: make(chan preparedSubmissionTask, 1)}
	pool.tasks <- preparedSubmissionTask{}
	submissionWorkers = pool
	t.Cleanup(func() { submissionWorkers = old })
	return pool
}

func newQueuePriorityMiner(t *testing.T) (*MinerConn, *Job, *recordConn, *countingSubmitRPC) {
	t.Helper()
	mc, job := newSubmitReadyMinerConnForModesTest(t)
	conn := &recordConn{}
	rpc := &countingSubmitRPC{}
	mc.conn = conn
	mc.rpc = rpc
	mc.cfg.DataDir = t.TempDir()
	mc.cfg.SubmitProcessInline = false
	mc.cfg.ShareCheckDuplicate = false
	return mc, job, conn, rpc
}

func makeQueuePriorityJobABlock(job *Job) {
	job.Target = new(big.Int).Set(maxUint256)
	job.targetBE = uint256BEFromBigInt(job.Target)
}

func TestSaturatedSubmissionQueueBlockBypassesOrdinaryFIFO(t *testing.T) {
	pool := installSaturatedSubmissionPool(t)
	mc, job, conn, rpc := newQueuePriorityMiner(t)
	makeQueuePriorityJobABlock(job)

	mc.handleSubmit(testSubmitRequestForJob(job, mc.currentWorker()))
	flushFoundBlockLog(t)

	if got := rpc.submitCalls.Load(); got != 1 {
		t.Fatalf("submitblock calls = %d, want 1", got)
	}
	if got := len(pool.tasks); got != 1 {
		t.Fatalf("ordinary queue depth = %d, want saturated sentinel to remain", got)
	}
	if out := conn.String(); !strings.Contains(out, `"result":true`) {
		t.Fatalf("winning submission response = %q, want success", out)
	}
}

func TestSaturatedQueueShedsNonBlockThenAcceptsLaterBlock(t *testing.T) {
	pool := installSaturatedSubmissionPool(t)
	mc, job, conn, rpc := newQueuePriorityMiner(t)

	first := testSubmitRequestForJob(job, mc.currentWorker())
	mc.handleSubmit(first)
	if out := conn.String(); !strings.Contains(out, "server busy") {
		t.Fatalf("saturated non-block response = %q, want server busy", out)
	}
	if got := rpc.submitCalls.Load(); got != 0 {
		t.Fatalf("submitblock calls after non-block = %d, want 0", got)
	}

	makeQueuePriorityJobABlock(job)
	second := testSubmitRequestForJob(job, mc.currentWorker())
	second.ID = 2
	second.Params[4] = "00000002"
	mc.handleSubmit(second)
	flushFoundBlockLog(t)

	if got := rpc.submitCalls.Load(); got != 1 {
		t.Fatalf("submitblock calls after later block = %d, want 1", got)
	}
	if got := len(pool.tasks); got != 1 {
		t.Fatalf("ordinary queue depth = %d, want saturated sentinel to remain", got)
	}
}

func TestSaturatedQueueRescueVersionBlockBypassesOrdinaryFIFO(t *testing.T) {
	const (
		baseVersion   = uint32(0x20006000)
		submittedBits = uint32(0x00004000)
		currentMask   = uint32(0x00002000)
		currentBIP310 = uint32(0x20004000)
		rawFull       = submittedBits
		legacyXOR     = uint32(0x20002000)
	)

	pool := installSaturatedSubmissionPool(t)
	mc, conn := minerConnForNotifyTest(t)
	mc.cfg.DataDir = t.TempDir()
	mc.cfg.SubmitProcessInline = false
	mc.cfg.ShareCheckDuplicate = false
	mc.cfg.ShareCheckParamFormat = true
	mc.cfg.ShareCheckVersionRolling = true
	mc.cfg.ShareAllowOutOfMaskVersionBits = false
	mc.cfg.ShareRequireAuthorizedConnection = true
	mc.cfg.ShareRequireWorkerMatch = true
	mc.cfg.ShareJobFreshnessMode = shareJobFreshnessJobID
	mc.cfg.ShareNTimeMaxForwardSeconds = 600
	rpc := &countingSubmitRPC{}
	mc.rpc = rpc
	mc.versionMu.Lock()
	mc.versionRoll = true
	mc.minerMask = currentMask
	mc.minVerBits = 1
	mc.versionMu.Unlock()

	job := benchmarkSubmitJobForTest(t)
	job.Generation = 1
	job.ScriptTime = job.Template.CurTime
	job.Template.Version = int32(baseVersion)
	job.VersionMask = currentMask
	mc.sendNotifyFor(job, true)
	ids := notifyJobIDsFromOutput(t, conn.String())
	if len(ids) != 1 {
		t.Fatalf("notify IDs = %#v, want 1", ids)
	}
	_, _, _, _, _, _, binding, bindingOK, jobOK := mc.jobForIDWithLast(ids[0])
	if !jobOK || !bindingOK {
		t.Fatalf("notified job binding missing: jobOK=%v bindingOK=%v", jobOK, bindingOK)
	}

	extranonce2 := []byte{0, 0, 0, 0}
	_, coinbaseTxID, err := buildNotifiedCoinbaseTx(binding, extranonce2)
	if err != nil {
		t.Fatalf("build notified coinbase: %v", err)
	}
	merkleRoot, ok := computeMerkleRootFromBranches32(coinbaseTxID, job.MerkleBranches)
	if !ok {
		t.Fatal("compute notified merkle root")
	}
	nonce, target, expectedHeader := findUniqueMinimumVersionHeader(
		t,
		job,
		merkleRoot,
		[]uint32{currentBIP310, rawFull, legacyXOR},
		legacyXOR,
	)
	job.Target = target
	job.targetBE = uint256BEFromBigInt(target)

	req := &StratumRequest{
		ID:     1,
		Method: "mining.submit",
		Params: []any{
			mc.currentWorker(),
			ids[0],
			"00000000",
			fmt.Sprintf("%08x", uint32(job.Template.CurTime)),
			fmt.Sprintf("%08x", nonce),
			fmt.Sprintf("%08x", submittedBits),
		},
	}
	mc.handleSubmit(req)
	flushFoundBlockLog(t)

	if got := rpc.submitCalls.Load(); got != 1 {
		t.Fatalf("submitblock calls = %d, want 1", got)
	}
	if got := len(pool.tasks); got != 1 {
		t.Fatalf("ordinary queue depth = %d, want saturated sentinel to remain", got)
	}
	if !strings.HasPrefix(rpc.blockHex, hex.EncodeToString(expectedHeader)) {
		t.Fatalf("submitted block does not use rescued version %08x", legacyXOR)
	}
}

func TestSaturatedQueueBusyNonBlockDoesNotAddBanStrike(t *testing.T) {
	installSaturatedSubmissionPool(t)
	mc, job, conn, _ := newQueuePriorityMiner(t)
	now := time.Now()
	mc.stateMu.Lock()
	mc.invalidSubs = 7
	mc.validSubsForBan = 3
	mc.lastPenalty = now
	mc.stateMu.Unlock()

	mc.handleSubmit(testSubmitRequestForJob(job, mc.currentWorker()))
	if out := conn.String(); !strings.Contains(out, "server busy") {
		t.Fatalf("saturated non-block response = %q, want server busy", out)
	}

	mc.stateMu.Lock()
	invalid := mc.invalidSubs
	valid := mc.validSubsForBan
	banUntil := mc.banUntil
	banReason := mc.banReason
	mc.stateMu.Unlock()
	if invalid != 7 || valid != 3 {
		t.Fatalf("ban counters changed on overload: invalid=%d valid=%d, want 7/3", invalid, valid)
	}
	if !banUntil.IsZero() || banReason != "" {
		t.Fatalf("overload created ban: until=%v reason=%q", banUntil, banReason)
	}
}
