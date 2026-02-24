package main

import (
	"encoding/hex"
	"fmt"
	"time"
)

// stratumV2NormalizedSubmitShare is a temporary SV2-facing normalized submit
// request used before the binary SV2 codec/handlers are in place. Fields are
// represented in the same hex forms currently consumed by the shared parser
// path so we can reuse validation behavior and keep parity with v1.
type stratumV2NormalizedSubmitShare struct {
	RequestID        any
	WorkerName       string
	JobID            string
	Extranonce2Hex   string
	NTimeHex         string
	NonceHex         string
	SubmittedVersion uint32
}

type stratumV2ChannelJobKey struct {
	ChannelID uint32
	WireJobID uint32
}

// stratumV2SubmitMapperState resolves SV2 wire identifiers into the normalized
// submit shape currently consumed by the shared submit bridge.
type stratumV2SubmitMapperState struct {
	channels map[uint32]stratumV2SubmitChannelMapping
	jobs     map[stratumV2ChannelJobKey]string
}

type stratumV2SubmitChannelMapping struct {
	WorkerName string
	// For standard channels (header-only submits), the current bridge requires
	// a synthetic/full extranonce2 to reuse v1-style submit reconstruction.
	StandardExtranonce2 []byte
	// For extended channels, SV2 submit carries extranonce bytes which can be
	// prefixed by a negotiated implicit prefix (SetExtranoncePrefix).
	ExtranoncePrefix []byte
}

func newStratumV2SubmitMapperState() *stratumV2SubmitMapperState {
	return &stratumV2SubmitMapperState{
		channels: make(map[uint32]stratumV2SubmitChannelMapping),
		jobs:     make(map[stratumV2ChannelJobKey]string),
	}
}

func (m *stratumV2SubmitMapperState) registerChannel(channelID uint32, cfg stratumV2SubmitChannelMapping) {
	if m == nil {
		return
	}
	if m.channels == nil {
		m.channels = make(map[uint32]stratumV2SubmitChannelMapping)
	}
	m.channels[channelID] = cfg
}

func (m *stratumV2SubmitMapperState) registerJob(channelID uint32, wireJobID uint32, localJobID string) {
	if m == nil {
		return
	}
	if m.jobs == nil {
		m.jobs = make(map[stratumV2ChannelJobKey]string)
	}
	m.jobs[stratumV2ChannelJobKey{ChannelID: channelID, WireJobID: wireJobID}] = localJobID
}

func (m *stratumV2SubmitMapperState) mapWireSubmitSharesStandard(msg stratumV2WireSubmitSharesStandard) (stratumV2NormalizedSubmitShare, error) {
	if m == nil {
		return stratumV2NormalizedSubmitShare{}, fmt.Errorf("missing mapper state")
	}
	ch, ok := m.channels[msg.ChannelID]
	if !ok {
		return stratumV2NormalizedSubmitShare{}, fmt.Errorf("unknown channel id: %d", msg.ChannelID)
	}
	localJobID, ok := m.jobs[stratumV2ChannelJobKey{ChannelID: msg.ChannelID, WireJobID: msg.JobID}]
	if !ok || localJobID == "" {
		return stratumV2NormalizedSubmitShare{}, fmt.Errorf("unknown job mapping for channel=%d job=%d", msg.ChannelID, msg.JobID)
	}
	if len(ch.StandardExtranonce2) == 0 {
		return stratumV2NormalizedSubmitShare{}, fmt.Errorf("missing standard-channel extranonce2 mapping for channel=%d", msg.ChannelID)
	}
	return stratumV2NormalizedSubmitShare{
		RequestID:        msg.SequenceNumber, // temporary bridge key until SV2 response path uses channel+sequence directly
		WorkerName:       ch.WorkerName,
		JobID:            localJobID,
		Extranonce2Hex:   hex.EncodeToString(ch.StandardExtranonce2),
		NTimeHex:         uint32ToHex8Lower(msg.NTime),
		NonceHex:         uint32ToHex8Lower(msg.Nonce),
		SubmittedVersion: msg.Version,
	}, nil
}

