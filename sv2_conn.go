package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

// sv2Conn is a minimal submit-path skeleton for the future SV2 connection
// handler. It currently supports reading framed submit messages, mapping them
// into the shared submit core, and writing submit success/error responses.
type sv2Conn struct {
	mc              *MinerConn
	reader          io.Reader
	writer          io.Writer
	transport       sv2FrameTransport
	writeMu         sync.Mutex
	submitMapper    *stratumV2SubmitMapperState
	channelTargets  map[uint32][32]byte
	channelPrevHash map[uint32]stratumV2WireSetNewPrevHash
	nextChannelID   uint32
	nextWireJobID   uint32
	setupDone       bool
	setupVersion    uint16
	setupFlags      uint32
}

func newSV2ConnForMiner(mc *MinerConn, reader io.Reader, writer io.Writer) *sv2Conn {
	c := &sv2Conn{
		mc:           mc,
		reader:       reader,
		writer:       writer,
		transport:    &sv2PlainFrameTransport{r: reader, w: writer},
		submitMapper: newStratumV2SubmitMapperState(),
	}
	if mc != nil {
		mc.attachSV2Conn(c)
	}
	return c
}

func (c *sv2Conn) handleReadLoop() error {
	for {
		if err := c.handleOneFrame(); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

func (c *sv2Conn) handleOneFrame() error {
	frameBytes, err := c.readFrame()
	if err != nil {
		return err
	}
	wireMsg, err := decodeStratumV2MiningWireFrame(frameBytes)
	if err != nil {
		return err
	}

	switch msg := wireMsg.(type) {
	case stratumV2WireSetupConnection:
		return c.handleSetupConnection(msg)
	case stratumV2WireOpenStandardMiningChannel:
		return c.handleOpenStandardMiningChannel(msg)
	case stratumV2WireOpenExtendedMiningChannel:
		return c.handleOpenExtendedMiningChannel(msg)
	case stratumV2WireSetExtranoncePrefix:
		c.applyStratumV2SetExtranoncePrefix(msg)
		return nil
	case stratumV2WireSetNewPrevHash:
		c.applyStratumV2SetNewPrevHash(msg)
		return nil
	case stratumV2WireNewMiningJob:
		if localJobID, ok := c.defaultLocalJobIDForWireNewMiningJob(msg); ok {
			c.applyStratumV2NewMiningJob(msg, localJobID)
		}
		return nil
	case stratumV2WireSubmitSharesStandard:
		return c.handleSubmitSharesStandard(msg)
	case stratumV2WireSubmitSharesExtended:
		return c.handleSubmitSharesExtended(msg)
	default:
		// Success/Error and other server-originated messages are outbound in this
		// skeleton; ignore if received.
		return nil
	}
}

func (c *sv2Conn) readFrame() ([]byte, error) {
	if c == nil {
		return nil, io.EOF
	}
	if c.transport == nil {
		c.transport = &sv2PlainFrameTransport{r: c.reader, w: c.writer}
	}
	return c.transport.ReadFrame()
}

func (c *sv2Conn) writeFrame(frame []byte) error {
	if c == nil {
		return io.ErrClosedPipe
	}
	if c.transport == nil {
		c.transport = &sv2PlainFrameTransport{r: c.reader, w: c.writer}
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.transport.WriteFrame(frame)
}

func (c *sv2Conn) handleSetupConnection(msg stratumV2WireSetupConnection) error {
	const (
		sv2ProtocolMining = uint8(0)
		sv2VersionCurrent = uint16(2)
	)
	if msg.Protocol != sv2ProtocolMining {
		return c.writeSetupConnectionError(stratumV2WireSetupConnectionError{
			Flags:     0,
			ErrorCode: "unsupported-protocol",
		})
	}
	if msg.MinVersion > sv2VersionCurrent || msg.MaxVersion < sv2VersionCurrent {
		return c.writeSetupConnectionError(stratumV2WireSetupConnectionError{
			Flags:     0,
			ErrorCode: "protocol-version-mismatch",
		})
	}
	c.setupDone = true
	c.setupVersion = sv2VersionCurrent
	c.setupFlags = msg.Flags
	return c.writeSetupConnectionSuccess(stratumV2WireSetupConnectionSuccess{
		UsedVersion: sv2VersionCurrent,
		Flags:       0,
	})
}

func (c *sv2Conn) handleOpenStandardMiningChannel(msg stratumV2WireOpenStandardMiningChannel) error {
	if !c.setupDone {
		return c.writeSetupConnectionError(stratumV2WireSetupConnectionError{
			Flags:     0,
			ErrorCode: "setup-connection-required",
		})
	}
	if c.mc == nil {
		return fmt.Errorf("sv2 conn missing miner context")
	}
	if c.submitMapper == nil {
		c.submitMapper = newStratumV2SubmitMapperState()
	}
	chID := c.allocateChannelID()
	en2Size := c.mc.cfg.Extranonce2Size
	if en2Size <= 0 {
		en2Size = 4
	}
	c.submitMapper.registerChannel(chID, stratumV2SubmitChannelMapping{
		WorkerName:          msg.UserIdentity,
		StandardExtranonce2: make([]byte, en2Size),
	})
	resp := stratumV2WireOpenStandardMiningChannelSuccess{
		RequestID:      msg.RequestID,
		ChannelID:      chID,
		Target:         c.currentSV2TargetBytes(),
		GroupChannelID: chID,
	}
	frame, err := encodeStratumV2OpenStandardMiningChannelSuccessFrame(resp)
	if err != nil {
		return err
	}
	if err := c.writeFrame(frame); err != nil {
		return err
	}
	return c.writeCurrentJobBundleForChannel(chID)
}

func (c *sv2Conn) handleOpenExtendedMiningChannel(msg stratumV2WireOpenExtendedMiningChannel) error {
	if !c.setupDone {
		return c.writeSetupConnectionError(stratumV2WireSetupConnectionError{
			Flags:     0,
			ErrorCode: "setup-connection-required",
		})
	}
	if c.mc == nil {
		return fmt.Errorf("sv2 conn missing miner context")
	}
	if c.submitMapper == nil {
		c.submitMapper = newStratumV2SubmitMapperState()
	}
	chID := c.allocateChannelID()
	c.submitMapper.registerChannel(chID, stratumV2SubmitChannelMapping{
		WorkerName: msg.UserIdentity,
	})
	en2Size := uint16(c.mc.cfg.Extranonce2Size)
	if en2Size == 0 {
		en2Size = 4
	}
	if msg.MinExtranonceSize > 0 && msg.MinExtranonceSize > en2Size {
		en2Size = msg.MinExtranonceSize
	}
	resp := stratumV2WireOpenExtendedMiningChannelSuccess{
		RequestID:      msg.RequestID,
		ChannelID:      chID,
		Target:         c.currentSV2TargetBytes(),
		ExtranonceSize: en2Size,
		GroupChannelID: chID,
	}
	frame, err := encodeStratumV2OpenExtendedMiningChannelSuccessFrame(resp)
	if err != nil {
		return err
	}
	if err := c.writeFrame(frame); err != nil {
		return err
	}
	return c.writeCurrentJobBundleForChannel(chID)
}

func (c *sv2Conn) handleSubmitSharesStandard(msg stratumV2WireSubmitSharesStandard) error {
	if c.mc == nil || c.submitMapper == nil {
		return fmt.Errorf("sv2 submit skeleton not initialized")
	}
	norm, err := c.submitMapper.mapWireSubmitSharesStandard(msg)
	responder := &stratumV2SubmitWireResponder{mc: c.mc, w: c.writer, writeFrame: c.writeFrame, channelID: msg.ChannelID, sequenceNumber: msg.SequenceNumber}
	if !c.isActiveSV2SubmitJob(msg.ChannelID, msg.JobID) {
		responder.writeSubmitSV2ErrorCode(msg.SequenceNumber, "stale-share")
		return responder.err
	}
	if err != nil {
		responder.writeSubmitSV2ErrorCode(msg.SequenceNumber, c.classifySV2SubmitMappingError(msg.ChannelID, msg.JobID, err))
		return responder.err
	}
	task, ok := c.mc.prepareStratumV2SubmissionTask(norm, responder, time.Now())
	if !ok {
		// Current bridge may emit errors before hooks attach on parse failures.
		return responder.err
	}
	c.mc.processSubmissionTask(task)
	return responder.err
}

func (c *sv2Conn) handleSubmitSharesExtended(msg stratumV2WireSubmitSharesExtended) error {
	if c.mc == nil || c.submitMapper == nil {
		return fmt.Errorf("sv2 submit skeleton not initialized")
	}
	norm, err := c.submitMapper.mapWireSubmitSharesExtended(msg)
	responder := &stratumV2SubmitWireResponder{mc: c.mc, w: c.writer, writeFrame: c.writeFrame, channelID: msg.ChannelID, sequenceNumber: msg.SequenceNumber}
	if !c.isActiveSV2SubmitJob(msg.ChannelID, msg.JobID) {
		responder.writeSubmitSV2ErrorCode(msg.SequenceNumber, "stale-share")
		return responder.err
	}
	if err != nil {
		responder.writeSubmitSV2ErrorCode(msg.SequenceNumber, c.classifySV2SubmitMappingError(msg.ChannelID, msg.JobID, err))
		return responder.err
	}
	task, ok := c.mc.prepareStratumV2SubmissionTask(norm, responder, time.Now())
	if !ok {
		return responder.err
	}
	c.mc.processSubmissionTask(task)
	return responder.err
}

func readOneStratumV2FrameFromReader(r io.Reader) ([]byte, error) {
	var hdr [stratumV2FrameHeaderLen]byte
	n, err := io.ReadFull(r, hdr[:])
	if err != nil {
		if err == io.EOF && n == 0 {
			return nil, io.EOF
		}
		if err == io.ErrUnexpectedEOF && n == 0 {
			return nil, io.EOF
		}
		return nil, err
	}
	payloadLen := int(readUint24LE(hdr[3:6]))
	out := make([]byte, stratumV2FrameHeaderLen+payloadLen)
	copy(out[:stratumV2FrameHeaderLen], hdr[:])
	if payloadLen == 0 {
		return out, nil
	}
	if _, err := io.ReadFull(r, out[stratumV2FrameHeaderLen:]); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *sv2Conn) allocateChannelID() uint32 {
	if c.nextChannelID == 0 {
		c.nextChannelID = 1
	}
	id := c.nextChannelID
	c.nextChannelID++
	return id
}

func (c *sv2Conn) allocateWireJobID() uint32 {
	if c.nextWireJobID == 0 {
		c.nextWireJobID = 1
	}
	id := c.nextWireJobID
	c.nextWireJobID++
	return id
}

func (c *sv2Conn) currentSV2TargetBytes() [32]byte {
	if c == nil || c.mc == nil {
		return [32]byte{}
	}
	return uint256BEFromBigInt(c.mc.shareTargetOrDefault())
}

func (c *sv2Conn) writeSetupConnectionSuccess(msg stratumV2WireSetupConnectionSuccess) error {
	frame, err := encodeStratumV2SetupConnectionSuccessFrame(msg)
	if err != nil {
		return err
	}
	return c.writeFrame(frame)
}

func (c *sv2Conn) writeSetupConnectionError(msg stratumV2WireSetupConnectionError) error {
	frame, err := encodeStratumV2SetupConnectionErrorFrame(msg)
	if err != nil {
		return err
	}
	return c.writeFrame(frame)
}

func (c *sv2Conn) defaultLocalJobIDForWireNewMiningJob(msg stratumV2WireNewMiningJob) (string, bool) {
	if c == nil || c.mc == nil {
		return "", false
	}
	c.mc.jobMu.Lock()
	defer c.mc.jobMu.Unlock()
	if c.mc.lastJob != nil && c.mc.lastJob.JobID != "" {
		return c.mc.lastJob.JobID, true
	}
	if len(c.mc.activeJobs) == 1 {
		for _, job := range c.mc.activeJobs {
			if job != nil && job.JobID != "" {
				return job.JobID, true
			}
		}
	}
	return "", false
}

func (c *sv2Conn) applyStratumV2SetExtranoncePrefix(msg stratumV2WireSetExtranoncePrefix) {
	if c == nil {
		return
	}
	if c.submitMapper == nil {
		c.submitMapper = newStratumV2SubmitMapperState()
	}
	ch, ok := c.submitMapper.channels[msg.ChannelID]
	if !ok {
		return
	}
	ch.ExtranoncePrefix = append([]byte(nil), msg.ExtranoncePrefix...)
	c.submitMapper.registerChannel(msg.ChannelID, ch)
}

func (c *sv2Conn) applyStratumV2NewMiningJob(msg stratumV2WireNewMiningJob, localJobID string) {
	if c == nil || localJobID == "" {
		return
	}
	if c.submitMapper == nil {
		c.submitMapper = newStratumV2SubmitMapperState()
	}
	c.submitMapper.registerJob(msg.ChannelID, msg.JobID, localJobID)
}

func (c *sv2Conn) writeStratumV2SetExtranoncePrefix(msg stratumV2WireSetExtranoncePrefix) error {
	frame, err := encodeStratumV2SetExtranoncePrefixFrame(msg)
	if err != nil {
		return err
	}
	if err := c.writeFrame(frame); err != nil {
		return err
	}
	c.applyStratumV2SetExtranoncePrefix(msg)
	return nil
}

func (c *sv2Conn) writeStratumV2NewMiningJob(msg stratumV2WireNewMiningJob, localJobID string) error {
	frame, err := encodeStratumV2NewMiningJobFrame(msg)
	if err != nil {
		return err
	}
	if err := c.writeFrame(frame); err != nil {
		return err
	}
	c.applyStratumV2NewMiningJob(msg, localJobID)
	return nil
}

func (c *sv2Conn) applyStratumV2SetTarget(msg stratumV2WireSetTarget) {
	if c == nil {
		return
	}
	if c.channelTargets == nil {
		c.channelTargets = make(map[uint32][32]byte)
	}
	c.channelTargets[msg.ChannelID] = msg.MaximumTarget
}

func (c *sv2Conn) writeStratumV2SetTarget(msg stratumV2WireSetTarget) error {
	frame, err := encodeStratumV2SetTargetFrame(msg)
	if err != nil {
		return err
	}
	if err := c.writeFrame(frame); err != nil {
		return err
	}
	c.applyStratumV2SetTarget(msg)
	return nil
}

func (c *sv2Conn) applyStratumV2SetNewPrevHash(msg stratumV2WireSetNewPrevHash) {
	if c == nil {
		return
	}
	if c.channelPrevHash == nil {
		c.channelPrevHash = make(map[uint32]stratumV2WireSetNewPrevHash)
	}
	c.channelPrevHash[msg.ChannelID] = msg
}

func (c *sv2Conn) writeStratumV2SetNewPrevHash(msg stratumV2WireSetNewPrevHash) error {
	frame, err := encodeStratumV2SetNewPrevHashFrame(msg)
	if err != nil {
		return err
	}
	if err := c.writeFrame(frame); err != nil {
		return err
	}
	c.applyStratumV2SetNewPrevHash(msg)
	return nil
}

// writeStratumV2JobBundleForLocalJob emits the core mining updates needed for a
// channel to start hashing on a pool Job and keeps SV2 connection state/mappers
// in sync. Merkle root derivation for standard-channel NewMiningJob is left as
// a follow-up; this emits the consensus header fields and local/wire job mapping.
func (c *sv2Conn) writeStratumV2JobBundleForLocalJob(channelID uint32, wireJobID uint32, job *Job) error {
	if c == nil || job == nil {
		return fmt.Errorf("missing sv2 conn or job")
	}
	nbits, err := parseUint32BEHex(job.Template.Bits)
	if err != nil {
		return fmt.Errorf("parse job bits: %w", err)
	}
	prevHash := job.prevHashBytes
	if prevHash == ([32]byte{}) && job.Template.Previous != "" {
		if err := decodeHexToFixedBytes(prevHash[:], job.Template.Previous); err != nil {
			return fmt.Errorf("decode prevhash: %w", err)
		}
	}
	if err := c.writeStratumV2SetTarget(stratumV2WireSetTarget{
		ChannelID:     channelID,
		MaximumTarget: c.currentSV2TargetBytes(),
	}); err != nil {
		return err
	}
	if err := c.writeStratumV2NewMiningJob(stratumV2WireNewMiningJob{
		ChannelID: channelID,
		JobID:     wireJobID,
		Version:   uint32(job.Template.Version),
		// TODO: derive per-channel merkle root for standards/jobs from job data.
	}, job.JobID); err != nil {
		return err
	}
	return c.writeStratumV2SetNewPrevHash(stratumV2WireSetNewPrevHash{
		ChannelID: channelID,
		JobID:     wireJobID,
		PrevHash:  prevHash,
		MinNTime:  uint32(job.Template.CurTime),
		NBits:     nbits,
	})
}

func (c *sv2Conn) writeStratumV2JobBundleForAllChannels(job *Job) error {
	if c == nil || job == nil {
		return fmt.Errorf("missing sv2 conn or job")
	}
	if c.submitMapper == nil || len(c.submitMapper.channels) == 0 {
		return nil
	}
	channelIDs := make([]uint32, 0, len(c.submitMapper.channels))
	for id := range c.submitMapper.channels {
		channelIDs = append(channelIDs, id)
	}
	sort.Slice(channelIDs, func(i, j int) bool { return channelIDs[i] < channelIDs[j] })
	for _, channelID := range channelIDs {
		if err := c.writeStratumV2JobBundleForLocalJob(channelID, c.allocateWireJobID(), job); err != nil {
			return err
		}
	}
	return nil
}

func (c *sv2Conn) writeCurrentJobBundleForChannel(channelID uint32) error {
	if c == nil || c.mc == nil {
		return nil
	}
	var job *Job
	if c.mc.jobMgr != nil {
		job = c.mc.jobMgr.CurrentJob()
	}
	if job == nil {
		c.mc.jobMu.Lock()
		if c.mc.lastJob != nil {
			job = c.mc.lastJob
		} else if len(c.mc.activeJobs) == 1 {
			for _, j := range c.mc.activeJobs {
				job = j
			}
		}
		c.mc.jobMu.Unlock()
	}
	if job == nil {
		return nil
	}
	return c.writeStratumV2JobBundleForLocalJob(channelID, c.allocateWireJobID(), job)
}

func (c *sv2Conn) classifySV2SubmitMappingError(channelID uint32, wireJobID uint32, err error) string {
	if err == nil {
		return "invalid-job-id"
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "unknown channel id") {
		return "invalid-channel-id"
	}
	if strings.Contains(msg, "unknown job mapping") {
		if active, ok := c.channelPrevHash[channelID]; ok && active.JobID != 0 && active.JobID != wireJobID {
			return "stale-share"
		}
		return "invalid-job-id"
	}
	if strings.Contains(msg, "extranonce") {
		return "invalid-job-id"
	}
	return "invalid-job-id"
}

func (c *sv2Conn) isActiveSV2SubmitJob(channelID uint32, wireJobID uint32) bool {
	if c == nil || c.channelPrevHash == nil {
		return true
	}
	active, ok := c.channelPrevHash[channelID]
	if !ok || active.JobID == 0 {
		return true
	}
	return active.JobID == wireJobID
}

func writeStratumV2SetTargetToWriter(w io.Writer, msg stratumV2WireSetTarget) error {
	frame, err := encodeStratumV2SetTargetFrame(msg)
	if err != nil {
		return err
	}
	_, err = w.Write(frame)
	return err
}

type stratumV2SubmitWireResponder struct {
	mc             *MinerConn
	w              io.Writer
	writeFrame     func([]byte) error
	channelID      uint32
	sequenceNumber uint32
	err            error
}

func (r *stratumV2SubmitWireResponder) writeEncodedFrame(frame []byte) error {
	if r == nil {
		return io.ErrClosedPipe
	}
	if r.writeFrame != nil {
		return r.writeFrame(frame)
	}
	if r.w == nil {
		return io.ErrClosedPipe
	}
	_, err := r.w.Write(frame)
	return err
}

func (r *stratumV2SubmitWireResponder) writeSubmitOK(reqID any) {
	if r == nil || r.err != nil {
		return
	}
	seq := r.sequenceNumber
	if v, ok := reqID.(uint32); ok && v != 0 {
		seq = v
	}
	frame, err := encodeStratumV2SubmitSharesSuccessFrame(stratumV2WireSubmitSharesSuccess{
		ChannelID:               r.channelID,
		LastSequenceNumber:      seq,
		NewSubmitsAcceptedCount: 1,
		// TODO: plumb share difficulty sum from shared core outcome.
		NewSharesSum: 0,
	})
	if err != nil {
		r.err = err
		return
	}
	r.err = r.writeEncodedFrame(frame)
}

func (r *stratumV2SubmitWireResponder) writeSubmitError(reqID any, errCode int, msg string, banned bool) {
	if r == nil || r.err != nil {
		return
	}
	seq := r.sequenceNumber
	if v, ok := reqID.(uint32); ok && v != 0 {
		seq = v
	}
	frame, err := encodeStratumV2SubmitSharesErrorFrame(stratumV2WireSubmitSharesError{
		ChannelID:      r.channelID,
		SequenceNumber: seq,
		ErrorCode:      mapStratumErrorToSv2SubmitErrorCode(errCode, msg, banned),
	})
	if err != nil {
		r.err = err
		return
	}
	r.err = r.writeEncodedFrame(frame)
}

func (r *stratumV2SubmitWireResponder) writeSubmitSV2ErrorCode(reqID any, code string) {
	if r == nil || r.err != nil {
		return
	}
	seq := r.sequenceNumber
	if v, ok := reqID.(uint32); ok && v != 0 {
		seq = v
	}
	frame, err := encodeStratumV2SubmitSharesErrorFrame(stratumV2WireSubmitSharesError{
		ChannelID:      r.channelID,
		SequenceNumber: seq,
		ErrorCode:      code,
	})
	if err != nil {
		r.err = err
		return
	}
	r.err = r.writeEncodedFrame(frame)
}

func (r *stratumV2SubmitWireResponder) sendSetTarget(job *Job) {
	if r == nil || r.err != nil {
		return
	}
	var target [32]byte
	if r.mc != nil {
		target = uint256BEFromBigInt(r.mc.shareTargetOrDefault())
	}
	frame, err := encodeStratumV2SetTargetFrame(stratumV2WireSetTarget{
		ChannelID:     r.channelID,
		MaximumTarget: target,
	})
	if err != nil {
		r.err = err
		return
	}
	if err := r.writeEncodedFrame(frame); err != nil {
		r.err = err
		return
	}
}

func mapStratumErrorToSv2SubmitErrorCode(errCode int, msg string, banned bool) string {
	msgLower := strings.ToLower(strings.TrimSpace(msg))
	if banned {
		return "unauthorized"
	}
	switch errCode {
	case stratumErrCodeUnauthorized:
		return "unauthorized"
	case stratumErrCodeLowDiffShare:
		return "difficulty-too-low"
	case stratumErrCodeJobNotFound:
		if strings.Contains(msgLower, "stale") {
			return "stale-share"
		}
		return "invalid-job-id"
	case stratumErrCodeDuplicateShare:
		return "duplicate-share"
	case stratumErrCodeInvalidRequest:
		switch {
		case strings.Contains(msgLower, "job"):
			return "invalid-job-id"
		case strings.Contains(msgLower, "ntime"), strings.Contains(msgLower, "time"):
			return "invalid-timestamp"
		case strings.Contains(msgLower, "extranonce"):
			return "invalid-extranonce"
		case strings.Contains(msgLower, "version"):
			return "invalid-version"
		case strings.Contains(msgLower, "nonce"),
			strings.Contains(msgLower, "coinbase"),
			strings.Contains(msgLower, "merkle"):
			return "invalid-solution"
		case strings.Contains(msgLower, "unauthorized"), strings.Contains(msgLower, "banned"):
			return "unauthorized"
		}
		return "invalid-job-id"
	default:
		if strings.Contains(msgLower, "duplicate") {
			return "duplicate-share"
		}
		if strings.Contains(msgLower, "low diff") || strings.Contains(msgLower, "difficulty") {
			return "difficulty-too-low"
		}
		if strings.Contains(msgLower, "stale") {
			return "stale-share"
		}
		if strings.Contains(msgLower, "ntime") || strings.Contains(msgLower, "time") {
			return "invalid-timestamp"
		}
		if strings.Contains(msgLower, "extranonce") {
			return "invalid-extranonce"
		}
		if strings.Contains(msgLower, "version") {
			return "invalid-version"
		}
		return "invalid-job-id"
	}
}

// helper used by tests/debugging for peeking at encoded success sequence.
func decodeSv2SubmitSuccessLastSeq(frame []byte) (uint32, error) {
	msg, err := decodeStratumV2SubmitWireFrame(frame)
	if err != nil {
		return 0, err
	}
	s, ok := msg.(stratumV2WireSubmitSharesSuccess)
	if !ok {
		return 0, fmt.Errorf("not submitshares.success: %T", msg)
	}
	return s.LastSequenceNumber, nil
}

// helper used by tests/debugging for peeking at encoded error sequence.
func decodeSv2SubmitErrorSeq(frame []byte) (uint32, error) {
	msg, err := decodeStratumV2SubmitWireFrame(frame)
	if err != nil {
		return 0, err
	}
	e, ok := msg.(stratumV2WireSubmitSharesError)
	if !ok {
		return 0, fmt.Errorf("not submitshares.error: %T", msg)
	}
	return e.SequenceNumber, nil
}
