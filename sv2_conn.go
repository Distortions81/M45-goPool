package main

import (
	"fmt"
	"io"
	"strings"
	"time"
)

// sv2Conn is a minimal submit-path skeleton for the future SV2 connection
// handler. It currently supports reading framed submit messages, mapping them
// into the shared submit core, and writing submit success/error responses.
type sv2Conn struct {
	mc            *MinerConn
	reader        io.Reader
	writer        io.Writer
	submitMapper  *stratumV2SubmitMapperState
	nextChannelID uint32
	setupDone     bool
	setupVersion  uint16
	setupFlags    uint32
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
	frameBytes, err := readOneStratumV2FrameFromReader(c.reader)
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
	_, err = c.writer.Write(frame)
	return err
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
	_, err = c.writer.Write(frame)
	return err
}

func (c *sv2Conn) handleSubmitSharesStandard(msg stratumV2WireSubmitSharesStandard) error {
	if c.mc == nil || c.submitMapper == nil {
		return fmt.Errorf("sv2 submit skeleton not initialized")
	}
	norm, err := c.submitMapper.mapWireSubmitSharesStandard(msg)
	responder := &stratumV2SubmitWireResponder{mc: c.mc, w: c.writer, channelID: msg.ChannelID, sequenceNumber: msg.SequenceNumber}
	if err != nil {
		responder.writeSubmitError(msg.SequenceNumber, stratumErrCodeInvalidRequest, err.Error(), false)
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
	responder := &stratumV2SubmitWireResponder{mc: c.mc, w: c.writer, channelID: msg.ChannelID, sequenceNumber: msg.SequenceNumber}
	if err != nil {
		responder.writeSubmitError(msg.SequenceNumber, stratumErrCodeInvalidRequest, err.Error(), false)
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
	_, err = c.writer.Write(frame)
	return err
}

func (c *sv2Conn) writeSetupConnectionError(msg stratumV2WireSetupConnectionError) error {
	frame, err := encodeStratumV2SetupConnectionErrorFrame(msg)
	if err != nil {
		return err
	}
	_, err = c.writer.Write(frame)
	return err
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
	if _, err := c.writer.Write(frame); err != nil {
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
	if _, err := c.writer.Write(frame); err != nil {
		return err
	}
	c.applyStratumV2NewMiningJob(msg, localJobID)
	return nil
}

type stratumV2SubmitWireResponder struct {
	mc             *MinerConn
	w              io.Writer
	channelID      uint32
	sequenceNumber uint32
	err            error
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
	_, r.err = r.w.Write(frame)
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
	_, r.err = r.w.Write(frame)
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
	_, r.err = r.w.Write(frame)
}

func mapStratumErrorToSv2SubmitErrorCode(errCode int, msg string, banned bool) string {
	if banned {
		return "invalid-channel-id"
	}
	switch errCode {
	case stratumErrCodeLowDiffShare:
		return "difficulty-too-low"
	case stratumErrCodeJobNotFound:
		if strings.Contains(strings.ToLower(msg), "stale") {
			return "stale-share"
		}
		return "invalid-job-id"
	case stratumErrCodeDuplicateShare:
		return "stale-share"
	case stratumErrCodeInvalidRequest:
		if strings.Contains(strings.ToLower(msg), "job") {
			return "invalid-job-id"
		}
		return "invalid-job-id"
	default:
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
