package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func newSubmitReadyMinerConnForModesTest(t *testing.T) (*MinerConn, *Job) {
	t.Helper()
	mc := benchmarkMinerConnForSubmit(NewPoolMetrics())
	mc.cfg.ShareNTimeMaxForwardSeconds = 600
	mc.cfg.ShareRequireAuthorizedConnection = true
	mc.cfg.ShareJobFreshnessMode = shareJobFreshnessJobID
	mc.cfg.ShareCheckParamFormat = true
	mc.authorized = true

	authorizedWorker := "authorized.worker"
	mc.stats.Worker = authorizedWorker
	mc.stats.WorkerSHA256 = workerNameHash(authorizedWorker)
	_, authorizedWallet, authorizedScript := generateTestWorker(t)
	mc.setWorkerWallet(authorizedWorker, authorizedWallet, authorizedScript)

	job := benchmarkSubmitJobForTest(t)
	jobID := job.JobID

	mc.jobMu.Lock()
	mc.activeJobs = map[string]*Job{jobID: job}
	mc.lastJob = job
	mc.jobMu.Unlock()
	mc.jobDifficulty[jobID] = 1e-12
	mc.jobScriptTime = map[string]int64{jobID: job.Template.CurTime}

	return mc, job
}

func testSubmitRequestForJob(job *Job, worker string) *StratumRequest {
	return &StratumRequest{
		ID:     1,
		Method: "mining.submit",
		Params: []any{
			worker,
			job.JobID,
			"00000000",
			fmt.Sprintf("%08x", uint32(job.Template.CurTime)),
			"00000001",
		},
	}
}

func cloneSubmitReq(req *StratumRequest) *StratumRequest {
	params := make([]any, len(req.Params))
	copy(params, req.Params)
	return &StratumRequest{
		ID:     req.ID,
		Method: req.Method,
		Params: params,
	}
}

type prepareOutcome struct {
	task submissionTask
	ok   bool
	out  string
}

func runPrepareSubmission(
	t *testing.T,
	configure func(mc *MinerConn, job *Job),
	mutateReq func(req *StratumRequest),
) prepareOutcome {
	t.Helper()

	mc, job := newSubmitReadyMinerConnForModesTest(t)
	if configure != nil {
		configure(mc, job)
	}
	conn := &recordConn{}
	mc.conn = conn
	req := testSubmitRequestForJob(job, mc.currentWorker())
	if mutateReq != nil {
		mutateReq(req)
	}
	task, ok := mc.prepareSubmissionTask(req, time.Unix(1700000000, 0))
	return prepareOutcome{task: task, ok: ok, out: conn.String()}
}

func TestPrepareSubmissionTask_WorkerMismatch_AuthorizationToggle(t *testing.T) {
	t.Run("authorization check defers mismatched worker until after PoW", func(t *testing.T) {
		mc, job := newSubmitReadyMinerConnForModesTest(t)
		mc.cfg.ShareRequireAuthorizedConnection = true
		mc.cfg.ShareRequireWorkerMatch = true

		conn := &recordConn{}
		mc.conn = conn

		req := testSubmitRequestForJob(job, "other.worker")
		task, ok := mc.prepareSubmissionTask(req, time.Now())
		if !ok {
			t.Fatalf("worker mismatch must remain processable for block safety")
		}
		if task.policyReject.reason != rejectUnauthorizedWorker {
			t.Fatalf("policy reason=%v, want unauthorized worker", task.policyReject.reason)
		}
		if out := conn.String(); out != "" {
			t.Fatalf("worker policy was rejected before PoW validation: %q", out)
		}
	})

	t.Run("allows mismatched worker when worker-match option disabled", func(t *testing.T) {
		mc, job := newSubmitReadyMinerConnForModesTest(t)
		mc.cfg.ShareRequireAuthorizedConnection = true
		mc.cfg.ShareRequireWorkerMatch = false

		req := testSubmitRequestForJob(job, "other.worker")
		task, ok := mc.prepareSubmissionTask(req, time.Now())
		if !ok {
			t.Fatalf("expected submit to allow mismatch when share_require_worker_match is disabled")
		}
		if got, want := task.workerName, mc.currentWorker(); got != want {
			t.Fatalf("task workerName=%q want authorized worker %q", got, want)
		}
	})

	t.Run("authorization check disabled accepts mismatched worker", func(t *testing.T) {
		mc, job := newSubmitReadyMinerConnForModesTest(t)
		mc.cfg.ShareRequireAuthorizedConnection = false
		mc.cfg.ShareRequireWorkerMatch = true

		req := testSubmitRequestForJob(job, "other.worker")
		task, ok := mc.prepareSubmissionTask(req, time.Now())
		if !ok {
			t.Fatalf("expected submit task to be accepted")
		}
		if got, want := task.workerName, mc.currentWorker(); got != want {
			t.Fatalf("task workerName=%q want authorized worker %q", got, want)
		}
	})
}

