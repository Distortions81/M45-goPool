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

func TestStratumV2SubmitMapperState_MapsWireStandardToNormalized(t *testing.T) {
	mapper := newStratumV2SubmitMapperState()
	mapper.registerChannel(10, stratumV2SubmitChannelMapping{
		WorkerName:          "wallet.worker",
		StandardExtranonce2: []byte{0x00, 0x00, 0x00, 0x02},
	})
	mapper.registerJob(10, 77, "job-77")

	got, err := mapper.mapWireSubmitSharesStandard(stratumV2WireSubmitSharesStandard{
		ChannelID:      10,
		SequenceNumber: 5,
		JobID:          77,
		Nonce:          1,
		NTime:          0x66554433,
		Version:        0x20000000,
	})
	if err != nil {
		t.Fatalf("mapWireSubmitSharesStandard: %v", err)
	}
	if got.RequestID != uint32(5) {
		t.Fatalf("RequestID=%v want 5", got.RequestID)
	}
	if got.WorkerName != "wallet.worker" || got.JobID != "job-77" {
		t.Fatalf("unexpected identity mapping: %+v", got)
	}
	if got.Extranonce2Hex != "00000002" {
		t.Fatalf("Extranonce2Hex=%q want 00000002", got.Extranonce2Hex)
	}
	if got.NTimeHex != "66554433" || got.NonceHex != "00000001" {
		t.Fatalf("unexpected ntime/nonce mapping: %+v", got)
	}
}

func TestStratumV2SubmitMapperState_MapsWireExtendedToNormalized(t *testing.T) {
	mapper := newStratumV2SubmitMapperState()
	mapper.registerChannel(22, stratumV2SubmitChannelMapping{
		WorkerName:       "wallet.worker",
		ExtranoncePrefix: []byte{0xaa, 0xbb},
	})
	mapper.registerJob(22, 88, "job-88")

	got, err := mapper.mapWireSubmitSharesExtended(stratumV2WireSubmitSharesExtended{
		ChannelID:      22,
		SequenceNumber: 9,
		JobID:          88,
		Nonce:          2,
		NTime:          0x01020304,
		Version:        3,
		Extranonce:     []byte{0xcc, 0xdd},
	})
	if err != nil {
		t.Fatalf("mapWireSubmitSharesExtended: %v", err)
	}
	if got.RequestID != uint32(9) {
		t.Fatalf("RequestID=%v want 9", got.RequestID)
	}
	if got.Extranonce2Hex != "aabbccdd" {
		t.Fatalf("Extranonce2Hex=%q want aabbccdd", got.Extranonce2Hex)
	}
	if got.JobID != "job-88" || got.WorkerName != "wallet.worker" {
		t.Fatalf("unexpected mapping: %+v", got)
	}
}

func TestStratumV2SubmitMapperState_MissingMappingsError(t *testing.T) {
	mapper := newStratumV2SubmitMapperState()
	if _, err := mapper.mapWireSubmitSharesStandard(stratumV2WireSubmitSharesStandard{ChannelID: 1, JobID: 1}); err == nil {
		t.Fatalf("expected unknown channel mapping error")
	}

	mapper.registerChannel(1, stratumV2SubmitChannelMapping{WorkerName: "w", StandardExtranonce2: []byte{1}})
	if _, err := mapper.mapWireSubmitSharesStandard(stratumV2WireSubmitSharesStandard{ChannelID: 1, JobID: 1}); err == nil {
		t.Fatalf("expected unknown job mapping error")
	}

	mapper.registerJob(1, 1, "job-1")
	mapper.registerChannel(2, stratumV2SubmitChannelMapping{WorkerName: "w"})
	mapper.registerJob(2, 2, "job-2")
	if _, err := mapper.mapWireSubmitSharesStandard(stratumV2WireSubmitSharesStandard{ChannelID: 2, JobID: 2}); err == nil {
		t.Fatalf("expected missing standard extranonce mapping error")
	}
	if _, err := mapper.mapWireSubmitSharesExtended(stratumV2WireSubmitSharesExtended{ChannelID: 2, JobID: 2}); err == nil {
		t.Fatalf("expected missing extended extranonce bytes error")
	}
}
