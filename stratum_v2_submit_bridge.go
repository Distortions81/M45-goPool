package main

import "time"

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
