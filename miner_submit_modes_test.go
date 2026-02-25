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

func runPrepareSubmissionBothPaths(
	t *testing.T,
	configure func(mc *MinerConn, job *Job),
	mutateReq func(req *StratumRequest),
) (prepareOutcome, prepareOutcome) {
	t.Helper()

	runStandard := func() prepareOutcome {
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

	runFast := func() prepareOutcome {
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

		worker := []byte(req.Params[0].(string))
		jobID := []byte(req.Params[1].(string))
		en2 := []byte(req.Params[2].(string))
		ntime := []byte(req.Params[3].(string))
		nonce := []byte(req.Params[4].(string))
		var version []byte
		haveVersion := len(req.Params) == 6
		if haveVersion {
			version = []byte(req.Params[5].(string))
		}

		task, ok := mc.prepareSubmissionTaskFastBytes(req.ID, worker, jobID, en2, ntime, nonce, version, haveVersion, time.Unix(1700000000, 0))
		return prepareOutcome{task: task, ok: ok, out: conn.String()}
	}

	return runStandard(), runFast()
}

func TestPrepareSubmissionTask_WorkerMismatch_AuthorizationToggle(t *testing.T) {
	t.Run("authorization check rejects mismatched worker", func(t *testing.T) {
		mc, job := newSubmitReadyMinerConnForModesTest(t)
		mc.cfg.ShareRequireAuthorizedConnection = true
		mc.cfg.ShareRequireWorkerMatch = true

		conn := &recordConn{}
		mc.conn = conn

		req := testSubmitRequestForJob(job, "other.worker")
		if _, ok := mc.prepareSubmissionTask(req, time.Now()); ok {
			t.Fatalf("expected submit to reject mismatched worker")
		}
		if out := conn.String(); out == "" {
			t.Fatalf("expected rejection response to be written")
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

	submissionWorkers = &submissionWorkerPool{tasks: make(chan submissionTask, 1)}

	t.Run("disabled queues to worker pool", func(t *testing.T) {
		mc, job := newSubmitReadyMinerConnForModesTest(t)
		mc.cfg.SubmitProcessInline = false

		req := testSubmitRequestForJob(job, mc.currentWorker())
		mc.handleSubmit(req)

		select {
		case task := <-submissionWorkers.tasks:
			if task.mc != mc {
				t.Fatalf("queued task miner mismatch")
			}
		default:
			t.Fatalf("expected task to be queued when direct processing is disabled")
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

func TestPrepareSubmissionTask_ShareRequireJobIDToggle(t *testing.T) {
	t.Run("disabled allows empty job id to reach stale-job rejection", func(t *testing.T) {
		mc, job := newSubmitReadyMinerConnForModesTest(t)
		mc.cfg.ShareRequireJobID = false

		conn := &recordConn{}
		mc.conn = conn

		req := testSubmitRequestForJob(job, mc.currentWorker())
		req.Params[1] = ""
		if _, ok := mc.prepareSubmissionTask(req, time.Now()); ok {
			t.Fatalf("expected empty job id submit to be rejected")
		}
		if out := conn.String(); !strings.Contains(out, "job not found") {
			t.Fatalf("expected stale-job rejection when share_require_job_id is disabled, got: %q", out)
		}
	})

	t.Run("enabled rejects empty job id during parse validation", func(t *testing.T) {
		mc, job := newSubmitReadyMinerConnForModesTest(t)
		mc.cfg.ShareRequireJobID = true

		conn := &recordConn{}
		mc.conn = conn

		req := testSubmitRequestForJob(job, mc.currentWorker())
		req.Params[1] = ""
		if _, ok := mc.prepareSubmissionTask(req, time.Now()); ok {
			t.Fatalf("expected empty job id submit to be rejected")
		}
		if out := conn.String(); !strings.Contains(out, "job id required") {
			t.Fatalf("expected parse-time empty-job-id rejection when share_require_job_id is enabled, got: %q", out)
		}
	})
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
	t.Run("invalid ntime length rejects", func(t *testing.T) {
		mc, job := newSubmitReadyMinerConnForModesTest(t)
		conn := &recordConn{}
		mc.conn = conn

		req := testSubmitRequestForJob(job, mc.currentWorker())
		req.Params[3] = "1234"
		if _, ok := mc.prepareSubmissionTask(req, time.Now()); ok {
			t.Fatalf("expected short ntime to be rejected")
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

	t.Run("invalid nonce length rejects", func(t *testing.T) {
		mc, job := newSubmitReadyMinerConnForModesTest(t)
		conn := &recordConn{}
		mc.conn = conn

		req := testSubmitRequestForJob(job, mc.currentWorker())
		req.Params[4] = "123"
		if _, ok := mc.prepareSubmissionTask(req, time.Now()); ok {
			t.Fatalf("expected short nonce to be rejected")
		}
		if out := conn.String(); !strings.Contains(out, "invalid nonce") {
			t.Fatalf("expected invalid nonce rejection, got: %q", out)
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

	t.Run("delta inside mask with enough bits accepted", func(t *testing.T) {
		mc, req := newVersionReq("00000003")
		task, ok := mc.prepareSubmissionTask(req, time.Now())
		if !ok {
			t.Fatalf("expected valid version delta to be accepted")
		}
		if task.policyReject.reason != rejectUnknown {
			t.Fatalf("unexpected policy reject: %+v", task.policyReject)
		}
	})

	t.Run("delta inside mask with insufficient bits rejected by policy", func(t *testing.T) {
		mc, req := newVersionReq("00000001")
		task, ok := mc.prepareSubmissionTask(req, time.Now())
		if !ok {
			t.Fatalf("expected insufficient bits to be policy-only reject")
		}
		if task.policyReject.reason != rejectInsufficientVersionBits {
			t.Fatalf("got policy=%v want %v", task.policyReject.reason, rejectInsufficientVersionBits)
		}
	})

	t.Run("degraded mode allows insufficient bits", func(t *testing.T) {
		mc, req := newVersionReq("00000001")
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
		task, ok := mc.prepareSubmissionTask(req, time.Now())
		if !ok {
			t.Fatalf("expected out-of-mask version to be policy-only reject")
		}
		if task.policyReject.reason != rejectInvalidVersionMask {
			t.Fatalf("got policy=%v want %v", task.policyReject.reason, rejectInvalidVersionMask)
		}
	})

	t.Run("version rolling disabled policy rejects non-zero delta", func(t *testing.T) {
		mc, req := newVersionReq("00000003")
		mc.versionRoll = false
		task, ok := mc.prepareSubmissionTask(req, time.Now())
		if !ok {
			t.Fatalf("expected disabled version rolling to return policy-only reject")
		}
		if task.policyReject.reason != rejectInvalidVersion {
			t.Fatalf("got policy=%v want %v", task.policyReject.reason, rejectInvalidVersion)
		}
	})
}

func TestPrepareSubmissionTaskFastBytes_Parity_FieldValidationAndBoundaries(t *testing.T) {
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
			name: "invalid ntime length rejects",
			mutateReq: func(req *StratumRequest) {
				req.Params[3] = "1234"
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
			name: "invalid nonce length rejects",
			mutateReq: func(req *StratumRequest) {
				req.Params[4] = "123"
			},
			wantOK:          false,
			wantErrContains: "invalid nonce",
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
			name: "version delta in mask with enough bits accepted",
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
			wantUseVersion:   2, // base version 1 XOR delta 3
		},
		{
			name: "version insufficient bits policy reject",
			configure: func(mc *MinerConn, _ *Job) {
				mc.cfg.ShareCheckVersionRolling = true
				mc.versionMask = 0x0000000f
				mc.versionRoll = true
				mc.minVerBits = 2
			},
			mutateReq: func(req *StratumRequest) {
				req.Params = append(req.Params, "00000001")
			},
			wantOK:           true,
			wantPolicyReason: rejectInsufficientVersionBits,
			wantNTime:        1700000000,
			wantNonce:        1,
			wantUseVersion:   0,
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
				req.Params = append(req.Params, "00000001")
			},
			wantOK:           true,
			wantPolicyReason: rejectUnknown,
			wantNTime:        1700000000,
			wantNonce:        1,
			wantUseVersion:   0,
		},
		{
			name: "version outside mask policy reject",
			configure: func(mc *MinerConn, _ *Job) {
				mc.cfg.ShareCheckVersionRolling = true
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
			wantUseVersion:   2,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			std, fast := runPrepareSubmissionBothPaths(t, tc.configure, tc.mutateReq)

			if std.ok != fast.ok {
				t.Fatalf("path parity mismatch ok: standard=%v fast=%v", std.ok, fast.ok)
			}
			if std.ok != tc.wantOK {
				t.Fatalf("ok=%v want %v", std.ok, tc.wantOK)
			}

			if !tc.wantOK {
				if !strings.Contains(std.out, tc.wantErrContains) {
					t.Fatalf("standard path expected error %q, got: %q", tc.wantErrContains, std.out)
				}
				if !strings.Contains(fast.out, tc.wantErrContains) {
					t.Fatalf("fast path expected error %q, got: %q", tc.wantErrContains, fast.out)
				}
				return
			}

			if std.task.policyReject.reason != fast.task.policyReject.reason {
				t.Fatalf("policy parity mismatch: standard=%v fast=%v", std.task.policyReject.reason, fast.task.policyReject.reason)
			}
			if std.task.policyReject.reason != tc.wantPolicyReason {
				t.Fatalf("policy=%v want %v", std.task.policyReject.reason, tc.wantPolicyReason)
			}
			if std.task.ntimeVal != fast.task.ntimeVal || std.task.ntimeVal != tc.wantNTime {
				t.Fatalf("ntime parity/value mismatch: standard=%d fast=%d want=%d", std.task.ntimeVal, fast.task.ntimeVal, tc.wantNTime)
			}
			if std.task.nonceVal != fast.task.nonceVal || std.task.nonceVal != tc.wantNonce {
				t.Fatalf("nonce parity/value mismatch: standard=%d fast=%d want=%d", std.task.nonceVal, fast.task.nonceVal, tc.wantNonce)
			}
			if std.task.useVersion != fast.task.useVersion {
				t.Fatalf("useVersion parity mismatch: standard=%d fast=%d", std.task.useVersion, fast.task.useVersion)
			}
			if std.task.useVersion != tc.wantUseVersion {
				t.Fatalf("useVersion=%d want=%d", std.task.useVersion, tc.wantUseVersion)
			}
		})
	}
}

func TestPrepareSubmissionTaskFastBytes_Parity_StaleAndFallbackFreshnessModes(t *testing.T) {
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
			std, fast := runPrepareSubmissionBothPaths(t, tc.configure, tc.mutateReq)

			if std.ok != fast.ok {
				t.Fatalf("path parity mismatch ok: standard=%v fast=%v", std.ok, fast.ok)
			}
			if std.ok != tc.wantOK {
				t.Fatalf("ok=%v want %v", std.ok, tc.wantOK)
			}

			if !tc.wantOK {
				if !strings.Contains(std.out, tc.wantErrContains) {
					t.Fatalf("standard path expected error %q, got: %q", tc.wantErrContains, std.out)
				}
				if !strings.Contains(fast.out, tc.wantErrContains) {
					t.Fatalf("fast path expected error %q, got: %q", tc.wantErrContains, fast.out)
				}
				return
			}

			if std.task.policyReject.reason != fast.task.policyReject.reason {
				t.Fatalf("policy parity mismatch: standard=%v fast=%v", std.task.policyReject.reason, fast.task.policyReject.reason)
			}
			if std.task.policyReject.reason != tc.wantPolicyReason {
				t.Fatalf("policy=%v want %v", std.task.policyReject.reason, tc.wantPolicyReason)
			}
			if std.task.job == nil || fast.task.job == nil {
				t.Fatalf("expected both paths to return a populated task job")
			}
		})
	}
}