func TestHandleSubmit_DirectProcessingModeSelection(t *testing.T) {
	ensureSubmissionWorkerPool()
	oldWorkers := submissionWorkers
	t.Cleanup(func() {
		submissionWorkers = oldWorkers
	})

	submissionWorkers = &submissionWorkerPool{tasks: make(chan preparedSubmissionTask, 1)}

	t.Run("disabled queues to worker pool", func(t *testing.T) {
		mc, job := newSubmitReadyMinerConnForModesTest(t)
		mc.cfg.SubmitProcessInline = false
		conn := &recordConn{}
		mc.conn = conn

		req := testSubmitRequestForJob(job, mc.currentWorker())
		mc.handleSubmit(req)

		select {
		case task := <-submissionWorkers.tasks:
			if task.task.mc != mc {
				t.Fatalf("queued task miner mismatch")
			}
		default:
			t.Fatalf("expected task to be queued when direct processing is disabled; output=%q", conn.String())
		}
	})

	t.Run("enabled processes inline without queuing", func(t *testing.T) {
		mc, job := newSubmitReadyMinerConnForModesTest(t)
		mc.cfg.SubmitProcessInline = true

		conn := &recordConn{}
		mc.conn = conn

		req := testSubmitRequestForJob(job, mc.currentWorker())
		mc.handleSubmit(req)
		if out := conn.String(); out == "" {
			t.Fatalf("expected inline submit processing to emit a response")
		}

		select {
		case <-submissionWorkers.tasks:
			t.Fatalf("did not expect task to be queued when direct processing is enabled")
		default:
		}
	})

	t.Run("closed pool falls back to inline processing", func(t *testing.T) {
		mc, job := newSubmitReadyMinerConnForModesTest(t)
		mc.cfg.SubmitProcessInline = false

		conn := &recordConn{}
		mc.conn = conn
		submissionWorkers = &submissionWorkerPool{
			tasks:  make(chan preparedSubmissionTask, 1),
			closed: true,
		}

		req := testSubmitRequestForJob(job, mc.currentWorker())
		mc.handleSubmit(req)
		if out := conn.String(); out == "" {
			t.Fatal("expected closed-pool fallback to process the submission inline")
		}
	})
}

func TestHandleSubmit_ShareCheckDuplicateMode(t *testing.T) {
	t.Run("enabled rejects duplicate non-block share", func(t *testing.T) {
		mc, job := newSubmitReadyMinerConnForModesTest(t)
		mc.cfg.ShareCheckDuplicate = true

		conn := &recordConn{}
		mc.conn = conn

		task := submissionTask{
			mc:          mc,
			reqID:       1,
			job:         job,
			jobID:       job.JobID,
			workerName:  mc.currentWorker(),
			extranonce2: "00000000",
			ntime:       fmt.Sprintf("%08x", uint32(job.Template.CurTime)),
			nonce:       "00000001",
			versionHex:  "00000001",
			useVersion:  1,
			receivedAt:  time.Now(),
		}
		ctx := shareContext{
			hashHex:   strings.Repeat("0", 64),
			shareDiff: 1,
			isBlock:   false,
		}
		mc.processShare(task, ctx)
		mc.processShare(task, ctx)

		out := conn.String()
		if !strings.Contains(out, "duplicate share") {
			t.Fatalf("expected duplicate-share rejection in response output, got: %q", out)
		}
		if got := strings.Count(out, `"result":true`); got != 1 {
			t.Fatalf("expected one accepted response before duplicate rejection, got %d; output=%q", got, out)
		}
	})

	t.Run("disabled allows duplicate non-block share", func(t *testing.T) {
		mc, job := newSubmitReadyMinerConnForModesTest(t)
		mc.cfg.ShareCheckDuplicate = false

		conn := &recordConn{}
		mc.conn = conn

		task := submissionTask{
			mc:          mc,
			reqID:       1,
			job:         job,
			jobID:       job.JobID,
			workerName:  mc.currentWorker(),
			extranonce2: "00000000",
			ntime:       fmt.Sprintf("%08x", uint32(job.Template.CurTime)),
			nonce:       "00000001",
			versionHex:  "00000001",
			useVersion:  1,
			receivedAt:  time.Now(),
		}
		ctx := shareContext{
			hashHex:   strings.Repeat("1", 64),
			shareDiff: 1,
			isBlock:   false,
		}
		mc.processShare(task, ctx)
		mc.processShare(task, ctx)

		out := conn.String()
		if strings.Contains(out, "duplicate share") {
			t.Fatalf("did not expect duplicate-share rejection when disabled, got: %q", out)
		}
		if got := strings.Count(out, `"result":true`); got != 2 {
			t.Fatalf("expected two accepted responses when duplicate check is disabled, got %d; output=%q", got, out)
		}
	})
}

func TestPrepareSubmissionTask_EmptyJobIDAlwaysRejected(t *testing.T) {
	mc, job := newSubmitReadyMinerConnForModesTest(t)

	conn := &recordConn{}
	mc.conn = conn

	req := testSubmitRequestForJob(job, mc.currentWorker())
	req.Params[1] = ""
	if _, ok := mc.prepareSubmissionTask(req, time.Now()); ok {
		t.Fatalf("expected empty job id submit to be rejected")
	}
	if out := conn.String(); !strings.Contains(out, "job id required") {
		t.Fatalf("expected parse-time empty-job-id rejection, got: %q", out)
	}
}

func TestHandleSubmit_UnknownJobFreshnessOff_ClassifiedAsStaleNotLowDiff(t *testing.T) {
	mc, job := newSubmitReadyMinerConnForModesTest(t)
	mc.cfg.ShareJobFreshnessMode = shareJobFreshnessOff
	mc.cfg.ShareCheckDuplicate = false

	conn := &recordConn{}
	mc.conn = conn

	req := testSubmitRequestForJob(job, mc.currentWorker())
	req.Params[1] = "expired-job-id"

	task, ok := mc.prepareSubmissionTask(req, time.Now())
	if !ok {
		t.Fatalf("expected unknown job to fall back when freshness mode is off")
	}
	ctx := shareContext{
		hashHex:   strings.Repeat("f", 64),
		shareDiff: 1e-12,
		isBlock:   false,
	}
	mc.processShare(task, ctx)

	out := conn.String()
	if !strings.Contains(out, "job not found") {
		t.Fatalf("expected stale classification for unknown job when freshness is off, got: %q", out)
	}
	if strings.Contains(out, "low difficulty share") {
		t.Fatalf("expected stale classification instead of lowdiff, got: %q", out)
	}
}

