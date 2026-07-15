package main

import (
	"bytes"
	"math/big"
	"strings"
	"testing"
	"time"
)

func TestRetiredJobBindingIsExactAndBounded(t *testing.T) {
	const notifiedMask = uint32(0x0000e000)

	mc, conn := minerConnForNotifyTest(t)
	mc.maxRecentJobs = 2
	mc.cfg.ShareCheckNTimeWindow = true
	mc.cfg.ShareNTimeMaxForwardSeconds = 600
	mc.jobNTimeBounds = make(map[string]jobNTimeBounds, mc.maxRecentJobs)
	mc.cfg.ShareCheckDuplicate = true
	mc.shareCache = make(map[string]*duplicateShareSet, mc.maxRecentJobs)
	mc.evictedShareCache = make(map[string]*evictedCacheEntry, mc.maxRecentJobs)
	mc.versionMu.Lock()
	mc.versionRoll = true
	mc.minerMask = notifiedMask
	mc.minVerBits = 1
	mc.versionMu.Unlock()

	job := benchmarkSubmitJobForTest(t)
	job.Generation = 1
	job.ScriptTime = job.Template.CurTime
	job.VersionMask = notifiedMask
	mc.sendNotifyFor(job, true)
	ids := notifyJobIDsFromOutput(t, conn.String())
	if len(ids) != 1 {
		t.Fatalf("notify IDs = %#v, want one", ids)
	}
	firstID := ids[0]
	mc.jobMu.Lock()
	firstCoinbase := mc.jobNotifyCoinbase[firstID]
	firstPrefix := append([]byte(nil), firstCoinbase.prefix...)
	firstSuffix := append([]byte(nil), firstCoinbase.suffix...)
	firstScriptTime := mc.jobScriptTime[firstID]
	mc.shareCache[firstID] = &duplicateShareSet{}
	mc.jobMu.Unlock()

	// The third active job retires the first when maxRecentJobs is two.
	mc.sendNotifyFor(job, false)
	mc.sendNotifyFor(job, false)
	ids = notifyJobIDsFromOutput(t, conn.String())
	if len(ids) != 3 {
		t.Fatalf("notify IDs = %#v, want three", ids)
	}

	mc.jobMu.Lock()
	retired, retiredOK := mc.retiredJobs[firstID]
	_, activeOK := mc.activeJobs[firstID]
	_, scriptOK := mc.jobScriptTime[firstID]
	_, coinbaseOK := mc.jobNotifyCoinbase[firstID]
	_, ntimeOK := mc.jobNTimeBounds[firstID]
	_, difficultyOK := mc.jobDifficulty[firstID]
	_, duplicateOK := mc.shareCache[firstID]
	mc.jobMu.Unlock()
	if activeOK || scriptOK || coinbaseOK || ntimeOK || difficultyOK || duplicateOK {
		t.Fatalf("retired job kept active policy state: active=%v script=%v coinbase=%v ntime=%v difficulty=%v duplicate=%v",
			activeOK, scriptOK, coinbaseOK, ntimeOK, difficultyOK, duplicateOK)
	}
	if !retiredOK || retired.job != job {
		t.Fatalf("retired job = (%v, %p), want (%v, %p)", retiredOK, retired.job, true, job)
	}
	if retired.worker != firstCoinbase.worker || retired.scriptTime != firstScriptTime ||
		!retired.versionRollingActive || retired.versionMask != notifiedMask ||
		!bytes.Equal(retired.prefix, firstPrefix) || !bytes.Equal(retired.suffix, firstSuffix) {
		t.Fatalf("retired binding did not preserve exact notify metadata: %+v", retired)
	}

	// Six notifies leave two active and two retired IDs. Retirement is FIFO and
	// uses the same immutable per-session cap as active history.
	mc.sendNotifyFor(job, false)
	mc.sendNotifyFor(job, false)
	mc.sendNotifyFor(job, false)
	ids = notifyJobIDsFromOutput(t, conn.String())
	if len(ids) != 6 {
		t.Fatalf("notify IDs = %#v, want six", ids)
	}
	mc.jobMu.Lock()
	retiredOrder := append([]string(nil), mc.retiredJobOrder...)
	activeLen := len(mc.activeJobs)
	retiredLen := len(mc.retiredJobs)
	_, firstRetained := mc.retiredJobs[ids[0]]
	_, secondRetained := mc.retiredJobs[ids[1]]
	_, thirdRetained := mc.retiredJobs[ids[2]]
	_, fourthRetained := mc.retiredJobs[ids[3]]
	mc.jobMu.Unlock()
	if activeLen != 2 || retiredLen != 2 {
		t.Fatalf("history sizes active=%d retired=%d, want 2/2", activeLen, retiredLen)
	}
	if firstRetained || secondRetained || !thirdRetained || !fourthRetained ||
		len(retiredOrder) != 2 || retiredOrder[0] != ids[2] || retiredOrder[1] != ids[3] {
		t.Fatalf("retired FIFO = %#v for IDs %#v", retiredOrder, ids)
	}

	t.Run("incomplete binding is not retired", func(t *testing.T) {
		bare := &MinerConn{
			activeJobs:        make(map[string]*Job, 1),
			jobScriptTime:     make(map[string]int64, 1),
			jobNotifyCoinbase: make(map[string]notifiedCoinbaseParts, 1),
			maxRecentJobs:     1,
		}
		bare.trackJob(job, "incomplete-a", true)
		bare.trackJob(job, "incomplete-b", true)
		if len(bare.retiredJobs) != 0 || len(bare.retiredJobOrder) != 0 {
			t.Fatalf("incomplete advertised binding was retired: jobs=%d order=%d", len(bare.retiredJobs), len(bare.retiredJobOrder))
		}
	})

	t.Run("reused ID cannot resolve older retired work", func(t *testing.T) {
		bare := &MinerConn{
			activeJobs:        make(map[string]*Job, 1),
			jobScriptTime:     make(map[string]int64, 1),
			jobNotifyCoinbase: make(map[string]notifiedCoinbaseParts, 1),
			maxRecentJobs:     1,
		}
		bind := func(id string, boundJob *Job, marker byte) {
			bare.jobNotifyCoinbase[id] = notifiedCoinbaseParts{
				prefix: []byte{marker},
				suffix: []byte{marker + 1},
			}
			bare.trackJob(boundJob, id, true)
		}
		first := *job
		first.JobID = "first"
		evictor := *job
		evictor.JobID = "evictor"
		replacement := *job
		replacement.JobID = "replacement"
		bind("reused", &first, 1)
		bind("evictor", &evictor, 3)
		if lookup := bare.jobForSubmissionWithLast("reused"); !lookup.retired || lookup.job != &first {
			t.Fatalf("initial retired lookup = %+v", lookup)
		}
		bind("reused", &replacement, 5)
		lookup := bare.jobForSubmissionWithLast("reused")
		if !lookup.found || lookup.retired || lookup.job != &replacement ||
			!bytes.Equal(lookup.coinbase.prefix, []byte{5}) {
			t.Fatalf("reused lookup resolved old work: %+v", lookup)
		}
	})
}

