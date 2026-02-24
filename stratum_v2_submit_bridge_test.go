package main

import (
	"fmt"
	"testing"
	"time"
)

type testStratumV2Responder struct {
	okCount     int
	errCount    int
	targetSends int
	lastReqID   any
	lastErrCode int
	lastErrMsg  string
	lastBanned  bool
}

func (r *testStratumV2Responder) writeSubmitOK(reqID any) {
	r.okCount++
	r.lastReqID = reqID
}

func (r *testStratumV2Responder) writeSubmitError(reqID any, errCode int, msg string, banned bool) {
	r.errCount++
	r.lastReqID = reqID
	r.lastErrCode = errCode
	r.lastErrMsg = msg
	r.lastBanned = banned
}

func (r *testStratumV2Responder) sendSetTarget(job *Job) {
	if job != nil {
		r.targetSends++
	}
}

func TestPrepareStratumV2SubmissionTask_BuildsSharedTask(t *testing.T) {
	mc, job := newSubmitReadyMinerConnForModesTest(t)
	now := time.Now()
	reqID := uint32(7)

	req := stratumV2NormalizedSubmitShare{
		RequestID:      reqID,
		WorkerName:     mc.currentWorker(),
		JobID:          job.JobID,
		Extranonce2Hex: "00000000",
		NTimeHex:       fmt.Sprintf("%08x", uint32(job.Template.CurTime)),
		NonceHex:       "00000001",
	}
	responder := &testStratumV2Responder{}

	task, ok := mc.prepareStratumV2SubmissionTask(req, responder, now)
	if !ok {
		t.Fatalf("expected v2 submit task to be prepared")
	}
	if task.job != job {
		t.Fatalf("task.job mismatch")
	}
	if task.reqID != reqID {
		t.Fatalf("task.reqID=%v want %v", task.reqID, reqID)
	}
	if task.workerName != mc.currentWorker() {
		t.Fatalf("task.workerName=%q want %q", task.workerName, mc.currentWorker())
	}
	if task.submitHooks == nil {
		t.Fatalf("expected v2 submit hooks to be attached")
	}
	if task.ntimeVal != uint32(job.Template.CurTime) {
		t.Fatalf("task.ntimeVal=%d want %d", task.ntimeVal, uint32(job.Template.CurTime))
	}
	if task.nonceVal != 1 {
		t.Fatalf("task.nonceVal=%d want 1", task.nonceVal)
	}
	if got := string(task.extranonce2Decoded()); got != "\x00\x00\x00\x00" {
		t.Fatalf("unexpected decoded extranonce2 bytes: %x", task.extranonce2Decoded())
	}
}
