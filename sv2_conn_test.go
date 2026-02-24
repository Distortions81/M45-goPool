package main

import (
	"bytes"
	"testing"
)

func testSV2SetupConnectionFrame(t *testing.T) []byte {
	t.Helper()
	frame, err := encodeStratumV2SetupConnectionFrame(stratumV2WireSetupConnection{
		Protocol:        0,
		MinVersion:      2,
		MaxVersion:      2,
		EndpointHost:    "127.0.0.1",
		EndpointPort:    3333,
		Vendor:          "goPool",
		HardwareVersion: "test",
		Firmware:        "test",
		DeviceID:        "dev1",
	})
	if err != nil {
		t.Fatalf("encode setupconnection: %v", err)
	}
	return frame
}

func TestSV2ConnReadLoopSkeleton_SetupConnection_Succeeds(t *testing.T) {
	mc, _ := newSubmitReadyMinerConnForModesTest(t)
	mc.conn = nopConn{}

	var in bytes.Buffer
	var out bytes.Buffer
	in.Write(testSV2SetupConnectionFrame(t))

	c := &sv2Conn{mc: mc, reader: &in, writer: &out}
	if err := c.handleReadLoop(); err != nil {
		t.Fatalf("handleReadLoop: %v", err)
	}
	msg, err := decodeStratumV2MiningWireFrame(out.Bytes())
	if err != nil {
		t.Fatalf("decode setup success: %v", err)
	}
	resp, ok := msg.(stratumV2WireSetupConnectionSuccess)
	if !ok {
		t.Fatalf("response type=%T want stratumV2WireSetupConnectionSuccess", msg)
	}
	if resp.UsedVersion != 2 {
		t.Fatalf("used_version=%d want 2", resp.UsedVersion)
	}
	if !c.setupDone || c.setupVersion != 2 {
		t.Fatalf("expected setup state to be recorded")
	}
}

func TestSV2ConnReadLoopSkeleton_OpenBeforeSetup_WritesSetupError(t *testing.T) {
	mc, _ := newSubmitReadyMinerConnForModesTest(t)
	mc.conn = nopConn{}

	var in bytes.Buffer
	var out bytes.Buffer
	reqFrame, err := encodeStratumV2OpenStandardMiningChannelFrame(stratumV2WireOpenStandardMiningChannel{
		RequestID:    1,
		UserIdentity: mc.currentWorker(),
	})
	if err != nil {
		t.Fatalf("encode open standard: %v", err)
	}
	in.Write(reqFrame)

	c := &sv2Conn{mc: mc, reader: &in, writer: &out, submitMapper: newStratumV2SubmitMapperState()}
	if err := c.handleReadLoop(); err != nil {
		t.Fatalf("handleReadLoop: %v", err)
	}
	msg, err := decodeStratumV2MiningWireFrame(out.Bytes())
	if err != nil {
		t.Fatalf("decode setup error: %v", err)
	}
	resp, ok := msg.(stratumV2WireSetupConnectionError)
	if !ok {
		t.Fatalf("response type=%T want stratumV2WireSetupConnectionError", msg)
	}
	if resp.ErrorCode == "" {
		t.Fatalf("expected non-empty setup error code")
	}
}

func TestSV2SubmitWireResponder_SendSetTarget_WritesFrame(t *testing.T) {
	mc, _ := newSubmitReadyMinerConnForModesTest(t)
	mc.conn = nopConn{}
	mc.shareTarget.Store(targetFromDifficulty(2))

	var out bytes.Buffer
	r := &stratumV2SubmitWireResponder{
		mc:        mc,
		w:         &out,
		channelID: 44,
	}
	r.sendSetTarget(nil)
	if r.err != nil {
		t.Fatalf("sendSetTarget err: %v", r.err)
	}
	msg, err := decodeStratumV2MiningWireFrame(out.Bytes())
	if err != nil {
		t.Fatalf("decode settarget frame: %v", err)
	}
	got, ok := msg.(stratumV2WireSetTarget)
	if !ok {
		t.Fatalf("decoded type=%T want stratumV2WireSetTarget", msg)
	}
	if got.ChannelID != 44 {
		t.Fatalf("settarget.ChannelID=%d want 44", got.ChannelID)
	}
	wantTarget := uint256BEFromBigInt(mc.shareTargetOrDefault())
	if got.MaximumTarget != wantTarget {
		t.Fatalf("settarget target mismatch")
	}
}