func TestPrepareSubmissionTask_FieldValidation_MalformedNTimeNonceVersion(t *testing.T) {
	t.Run("empty ntime rejects", func(t *testing.T) {
		mc, job := newSubmitReadyMinerConnForModesTest(t)
		conn := &recordConn{}
		mc.conn = conn

		req := testSubmitRequestForJob(job, mc.currentWorker())
		req.Params[3] = ""
		if _, ok := mc.prepareSubmissionTask(req, time.Now()); ok {
			t.Fatalf("expected empty ntime to be rejected")
		}
		if out := conn.String(); !strings.Contains(out, "invalid ntime") {
			t.Fatalf("expected invalid ntime rejection, got: %q", out)
		}
	})

	t.Run("invalid ntime hex rejects", func(t *testing.T) {
		mc, job := newSubmitReadyMinerConnForModesTest(t)
		conn := &recordConn{}
		mc.conn = conn

		req := testSubmitRequestForJob(job, mc.currentWorker())
		req.Params[3] = "zzzzzzzz"
		if _, ok := mc.prepareSubmissionTask(req, time.Now()); ok {
			t.Fatalf("expected non-hex ntime to be rejected")
		}
		if out := conn.String(); !strings.Contains(out, "invalid ntime") {
			t.Fatalf("expected invalid ntime rejection, got: %q", out)
		}
	})

	t.Run("non-padded nonce accepted", func(t *testing.T) {
		mc, job := newSubmitReadyMinerConnForModesTest(t)

		req := testSubmitRequestForJob(job, mc.currentWorker())
		req.Params[4] = "123"
		task, ok := mc.prepareSubmissionTask(req, time.Now())
		if !ok {
			t.Fatalf("expected short nonce to be accepted")
		}
		if got, want := task.nonceVal, uint32(0x123); got != want {
			t.Fatalf("nonce=%08x want=%08x", got, want)
		}
	})

	t.Run("long nonce truncated", func(t *testing.T) {
		mc, job := newSubmitReadyMinerConnForModesTest(t)

		req := testSubmitRequestForJob(job, mc.currentWorker())
		req.Params[4] = "123456789abc"
		task, ok := mc.prepareSubmissionTask(req, time.Now())
		if !ok {
			t.Fatalf("expected long nonce to be normalized")
		}
		if got, want := task.nonce, "12345678"; got != want {
			t.Fatalf("nonce string=%q want=%q", got, want)
		}
		if got, want := task.nonceVal, uint32(0x12345678); got != want {
			t.Fatalf("nonce=%08x want=%08x", got, want)
		}
	})

	t.Run("invalid nonce hex rejects", func(t *testing.T) {
		mc, job := newSubmitReadyMinerConnForModesTest(t)
		conn := &recordConn{}
		mc.conn = conn

		req := testSubmitRequestForJob(job, mc.currentWorker())
		req.Params[4] = "g0000001"
		if _, ok := mc.prepareSubmissionTask(req, time.Now()); ok {
			t.Fatalf("expected non-hex nonce to be rejected")
		}
		if out := conn.String(); !strings.Contains(out, "invalid nonce") {
			t.Fatalf("expected invalid nonce rejection, got: %q", out)
		}
	})

	t.Run("empty version rejects", func(t *testing.T) {
		mc, job := newSubmitReadyMinerConnForModesTest(t)
		conn := &recordConn{}
		mc.conn = conn

		req := testSubmitRequestForJob(job, mc.currentWorker())
		req.Params = append(req.Params, "")
		if _, ok := mc.prepareSubmissionTask(req, time.Now()); ok {
			t.Fatalf("expected empty version to be rejected")
		}
		if out := conn.String(); !strings.Contains(out, "version required") {
			t.Fatalf("expected version-required rejection, got: %q", out)
		}
	})

	t.Run("version too long rejects", func(t *testing.T) {
		mc, job := newSubmitReadyMinerConnForModesTest(t)
		conn := &recordConn{}
		mc.conn = conn

		req := testSubmitRequestForJob(job, mc.currentWorker())
		req.Params = append(req.Params, "000000001")
		if _, ok := mc.prepareSubmissionTask(req, time.Now()); ok {
			t.Fatalf("expected long version to be rejected")
		}
		if out := conn.String(); !strings.Contains(out, "version too long") {
			t.Fatalf("expected version-too-long rejection, got: %q", out)
		}
	})

	t.Run("invalid version hex rejects", func(t *testing.T) {
		mc, job := newSubmitReadyMinerConnForModesTest(t)
		conn := &recordConn{}
		mc.conn = conn

		req := testSubmitRequestForJob(job, mc.currentWorker())
		req.Params = append(req.Params, "zzzzzzzz")
		if _, ok := mc.prepareSubmissionTask(req, time.Now()); ok {
			t.Fatalf("expected non-hex version to be rejected")
		}
		if out := conn.String(); !strings.Contains(out, "invalid version") {
			t.Fatalf("expected invalid-version rejection, got: %q", out)
		}
	})

	t.Run("short extranonce2 right-padded", func(t *testing.T) {
		mc, job := newSubmitReadyMinerConnForModesTest(t)

		req := testSubmitRequestForJob(job, mc.currentWorker())
		req.Params[2] = "abcd"
		task, ok := mc.prepareSubmissionTask(req, time.Now())
		if !ok {
			t.Fatalf("expected short extranonce2 to be normalized")
		}
		if got, want := task.extranonce2, "abcd0000"; got != want {
			t.Fatalf("extranonce2=%q want=%q", got, want)
		}
		if got, want := fmt.Sprintf("%x", task.extranonce2Decoded()), "abcd0000"; got != want {
			t.Fatalf("decoded extranonce2=%q want=%q", got, want)
		}
	})

	t.Run("long extranonce2 truncated", func(t *testing.T) {
		mc, job := newSubmitReadyMinerConnForModesTest(t)

		req := testSubmitRequestForJob(job, mc.currentWorker())
		req.Params[2] = "abcdef012345"
		task, ok := mc.prepareSubmissionTask(req, time.Now())
		if !ok {
			t.Fatalf("expected long extranonce2 to be normalized")
		}
		if got, want := task.extranonce2, "abcdef01"; got != want {
			t.Fatalf("extranonce2=%q want=%q", got, want)
		}
		if got, want := fmt.Sprintf("%x", task.extranonce2Decoded()), "abcdef01"; got != want {
			t.Fatalf("decoded extranonce2=%q want=%q", got, want)
		}
	})

	t.Run("invalid extranonce2 hex rejects", func(t *testing.T) {
		mc, job := newSubmitReadyMinerConnForModesTest(t)
		conn := &recordConn{}
		mc.conn = conn

		req := testSubmitRequestForJob(job, mc.currentWorker())
		req.Params[2] = "zzzz"
		if _, ok := mc.prepareSubmissionTask(req, time.Now()); ok {
			t.Fatalf("expected non-hex extranonce2 to be rejected")
		}
		if out := conn.String(); !strings.Contains(out, "invalid extranonce2") {
			t.Fatalf("expected invalid extranonce2 rejection, got: %q", out)
		}
	})
}

