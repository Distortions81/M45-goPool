package main

import (
	"bufio"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func newSubmitReadyMinerConnForModesTest(t *testing.T) (*MinerConn, *Job) {
	t.Helper()
	mc := benchmarkMinerConnForSubmit(NewPoolMetrics())
	mc.cfg.NTimeForwardSlackSeconds = 600
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
		Params: []interface{}{
			worker,
			job.JobID,
			"00000000",
			fmt.Sprintf("%08x", uint32(job.Template.CurTime)),
			"00000001",
		},
	}
}

func TestPrepareSubmissionTask_WorkerMismatch_StrictVsRelaxed(t *testing.T) {
	t.Run("strict rejects mismatched worker", func(t *testing.T) {
		mc, job := newSubmitReadyMinerConnForModesTest(t)
		mc.cfg.RelaxedSubmitValidation = false
		mc.cfg.SubmitWorkerNameMatch = true

		conn := &recordConn{}
		mc.conn = conn
		mc.writer = bufio.NewWriterSize(conn, 4096)

		req := testSubmitRequestForJob(job, "other.worker")
		if _, ok := mc.prepareSubmissionTask(req, time.Now()); ok {
			t.Fatalf("expected strict mode to reject mismatched worker")
		}
		if out := conn.String(); out == "" {
			t.Fatalf("expected strict rejection response to be written")
		}
	})

	t.Run("strict allows mismatched worker when worker-match option disabled", func(t *testing.T) {
		mc, job := newSubmitReadyMinerConnForModesTest(t)
		mc.cfg.RelaxedSubmitValidation = false
		mc.cfg.SubmitWorkerNameMatch = false

		req := testSubmitRequestForJob(job, "other.worker")
		task, ok := mc.prepareSubmissionTask(req, time.Now())
		if !ok {
			t.Fatalf("expected strict mode to allow mismatch when submit_worker_name_match is disabled")
		}
		if got, want := task.workerName, mc.currentWorker(); got != want {
			t.Fatalf("task workerName=%q want authorized worker %q", got, want)
		}
	})

	t.Run("relaxed accepts mismatched worker and keeps authorized identity", func(t *testing.T) {
		mc, job := newSubmitReadyMinerConnForModesTest(t)
		mc.cfg.RelaxedSubmitValidation = true
		mc.cfg.SubmitWorkerNameMatch = true

		req := testSubmitRequestForJob(job, "other.worker")
		task, ok := mc.prepareSubmissionTask(req, time.Now())
		if !ok {
			t.Fatalf("expected relaxed mode to accept submit task")
		}
		if got, want := task.workerName, mc.currentWorker(); got != want {
			t.Fatalf("task workerName=%q want authorized worker %q", got, want)
		}
	})
}

func TestHandleSubmit_DirectProcessingModeSelection(t *testing.T) {
	oldWorkers := submissionWorkers
	oldOnce := submissionWorkerOnce
	t.Cleanup(func() {
		submissionWorkers = oldWorkers
		submissionWorkerOnce = oldOnce
	})

	submissionWorkers = &submissionWorkerPool{tasks: make(chan submissionTask, 1)}
	submissionWorkerOnce = sync.Once{}
	// Mark the once as done so ensureSubmissionWorkerPool() does not replace our test pool.
	submissionWorkerOnce.Do(func() {})

	t.Run("disabled queues to worker pool", func(t *testing.T) {
		mc, job := newSubmitReadyMinerConnForModesTest(t)
		mc.cfg.RelaxedSubmitValidation = true
		mc.cfg.DirectSubmitProcessing = false

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
		mc.cfg.RelaxedSubmitValidation = true
		mc.cfg.DirectSubmitProcessing = true

		conn := &recordConn{}
		mc.conn = conn
		mc.writer = bufio.NewWriterSize(conn, 4096)

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

func TestHandleSubmit_CheckDuplicateSharesMode(t *testing.T) {
	t.Run("enabled rejects duplicate non-block share", func(t *testing.T) {
		mc, job := newSubmitReadyMinerConnForModesTest(t)
		mc.cfg.CheckDuplicateShares = true

		conn := &recordConn{}
		mc.conn = conn
		mc.writer = bufio.NewWriterSize(conn, 4096)

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
		mc.processRegularShare(task, ctx)
		mc.processRegularShare(task, ctx)

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
		mc.cfg.CheckDuplicateShares = false

		conn := &recordConn{}
		mc.conn = conn
		mc.writer = bufio.NewWriterSize(conn, 4096)

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
		mc.processRegularShare(task, ctx)
		mc.processRegularShare(task, ctx)

		out := conn.String()
		if strings.Contains(out, "duplicate share") {
			t.Fatalf("did not expect duplicate-share rejection when disabled, got: %q", out)
		}
		if got := strings.Count(out, `"result":true`); got != 2 {
			t.Fatalf("expected two accepted responses when duplicate check is disabled, got %d; output=%q", got, out)
		}
	})
}

func TestPrepareSubmissionTask_RejectNoJobIDToggle(t *testing.T) {
	t.Run("disabled allows empty job id to reach stale-job rejection", func(t *testing.T) {
		mc, job := newSubmitReadyMinerConnForModesTest(t)
		mc.cfg.RelaxedSubmitValidation = true
		mc.cfg.RejectNoJobID = false

		conn := &recordConn{}
		mc.conn = conn
		mc.writer = bufio.NewWriterSize(conn, 4096)

		req := testSubmitRequestForJob(job, mc.currentWorker())
		req.Params[1] = ""
		if _, ok := mc.prepareSubmissionTask(req, time.Now()); ok {
			t.Fatalf("expected empty job id submit to be rejected")
		}
		if out := conn.String(); !strings.Contains(out, "job not found") {
			t.Fatalf("expected stale-job rejection when reject_no_job_id is disabled, got: %q", out)
		}
	})

	t.Run("enabled rejects empty job id during parse validation", func(t *testing.T) {
		mc, job := newSubmitReadyMinerConnForModesTest(t)
		mc.cfg.RelaxedSubmitValidation = true
		mc.cfg.RejectNoJobID = true

		conn := &recordConn{}
		mc.conn = conn
		mc.writer = bufio.NewWriterSize(conn, 4096)

		req := testSubmitRequestForJob(job, mc.currentWorker())
		req.Params[1] = ""
		if _, ok := mc.prepareSubmissionTask(req, time.Now()); ok {
			t.Fatalf("expected empty job id submit to be rejected")
		}
		if out := conn.String(); !strings.Contains(out, "job id required") {
			t.Fatalf("expected parse-time empty-job-id rejection when reject_no_job_id is enabled, got: %q", out)
		}
	})
}
