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
