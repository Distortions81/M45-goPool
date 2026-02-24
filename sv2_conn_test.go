package main

import (
	"bufio"
	"bytes"
	"net"
	"testing"
	"time"
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
	wantTarget := uint256LEFromBigInt(mc.shareTargetOrDefault())
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

func TestSV2ConnWriteStratumV2SetNewPrevHash_WritesFrameAndTracksState(t *testing.T) {
	mc, _ := newSubmitReadyMinerConnForModesTest(t)
	mc.conn = nopConn{}
	var out bytes.Buffer
	c := &sv2Conn{mc: mc, writer: &out}
	msg := stratumV2WireSetNewPrevHash{
		ChannelID: 55,
		JobID:     12,
		PrevHash:  [32]byte{0xaa, 0xbb},
		MinNTime:  100,
		NBits:     0x1d00ffff,
	}
	if err := c.writeStratumV2SetNewPrevHash(msg); err != nil {
		t.Fatalf("writeStratumV2SetNewPrevHash: %v", err)
	}
	dec, err := decodeStratumV2MiningWireFrame(out.Bytes())
	if err != nil {
		t.Fatalf("decode setnewprevhash frame: %v", err)
	}
	got, ok := dec.(stratumV2WireSetNewPrevHash)
	if !ok {
		t.Fatalf("decoded type=%T want stratumV2WireSetNewPrevHash", dec)
	}
	if got != msg {
		t.Fatalf("frame mismatch: got=%#v want=%#v", got, msg)
	}
	tracked, ok := c.channelPrevHash[msg.ChannelID]
	if !ok || tracked != msg {
		t.Fatalf("channel prevhash tracking mismatch: ok=%v tracked=%#v want=%#v", ok, tracked, msg)
	}
}

func TestSV2ConnWriteStratumV2JobBundleForLocalJob_WritesFramesAndSyncsState(t *testing.T) {
	mc, job := newSubmitReadyMinerConnForModesTest(t)
	mc.conn = nopConn{}
	mc.shareTarget.Store(targetFromDifficulty(3))

	var out bytes.Buffer
	c := &sv2Conn{
		mc:           mc,
		writer:       &out,
		submitMapper: newStratumV2SubmitMapperState(),
	}
	c.submitMapper.registerChannel(21, stratumV2SubmitChannelMapping{
		WorkerName:          mc.currentWorker(),
		StandardExtranonce2: []byte{0, 0, 0, 0},
	})

	if err := c.writeStratumV2JobBundleForLocalJob(21, 700, job); err != nil {
		t.Fatalf("writeStratumV2JobBundleForLocalJob: %v", err)
	}

	b := out.Bytes()
	// Frame 1: SetTarget
	n1 := stratumV2FrameHeaderLen + int(readUint24LE(b[3:6]))
	msg1, err := decodeStratumV2MiningWireFrame(b[:n1])
	if err != nil {
		t.Fatalf("decode frame1: %v", err)
	}
	if _, ok := msg1.(stratumV2WireSetTarget); !ok {
		t.Fatalf("frame1 type=%T want stratumV2WireSetTarget", msg1)
	}
	// Frame 2: NewMiningJob
	n2 := stratumV2FrameHeaderLen + int(readUint24LE(b[n1+3:n1+6]))
	msg2, err := decodeStratumV2MiningWireFrame(b[n1 : n1+n2])
	if err != nil {
		t.Fatalf("decode frame2: %v", err)
	}
	if got, ok := msg2.(stratumV2WireNewMiningJob); !ok || got.ChannelID != 21 || got.JobID != 700 {
		t.Fatalf("frame2 type/value unexpected: %#v", msg2)
	} else {
		wantMerkleRoot, err := c.standardSV2MerkleRootU256LE(21, job)
		if err != nil {
			t.Fatalf("standardSV2MerkleRootU256LE: %v", err)
		}
		if got.MerkleRoot != wantMerkleRoot {
			t.Fatalf("frame2 merkle root mismatch: got=%x want=%x", got.MerkleRoot, wantMerkleRoot)
		}
	}
	// Frame 3: SetNewPrevHash
	msg3, err := decodeStratumV2MiningWireFrame(b[n1+n2:])
	if err != nil {
		t.Fatalf("decode frame3: %v", err)
	}
	if got, ok := msg3.(stratumV2WireSetNewPrevHash); !ok || got.ChannelID != 21 || got.JobID != 700 {
		t.Fatalf("frame3 type/value unexpected: %#v", msg3)
	} else if got.PrevHash != reverseU256Bytes(job.prevHashBytes) {
		t.Fatalf("frame3 prev_hash mismatch: got=%x want=%x", got.PrevHash, reverseU256Bytes(job.prevHashBytes))
	}

	if got, ok := c.submitMapper.jobs[stratumV2ChannelJobKey{ChannelID: 21, WireJobID: 700}]; !ok || got != job.JobID {
		t.Fatalf("job mapping not synced: got=%q ok=%v want=%q", got, ok, job.JobID)
	}
	if _, ok := c.channelTargets[21]; !ok {
		t.Fatalf("expected channel target state for channel 21")
	}
	if ph, ok := c.channelPrevHash[21]; !ok || ph.JobID != 700 {
		t.Fatalf("expected channel prevhash state for channel 21 job 700, got=%#v ok=%v", ph, ok)
	}
}

func TestSV2ConnWriteStratumV2JobBundleForAllChannels_WritesPerChannelBundles(t *testing.T) {
	mc, job := newSubmitReadyMinerConnForModesTest(t)
	mc.conn = nopConn{}
	var out bytes.Buffer
	c := &sv2Conn{
		mc:           mc,
		writer:       &out,
		submitMapper: newStratumV2SubmitMapperState(),
	}
	c.submitMapper.registerChannel(2, stratumV2SubmitChannelMapping{WorkerName: mc.currentWorker(), StandardExtranonce2: []byte{0, 0, 0, 0}})
	c.submitMapper.registerChannel(5, stratumV2SubmitChannelMapping{WorkerName: mc.currentWorker(), StandardExtranonce2: []byte{0, 0, 0, 0}})

	if err := c.writeStratumV2JobBundleForAllChannels(job); err != nil {
		t.Fatalf("writeStratumV2JobBundleForAllChannels: %v", err)
	}
	if len(c.submitMapper.jobs) != 2 {
		t.Fatalf("expected 2 wire job mappings, got %d", len(c.submitMapper.jobs))
	}
	if len(c.channelTargets) != 2 {
		t.Fatalf("expected 2 channel targets, got %d", len(c.channelTargets))
	}
	if len(c.channelPrevHash) != 2 {
		t.Fatalf("expected 2 channel prevhash states, got %d", len(c.channelPrevHash))
	}
	if out.Len() == 0 {
		t.Fatalf("expected frames to be written")
	}
}

func TestMinerConnSendWorkForProtocol_UsesSV2WhenAttached(t *testing.T) {
	mc, job := newSubmitReadyMinerConnForModesTest(t)
	mc.conn = nopConn{}
	var out bytes.Buffer
	c := newSV2ConnForMiner(mc, nil, &out)
	c.submitMapper.registerChannel(9, stratumV2SubmitChannelMapping{WorkerName: mc.currentWorker(), StandardExtranonce2: []byte{0, 0, 0, 0}})

	mc.sendWorkForProtocol(job, true)

	if len(c.submitMapper.jobs) != 1 {
		t.Fatalf("expected sv2 job mapping update, got %d", len(c.submitMapper.jobs))
	}
	if out.Len() == 0 {
		t.Fatalf("expected sv2 frames written")
	}
}

func TestMinerConnAttachDetachSV2Conn_Lifecycle(t *testing.T) {
	mc, _ := newSubmitReadyMinerConnForModesTest(t)
	mc.conn = nopConn{}
	var out bytes.Buffer

	c := newSV2ConnForMiner(mc, nil, &out)
	if mc.sv2 != c {
		t.Fatalf("expected mc.sv2 to be attached")
	}
	if c.mc != mc {
		t.Fatalf("expected sv2Conn.mc backref to be set")
	}

	mc.detachSV2Conn()
	if mc.sv2 != nil {
		t.Fatalf("expected mc.sv2 to be nil after detach")
	}
	if c.mc != nil {
		t.Fatalf("expected sv2Conn.mc to be nil after detach")
	}
}

func TestMinerConnCleanup_DetachesSV2Conn(t *testing.T) {
	mc, _ := newSubmitReadyMinerConnForModesTest(t)
	mc.conn = nopConn{}
	var out bytes.Buffer
	c := newSV2ConnForMiner(mc, nil, &out)

	mc.cleanup()

	if mc.sv2 != nil {
		t.Fatalf("expected cleanup to clear mc.sv2")
	}
	if c.mc != nil {
		t.Fatalf("expected cleanup to clear sv2Conn backref")
	}
}

func TestNewSV2ConnForMiner_AttachesAndInitializesMapper(t *testing.T) {
	mc, _ := newSubmitReadyMinerConnForModesTest(t)
	mc.conn = nopConn{}
	var out bytes.Buffer
	c := newSV2ConnForMiner(mc, nil, &out)
	if c == nil {
		t.Fatalf("expected sv2 conn")
	}
	if c.submitMapper == nil {
		t.Fatalf("expected submitMapper to be initialized")
	}
	if mc.sv2 != c {
		t.Fatalf("expected miner conn attachment")
	}
}

func TestMinerConnServeSV2_RunsSetupAndCleansUp(t *testing.T) {
	mc, _ := newSubmitReadyMinerConnForModesTest(t)
	mc.conn = nopConn{}

	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	mc.conn = server
	mc.reader = bufio.NewReader(server)

	done := make(chan struct{})
	go func() {
		defer close(done)
		mc.serveSV2()
	}()

	setupFrame := testSV2SetupConnectionFrame(t)
	if _, err := client.Write(setupFrame); err != nil {
		t.Fatalf("client write setup: %v", err)
	}
	_ = client.Close() // terminate read loop
	<-done

	if mc.sv2 != nil {
		t.Fatalf("expected serveSV2 cleanup to detach sv2 conn")
	}
}

func TestMinerConnServeSV2_OpenChannelGetsImmediateJobBundle(t *testing.T) {
	mc, job := newSubmitReadyMinerConnForModesTest(t)
	mc.conn = nopConn{}
	walletAddr, walletScript := generateTestWallet(t)
	mc.setWorkerWallet(mc.currentWorker(), walletAddr, walletScript)

	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	mc.conn = server
	mc.reader = bufio.NewReader(server)

	done := make(chan struct{})
	go func() {
		defer close(done)
		mc.serveSV2()
	}()

	openFrame, err := encodeStratumV2OpenStandardMiningChannelFrame(stratumV2WireOpenStandardMiningChannel{
		RequestID:    77,
		UserIdentity: mc.currentWorker(),
	})
	if err != nil {
		t.Fatalf("encode open standard: %v", err)
	}
	readFrame := func() any {
		t.Helper()
		_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
		frame, err := readOneStratumV2FrameFromReader(client)
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		msg, err := decodeStratumV2MiningWireFrame(frame)
		if err != nil {
			t.Fatalf("decode frame: %v", err)
		}
		return msg
	}

	if _, err := client.Write(testSV2SetupConnectionFrame(t)); err != nil {
		t.Fatalf("client write setup: %v", err)
	}
	if msg := readFrame(); any(msg) == nil {
		t.Fatalf("expected setup success frame")
	} else if setup, ok := msg.(stratumV2WireSetupConnectionSuccess); !ok || setup.UsedVersion != 2 {
		t.Fatalf("frame1 type=%T want setup success v2", msg)
	}

	if _, err := client.Write(openFrame); err != nil {
		t.Fatalf("client write open: %v", err)
	}
	msg2 := readFrame()
	openResp, ok := msg2.(stratumV2WireOpenStandardMiningChannelSuccess)
	if !ok {
		t.Fatalf("frame2 type=%T want open standard success", msg2)
	}
	if openResp.RequestID != 77 || openResp.ChannelID == 0 {
		t.Fatalf("unexpected open response: %#v", openResp)
	}
	if !mc.stratumV1.authorized {
		t.Fatalf("expected sv2 open to mark miner authorized for shared submit path")
	}

	msg3 := readFrame()
	setTarget, ok := msg3.(stratumV2WireSetTarget)
	if !ok {
		t.Fatalf("frame3 type=%T want set target", msg3)
	}
	if setTarget.ChannelID != openResp.ChannelID {
		t.Fatalf("settarget channel=%d want %d", setTarget.ChannelID, openResp.ChannelID)
	}

	msg4 := readFrame()
	newJob, ok := msg4.(stratumV2WireNewMiningJob)
	if !ok {
		t.Fatalf("frame4 type=%T want new mining job", msg4)
	}
	if newJob.ChannelID != openResp.ChannelID {
		t.Fatalf("newjob channel=%d want %d", newJob.ChannelID, openResp.ChannelID)
	}

	msg5 := readFrame()
	prevhash, ok := msg5.(stratumV2WireSetNewPrevHash)
	if !ok {
		t.Fatalf("frame5 type=%T want set new prevhash", msg5)
	}
	if prevhash.ChannelID != openResp.ChannelID {
		t.Fatalf("prevhash channel=%d want %d", prevhash.ChannelID, openResp.ChannelID)
	}
	if prevhash.JobID != newJob.JobID {
		t.Fatalf("prevhash job_id=%d want %d", prevhash.JobID, newJob.JobID)
	}
	if prevhash.MinNTime != uint32(job.Template.CurTime) {
		t.Fatalf("prevhash min_ntime=%d want %d", prevhash.MinNTime, uint32(job.Template.CurTime))
	}

	_ = client.Close()
	<-done
	if mc.sv2 != nil {
		t.Fatalf("expected serveSV2 cleanup to detach sv2 conn")
	}
}

func TestSV2ConnReadLoopSkeleton_OpensStandardChannelAndRegistersMapper(t *testing.T) {
	mc, _ := newSubmitReadyMinerConnForModesTest(t)
	mc.conn = nopConn{}
	walletAddr, walletScript := generateTestWallet(t)
	mc.setWorkerWallet(mc.currentWorker(), walletAddr, walletScript)

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
		secondLen := stratumV2FrameHeaderLen + int(readUint24LE(allOut[firstLen+3:firstLen+6]))
		msg, err = decodeStratumV2MiningWireFrame(allOut[firstLen : firstLen+secondLen])
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
	if !mc.stratumV1.authorized {
		t.Fatalf("expected sv2 open to mark miner authorized")
	}
	wantEx2Len := mc.cfg.Extranonce2Size
	if wantEx2Len <= 0 {
		wantEx2Len = 4
	}
	if len(ch.StandardExtranonce2) != wantEx2Len {
		t.Fatalf("standard extranonce2 len=%d want %d", len(ch.StandardExtranonce2), wantEx2Len)
	}
	if len(c.submitMapper.jobs) == 0 {
		t.Fatalf("expected current-job mapping to be sent on open")
	}
	if _, ok := c.channelTargets[resp.ChannelID]; !ok {
		t.Fatalf("expected set_target state for opened channel")
	}
	if ph, ok := c.channelPrevHash[resp.ChannelID]; !ok || ph.JobID == 0 {
		t.Fatalf("expected set_new_prevhash state for opened channel, got=%#v ok=%v", ph, ok)
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
	secondLen := stratumV2FrameHeaderLen + int(readUint24LE(all[firstLen+3:firstLen+6]))
	openMsg, err := decodeStratumV2MiningWireFrame(all[firstLen : firstLen+secondLen])
	if err != nil {
		t.Fatalf("decode open response: %v", err)
	}
	openResp, ok := openMsg.(stratumV2WireOpenStandardMiningChannelSuccess)
	if !ok {
		t.Fatalf("open response type=%T want success", openMsg)
	}
	out.Reset() // discard setup/open + immediate job bundle frames

	activePrev, ok := c.channelPrevHash[openResp.ChannelID]
	if !ok || activePrev.JobID == 0 {
		t.Fatalf("expected active prevhash/job after open")
	}
	submitFrame, err := encodeStratumV2SubmitSharesStandardFrame(stratumV2WireSubmitSharesStandard{
		ChannelID:      openResp.ChannelID,
		SequenceNumber: 55,
		JobID:          activePrev.JobID,
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

func TestSV2ConnReadLoopSkeleton_OpenExtendedThenSubmitExtended(t *testing.T) {
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

	openFrame, err := encodeStratumV2OpenExtendedMiningChannelFrame(stratumV2WireOpenExtendedMiningChannel{
		stratumV2WireOpenStandardMiningChannel: stratumV2WireOpenStandardMiningChannel{
			RequestID:    101,
			UserIdentity: mc.currentWorker(),
		},
		MinExtranonceSize: 2,
	})
	if err != nil {
		t.Fatalf("encode open extended: %v", err)
	}
	in.Write(testSV2SetupConnectionFrame(t))
	in.Write(openFrame)
	if err := c.handleReadLoop(); err != nil {
		t.Fatalf("handleReadLoop open extended: %v", err)
	}
	all := out.Bytes()
	firstLen := stratumV2FrameHeaderLen + int(readUint24LE(all[3:6]))
	secondLen := stratumV2FrameHeaderLen + int(readUint24LE(all[firstLen+3:firstLen+6]))
	openMsg, err := decodeStratumV2MiningWireFrame(all[firstLen : firstLen+secondLen])
	if err != nil {
		t.Fatalf("decode open extended response: %v", err)
	}
	openResp, ok := openMsg.(stratumV2WireOpenExtendedMiningChannelSuccess)
	if !ok {
		t.Fatalf("open extended response type=%T want success", openMsg)
	}
	out.Reset()

	// Extended path requires prefix + submit extranonce to form full extranonce2
	// expected by the shared submit core (4 bytes in test fixtures).
	if err := c.writeStratumV2SetExtranoncePrefix(stratumV2WireSetExtranoncePrefix{
		ChannelID:        openResp.ChannelID,
		ExtranoncePrefix: []byte{0x00, 0x00},
	}); err != nil {
		t.Fatalf("write set extranonce prefix: %v", err)
	}
	out.Reset()
	if err := c.writeStratumV2NewMiningJob(stratumV2WireNewMiningJob{
		ChannelID: openResp.ChannelID,
		JobID:     7,
		Version:   uint32(job.Template.Version),
	}, job.JobID); err != nil {
		t.Fatalf("write new mining job: %v", err)
	}
	out.Reset()
	if err := c.writeStratumV2SetNewPrevHash(stratumV2WireSetNewPrevHash{
		ChannelID: openResp.ChannelID,
		JobID:     7,
		MinNTime:  uint32(job.Template.CurTime),
		NBits:     0x1d00ffff,
	}); err != nil {
		t.Fatalf("write set new prevhash: %v", err)
	}
	out.Reset()

	submitFrame, err := encodeStratumV2SubmitSharesExtendedFrame(stratumV2WireSubmitSharesExtended{
		ChannelID:      openResp.ChannelID,
		SequenceNumber: 56,
		JobID:          7,
		Nonce:          1,
		NTime:          uint32(job.Template.CurTime),
		Version:        uint32(job.Template.Version),
		Extranonce:     []byte{0x00, 0x00},
	})
	if err != nil {
		t.Fatalf("encode extended submit: %v", err)
	}
	in.Write(submitFrame)
	if err := c.handleReadLoop(); err != nil {
		t.Fatalf("handleReadLoop extended submit: %v", err)
	}
	respMsg, err := decodeStratumV2SubmitWireFrame(out.Bytes())
	if err != nil {
		t.Fatalf("decode extended submit response: %v", err)
	}
	if _, ok := respMsg.(stratumV2WireSubmitSharesSuccess); !ok {
		t.Fatalf("extended submit response type=%T want stratumV2WireSubmitSharesSuccess", respMsg)
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

func TestSV2ConnReadLoopSkeleton_IncomingSetNewPrevHash_TracksState(t *testing.T) {
	mc, _ := newSubmitReadyMinerConnForModesTest(t)
	mc.conn = nopConn{}
	var in bytes.Buffer
	var out bytes.Buffer
	msg := stratumV2WireSetNewPrevHash{
		ChannelID: 7,
		JobID:     99,
		PrevHash:  [32]byte{1, 2, 3},
		MinNTime:  123,
		NBits:     0x1d00ffff,
	}
	frame, err := encodeStratumV2SetNewPrevHashFrame(msg)
	if err != nil {
		t.Fatalf("encode setnewprevhash: %v", err)
	}
	in.Write(frame)
	c := &sv2Conn{mc: mc, reader: &in, writer: &out}
	if err := c.handleReadLoop(); err != nil {
		t.Fatalf("handleReadLoop: %v", err)
	}
	tracked, ok := c.channelPrevHash[msg.ChannelID]
	if !ok || tracked != msg {
		t.Fatalf("incoming setnewprevhash not tracked: ok=%v tracked=%#v want=%#v", ok, tracked, msg)
	}
	if out.Len() != 0 {
		t.Fatalf("did not expect outbound response for incoming setnewprevhash, got %d bytes", out.Len())
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
	if e.ErrorCode != "invalid-channel-id" {
		t.Fatalf("error_code=%q want invalid-channel-id", e.ErrorCode)
	}
}

func TestSV2ConnReadLoopSkeleton_UnknownJobOnNonActiveMapping_ReturnsStaleShare(t *testing.T) {
	mc, _ := newSubmitReadyMinerConnForModesTest(t)
	mc.conn = nopConn{}

	mapper := newStratumV2SubmitMapperState()
	mapper.registerChannel(10, stratumV2SubmitChannelMapping{
		WorkerName:          mc.currentWorker(),
		StandardExtranonce2: []byte{0x00, 0x00, 0x00, 0x00},
	})

	var in bytes.Buffer
	var out bytes.Buffer
	inFrame, err := encodeStratumV2SubmitSharesStandardFrame(stratumV2WireSubmitSharesStandard{
		ChannelID:      10,
		SequenceNumber: 88,
		JobID:          7, // no mapping
		Nonce:          1,
		NTime:          1,
		Version:        1,
	})
	if err != nil {
		t.Fatalf("encode submit frame: %v", err)
	}
	in.Write(inFrame)

	c := &sv2Conn{
		mc:           mc,
		reader:       &in,
		writer:       &out,
		submitMapper: mapper,
		channelPrevHash: map[uint32]stratumV2WireSetNewPrevHash{
			10: {ChannelID: 10, JobID: 99},
		},
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
	if e.ErrorCode != "stale-share" {
		t.Fatalf("error_code=%q want stale-share", e.ErrorCode)
	}
}

func TestSV2ConnReadLoopSkeleton_MappedOldJobOnNonActivePrevhash_ReturnsStaleShare(t *testing.T) {
	mc, job := newSubmitReadyMinerConnForModesTest(t)
	mc.conn = nopConn{}

	mapper := newStratumV2SubmitMapperState()
	mapper.registerChannel(10, stratumV2SubmitChannelMapping{
		WorkerName:          mc.currentWorker(),
		StandardExtranonce2: []byte{0x00, 0x00, 0x00, 0x00},
	})
	mapper.registerJob(10, 7, job.JobID) // old mapped job still known

	var in bytes.Buffer
	var out bytes.Buffer
	inFrame, err := encodeStratumV2SubmitSharesStandardFrame(stratumV2WireSubmitSharesStandard{
		ChannelID:      10,
		SequenceNumber: 89,
		JobID:          7,
		Nonce:          1,
		NTime:          uint32(job.Template.CurTime),
		Version:        uint32(job.Template.Version),
	})
	if err != nil {
		t.Fatalf("encode submit frame: %v", err)
	}
	in.Write(inFrame)

	c := &sv2Conn{
		mc:           mc,
		reader:       &in,
		writer:       &out,
		submitMapper: mapper,
		channelPrevHash: map[uint32]stratumV2WireSetNewPrevHash{
			10: {ChannelID: 10, JobID: 99}, // active job is different
		},
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
	if e.ErrorCode != "stale-share" {
		t.Fatalf("error_code=%q want stale-share", e.ErrorCode)
	}
}

func TestMapStratumErrorToSv2SubmitErrorCode(t *testing.T) {
	tests := []struct {
		name   string
		code   int
		msg    string
		banned bool
		want   string
	}{
		{name: "banned", code: stratumErrCodeUnauthorized, msg: "banned", banned: true, want: "unauthorized"},
		{name: "unauthorized code", code: stratumErrCodeUnauthorized, msg: "unauthorized", want: "unauthorized"},
		{name: "low diff", code: stratumErrCodeLowDiffShare, msg: "low difficulty share", want: "difficulty-too-low"},
		{name: "job not found", code: stratumErrCodeJobNotFound, msg: "job not found", want: "invalid-job-id"},
		{name: "stale job", code: stratumErrCodeJobNotFound, msg: "stale job", want: "stale-share"},
		{name: "duplicate", code: stratumErrCodeDuplicateShare, msg: "duplicate share", want: "duplicate-share"},
		{name: "invalid ntime", code: stratumErrCodeInvalidRequest, msg: "invalid ntime", want: "invalid-timestamp"},
		{name: "invalid extranonce", code: stratumErrCodeInvalidRequest, msg: "invalid extranonce2", want: "invalid-extranonce"},
		{name: "invalid version", code: stratumErrCodeInvalidRequest, msg: "invalid version mask", want: "invalid-version"},
		{name: "invalid nonce", code: stratumErrCodeInvalidRequest, msg: "invalid nonce", want: "invalid-solution"},
		{name: "invalid merkle", code: stratumErrCodeInvalidRequest, msg: "invalid merkle", want: "invalid-solution"},
		{name: "fallback duplicate by msg", code: 999, msg: "duplicate share", want: "duplicate-share"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := mapStratumErrorToSv2SubmitErrorCode(tc.code, tc.msg, tc.banned); got != tc.want {
				t.Fatalf("mapStratumErrorToSv2SubmitErrorCode(%d,%q,banned=%v)=%q want %q", tc.code, tc.msg, tc.banned, got, tc.want)
			}
		})
	}
}