func (m *stratumV2SubmitMapperState) mapWireSubmitSharesExtended(msg stratumV2WireSubmitSharesExtended) (stratumV2NormalizedSubmitShare, error) {
	if m == nil {
		return stratumV2NormalizedSubmitShare{}, fmt.Errorf("missing mapper state")
	}
	ch, ok := m.channels[msg.ChannelID]
	if !ok {
		return stratumV2NormalizedSubmitShare{}, fmt.Errorf("unknown channel id: %d", msg.ChannelID)
	}
	localJobID, ok := m.jobs[stratumV2ChannelJobKey{ChannelID: msg.ChannelID, WireJobID: msg.JobID}]
	if !ok || localJobID == "" {
		return stratumV2NormalizedSubmitShare{}, fmt.Errorf("unknown job mapping for channel=%d job=%d", msg.ChannelID, msg.JobID)
	}
	en2 := append(append([]byte(nil), ch.ExtranoncePrefix...), msg.Extranonce...)
	if len(en2) == 0 {
		return stratumV2NormalizedSubmitShare{}, fmt.Errorf("missing extranonce bytes for extended submit on channel=%d", msg.ChannelID)
	}
	return stratumV2NormalizedSubmitShare{
		RequestID:        msg.SequenceNumber, // temporary bridge key until SV2 response path uses channel+sequence directly
		WorkerName:       ch.WorkerName,
		JobID:            localJobID,
		Extranonce2Hex:   hex.EncodeToString(en2),
		NTimeHex:         uint32ToHex8Lower(msg.NTime),
		NonceHex:         uint32ToHex8Lower(msg.Nonce),
		SubmittedVersion: msg.Version,
	}, nil
}

func decodeStratumV2SubmitSharesMessage(msg stratumV2SubmitSharesMessage) (stratumV2NormalizedSubmitShare, error) {
	if msg.JobID == "" {
		return stratumV2NormalizedSubmitShare{}, fmt.Errorf("missing job id")
	}
	if len(msg.Extranonce2) == 0 {
		return stratumV2NormalizedSubmitShare{}, fmt.Errorf("missing extranonce2")
	}
	out := stratumV2NormalizedSubmitShare{
		RequestID:      msg.RequestID,
		WorkerName:     msg.WorkerName,
		JobID:          msg.JobID,
		Extranonce2Hex: hex.EncodeToString(msg.Extranonce2),
		NTimeHex:       uint32ToHex8Lower(msg.NTime),
		NonceHex:       uint32ToHex8Lower(msg.Nonce),
	}
	if msg.HasVersion {
		out.SubmittedVersion = msg.Version
	}
	return out, nil
}

// parseStratumV2SubmitShareToMiningShareTaskInput converts a normalized SV2
// submit into the protocol-neutral task input used by the shared submit core.
// For parity, it reuses the current v1 submit validation/preparation path.
func (mc *MinerConn) parseStratumV2SubmitShareToMiningShareTaskInput(req stratumV2NormalizedSubmitShare, now time.Time) (miningShareTaskInput, bool) {
	params := submitParams{
		worker:           req.WorkerName,
		jobID:            req.JobID,
		extranonce2:      req.Extranonce2Hex,
		ntime:            req.NTimeHex,
		nonce:            req.NonceHex,
		submittedVersion: req.SubmittedVersion,
	}

	var (
		task submissionTask
		ok   bool
	)
	if mc.useStrictSubmitPath() {
		task, ok = mc.prepareSubmissionTaskStrictParsed(req.RequestID, params, now)
	} else {
		task, ok = mc.prepareSubmissionTaskSoloParsed(req.RequestID, params, now)
	}
	if !ok {
		return miningShareTaskInput{}, false
	}
	return miningShareTaskInputFromSubmissionTask(task), true
}

// prepareStratumV2SubmissionTask builds a shared submissionTask for the
// current submit pipeline while attaching SV2 protocol response hooks.
func (mc *MinerConn) prepareStratumV2SubmissionTask(req stratumV2NormalizedSubmitShare, responder stratumV2SubmitResponder, now time.Time) (submissionTask, bool) {
	in, ok := mc.parseStratumV2SubmitShareToMiningShareTaskInput(req, now)
	if !ok {
		return submissionTask{}, false
	}
	task := mc.newMiningShareSubmissionTask(in)
	task.submitHooks = newStratumV2MiningShareSubmitHooks(mc, responder)
	return task, true
}

func (mc *MinerConn) prepareStratumV2SubmissionTaskFromMessage(msg stratumV2SubmitSharesMessage, responder stratumV2SubmitResponder) (submissionTask, bool) {
	now := msg.ReceivedAt
	if now.IsZero() {
		now = time.Now()
	}
	req, err := decodeStratumV2SubmitSharesMessage(msg)
	if err != nil {
		if responder != nil {
			responder.writeSubmitError(msg.RequestID, stratumErrCodeInvalidRequest, err.Error(), false)
		}
		return submissionTask{}, false
	}
	return mc.prepareStratumV2SubmissionTask(req, responder, now)
}
