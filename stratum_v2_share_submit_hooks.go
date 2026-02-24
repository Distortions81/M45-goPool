package main

import "time"

// stratumV2SubmitResponder is a placeholder responder boundary for the future
// SV2 wire implementation. It receives normalized outcomes from the shared
// submit processing core.
type stratumV2SubmitResponder interface {
	writeSubmitOK(reqID any)
	writeSubmitError(reqID any, errCode int, msg string, banned bool)
	sendSetTarget(job *Job)
}

type stratumV2MiningShareSubmitHooks struct {
	mc        *MinerConn
	responder stratumV2SubmitResponder
}

func newStratumV2MiningShareSubmitHooks(mc *MinerConn, responder stratumV2SubmitResponder) miningShareSubmitHooks {
	return stratumV2MiningShareSubmitHooks{mc: mc, responder: responder}
}

func (h stratumV2MiningShareSubmitHooks) rejectWithBan(reqID any, workerName string, reason shareRejectReason, errCode int, errMsg string, now time.Time) {
	// Mirror the v1 invalid-submission accounting and ban semantics while
	// delegating protocol encoding to the responder.
	reasonText := reason.String()
	h.mc.recordShare(workerName, false, 0, 0, reasonText, "", nil, now)
	banned, invalids := h.mc.noteInvalidSubmit(now, reason)
	if banned {
		h.mc.logBan(reasonText, workerName, invalids)
		h.responder.writeSubmitError(reqID, errCode, errMsg, true)
		return
	}
	if reason == rejectDuplicateShare {
		h.mc.maybeWarnDuplicateShares(now)
	} else {
		h.mc.maybeWarnApproachingInvalidBan(now, reason, invalids)
	}
	h.responder.writeSubmitError(reqID, errCode, errMsg, false)
}

func (h stratumV2MiningShareSubmitHooks) writeTrue(reqID any) {
	h.responder.writeSubmitOK(reqID)
}

func (h stratumV2MiningShareSubmitHooks) writeInvalidRequest(reqID any, msg string) {
	h.responder.writeSubmitError(reqID, stratumErrCodeInvalidRequest, msg, false)
}

func (h stratumV2MiningShareSubmitHooks) writeBanned(reqID any) {
	h.responder.writeSubmitError(reqID, stratumErrCodeUnauthorized, "banned", true)
}

func (h stratumV2MiningShareSubmitHooks) writeLowDiff(reqID any, shareDiff float64, expectedDiff float64) {
	h.responder.writeSubmitError(reqID, stratumErrCodeLowDiffShare, "low difficulty share", false)
}

func (h stratumV2MiningShareSubmitHooks) sendDifficultyNotify(job *Job) {
	h.responder.sendSetTarget(job)
}