func TestRetiredNonBlockIsAlwaysStale(t *testing.T) {
	tests := []struct {
		name string
		mode int
	}{
		{name: "freshness off", mode: shareJobFreshnessOff},
		{name: "job id", mode: shareJobFreshnessJobID},
		{name: "job id and prevhash", mode: shareJobFreshnessJobIDPrev},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mc, notifyConn := minerConnForNotifyTest(t)
			mc.maxRecentJobs = 1
			mc.cfg.ShareJobFreshnessMode = tc.mode
			mc.cfg.ShareCheckParamFormat = true
			mc.cfg.ShareCheckDuplicate = false
			mc.cfg.ShareRequireAuthorizedConnection = true
			mc.cfg.ShareRequireWorkerMatch = true
			rpc := &countingSubmitRPC{}
			mc.rpc = rpc

			old := benchmarkSubmitJobForTest(t)
			old.Generation = 1
			old.Target = new(big.Int)
			old.targetBE = [32]byte{}
			mc.sendNotifyFor(old, true)
			reorg := *old
			reorg.JobID = "retired-nonblock-reorg"
			reorg.Generation = 2
			reorg.Template.Previous = strings.Repeat("33", 32)
			reorg.PrevHash = reorg.Template.Previous
			reorg.Template.Height--
			mc.sendNotifyFor(&reorg, true)

			ids := notifyJobIDsFromOutput(t, notifyConn.String())
			if len(ids) != 2 {
				t.Fatalf("notify IDs = %#v, want two", ids)
			}
			lookup := mc.jobForSubmissionWithLast(ids[0])
			if !lookup.found || !lookup.retired || !lookup.coinbaseOK {
				t.Fatalf("retired lookup = found:%v retired:%v coinbase:%v", lookup.found, lookup.retired, lookup.coinbaseOK)
			}

			responseConn := &recordConn{}
			mc.conn = responseConn
			task, ok := mc.prepareSubmissionTask(&StratumRequest{
				ID:     1,
				Method: "mining.submit",
				Params: []any{mc.currentWorker(), ids[0], "00000000", uint32ToBEHex(uint32(old.Template.CurTime)), "00000000"},
			}, time.Unix(old.Template.CurTime, 0))
			if !ok {
				t.Fatal("retired non-block was rejected before PoW evaluation")
			}
			if task.policyReject.reason != rejectStaleJob {
				t.Fatalf("retired policy = %v, want stale", task.policyReject.reason)
			}
			acceptedBefore := mc.snapshotStats().Accepted
			mc.processSubmissionTask(task)
			if got := mc.snapshotStats().Accepted; got != acceptedBefore {
				t.Fatalf("accepted shares changed from %d to %d", acceptedBefore, got)
			}
			if got := rpc.submitCalls.Load(); got != 0 {
				t.Fatalf("submitblock calls = %d, want zero", got)
			}
			if out := responseConn.String(); !strings.Contains(out, "job not found") || strings.Contains(out, `"result":true`) {
				t.Fatalf("retired non-block response = %q, want stale rejection", out)
			}
		})
	}
}

