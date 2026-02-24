package main

import (
	"bytes"
	"testing"
)

func TestSV2ConnReadLoopSkeleton_ProcessesStandardSubmit(t *testing.T) {
	mc, job := newSubmitReadyMinerConnForModesTest(t)
	mc.conn = nopConn{} // defensive: avoid nil conn writes if some path falls back
	walletAddr, walletScript := generateTestWallet(t)
	mc.setWorkerWallet(mc.currentWorker(), walletAddr, walletScript)
	// Make acceptance deterministic for this skeleton test; we only want to
	// validate the read/decode/map/process/respond pipeline here.
	mc.stratumV1.notify.jobDifficulty[job.JobID] = 1e-30
	atomicStoreFloat64(&mc.difficulty, 1e-30)
	mc.shareTarget.Store(targetFromDifficulty(1e-30))

	mapper := newStratumV2SubmitMapperState()
	mapper.registerChannel(10, stratumV2SubmitChannelMapping{
		WorkerName:          mc.currentWorker(),
		StandardExtranonce2: []byte{0x00, 0x00, 0x00, 0x00},
	})
	mapper.registerJob(10, 7, job.JobID)

	inFrame, err := encodeStratumV2SubmitSharesStandardFrame(stratumV2WireSubmitSharesStandard{
		ChannelID:      10,
		SequenceNumber: 55,
		JobID:          7,
		Nonce:          1,
		NTime:          uint32(job.Template.CurTime),
		Version:        uint32(job.Template.Version),
	})
	if err != nil {
		t.Fatalf("encode submit frame: %v", err)
	}

	var in bytes.Buffer
	var out bytes.Buffer
	in.Write(inFrame)

	c := &sv2Conn{
		mc:           mc,
		reader:       &in,
		writer:       &out,
		submitMapper: mapper,
	}
	if err := c.handleReadLoop(); err != nil {
		t.Fatalf("handleReadLoop: %v", err)
	}
	respBytes := out.Bytes()
	if len(respBytes) == 0 {
		t.Fatalf("expected sv2 response frame")
	}
	msg, err := decodeStratumV2SubmitWireFrame(respBytes)
	if err != nil {
		t.Fatalf("decode response frame: %v", err)
	}
	succ, ok := msg.(stratumV2WireSubmitSharesSuccess)
	if !ok {
		if e, ok := msg.(stratumV2WireSubmitSharesError); ok {
			t.Fatalf("response type=%T (%+v) want stratumV2WireSubmitSharesSuccess", msg, e)
		}
		t.Fatalf("response type=%T want stratumV2WireSubmitSharesSuccess", msg)
	}
	if succ.ChannelID != 10 {
		t.Fatalf("success.ChannelID=%d want 10", succ.ChannelID)
	}
	if succ.LastSequenceNumber != 55 {
		t.Fatalf("success.LastSequenceNumber=%d want 55", succ.LastSequenceNumber)
	}
	if succ.NewSubmitsAcceptedCount != 1 {
		t.Fatalf("success.NewSubmitsAcceptedCount=%d want 1", succ.NewSubmitsAcceptedCount)
	}
}

func TestSV2ConnReadLoopSkeleton_MapperErrorWritesSubmitError(t *testing.T) {
	mc, _ := newSubmitReadyMinerConnForModesTest(t)
	mc.conn = nopConn{}

	mapper := newStratumV2SubmitMapperState() // no mappings -> unknown channel
	inFrame, err := encodeStratumV2SubmitSharesStandardFrame(stratumV2WireSubmitSharesStandard{
		ChannelID:      999,
		SequenceNumber: 77,
		JobID:          1,
		Nonce:          1,
		NTime:          1,
		Version:        1,
	})
	if err != nil {
		t.Fatalf("encode submit frame: %v", err)
	}

	var in bytes.Buffer
	var out bytes.Buffer
	in.Write(inFrame)
	c := &sv2Conn{
		mc:           mc,
		reader:       &in,
		writer:       &out,
		submitMapper: mapper,
	}
	if err := c.handleReadLoop(); err != nil {
		t.Fatalf("handleReadLoop: %v", err)
	}
	msg, err := decodeStratumV2SubmitWireFrame(out.Bytes())
	if err != nil {
		t.Fatalf("decode error response frame: %v", err)
	}
	e, ok := msg.(stratumV2WireSubmitSharesError)
	if !ok {
		t.Fatalf("response type=%T want stratumV2WireSubmitSharesError", msg)
	}
	if e.ChannelID != 999 || e.SequenceNumber != 77 {
		t.Fatalf("unexpected response ids: %+v", e)
	}
	if e.ErrorCode == "" {
		t.Fatalf("expected non-empty error code")
	}
}