func TestPrepareSubmissionTask_NTimeWindowBoundariesAndPolicyPrecedence(t *testing.T) {
	mc, job := newSubmitReadyMinerConnForModesTest(t)
	mc.cfg.ShareCheckNTimeWindow = true
	mc.jobNTimeBounds = map[string]jobNTimeBounds{
		job.JobID: {min: 1700000000, max: 1700000600},
	}

	baseReq := testSubmitRequestForJob(job, mc.currentWorker())

	t.Run("ntime at min boundary accepted", func(t *testing.T) {
		req := cloneSubmitReq(baseReq)
		req.Params[3] = "6553f100" // 1700000000

		task, ok := mc.prepareSubmissionTask(req, time.Now())
		if !ok {
			t.Fatalf("expected min-boundary ntime to be accepted")
		}
		if task.policyReject.reason != rejectUnknown {
			t.Fatalf("unexpected policy reject at min boundary: %+v", task.policyReject)
		}
	})

	t.Run("ntime at max boundary accepted", func(t *testing.T) {
		req := cloneSubmitReq(baseReq)
		req.Params[3] = "6553f358" // 1700000600

		task, ok := mc.prepareSubmissionTask(req, time.Now())
		if !ok {
			t.Fatalf("expected max-boundary ntime to be accepted")
		}
		if task.policyReject.reason != rejectUnknown {
			t.Fatalf("unexpected policy reject at max boundary: %+v", task.policyReject)
		}
	})

	t.Run("ntime below min becomes policy reject", func(t *testing.T) {
		req := cloneSubmitReq(baseReq)
		req.Params[3] = "6553f0ff" // 1699999999

		task, ok := mc.prepareSubmissionTask(req, time.Now())
		if !ok {
			t.Fatalf("expected out-of-window ntime to remain processable (policy-only)")
		}
		if task.policyReject.reason != rejectInvalidNTime {
			t.Fatalf("got policy=%v want %v", task.policyReject.reason, rejectInvalidNTime)
		}
	})

	t.Run("ntime above max becomes policy reject", func(t *testing.T) {
		req := cloneSubmitReq(baseReq)
		req.Params[3] = "6553f359" // 1700000601

		task, ok := mc.prepareSubmissionTask(req, time.Now())
		if !ok {
			t.Fatalf("expected out-of-window ntime to remain processable (policy-only)")
		}
		if task.policyReject.reason != rejectInvalidNTime {
			t.Fatalf("got policy=%v want %v", task.policyReject.reason, rejectInvalidNTime)
		}
	})

	t.Run("ntime policy reject wins over later version policy reject", func(t *testing.T) {
		req := cloneSubmitReq(baseReq)
		req.Params[3] = "6553f359" // above max -> invalid ntime policy
		req.Params = append(req.Params, "00000010")

		mc.cfg.ShareCheckVersionRolling = true
		mc.versionRoll = true
		mc.versionMask = 0x0000000f
		mc.minVerBits = 0

		task, ok := mc.prepareSubmissionTask(req, time.Now())
		if !ok {
			t.Fatalf("expected policy-only failures to return a task")
		}
		if task.policyReject.reason != rejectInvalidNTime {
			t.Fatalf("got policy=%v want ntime precedence", task.policyReject.reason)
		}
	})
}

