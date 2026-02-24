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
	mc           *MinerConn
	reader       io.Reader
	writer       io.Writer
	submitMapper *stratumV2SubmitMapperState
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
	wireMsg, err := decodeStratumV2SubmitWireFrame(frameBytes)
	if err != nil {
		return err
	}

	switch msg := wireMsg.(type) {
	case stratumV2WireSubmitSharesStandard:
		return c.handleSubmitSharesStandard(msg)
	case stratumV2WireSubmitSharesExtended:
		return c.handleSubmitSharesExtended(msg)
	default:
		// Success/Error are outbound in this skeleton; ignore if received.
		return nil
	}
}

func (c *sv2Conn) handleSubmitSharesStandard(msg stratumV2WireSubmitSharesStandard) error {
	if c.mc == nil || c.submitMapper == nil {
		return fmt.Errorf("sv2 submit skeleton not initialized")
	}
	norm, err := c.submitMapper.mapWireSubmitSharesStandard(msg)
	responder := &stratumV2SubmitWireResponder{w: c.writer, channelID: msg.ChannelID, sequenceNumber: msg.SequenceNumber}
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
	responder := &stratumV2SubmitWireResponder{w: c.writer, channelID: msg.ChannelID, sequenceNumber: msg.SequenceNumber}
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

type stratumV2SubmitWireResponder struct {
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
	// Not implemented in the skeleton yet. The submit core can request a target
	// update, but full SV2 SetTarget encoding/channel state is wired later.
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
