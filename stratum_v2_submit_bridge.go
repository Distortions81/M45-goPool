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