func TestPrepareSubmissionTask_VersionRollingPolicyBoundaries(t *testing.T) {
	newVersionReq := func(ver string) (*MinerConn, *StratumRequest) {
		mc, job := newSubmitReadyMinerConnForModesTest(t)
		mc.cfg.ShareCheckVersionRolling = true
		mc.versionMask = 0x0000000f
		mc.versionRoll = true
		mc.minVerBits = 2
		req := testSubmitRequestForJob(job, mc.currentWorker())
		req.Params = append(req.Params, ver)
		return mc, req
	}

	t.Run("BIP310 bits inside mask with enough changed bits accepted", func(t *testing.T) {
		mc, req := newVersionReq("00000006")
		task, ok := mc.prepareSubmissionTask(req, time.Now())
		if !ok {
			t.Fatalf("expected valid version delta to be accepted")
		}
		if task.policyReject.reason != rejectUnknown {
			t.Fatalf("unexpected policy reject: %+v", task.policyReject)
		}
	})

	t.Run("BIP310 bits inside mask accepted regardless of changed bit count", func(t *testing.T) {
		mc, req := newVersionReq("00000003")
		task, ok := mc.prepareSubmissionTask(req, time.Now())
		if !ok {
			t.Fatalf("expected in-mask version bits to be accepted")
		}
		if task.policyReject.reason != rejectUnknown {
			t.Fatalf("unexpected policy reject: %+v", task.policyReject)
		}
	})

	t.Run("degraded mode allows insufficient bits", func(t *testing.T) {
		mc, req := newVersionReq("00000003")
		mc.cfg.ShareAllowDegradedVersionBits = true
		task, ok := mc.prepareSubmissionTask(req, time.Now())
		if !ok {
			t.Fatalf("expected degraded mode to allow submit task")
		}
		if task.policyReject.reason != rejectUnknown {
			t.Fatalf("unexpected policy reject in degraded mode: %+v", task.policyReject)
		}
	})

	t.Run("version outside mask rejected by policy", func(t *testing.T) {
		mc, req := newVersionReq("00000010")
		mc.minVerBits = 0
		mc.cfg.ShareAllowOutOfMaskVersionBits = false
		task, ok := mc.prepareSubmissionTask(req, time.Now())
		if !ok {
			t.Fatalf("expected out-of-mask version to be policy-only reject")
		}
		if task.policyReject.reason != rejectInvalidVersionMask {
			t.Fatalf("got policy=%v want %v", task.policyReject.reason, rejectInvalidVersionMask)
		}
	})

	t.Run("version outside mask allowed when compatibility mode enabled", func(t *testing.T) {
		mc, req := newVersionReq("00000010")
		mc.minVerBits = 0
		mc.cfg.ShareAllowOutOfMaskVersionBits = true
		task, ok := mc.prepareSubmissionTask(req, time.Now())
		if !ok {
			t.Fatalf("expected out-of-mask version to remain processable when compatibility mode is enabled")
		}
		if task.policyReject.reason != rejectUnknown {
			t.Fatalf("unexpected policy reject: %+v", task.policyReject)
		}
	})

	t.Run("full rolled version remains accepted when its diff is in mask", func(t *testing.T) {
		mc, req := newVersionReq("20000003")
		mc.cfg.ShareAllowOutOfMaskVersionBits = false
		mc.jobMu.Lock()
		mc.lastJob.Template.Version = int32(0x20000001)
		mc.jobMu.Unlock()
		task, ok := mc.prepareSubmissionTask(req, time.Now())
		if !ok {
			t.Fatalf("expected compatible full version to remain processable")
		}
		if task.policyReject.reason != rejectUnknown {
			t.Fatalf("unexpected full-version policy reject: %+v", task.policyReject)
		}
		if task.useVersion != 0x20000003 {
			t.Fatalf("full rolled version=%08x want 20000003", task.useVersion)
		}
	})

	t.Run("version rolling disabled policy rejects non-zero delta in strict mode", func(t *testing.T) {
		mc, req := newVersionReq("00000003")
		mc.versionRoll = false
		mc.cfg.ShareAllowOutOfMaskVersionBits = false
		task, ok := mc.prepareSubmissionTask(req, time.Now())
		if !ok {
			t.Fatalf("expected disabled version rolling to return policy-only reject")
		}
		if task.policyReject.reason != rejectInvalidVersion {
			t.Fatalf("got policy=%v want %v", task.policyReject.reason, rejectInvalidVersion)
		}
	})

	t.Run("version rolling disabled allowed in compatibility mode", func(t *testing.T) {
		mc, req := newVersionReq("00000003")
		mc.versionRoll = false
		mc.cfg.ShareAllowOutOfMaskVersionBits = true
		task, ok := mc.prepareSubmissionTask(req, time.Now())
		if !ok {
			t.Fatalf("expected disabled version rolling to return a processable task in compatibility mode")
		}
		if task.policyReject.reason != rejectUnknown {
			t.Fatalf("unexpected policy reject: %+v", task.policyReject)
		}
	})
}

func TestResolveSubmittedVersionPrefersBIP310WithLegacyAlternate(t *testing.T) {
	base := uint32(0x20002000)
	mask := uint32(0x0000e000)
	got := resolveSubmittedVersion(base, 0x00004000, mask, true, false, true)

	if got.useVersion != 0x20004000 {
		t.Fatalf("BIP310 useVersion=%08x want 20004000", got.useVersion)
	}
	if got.versionDiff != 0x00006000 {
		t.Fatalf("BIP310 versionDiff=%08x want 00006000", got.versionDiff)
	}
	if !got.hasAlternateVersion {
		t.Fatalf("expected XOR-delta alternate for ambiguous in-mask submit")
	}
	if got.alternateUseVersion != 0x20006000 {
		t.Fatalf("alternateUseVersion=%08x want 20006000", got.alternateUseVersion)
	}
}

