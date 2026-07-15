package main

import (
	"bytes"
	"io"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// partialFailNotifyConn succeeds on the first notify, then reports an error
// after accepting part of the second. That is the ambiguous failure mode where
// the server cannot safely tell whether the peer received usable work.
type partialFailNotifyConn struct {
	recordConn
	notifyWrites atomic.Int32
	closeCalls   atomic.Int32
}

type partialFailMethodConn struct {
	recordConn
	failNeedle       []byte
	failed           atomic.Bool
	closeCalls       atomic.Int32
	responseWrites   atomic.Int32
	notifyWrites     atomic.Int32
	difficultyWrites atomic.Int32
	maskWrites       atomic.Int32
	extranonceWrites atomic.Int32
}

type blockingFailNotifyConn struct {
	recordConn
	started    chan struct{}
	release    chan struct{}
	startOnce  sync.Once
	closeCalls atomic.Int32
}

func (c *blockingFailNotifyConn) Write(b []byte) (int, error) {
	if !bytes.Contains(b, []byte(`"method":"mining.notify"`)) {
		return c.recordConn.Write(b)
	}
	c.startOnce.Do(func() { close(c.started) })
	<-c.release
	n := len(b) / 2
	written, _ := c.recordConn.Write(b[:n])
	return written, io.ErrClosedPipe
}

func (c *blockingFailNotifyConn) Close() error {
	c.closeCalls.Add(1)
	return nil
}

func (c *partialFailMethodConn) Write(b []byte) (int, error) {
	if !bytes.Contains(b, []byte(`"method":`)) && bytes.Contains(b, []byte(`"id":`)) {
		c.responseWrites.Add(1)
	}
	if bytes.Contains(b, []byte(`"method":"mining.notify"`)) {
		c.notifyWrites.Add(1)
	}
	if bytes.Contains(b, []byte(`"method":"mining.set_difficulty"`)) {
		c.difficultyWrites.Add(1)
	}
	if bytes.Contains(b, []byte(`"method":"mining.set_version_mask"`)) {
		c.maskWrites.Add(1)
	}
	if bytes.Contains(b, []byte(`"method":"mining.set_extranonce"`)) {
		c.extranonceWrites.Add(1)
	}
	if len(c.failNeedle) > 0 && bytes.Contains(b, c.failNeedle) && c.failed.CompareAndSwap(false, true) {
		n := len(b) / 2
		if n == 0 {
			n = 1
		}
		written, _ := c.recordConn.Write(b[:n])
		return written, io.ErrClosedPipe
	}
	return c.recordConn.Write(b)
}

func (c *partialFailMethodConn) Close() error {
	c.closeCalls.Add(1)
	return nil
}

func (c *partialFailNotifyConn) Write(b []byte) (int, error) {
	if bytes.Contains(b, []byte(`"method":"mining.notify"`)) && c.notifyWrites.Add(1) == 2 {
		n := len(b) / 2
		if n == 0 {
			n = 1
		}
		written, _ := c.recordConn.Write(b[:n])
		return written, io.ErrClosedPipe
	}
	return c.recordConn.Write(b)
}

func (c *partialFailNotifyConn) Close() error {
	c.closeCalls.Add(1)
	return nil
}

func TestOrdinaryResponseWriteFailureClosesAndSuppressesFurtherOutput(t *testing.T) {
	mc, _ := minerConnForNotifyTest(t)
	conn := &partialFailMethodConn{failNeedle: []byte(`"id":41`)}
	mc.conn = conn

	if mc.writeResponse(StratumResponse{ID: 41, Result: true}) {
		t.Fatal("partial response write unexpectedly succeeded")
	}
	if !mc.closed.Load() || conn.closeCalls.Load() != 1 {
		t.Fatalf("response failure did not close once: closed=%v close_calls=%d", mc.closed.Load(), conn.closeCalls.Load())
	}

	if mc.writeResponse(StratumResponse{ID: 42, Result: true}) {
		t.Fatal("response on closed session unexpectedly succeeded")
	}
	job := benchmarkSubmitJobForTest(t)
	job.ScriptTime = job.Template.CurTime
	mc.sendNotifyFor(job, true)
	mc.Close("redundant close")

	mc.jobMu.Lock()
	activeCount := len(mc.activeJobs)
	retiredCount := len(mc.retiredJobs)
	notifySeq := mc.notifySeq
	mc.jobMu.Unlock()
	if got := conn.responseWrites.Load(); got != 1 {
		t.Fatalf("response write attempts = %d, want 1", got)
	}
	if got := conn.notifyWrites.Load(); got != 0 {
		t.Fatalf("notify write attempts = %d, want 0", got)
	}
	if activeCount != 0 || retiredCount != 0 || notifySeq != 0 {
		t.Fatalf("closed session changed job history: active=%d retired=%d notify_seq=%d", activeCount, retiredCount, notifySeq)
	}
	if got := conn.closeCalls.Load(); got != 1 {
		t.Fatalf("connection close calls after redundant close = %d, want 1", got)
	}
}

func TestClientShowMessageWriteFailureClosesAndSuppressesResponse(t *testing.T) {
	mc, _ := minerConnForNotifyTest(t)
	conn := &partialFailMethodConn{failNeedle: []byte(`"method":"client.show_message"`)}
	mc.conn = conn

	mc.sendClientShowMessage("Warning: terminal test")
	if !mc.closed.Load() || conn.closeCalls.Load() != 1 {
		t.Fatalf("show-message failure did not close once: closed=%v close_calls=%d", mc.closed.Load(), conn.closeCalls.Load())
	}
	if mc.writeResponse(StratumResponse{ID: 51, Result: true}) {
		t.Fatal("response after show-message failure unexpectedly succeeded")
	}
	if got := conn.responseWrites.Load(); got != 0 {
		t.Fatalf("response writes after show-message failure = %d, want 0", got)
	}
}

func TestNilIDResponseRemainsNoOp(t *testing.T) {
	mc, _ := minerConnForNotifyTest(t)
	conn := &partialFailMethodConn{failNeedle: []byte(`"id":`)}
	mc.conn = conn

	if !mc.writeResponse(StratumResponse{ID: nil, Result: true}) {
		t.Fatal("nil-ID response should remain a successful no-op")
	}
	if mc.closed.Load() || conn.closeCalls.Load() != 0 || conn.responseWrites.Load() != 0 || conn.failed.Load() {
		t.Fatalf("nil-ID response touched transport: closed=%v close_calls=%d response_writes=%d failed=%v",
			mc.closed.Load(), conn.closeCalls.Load(), conn.responseWrites.Load(), conn.failed.Load())
	}
}

func TestResponseWriteFailureDoesNotDeadlockUnderNotifyMu(t *testing.T) {
	mc, _ := minerConnForNotifyTest(t)
	conn := &partialFailMethodConn{failNeedle: []byte(`"id":61`)}
	mc.conn = conn
	done := make(chan struct{})

	go func() {
		mc.notifyMu.Lock()
		defer mc.notifyMu.Unlock()
		mc.writeResponse(StratumResponse{ID: 61, Result: true})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("response failure deadlocked while notifyMu was held")
	}
	if !mc.closed.Load() || conn.closeCalls.Load() != 1 {
		t.Fatalf("response failure under notifyMu did not close once: closed=%v close_calls=%d", mc.closed.Load(), conn.closeCalls.Load())
	}
}

func TestCapturedBlockTaskStillSubmitsAfterWriteClosure(t *testing.T) {
	mc := benchmarkMinerConnForSubmit(NewPoolMetrics())
	mc.cfg.DataDir = t.TempDir()
	rpc := &countingSubmitRPC{}
	mc.rpc = rpc
	job := benchmarkSubmitJobForTest(t)
	job.Target = new(big.Int).Set(maxUint256)
	task := submissionTask{
		mc:               mc,
		reqID:            71,
		job:              job,
		jobID:            job.JobID,
		workerName:       mc.currentWorker(),
		extranonce2:      "00000000",
		extranonce2Large: []byte{0, 0, 0, 0},
		ntime:            "6553f100",
		ntimeVal:         0x6553f100,
		nonce:            "00000000",
		nonceVal:         0,
		versionHex:       "00000001",
		useVersion:       1,
		scriptTime:       job.ScriptTime,
		receivedAt:       time.Unix(1700000000, 0),
	}

	conn := &partialFailMethodConn{failNeedle: []byte(`"id":70`)}
	mc.conn = conn
	if mc.writeResponse(StratumResponse{ID: 70, Result: true}) {
		t.Fatal("setup response write unexpectedly succeeded")
	}
	if !mc.closed.Load() {
		t.Fatal("setup write failure did not close session")
	}

	mc.processSubmissionTask(task)
	flushFoundBlockLog(t)
	if got := rpc.submitCalls.Load(); got != 1 {
		t.Fatalf("captured block submit calls after closure = %d, want 1", got)
	}
	if got := conn.responseWrites.Load(); got != 1 {
		t.Fatalf("closed block task wrote another response: writes=%d, want only setup write", got)
	}
}

func TestSendNotifyForClosesAfterPartialWriteWithoutChurningHistory(t *testing.T) {
	mc, _ := minerConnForNotifyTest(t)
	mc.maxRecentJobs = 1
	conn := &partialFailNotifyConn{}
	mc.conn = conn

	first := benchmarkSubmitJobForTest(t)
	first.JobID = "notify-before-failure"
	first.ScriptTime = first.Template.CurTime
	second := *first
	second.JobID = "notify-partial-failure"
	third := *first
	third.JobID = "notify-buffered-after-close"

	mc.sendNotifyFor(first, true)
	mc.sendNotifyFor(&second, false)

	if !mc.closed.Load() {
		t.Fatal("partial mining.notify failure did not close the miner session")
	}
	if got := conn.closeCalls.Load(); got != 1 {
		t.Fatalf("connection close calls = %d, want 1", got)
	}

	// Simulate another job that was already buffered when cleanup unsubscribed
	// and closed the job channel. It must not evict more advertised history.
	mc.sendNotifyFor(&third, false)

	firstID := stratumNotifyJobID(first.JobID, 1)
	secondID := stratumNotifyJobID(second.JobID, 2)
	thirdID := stratumNotifyJobID(third.JobID, 3)
	mc.jobMu.Lock()
	_, firstRetired := mc.retiredJobs[firstID]
	_, secondActive := mc.activeJobs[secondID]
	_, thirdActive := mc.activeJobs[thirdID]
	_, thirdRetired := mc.retiredJobs[thirdID]
	activeCount := len(mc.activeJobs)
	retiredCount := len(mc.retiredJobs)
	notifySeq := mc.notifySeq
	mc.jobMu.Unlock()

	if !firstRetired || !secondActive {
		t.Fatalf("ambiguous write bindings not preserved: first_retired=%v second_active=%v", firstRetired, secondActive)
	}
	if thirdActive || thirdRetired || activeCount != 1 || retiredCount != 1 || notifySeq != 2 {
		t.Fatalf("post-close notify churned history: third_active=%v third_retired=%v active=%d retired=%d notify_seq=%d",
			thirdActive, thirdRetired, activeCount, retiredCount, notifySeq)
	}
	if got := conn.notifyWrites.Load(); got != 2 {
		t.Fatalf("notify write attempts = %d, want 2", got)
	}
}

func TestSendNotifyForStopsAfterCriticalSetupWriteFailure(t *testing.T) {
	tests := []struct {
		name       string
		failMethod string
		setup      func(*MinerConn, *Job)
	}{
		{
			name:       "pending difficulty",
			failMethod: `"method":"mining.set_difficulty"`,
			setup: func(mc *MinerConn, _ *Job) {
				mc.pendingDifficulty = true
			},
		},
		{
			name:       "changed version mask",
			failMethod: `"method":"mining.set_version_mask"`,
			setup: func(mc *MinerConn, job *Job) {
				mc.versionMu.Lock()
				mc.versionRoll = true
				mc.poolMask = 0x00000001
				mc.minerMask = ^uint32(0)
				mc.versionMask = 0x00000001
				mc.minVerBits = 1
				mc.versionMu.Unlock()
				job.VersionMask = 0x00000002
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mc, _ := minerConnForNotifyTest(t)
			conn := &partialFailMethodConn{failNeedle: []byte(tc.failMethod)}
			mc.conn = conn
			job := benchmarkSubmitJobForTest(t)
			job.ScriptTime = job.Template.CurTime
			tc.setup(mc, job)

			mc.sendNotifyFor(job, true)

			mc.jobMu.Lock()
			activeCount := len(mc.activeJobs)
			retiredCount := len(mc.retiredJobs)
			notifySeq := mc.notifySeq
			mc.jobMu.Unlock()
			if !mc.closed.Load() || conn.closeCalls.Load() != 1 {
				t.Fatalf("setup write failure did not close session: closed=%v close_calls=%d", mc.closed.Load(), conn.closeCalls.Load())
			}
			if conn.notifyWrites.Load() != 0 || activeCount != 0 || retiredCount != 0 || notifySeq != 0 {
				t.Fatalf("notify proceeded after setup failure: writes=%d active=%d retired=%d notify_seq=%d",
					conn.notifyWrites.Load(), activeCount, retiredCount, notifySeq)
			}
		})
	}
}

func TestConfigureStopsCriticalWriteSequenceAtFirstFailure(t *testing.T) {
	tests := []struct {
		name            string
		failNeedle      string
		wantMaskWrites  int32
		wantExtraWrites int32
	}{
		{name: "response", failNeedle: `"id":7`, wantMaskWrites: 0, wantExtraWrites: 0},
		{name: "version mask", failNeedle: `"method":"mining.set_version_mask"`, wantMaskWrites: 1, wantExtraWrites: 0},
		{name: "extranonce", failNeedle: `"method":"mining.set_extranonce"`, wantMaskWrites: 1, wantExtraWrites: 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mc, _ := minerConnForNotifyTest(t)
			conn := &partialFailMethodConn{failNeedle: []byte(tc.failNeedle)}
			mc.conn = conn
			mc.versionMu.Lock()
			mc.poolMask = 0x0000e000
			mc.versionMask = 0x0000e000
			mc.minVerBits = 1
			mc.versionMu.Unlock()

			mc.handleConfigure(&StratumRequest{
				ID:     7,
				Method: "mining.configure",
				Params: []any{
					[]any{"version-rolling", "subscribe-extranonce"},
					map[string]any{"version-rolling.mask": "00006000"},
				},
			})

			if !mc.closed.Load() || conn.closeCalls.Load() != 1 {
				t.Fatalf("configure failure did not close session: closed=%v close_calls=%d", mc.closed.Load(), conn.closeCalls.Load())
			}
			if got := conn.responseWrites.Load(); got != 1 {
				t.Fatalf("configure response writes = %d, want 1", got)
			}
			if got := conn.maskWrites.Load(); got != tc.wantMaskWrites {
				t.Fatalf("version-mask writes = %d, want %d", got, tc.wantMaskWrites)
			}
			if got := conn.extranonceWrites.Load(); got != tc.wantExtraWrites {
				t.Fatalf("extranonce writes = %d, want %d", got, tc.wantExtraWrites)
			}
			if got := conn.notifyWrites.Load(); got != 0 {
				t.Fatalf("notify writes after configure failure = %d, want 0", got)
			}
		})
	}
}