func TestSV2ConnWriteStratumV2SetTarget_WritesFrameAndTracksChannelTarget(t *testing.T) {
	mc, _ := newSubmitReadyMinerConnForModesTest(t)
	mc.conn = nopConn{}
	var out bytes.Buffer
	c := &sv2Conn{
		mc:     mc,
		writer: &out,
	}
	msg := stratumV2WireSetTarget{
		ChannelID:     88,
		MaximumTarget: [32]byte{0xaa, 0xbb, 0xcc},
	}
	if err := c.writeStratumV2SetTarget(msg); err != nil {
		t.Fatalf("writeStratumV2SetTarget: %v", err)
	}
	dec, err := decodeStratumV2MiningWireFrame(out.Bytes())
	if err != nil {
		t.Fatalf("decode settarget frame: %v", err)
	}
	got, ok := dec.(stratumV2WireSetTarget)
	if !ok {
		t.Fatalf("decoded type=%T want stratumV2WireSetTarget", dec)
	}
	if got != msg {
		t.Fatalf("frame mismatch: got=%#v want=%#v", got, msg)
	}
	tracked, ok := c.channelTargets[msg.ChannelID]
	if !ok || tracked != msg.MaximumTarget {
		t.Fatalf("channel target tracking mismatch: ok=%v tracked=%x want=%x", ok, tracked, msg.MaximumTarget)
	}
}

func TestSV2ConnReadLoopSkeleton_OpensStandardChannelAndRegistersMapper(t *testing.T) {
	mc, _ := newSubmitReadyMinerConnForModesTest(t)
	mc.conn = nopConn{}

	var in bytes.Buffer
	var out bytes.Buffer
	in.Write(testSV2SetupConnectionFrame(t))
	reqFrame, err := encodeStratumV2OpenStandardMiningChannelFrame(stratumV2WireOpenStandardMiningChannel{
		RequestID:    9,
		UserIdentity: mc.currentWorker(),
	})
	if err != nil {
		t.Fatalf("encode open standard: %v", err)
	}
	in.Write(reqFrame)

	c := &sv2Conn{
		mc:           mc,
		reader:       &in,
		writer:       &out,
		submitMapper: newStratumV2SubmitMapperState(),
	}
	if err := c.handleReadLoop(); err != nil {
		t.Fatalf("handleReadLoop: %v", err)
	}

	allOut := out.Bytes()
	firstLen := stratumV2FrameHeaderLen + int(readUint24LE(allOut[3:6]))
	msg, err := decodeStratumV2MiningWireFrame(allOut[:firstLen])
	if err != nil {
		t.Fatalf("decode open response frame: %v", err)
	}
	resp, ok := msg.(stratumV2WireOpenStandardMiningChannelSuccess)
	if !ok {
		// first frame is setup.success; decode next frame
		first, ok := msg.(stratumV2WireSetupConnectionSuccess)
		if !ok {
			t.Fatalf("response type=%T want setup/open success", msg)
		}
		if first.UsedVersion != 2 {
			t.Fatalf("setup used_version=%d want 2", first.UsedVersion)
		}
		msg, err = decodeStratumV2MiningWireFrame(allOut[firstLen:])
		if err != nil {
			t.Fatalf("decode open response frame after setup: %v", err)
		}
		resp, ok = msg.(stratumV2WireOpenStandardMiningChannelSuccess)
		if !ok {
			t.Fatalf("second response type=%T want stratumV2WireOpenStandardMiningChannelSuccess", msg)
		}
	}
	if resp.RequestID != 9 {
		t.Fatalf("resp.RequestID=%d want 9", resp.RequestID)
	}
	if resp.ChannelID == 0 {
		t.Fatalf("expected non-zero channel id")
	}
	ch, ok := c.submitMapper.channels[resp.ChannelID]
	if !ok {
		t.Fatalf("expected mapper channel registration for channel %d", resp.ChannelID)
	}
	if ch.WorkerName != mc.currentWorker() {
		t.Fatalf("mapper worker=%q want %q", ch.WorkerName, mc.currentWorker())
	}
	wantEx2Len := mc.cfg.Extranonce2Size
	if wantEx2Len <= 0 {
		wantEx2Len = 4
	}
	if len(ch.StandardExtranonce2) != wantEx2Len {
		t.Fatalf("standard extranonce2 len=%d want %d", len(ch.StandardExtranonce2), wantEx2Len)
	}
}

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