func TestResolveSubmittedVersionExplicitZeroClearsNegotiatedBits(t *testing.T) {
	const (
		base = uint32(0x20100000)
		mask = uint32(0x1fffe000)
	)

	omitted := resolveSubmittedVersion(base, 0, mask, true, false, false)
	if omitted.useVersion != base {
		t.Fatalf("omitted version = %08x, want base %08x", omitted.useVersion, base)
	}

	explicitZero := resolveSubmittedVersion(base, 0, mask, true, false, true)
	want := base &^ mask
	if explicitZero.useVersion != want {
		t.Fatalf("explicit zero version = %08x, want %08x", explicitZero.useVersion, want)
	}
	if explicitZero.versionDiff != base^want {
		t.Fatalf("explicit zero diff = %08x, want %08x", explicitZero.versionDiff, base^want)
	}
}

func TestPreferAlternateSubmissionContext(t *testing.T) {
	tests := []struct {
		name              string
		primary           shareContext
		alternate         shareContext
		primaryAcceptable bool
		altAcceptable     bool
		want              bool
	}{
		{
			name:              "alternate block wins over primary share",
			primary:           shareContext{shareDiff: 10},
			alternate:         shareContext{isBlock: true, shareDiff: 10},
			primaryAcceptable: true,
			altAcceptable:     true,
			want:              true,
		},
		{
			name:              "primary block is kept",
			primary:           shareContext{isBlock: true, shareDiff: 10},
			alternate:         shareContext{shareDiff: 10},
			primaryAcceptable: true,
			altAcceptable:     true,
			want:              false,
		},
		{
			name:              "alternate acceptable share replaces low-diff primary",
			primary:           shareContext{shareDiff: 0.5},
			alternate:         shareContext{shareDiff: 10},
			primaryAcceptable: false,
			altAcceptable:     true,
			want:              true,
		},
		{
			name:              "primary acceptable share is kept",
			primary:           shareContext{shareDiff: 10},
			alternate:         shareContext{shareDiff: 20},
			primaryAcceptable: true,
			altAcceptable:     true,
			want:              false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := preferAlternateSubmissionContext(tc.primary, tc.alternate, tc.primaryAcceptable, tc.altAcceptable)
			if got != tc.want {
				t.Fatalf("preferAlternateSubmissionContext=%v want %v", got, tc.want)
			}
		})
	}
}