func TestHandshakeResponseFailureDoesNotStartWork(t *testing.T) {
	t.Run("subscribe", func(t *testing.T) {
		mc := benchmarkMinerConnForSubmit(nil)
		mc.subscribed = false
		mc.authorized = true
		mc.listenerOn = false
		mc.extranonceSubscribed = true
		mc.jobCh = make(chan *Job, 1)
		conn := &partialFailMethodConn{failNeedle: []byte(`"id":11`)}
		mc.conn = conn

		mc.handleSubscribeID(11, "", false, "", false)

		if !mc.closed.Load() || mc.listenerOn || mc.initialWorkScheduled {
			t.Fatalf("failed subscribe response advanced session: closed=%v listener=%v initial_work=%v",
				mc.closed.Load(), mc.listenerOn, mc.initialWorkScheduled)
		}
		if conn.extranonceWrites.Load() != 0 || conn.notifyWrites.Load() != 0 {
			t.Fatalf("setup followed failed subscribe response: extranonce=%d notify=%d",
				conn.extranonceWrites.Load(), conn.notifyWrites.Load())
		}
	})

	t.Run("authorize", func(t *testing.T) {
		mc := benchmarkMinerConnForSubmit(nil)
		worker := mc.currentWorker()
		mc.authorized = false
		mc.listenerOn = false
		mc.jobCh = make(chan *Job, 1)
		mc.workerRegistry = newWorkerConnectionRegistry()
		conn := &partialFailMethodConn{failNeedle: []byte(`"id":12`)}
		mc.conn = conn

		mc.handleAuthorizeID(12, worker, "")

		seq := atomic.LoadUint64(&mc.connectionSeq)
		if !mc.closed.Load() || mc.listenerOn || mc.initialWorkScheduled {
			t.Fatalf("failed authorize response advanced session: closed=%v listener=%v initial_work=%v",
				mc.closed.Load(), mc.listenerOn, mc.initialWorkScheduled)
		}
		if got := mc.workerRegistry.connectionBySeq(seq); got != nil {
			t.Fatal("failed authorize response left a worker registry entry")
		}
		if mc.registeredWorkerHash != "" {
			t.Fatalf("failed authorize response left registered worker %q", mc.registeredWorkerHash)
		}
		if conn.notifyWrites.Load() != 0 {
			t.Fatalf("notify writes after failed authorize response = %d, want 0", conn.notifyWrites.Load())
		}
	})
}

