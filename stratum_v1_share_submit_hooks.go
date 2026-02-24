package main

import (
	"fmt"
	"time"
)

type stratumV1MiningShareSubmitHooks struct {
	mc *MinerConn
}

func newStratumV1MiningShareSubmitHooks(mc *MinerConn) miningShareSubmitHooks {
	return stratumV1MiningShareSubmitHooks{mc: mc}
}

func (h stratumV1MiningShareSubmitHooks) rejectWithBan(reqID any, workerName string, reason shareRejectReason, errCode int, errMsg string, now time.Time) {
	h.mc.rejectShareWithBan(&StratumRequest{ID: reqID, Method: "mining.submit"}, workerName, reason, errCode, errMsg, now)
}

func (h stratumV1MiningShareSubmitHooks) writeTrue(reqID any) {
	h.mc.writeTrueResponse(reqID)
}

func (h stratumV1MiningShareSubmitHooks) writeInvalidRequest(reqID any, msg string) {
	h.mc.writeResponse(StratumResponse{ID: reqID, Result: false, Error: newStratumError(stratumErrCodeInvalidRequest, msg)})
}

func (h stratumV1MiningShareSubmitHooks) writeBanned(reqID any) {
	h.mc.writeResponse(StratumResponse{ID: reqID, Result: false, Error: h.mc.bannedStratumError()})
}

func (h stratumV1MiningShareSubmitHooks) writeLowDiff(reqID any, shareDiff float64, expectedDiff float64) {
	h.mc.writeResponse(StratumResponse{
		ID:     reqID,
		Result: false,
		Error:  []any{stratumErrCodeLowDiffShare, fmt.Sprintf("low difficulty share (%.6g expected %.6g)", shareDiff, expectedDiff), nil},
	})
}

func (h stratumV1MiningShareSubmitHooks) sendDifficultyNotify(job *Job) {
	h.mc.sendNotifyFor(job, true)
}