func TestPrepareSubmissionTask_FieldValidationAndBoundaries(t *testing.T) {
	type parityCase struct {
		name             string
		configure        func(mc *MinerConn, job *Job)
		mutateReq        func(req *StratumRequest)
		wantOK           bool
		wantErrContains  string
		wantPolicyReason submitRejectReason
		wantNTime        uint32
		wantNonce        uint32
		wantUseVersion   uint32
	}

	cases := []parityCase{
		{
			name: "empty ntime rejects",
			mutateReq: func(req *StratumRequest) {
				req.Params[3] = ""
			},
			wantOK:          false,
			wantErrContains: "invalid ntime",
		},
		{
			name: "invalid ntime hex rejects",
			mutateReq: func(req *StratumRequest) {
				req.Params[3] = "zzzzzzzz"
			},
			wantOK:          false,
			wantErrContains: "invalid ntime",
		},
		{
			name: "non-padded nonce accepted",
			mutateReq: func(req *StratumRequest) {
				req.Params[4] = "123"
			},
			wantOK:           true,
			wantPolicyReason: rejectUnknown,
			wantNTime:        1700000000,
			wantNonce:        0x123,
			wantUseVersion:   1,
		},
		{
			name: "invalid nonce hex rejects",
			mutateReq: func(req *StratumRequest) {
				req.Params[4] = "g0000001"
			},
			wantOK:          false,
			wantErrContains: "invalid nonce",
		},
		{
			name: "empty version rejects",
			mutateReq: func(req *StratumRequest) {
				req.Params = append(req.Params, "")
			},
			wantOK:          false,
			wantErrContains: "version required",
		},
		{
			name: "version too long rejects",
			mutateReq: func(req *StratumRequest) {
				req.Params = append(req.Params, "000000001")
			},
			wantOK:          false,
			wantErrContains: "version too long",
		},
		{
			name: "invalid version hex rejects",
			mutateReq: func(req *StratumRequest) {
				req.Params = append(req.Params, "zzzzzzzz")
			},
			wantOK:          false,
			wantErrContains: "invalid version",
		},
		{
			name: "ntime at min boundary accepted",
			configure: func(mc *MinerConn, job *Job) {
				mc.cfg.ShareCheckNTimeWindow = true
				mc.jobNTimeBounds = map[string]jobNTimeBounds{job.JobID: {min: 1700000000, max: 1700000600}}
			},
			mutateReq: func(req *StratumRequest) {
				req.Params[3] = "6553f100"
			},
			wantOK:           true,
			wantPolicyReason: rejectUnknown,
			wantNTime:        1700000000,
			wantNonce:        1,
			wantUseVersion:   1,
		},
		{
			name: "ntime at max boundary accepted",
			configure: func(mc *MinerConn, job *Job) {
				mc.cfg.ShareCheckNTimeWindow = true
				mc.jobNTimeBounds = map[string]jobNTimeBounds{job.JobID: {min: 1700000000, max: 1700000600}}
			},
			mutateReq: func(req *StratumRequest) {
				req.Params[3] = "6553f358"
			},
			wantOK:           true,
			wantPolicyReason: rejectUnknown,
			wantNTime:        1700000600,
			wantNonce:        1,
			wantUseVersion:   1,
		},
		{
			name: "ntime above max policy reject",
			configure: func(mc *MinerConn, job *Job) {
				mc.cfg.ShareCheckNTimeWindow = true
				mc.jobNTimeBounds = map[string]jobNTimeBounds{job.JobID: {min: 1700000000, max: 1700000600}}
			},
			mutateReq: func(req *StratumRequest) {
				req.Params[3] = "6553f359"
			},
			wantOK:           true,
			wantPolicyReason: rejectInvalidNTime,
			wantNTime:        1700000601,
			wantNonce:        1,
			wantUseVersion:   1,
		},
		{
			name: "ntime policy precedence over version policy",
			configure: func(mc *MinerConn, job *Job) {
				mc.cfg.ShareCheckNTimeWindow = true
				mc.jobNTimeBounds = map[string]jobNTimeBounds{job.JobID: {min: 1700000000, max: 1700000600}}
				mc.cfg.ShareCheckVersionRolling = true
				mc.versionRoll = true
				mc.versionMask = 0x0000000f
			},
			mutateReq: func(req *StratumRequest) {
				req.Params[3] = "6553f359"
				req.Params = append(req.Params, "00000010")
			},
			wantOK:           true,
			wantPolicyReason: rejectInvalidNTime,
			wantNTime:        1700000601,
			wantNonce:        1,
			wantUseVersion:   0x10,
		},
		{
			name: "BIP310 version bits in mask with enough changed bits accepted",
			configure: func(mc *MinerConn, _ *Job) {
				mc.cfg.ShareCheckVersionRolling = true
				mc.versionMask = 0x0000000f
				mc.versionRoll = true
				mc.minVerBits = 2
			},
			mutateReq: func(req *StratumRequest) {
				req.Params = append(req.Params, "00000006")
			},
			wantOK:           true,
			wantPolicyReason: rejectUnknown,
			wantNTime:        1700000000,
			wantNonce:        1,
			wantUseVersion:   6, // BIP310 replacement bits with base version 1
		},
		{
			name: "BIP310 version bits accepted regardless of changed bit count",
			configure: func(mc *MinerConn, _ *Job) {
				mc.cfg.ShareCheckVersionRolling = true
				mc.versionMask = 0x0000000f
				mc.versionRoll = true
				mc.minVerBits = 2
			},
			mutateReq: func(req *StratumRequest) {
				req.Params = append(req.Params, "00000003")
			},
			wantOK:           true,
			wantPolicyReason: rejectUnknown,
			wantNTime:        1700000000,
			wantNonce:        1,
			wantUseVersion:   3,
		},
		{
			name: "version degraded mode allows insufficient bits",
			configure: func(mc *MinerConn, _ *Job) {
				mc.cfg.ShareCheckVersionRolling = true
				mc.cfg.ShareAllowDegradedVersionBits = true
				mc.versionMask = 0x0000000f
				mc.versionRoll = true
				mc.minVerBits = 2
			},
			mutateReq: func(req *StratumRequest) {
				req.Params = append(req.Params, "00000003")
			},
			wantOK:           true,
			wantPolicyReason: rejectUnknown,
			wantNTime:        1700000000,
			wantNonce:        1,
			wantUseVersion:   3,
		},
		{
			name: "version outside mask allowed in compatibility mode",
			configure: func(mc *MinerConn, _ *Job) {
				mc.cfg.ShareCheckVersionRolling = true
				mc.cfg.ShareAllowOutOfMaskVersionBits = true
				mc.versionMask = 0x0000000f
				mc.versionRoll = true
				mc.minVerBits = 0
			},
			mutateReq: func(req *StratumRequest) {
				req.Params = append(req.Params, "00000010")
			},
			wantOK:           true,
			wantPolicyReason: rejectUnknown,
			wantNTime:        1700000000,
			wantNonce:        1,
			wantUseVersion:   0x11, // base version 1 XOR out-of-mask delta 0x10
		},
		{
			name: "full version outside mask is interpreted as delta in compatibility mode",
			configure: func(mc *MinerConn, _ *Job) {
				mc.cfg.ShareCheckVersionRolling = true
				mc.cfg.ShareAllowOutOfMaskVersionBits = true
				mc.versionMask = 0x0000000f
				mc.versionRoll = true
				mc.minVerBits = 0
			},
			mutateReq: func(req *StratumRequest) {
				req.Params = append(req.Params, "20000010")
			},
			wantOK:           true,
			wantPolicyReason: rejectUnknown,
			wantNTime:        1700000000,
			wantNonce:        1,
			wantUseVersion:   0x20000011, // base version 1 XOR submitted value
		},
		{
			name: "version outside mask policy reject",
			configure: func(mc *MinerConn, _ *Job) {
				mc.cfg.ShareCheckVersionRolling = true
				mc.cfg.ShareAllowOutOfMaskVersionBits = false
				mc.versionMask = 0x0000000f
				mc.versionRoll = true
				mc.minVerBits = 0
			},
			mutateReq: func(req *StratumRequest) {
				req.Params = append(req.Params, "00000010")
			},
			wantOK:           true,
			wantPolicyReason: rejectInvalidVersionMask,
			wantNTime:        1700000000,
			wantNonce:        1,
			wantUseVersion:   0x10,
		},
		{
			name: "version rolling disabled policy reject",
			configure: func(mc *MinerConn, _ *Job) {
				mc.cfg.ShareCheckVersionRolling = true
				mc.cfg.ShareAllowOutOfMaskVersionBits = false
				mc.versionMask = 0x0000000f
				mc.versionRoll = false
				mc.minVerBits = 0
			},
			mutateReq: func(req *StratumRequest) {
				req.Params = append(req.Params, "00000003")
			},
			wantOK:           true,
			wantPolicyReason: rejectInvalidVersion,
			wantNTime:        1700000000,
			wantNonce:        1,
			wantUseVersion:   3,
		},
		{
			name: "version rolling disabled allowed in compatibility mode",
			configure: func(mc *MinerConn, _ *Job) {
				mc.cfg.ShareCheckVersionRolling = true
				mc.cfg.ShareAllowOutOfMaskVersionBits = true
				mc.versionMask = 0x0000000f
				mc.versionRoll = false
				mc.minVerBits = 0
			},
			mutateReq: func(req *StratumRequest) {
				req.Params = append(req.Params, "00000003")
			},
			wantOK:           true,
			wantPolicyReason: rejectUnknown,
			wantNTime:        1700000000,
			wantNonce:        1,
			wantUseVersion:   3,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runPrepareSubmission(t, tc.configure, tc.mutateReq)

			if got.ok != tc.wantOK {
				t.Fatalf("ok=%v want %v", got.ok, tc.wantOK)
			}

			if !tc.wantOK {
				if !strings.Contains(got.out, tc.wantErrContains) {
					t.Fatalf("expected error %q, got: %q", tc.wantErrContains, got.out)
				}
				return
			}

			if got.task.policyReject.reason != tc.wantPolicyReason {
				t.Fatalf("policy=%v want %v", got.task.policyReject.reason, tc.wantPolicyReason)
			}
			if got.task.ntimeVal != tc.wantNTime {
				t.Fatalf("ntime=%d want=%d", got.task.ntimeVal, tc.wantNTime)
			}
			if got.task.nonceVal != tc.wantNonce {
				t.Fatalf("nonce=%d want=%d", got.task.nonceVal, tc.wantNonce)
			}
			if got.task.useVersion != tc.wantUseVersion {
				t.Fatalf("useVersion=%d want=%d", got.task.useVersion, tc.wantUseVersion)
			}
		})
	}
}

