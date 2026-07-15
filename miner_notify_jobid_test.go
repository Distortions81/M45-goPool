package main

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

type blockingNotifyConn struct {
	recordConn
	notifyStarted chan struct{}
	releaseNotify chan struct{}
	notifyOnce    sync.Once
}

func (c *blockingNotifyConn) Write(b []byte) (int, error) {
	if bytes.Contains(b, []byte(`"method":"mining.notify"`)) {
		c.notifyOnce.Do(func() { close(c.notifyStarted) })
		<-c.releaseNotify
	}
	return c.recordConn.Write(b)
}

func notifyJobIDsFromOutput(t *testing.T, out string) []string {
	t.Helper()
	msgs := notifyMessagesFromOutput(t, out)
	ids := make([]string, 0, len(msgs))
	for _, msg := range msgs {
		if len(msg.Params) == 0 {
			t.Fatalf("notify without params: %#v", msg)
		}
		id, ok := msg.Params[0].(string)
		if !ok || id == "" {
			t.Fatalf("notify job id is not a non-empty string: %#v", msg.Params[0])
		}
		ids = append(ids, id)
	}
	return ids
}

func notifyMessagesFromOutput(t *testing.T, out string) []StratumMessage {
	t.Helper()
	var msgs []StratumMessage
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var msg StratumMessage
		if err := fastJSONUnmarshal([]byte(line), &msg); err != nil {
			t.Fatalf("decode stratum message: %v; line=%q", err, line)
		}
		if msg.Method != "mining.notify" {
			continue
		}
		msgs = append(msgs, msg)
	}
	return msgs
}

func minerConnForNotifyTest(t *testing.T) (*MinerConn, *recordConn) {
	t.Helper()
	mc := benchmarkMinerConnForSubmit(NewPoolMetrics())
	conn := &recordConn{}
	mc.conn = conn
	mc.lockDifficulty = true
	mc.maxRecentJobs = 10
	mc.activeJobs = make(map[string]*Job, mc.maxRecentJobs)
	mc.jobOrder = make([]string, 0, mc.maxRecentJobs)
	mc.jobDifficulty = make(map[string]float64, mc.maxRecentJobs)
	mc.jobScriptTime = make(map[string]int64, mc.maxRecentJobs)
	mc.jobNotifyCoinbase = make(map[string]notifiedCoinbaseParts, mc.maxRecentJobs)
	return mc, conn
}

func TestSendNotifyForUsesUniqueStratumJobIDsForRepeatedNotify(t *testing.T) {
	mc, conn := minerConnForNotifyTest(t)

	job := benchmarkSubmitJobForTest(t)
	job.ScriptTime = job.Template.CurTime

	mc.sendNotifyFor(job, true)
	mc.sendNotifyFor(job, true)

	ids := notifyJobIDsFromOutput(t, conn.String())
	if len(ids) != 2 {
		t.Fatalf("expected two notify job ids, got %d: %#v", len(ids), ids)
	}
	if ids[0] == ids[1] {
		t.Fatalf("expected repeated notifies to use distinct job ids, got %q", ids[0])
	}
	if ids[0] == job.JobID || ids[1] == job.JobID {
		t.Fatalf("expected emitted Stratum job ids to be per-notify ids, base=%q ids=%#v", job.JobID, ids)
	}

	firstJob, _, _, _, _, firstScriptTime, _, _, firstOK := mc.jobForIDWithLast(ids[0])
	secondJob, _, _, _, _, secondScriptTime, _, _, secondOK := mc.jobForIDWithLast(ids[1])
	if !firstOK || !secondOK || firstJob != job || secondJob != job {
		t.Fatalf("notify ids did not resolve to the underlying job")
	}
	if firstScriptTime == 0 || secondScriptTime == 0 || firstScriptTime == secondScriptTime {
		t.Fatalf("expected immutable per-notify script times, got first=%d second=%d", firstScriptTime, secondScriptTime)
	}
}

