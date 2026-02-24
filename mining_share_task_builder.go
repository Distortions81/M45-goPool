package main

import "time"

// miningShareTaskInput is a protocol-neutral container for parsed submit data
// and policy state needed by the shared share-processing core.
type miningShareTaskInput struct {
	reqID            any
	job              *Job
	jobID            string
	workerName       string
	extranonce2      string
	extranonce2Len   uint16
	extranonce2Bytes [32]byte
	extranonce2Large []byte
	ntime            string
	ntimeVal         uint32
	nonce            string
	nonceVal         uint32
	useVersion       uint32
	scriptTime       int64
	policyReject     sharePolicyReject
	receivedAt       time.Time
}

func (mc *MinerConn) newMiningShareSubmissionTask(in miningShareTaskInput) submissionTask {
	versionHex := ""
	if debugLogging || verboseRuntimeLogging {
		versionHex = uint32ToHex8Lower(in.useVersion)
	}
	return submissionTask{
		mc:               mc,
		reqID:            in.reqID,
		job:              in.job,
		jobID:            in.jobID,
		workerName:       in.workerName,
		extranonce2:      in.extranonce2,
		extranonce2Len:   in.extranonce2Len,
		extranonce2Bytes: in.extranonce2Bytes,
		extranonce2Large: in.extranonce2Large,
		ntime:            in.ntime,
		ntimeVal:         in.ntimeVal,
		nonce:            in.nonce,
		nonceVal:         in.nonceVal,
		versionHex:       versionHex,
		useVersion:       in.useVersion,
		scriptTime:       in.scriptTime,
		policyReject:     in.policyReject,
		receivedAt:       in.receivedAt,
	}
}