func TestPrepareSubmissionTask_StaleAndFallbackFreshnessModes(t *testing.T) {
	type parityCase struct {
		name             string
		configure        func(mc *MinerConn, job *Job)
		mutateReq        func(req *StratumRequest)
		wantOK           bool
		wantErrContains  string
		wantPolicyReason submitRejectReason
	}

	cases := []parityCase{
		{
			name: "unknown job in strict job-id mode rejects immediately",
			configure: func(mc *MinerConn, _ *Job) {
				mc.cfg.ShareJobFreshnessMode = shareJobFreshnessJobID
			},
			mutateReq: func(req *StratumRequest) {
				req.Params[1] = "missing-job"
			},
			wantOK:          false,
			wantErrContains: "job not found",
		},
		{
			name: "unknown job with freshness off uses fallback and marks stale policy",
			configure: func(mc *MinerConn, _ *Job) {
				mc.cfg.ShareJobFreshnessMode = shareJobFreshnessOff
			},
			mutateReq: func(req *StratumRequest) {
				req.Params[1] = "missing-job"
			},
			wantOK:           true,
			wantPolicyReason: rejectStaleJob,
		},
		{
			name: "unknown job with freshness off but no fallback rejects",
			configure: func(mc *MinerConn, _ *Job) {
				mc.cfg.ShareJobFreshnessMode = shareJobFreshnessOff
				mc.jobMu.Lock()
				mc.lastJob = nil
				mc.jobMu.Unlock()
			},
			mutateReq: func(req *StratumRequest) {
				req.Params[1] = "missing-job"
			},
			wantOK:          false,
			wantErrContains: "job not found",
		},
		{
			name: "job-id-prev mode marks prevhash mismatch as stale policy",
			configure: func(mc *MinerConn, job *Job) {
				mc.cfg.ShareJobFreshnessMode = shareJobFreshnessJobIDPrev
				mc.jobMu.Lock()
				mc.lastJob = job
				mc.lastJobPrevHash = strings.Repeat("f", 64)
				mc.lastJobHeight = job.Template.Height + 99
				mc.jobMu.Unlock()
			},
			wantOK:           true,
			wantPolicyReason: rejectStaleJob,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runPrepareSubmission(t, tc.configure, tc.mutateReq)

			if got.ok != tc.wantOK {
				t.Fatalf("ok=%v want %v", got.ok, tc.wantOK)
			}

			if !tc.wantOK {
				if !strings.Contains(got.out, tc.wantErrContains) {
					t.Fatalf("expected error %q, got: %q", tc.wantErrContains, got.out)
				}
				return
			}

			if got.task.policyReject.reason != tc.wantPolicyReason {
				t.Fatalf("policy=%v want %v", got.task.policyReject.reason, tc.wantPolicyReason)
			}
			if got.task.job == nil {
				t.Fatalf("expected a populated task job")
			}
		})
	}
}