func TestNotifiedCoinbaseRemainsBoundToWorkerAcrossReauthorization(t *testing.T) {
	mc, conn := minerConnForNotifyTest(t)
	mc.cfg.ShareRequireAuthorizedConnection = true
	mc.cfg.ShareRequireWorkerMatch = true
	mc.cfg.ShareJobFreshnessMode = shareJobFreshnessJobID
	mc.cfg.ShareCheckParamFormat = true
	mc.cfg.ShareNTimeMaxForwardSeconds = 600

	job := benchmarkSubmitJobForTest(t)
	job.ScriptTime = job.Template.CurTime
	workerA := mc.currentWorker()
	mc.sendNotifyFor(job, true)

	workerB, walletB, scriptB := generateTestWorker(t)
	mc.setWorkerWallet(workerB, walletB, scriptB)
	mc.updateWorker(workerB)
	mc.sendNotifyFor(job, true)

	ids := notifyJobIDsFromOutput(t, conn.String())
	if len(ids) != 2 {
		t.Fatalf("notify ids = %#v, want two", ids)
	}
	_, _, _, _, _, _, firstBinding, firstBindingOK, firstOK := mc.jobForIDWithLast(ids[0])
	_, _, _, _, _, _, secondBinding, secondBindingOK, secondOK := mc.jobForIDWithLast(ids[1])
	if !firstOK || !firstBindingOK || firstBinding.worker != workerA {
		t.Fatalf("first job binding = (%v, %q), want wallet A %q", firstBindingOK, firstBinding.worker, workerA)
	}
	if !secondOK || !secondBindingOK || secondBinding.worker != workerB {
		t.Fatalf("second job binding = (%v, %q), want wallet B %q", secondBindingOK, secondBinding.worker, workerB)
	}

	oldReq := &StratumRequest{
		ID:     1,
		Method: "mining.submit",
		Params: []any{workerA, ids[0], "00000000", "6553f100", "00000001"},
	}
	oldTask, ok := mc.prepareSubmissionTask(oldReq, time.Unix(job.Template.CurTime, 0))
	if !ok || oldTask.workerName != workerA || oldTask.policyReject.reason != rejectUnknown || !oldTask.hasNotifiedCoinbase {
		t.Fatalf("old wallet task not preserved: ok=%v worker=%q policy=%v bound=%v",
			ok, oldTask.workerName, oldTask.policyReject.reason, oldTask.hasNotifiedCoinbase)
	}

	mislabeledReq := *oldReq
	mislabeledReq.Params = append([]any(nil), oldReq.Params...)
	mislabeledReq.Params[0] = workerB
	mislabeledTask, ok := mc.prepareSubmissionTask(&mislabeledReq, time.Unix(job.Template.CurTime, 0))
	if !ok {
		t.Fatal("worker-label mismatch should remain processable for block safety")
	}
	if mislabeledTask.workerName != workerA || mislabeledTask.policyReject.reason != rejectUnauthorizedWorker {
		t.Fatalf("mislabeled task worker=%q policy=%v, want wallet A and unauthorized policy",
			mislabeledTask.workerName, mislabeledTask.policyReject.reason)
	}
}

func TestLateShareDoesNotReplaceReauthorizedWorker(t *testing.T) {
	mc := benchmarkMinerConnForSubmit(nil)
	workerA := mc.currentWorker()
	workerB, _, _ := generateTestWorker(t)
	mc.updateWorker(workerB)

	mc.recordShareSync(statsUpdate{worker: workerA, accepted: true, timestamp: time.Now()})
	if got := mc.currentWorker(); got != workerB {
		t.Fatalf("late share changed current worker to %q, want %q", got, workerB)
	}
}

func TestRejectedReauthorizationDoesNotChangeWorker(t *testing.T) {
	mc := benchmarkMinerConnForSubmit(nil)
	workerA := mc.currentWorker()
	workerB, _, _ := generateTestWorker(t)
	mc.conn = &writeRecorderConn{}
	mc.cfg.MinDifficulty = 1
	mc.cfg.MaxDifficulty = 2
	mc.cfg.EnforceSuggestedDifficultyLimits = true

	mc.handleAuthorizeID(2, workerB, "x,d=3")

	if got := mc.currentWorker(); got != workerA {
		t.Fatalf("rejected authorization changed current worker to %q, want %q", got, workerA)
	}
}