func TestFailedNotifyPreventsWaitingReauthorizationRegistration(t *testing.T) {
	mc, _ := minerConnForNotifyTest(t)
	mc.workerRegistry = newWorkerConnectionRegistry()
	mc.assignConnectionSeq()
	workerA := mc.currentWorker()
	mc.registerWorker(workerA)
	conn := &blockingFailNotifyConn{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	mc.conn = conn
	job := benchmarkSubmitJobForTest(t)
	job.ScriptTime = job.Template.CurTime

	notifyDone := make(chan struct{})
	go func() {
		mc.sendNotifyFor(job, true)
		close(notifyDone)
	}()
	select {
	case <-conn.started:
	case <-time.After(time.Second):
		t.Fatal("notify did not reach blocked write")
	}

	workerB, walletB, scriptB := generateTestWorker(t)
	mc.setWorkerWallet(workerB, walletB, scriptB)
	authorizeDone := make(chan struct{})
	go func() {
		mc.handleAuthorizeID(21, workerB, "")
		close(authorizeDone)
	}()
	select {
	case <-authorizeDone:
		t.Fatal("reauthorization bypassed the in-flight notify")
	case <-time.After(25 * time.Millisecond):
	}

	close(conn.release)
	select {
	case <-notifyDone:
	case <-time.After(time.Second):
		t.Fatal("failed notify did not finish")
	}
	select {
	case <-authorizeDone:
	case <-time.After(time.Second):
		t.Fatal("waiting reauthorization did not return")
	}

	seq := atomic.LoadUint64(&mc.connectionSeq)
	if !mc.closed.Load() || conn.closeCalls.Load() != 1 {
		t.Fatalf("failed notify did not close session: closed=%v close_calls=%d", mc.closed.Load(), conn.closeCalls.Load())
	}
	if got := mc.currentWorker(); got != workerA {
		t.Fatalf("closed session switched worker to %q, want %q", got, workerA)
	}
	if got := mc.workerRegistry.connectionBySeq(seq); got != nil {
		t.Fatal("waiting reauthorization re-registered the closed connection")
	}
	if mc.registeredWorkerHash != "" {
		t.Fatalf("closed connection retained registered worker %q", mc.registeredWorkerHash)
	}
}
