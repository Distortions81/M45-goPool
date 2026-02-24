package main

import "time"

// miningShareSubmitHooks abstracts protocol-specific submit responses and
// follow-up notifications so the share processing core can be reused by
// multiple protocol frontends (e.g. Stratum v1, Stratum v2).
type miningShareSubmitHooks interface {
	rejectWithBan(reqID any, workerName string, reason submitRejectReason, errCode int, errMsg string, now time.Time)
	writeTrue(reqID any)
	writeInvalidRequest(reqID any, msg string)
	writeBanned(reqID any)
	writeLowDiff(reqID any, shareDiff float64, expectedDiff float64)
	sendDifficultyNotify(job *Job)
}