func TestReauthorizationWaitsForInFlightNotify(t *testing.T) {
	mc, _ := minerConnForNotifyTest(t)
	conn := &blockingNotifyConn{
		notifyStarted: make(chan struct{}),
		releaseNotify: make(chan struct{}),
	}
	mc.conn = conn
	workerA := mc.currentWorker()
	workerB, walletB, scriptB := generateTestWorker(t)
	mc.setWorkerWallet(workerB, walletB, scriptB)
	job := benchmarkSubmitJobForTest(t)
	job.ScriptTime = job.Template.CurTime

	notifyDone := make(chan struct{})
	go func() {
		mc.sendNotifyFor(job, true)
		close(notifyDone)
	}()
	select {
	case <-conn.notifyStarted:
	case <-time.After(time.Second):
		t.Fatal("notify did not reach blocked write")
	}

	authDone := make(chan struct{})
	go func() {
		mc.handleAuthorizeID(2, workerB, "")
		close(authDone)
	}()

	select {
	case <-authDone:
		t.Fatal("reauthorization completed while the old-wallet notify was in flight")
	case <-time.After(25 * time.Millisecond):
	}
	if got := mc.currentWorker(); got != workerA {
		t.Fatalf("in-flight notify observed worker switch to %q before completion", got)
	}

	close(conn.releaseNotify)
	select {
	case <-notifyDone:
	case <-time.After(time.Second):
		t.Fatal("notify did not complete")
	}
	select {
	case <-authDone:
	case <-time.After(time.Second):
		t.Fatal("reauthorization did not complete")
	}

	out := conn.String()
	notifyAt := strings.Index(out, `"method":"mining.notify"`)
	authAt := strings.Index(out, `"id":2`)
	if notifyAt < 0 || authAt < 0 || notifyAt > authAt {
		t.Fatalf("old notify must precede new authorization response: %q", out)
	}
	if got := mc.currentWorker(); got != workerB {
		t.Fatalf("current worker = %q, want reauthorized worker %q", got, workerB)
	}
	ids := notifyJobIDsFromOutput(t, out)
	if len(ids) != 1 {
		t.Fatalf("notify ids = %#v, want one", ids)
	}
	_, _, _, _, _, _, binding, bindingOK, ok := mc.jobForIDWithLast(ids[0])
	if !ok || !bindingOK || binding.worker != workerA {
		t.Fatalf("in-flight notify binding = (%v, %v, %q), want wallet A %q", ok, bindingOK, binding.worker, workerA)
	}
}

func TestOldNonBlockShareProcessesAfterReauthorization(t *testing.T) {
	mc, conn := minerConnForNotifyTest(t)
	mc.cfg.ShareRequireAuthorizedConnection = true
	mc.cfg.ShareRequireWorkerMatch = true
	mc.cfg.ShareJobFreshnessMode = shareJobFreshnessJobID
	mc.cfg.ShareCheckParamFormat = true
	job := benchmarkSubmitJobForTest(t)
	job.ScriptTime = job.Template.CurTime
	workerA := mc.currentWorker()
	mc.sendNotifyFor(job, true)
	ids := notifyJobIDsFromOutput(t, conn.String())
	if len(ids) != 1 {
		t.Fatalf("notify ids = %#v, want one", ids)
	}
	mc.setJobDifficulty(ids[0], 1e-20)

	workerB, walletB, scriptB := generateTestWorker(t)
	mc.setWorkerWallet(workerB, walletB, scriptB)
	mc.handleAuthorizeID(2, workerB, "")

	task, ok := mc.prepareSubmissionTask(&StratumRequest{
		ID:     3,
		Method: "mining.submit",
		Params: []any{workerA, ids[0], "00000000", "6553f100", "00000001"},
	}, time.Unix(job.Template.CurTime, 0))
	if !ok {
		t.Fatal("old wallet share did not pass submission preparation")
	}
	mc.processSubmissionTask(task)

	stats := mc.snapshotStats()
	if stats.Worker != workerB {
		t.Fatalf("late share changed connection worker to %q, want %q", stats.Worker, workerB)
	}
	if stats.Accepted == 0 {
		t.Fatal("old wallet non-block share was not accepted")
	}
}