func TestSV2ConnReadLoopSkeleton_OpenThenRegisterJobThenSubmit(t *testing.T) {
	mc, job := newSubmitReadyMinerConnForModesTest(t)
	mc.conn = nopConn{}
	walletAddr, walletScript := generateTestWallet(t)
	mc.setWorkerWallet(mc.currentWorker(), walletAddr, walletScript)
	mc.stratumV1.notify.jobDifficulty[job.JobID] = 1e-30
	atomicStoreFloat64(&mc.difficulty, 1e-30)
	mc.shareTarget.Store(targetFromDifficulty(1e-30))

	var in bytes.Buffer
	var out bytes.Buffer
	c := &sv2Conn{
		mc:           mc,
		reader:       &in,
		writer:       &out,
		submitMapper: newStratumV2SubmitMapperState(),
	}

	openFrame, err := encodeStratumV2OpenStandardMiningChannelFrame(stratumV2WireOpenStandardMiningChannel{
		RequestID:    100,
		UserIdentity: mc.currentWorker(),
	})
	if err != nil {
		t.Fatalf("encode open standard: %v", err)
	}
	in.Write(testSV2SetupConnectionFrame(t))
	in.Write(openFrame)
	if err := c.handleReadLoop(); err != nil {
		t.Fatalf("handleReadLoop open: %v", err)
	}
	// read loop processed setup + open; decode second response frame (open success).
	all := out.Bytes()
	firstLen := stratumV2FrameHeaderLen + int(readUint24LE(all[3:6]))
	openMsg, err := decodeStratumV2MiningWireFrame(all[firstLen:])
	if err != nil {
		t.Fatalf("decode open response: %v", err)
	}
	openResp, ok := openMsg.(stratumV2WireOpenStandardMiningChannelSuccess)
	if !ok {
		t.Fatalf("open response type=%T want success", openMsg)
	}
	out.Reset()

	if err := c.writeStratumV2NewMiningJob(stratumV2WireNewMiningJob{
		ChannelID: openResp.ChannelID,
		JobID:     7,
		Version:   uint32(job.Template.Version),
	}, job.JobID); err != nil {
		t.Fatalf("write new mining job: %v", err)
	}
	out.Reset() // keep submit response decode simple in this test

	submitFrame, err := encodeStratumV2SubmitSharesStandardFrame(stratumV2WireSubmitSharesStandard{
		ChannelID:      openResp.ChannelID,
		SequenceNumber: 55,
		JobID:          7,
		Nonce:          1,
		NTime:          uint32(job.Template.CurTime),
		Version:        uint32(job.Template.Version),
	})
	if err != nil {
		t.Fatalf("encode submit: %v", err)
	}
	in.Write(submitFrame)
	if err := c.handleReadLoop(); err != nil {
		t.Fatalf("handleReadLoop submit: %v", err)
	}
	respMsg, err := decodeStratumV2SubmitWireFrame(out.Bytes())
	if err != nil {
		t.Fatalf("decode submit response: %v", err)
	}
	if _, ok := respMsg.(stratumV2WireSubmitSharesSuccess); !ok {
		t.Fatalf("submit response type=%T want stratumV2WireSubmitSharesSuccess", respMsg)
	}
}

func TestSV2ConnWriteStratumV2MiningUpdates_UpdateMapperAndWriteFrames(t *testing.T) {
	mc, job := newSubmitReadyMinerConnForModesTest(t)
	mc.conn = nopConn{}
	var out bytes.Buffer
	c := &sv2Conn{
		mc:           mc,
		writer:       &out,
		submitMapper: newStratumV2SubmitMapperState(),
	}
	c.submitMapper.registerChannel(7, stratumV2SubmitChannelMapping{WorkerName: mc.currentWorker()})
	prefixMsg := stratumV2WireSetExtranoncePrefix{
		ChannelID:        7,
		ExtranoncePrefix: []byte{0x11, 0x22},
	}
	if err := c.writeStratumV2SetExtranoncePrefix(prefixMsg); err != nil {
		t.Fatalf("write set extranonce prefix: %v", err)
	}
	msg, err := decodeStratumV2MiningWireFrame(out.Bytes())
	if err != nil {
		t.Fatalf("decode set extranonce prefix frame: %v", err)
	}
	if got, ok := msg.(stratumV2WireSetExtranoncePrefix); !ok || got.ChannelID != prefixMsg.ChannelID || len(got.ExtranoncePrefix) != len(prefixMsg.ExtranoncePrefix) {
		t.Fatalf("unexpected prefix frame decode: %#v", msg)
	}
	out.Reset()
	ch := c.submitMapper.channels[7]
	if got := ch.ExtranoncePrefix; len(got) != 2 || got[0] != 0x11 || got[1] != 0x22 {
		t.Fatalf("unexpected extranonce prefix: %x", got)
	}

	jobMsg := stratumV2WireNewMiningJob{
		ChannelID: 7,
		JobID:     1234,
		Version:   uint32(job.Template.Version),
	}
	if err := c.writeStratumV2NewMiningJob(jobMsg, job.JobID); err != nil {
		t.Fatalf("write new mining job: %v", err)
	}
	msg, err = decodeStratumV2MiningWireFrame(out.Bytes())
	if err != nil {
		t.Fatalf("decode new mining job frame: %v", err)
	}
	if got, ok := msg.(stratumV2WireNewMiningJob); !ok || got.ChannelID != jobMsg.ChannelID || got.JobID != jobMsg.JobID {
		t.Fatalf("unexpected new mining job frame decode: %#v", msg)
	}
	if got, ok := c.submitMapper.jobs[stratumV2ChannelJobKey{ChannelID: 7, WireJobID: 1234}]; !ok || got != job.JobID {
		t.Fatalf("job mapping not updated from framed traffic: got=%q ok=%v want=%q", got, ok, job.JobID)
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