func TestPreparedRetiredBlockSurvivesPruningAndIsSessionLocal(t *testing.T) {
	mc, notifyConn := minerConnForNotifyTest(t)
	mc.maxRecentJobs = 1
	mc.cfg.DataDir = t.TempDir()
	setupTestStateDB(t, mc.cfg.DataDir)
	mc.cfg.ShareCheckParamFormat = true
	mc.cfg.ShareJobFreshnessMode = shareJobFreshnessJobIDPrev
	mc.cfg.ShareRequireAuthorizedConnection = true
	mc.cfg.ShareRequireWorkerMatch = true
	rpc := &countingSubmitRPC{}
	mc.rpc = rpc

	old := benchmarkSubmitJobForTest(t)
	old.Generation = 1
	old.Target = new(big.Int).Set(maxUint256)
	old.targetBE = uint256BEFromBigInt(old.Target)
	mc.sendNotifyFor(old, true)
	next := *old
	next.JobID = "snapshot-next"
	next.Generation = 2
	next.Template.Previous = strings.Repeat("44", 32)
	next.PrevHash = next.Template.Previous
	next.Template.Height--
	next.Target = new(big.Int)
	next.targetBE = [32]byte{}
	mc.sendNotifyFor(&next, true)

	ids := notifyJobIDsFromOutput(t, notifyConn.String())
	if len(ids) != 2 {
		t.Fatalf("notify IDs = %#v, want two", ids)
	}
	oldID := ids[0]
	task, ok := mc.prepareSubmissionTask(&StratumRequest{
		ID:     1,
		Method: "mining.submit",
		Params: []any{mc.currentWorker(), oldID, "00000000", uint32ToBEHex(uint32(old.Template.CurTime)), "00000000"},
	}, time.Unix(old.Template.CurTime, 0))
	if !ok || task.policyReject.reason != rejectStaleJob || !task.hasNotifiedCoinbase {
		t.Fatalf("prepared retired task = ok:%v policy:%v coinbase:%v", ok, task.policyReject.reason, task.hasNotifiedCoinbase)
	}

	other, _ := minerConnForNotifyTest(t)
	if lookup := other.jobForSubmissionWithLast(oldID); lookup.found || lookup.retired {
		t.Fatalf("retired job leaked to another connection: %+v", lookup)
	}

	latest := next
	latest.JobID = "snapshot-latest"
	latest.Generation = 3
	latest.Template.Previous = strings.Repeat("55", 32)
	latest.PrevHash = latest.Template.Previous
	mc.sendNotifyFor(&latest, true)
	mc.jobMu.Lock()
	_, stillRetired := mc.retiredJobs[oldID]
	mc.jobMu.Unlock()
	if stillRetired {
		t.Fatal("old retired binding was not pruned at the configured cap")
	}

	// The queued task owns an immutable snapshot of the exact job and coinbase,
	// so pruning the connection lookup must not affect block reconstruction.
	ctx, ok := mc.prepareShareContext(task)
	if !ok || !ctx.isBlock {
		t.Fatalf("prepared task context = ok:%v block:%v", ok, ctx.isBlock)
	}
	expectedBlock, err := assembleSolvedBlock(task.job, ctx.header, ctx.cbTx)
	if err != nil {
		t.Fatalf("assemble expected block: %v", err)
	}
	mc.processShare(task, ctx)
	flushFoundBlockLog(t)
	if got := rpc.submitCalls.Load(); got != 1 {
		t.Fatalf("submitblock calls = %d, want one", got)
	}
	if rpc.blockHex != expectedBlock {
		t.Fatal("submitted block changed after retired binding pruning")
	}
}
