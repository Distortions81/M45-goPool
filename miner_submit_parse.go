package main

import (
	"fmt"
	"math/bits"
	"strings"
	"time"
	"unicode/utf8"
)

func trimSpaceFast(s string) string {
	if len(s) == 0 {
		return s
	}
	first := s[0]
	last := s[len(s)-1]
	if first < utf8.RuneSelf && last < utf8.RuneSelf && first > ' ' && last > ' ' {
		return s
	}
	return strings.TrimSpace(s)
}

func decodeExtranonce2Hex(extranonce2 string, validateFields bool, expectedSize int) ([32]byte, uint16, []byte, error) {
	var small [32]byte
	if validateFields && expectedSize > 0 && len(extranonce2) != expectedSize*2 {
		return small, 0, nil, fmt.Errorf("expected extranonce2 len %d, got %d", expectedSize*2, len(extranonce2))
	}
	if len(extranonce2)%2 != 0 {
		return small, 0, nil, fmt.Errorf("odd-length extranonce2 hex")
	}
	size := len(extranonce2) / 2
	if size <= len(small) {
		dst := small[:size]
		if err := decodeHexToFixedBytes(dst, extranonce2); err != nil {
			return small, 0, nil, err
		}
		return small, uint16(size), nil, nil
	}
	large := make([]byte, size)
	if err := decodeHexToFixedBytes(large, extranonce2); err != nil {
		return small, 0, nil, err
	}
	return small, uint16(size), large, nil
}

func decodeExtranonce2HexBytes(extranonce2 []byte, validateFields bool, expectedSize int) ([32]byte, uint16, []byte, error) {
	var small [32]byte
	if validateFields && expectedSize > 0 && len(extranonce2) != expectedSize*2 {
		return small, 0, nil, fmt.Errorf("expected extranonce2 len %d, got %d", expectedSize*2, len(extranonce2))
	}
	if len(extranonce2)%2 != 0 {
		return small, 0, nil, fmt.Errorf("odd-length extranonce2 hex")
	}
	size := len(extranonce2) / 2
	if size <= len(small) {
		dst := small[:size]
		if err := decodeHexToFixedBytesBytes(dst, extranonce2); err != nil {
			return small, 0, nil, err
		}
		return small, uint16(size), nil, nil
	}
	large := make([]byte, size)
	if err := decodeHexToFixedBytesBytes(large, extranonce2); err != nil {
		return small, 0, nil, err
	}
	return small, uint16(size), large, nil
}

// parseSubmitParams validates and extracts the core fields from a mining.submit
// request, recording and responding to any parameter errors. It returns params
// and ok=false when a response has already been sent.
func (mc *MinerConn) parseSubmitParams(req *StratumRequest, now time.Time) (submitParams, bool) {
	var out submitParams
	validateFields := mc.cfg.ShareCheckParamFormat

	if len(req.Params) < 5 || len(req.Params) > 6 {
		logger.Debug("submit invalid params", "remote", mc.id, "params", req.Params)
		mc.recordShare("", false, 0, 0, "invalid params", "", nil, now)
		mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(stratumErrCodeInvalidRequest, "invalid params")})
		return out, false
	}

	worker, ok := req.Params[0].(string)
	if !ok {
		mc.recordShare("", false, 0, 0, "invalid worker", "", nil, now)
		mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(stratumErrCodeInvalidRequest, "invalid worker")})
		return out, false
	}
	if validateFields {
		worker = trimSpaceFast(worker)
	}
	if validateFields && len(worker) == 0 {
		mc.recordShare("", false, 0, 0, "empty worker", "", nil, now)
		mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(stratumErrCodeInvalidRequest, "worker name required")})
		return out, false
	}
	if validateFields && len(worker) > maxWorkerNameLen {
		logger.Debug("submit rejected: worker name too long", "remote", mc.id, "len", len(worker))
		mc.recordShare("", false, 0, 0, "worker name too long", "", nil, now)
		mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(stratumErrCodeInvalidRequest, "worker name too long")})
		return out, false
	}

	jobID, ok := req.Params[1].(string)
	if !ok {
		mc.recordShare(worker, false, 0, 0, "invalid job id", "", nil, now)
		mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(stratumErrCodeInvalidRequest, "invalid job id")})
		return out, false
	}
	if validateFields {
		jobID = trimSpaceFast(jobID)
	}
	if validateFields && len(jobID) == 0 && mc.cfg.ShareRequireJobID {
		mc.recordShare(worker, false, 0, 0, "empty job id", "", nil, now)
		mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(stratumErrCodeInvalidRequest, "job id required")})
		return out, false
	}
	if validateFields && len(jobID) > maxJobIDLen {
		logger.Debug("submit rejected: job id too long", "remote", mc.id, "len", len(jobID))
		mc.recordShare(worker, false, 0, 0, "job id too long", "", nil, now)
		mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(stratumErrCodeInvalidRequest, "job id too long")})
		return out, false
	}
	extranonce2, ok := req.Params[2].(string)
	if !ok {
		mc.recordShare(worker, false, 0, 0, "invalid extranonce2", "", nil, now)
		mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(stratumErrCodeInvalidRequest, "invalid extranonce2")})
		return out, false
	}
	ntime, ok := req.Params[3].(string)
	if !ok {
		mc.recordShare(worker, false, 0, 0, "invalid ntime", "", nil, now)
		mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(stratumErrCodeInvalidRequest, "invalid ntime")})
		return out, false
	}
	nonce, ok := req.Params[4].(string)
	if !ok {
		mc.recordShare(worker, false, 0, 0, "invalid nonce", "", nil, now)
		mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(stratumErrCodeInvalidRequest, "invalid nonce")})
		return out, false
	}

	submittedVersion := uint32(0)
	if len(req.Params) == 6 {
		verStr, ok := req.Params[5].(string)
		if !ok {
			mc.recordShare(worker, false, 0, 0, "invalid version", "", nil, now)
			mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(stratumErrCodeInvalidRequest, "invalid version")})
			return out, false
		}
		if validateFields && len(verStr) == 0 {
			mc.recordShare(worker, false, 0, 0, "empty version", "", nil, now)
			mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(stratumErrCodeInvalidRequest, "version required")})
			return out, false
		}
		if validateFields && len(verStr) > maxVersionHexLen {
			logger.Debug("submit rejected: version too long", "remote", mc.id, "len", len(verStr))
			mc.recordShare(worker, false, 0, 0, "version too long", "", nil, now)
			mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(stratumErrCodeInvalidRequest, "version too long")})
			return out, false
		}
		verVal, err := parseUint32BEHex(verStr)
		if err != nil {
			if validateFields {
				mc.recordShare(worker, false, 0, 0, "invalid version", "", nil, now)
				mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(stratumErrCodeInvalidRequest, "invalid version")})
				return out, false
			}
			verVal = 0
		}
		submittedVersion = verVal
	}

	out.worker = worker
	out.jobID = jobID
	out.extranonce2 = extranonce2
	out.ntime = ntime
	out.nonce = nonce
	out.submittedVersion = submittedVersion
	return out, true
}

func (mc *MinerConn) parseSubmitParamsStrings(id any, params []string, now time.Time) (submitParams, bool) {
	var out submitParams
	validateFields := mc.cfg.ShareCheckParamFormat

	if len(params) < 5 || len(params) > 6 {
		logger.Debug("submit invalid params", "remote", mc.id, "params", params)
		mc.recordShare("", false, 0, 0, "invalid params", "", nil, now)
		mc.writeResponse(StratumResponse{ID: id, Result: false, Error: newStratumError(stratumErrCodeInvalidRequest, "invalid params")})
		return out, false
	}

	worker := params[0]
	if validateFields {
		worker = trimSpaceFast(worker)
	}
	if validateFields && len(worker) == 0 {
		mc.recordShare("", false, 0, 0, "empty worker", "", nil, now)
		mc.writeResponse(StratumResponse{ID: id, Result: false, Error: newStratumError(stratumErrCodeInvalidRequest, "worker name required")})
		return out, false
	}
	if validateFields && len(worker) > maxWorkerNameLen {
		logger.Debug("submit rejected: worker name too long", "remote", mc.id, "len", len(worker))
		mc.recordShare("", false, 0, 0, "worker name too long", "", nil, now)
		mc.writeResponse(StratumResponse{ID: id, Result: false, Error: newStratumError(stratumErrCodeInvalidRequest, "worker name too long")})
		return out, false
	}

	jobID := params[1]
	if validateFields {
		jobID = trimSpaceFast(jobID)
	}
	if validateFields && len(jobID) == 0 && mc.cfg.ShareRequireJobID {
		mc.recordShare(worker, false, 0, 0, "empty job id", "", nil, now)
		mc.writeResponse(StratumResponse{ID: id, Result: false, Error: newStratumError(stratumErrCodeInvalidRequest, "job id required")})
		return out, false
	}
	if validateFields && len(jobID) > maxJobIDLen {
		logger.Debug("submit rejected: job id too long", "remote", mc.id, "len", len(jobID))
		mc.recordShare(worker, false, 0, 0, "job id too long", "", nil, now)
		mc.writeResponse(StratumResponse{ID: id, Result: false, Error: newStratumError(stratumErrCodeInvalidRequest, "job id too long")})
		return out, false
	}

	extranonce2 := params[2]
	ntime := params[3]
	nonce := params[4]

	submittedVersion := uint32(0)
	if len(params) == 6 {
		verStr := params[5]
		if validateFields && len(verStr) == 0 {
			mc.recordShare(worker, false, 0, 0, "empty version", "", nil, now)
			mc.writeResponse(StratumResponse{ID: id, Result: false, Error: newStratumError(stratumErrCodeInvalidRequest, "version required")})
			return out, false
		}
		if validateFields && len(verStr) > maxVersionHexLen {
			logger.Debug("submit rejected: version too long", "remote", mc.id, "len", len(verStr))
			mc.recordShare(worker, false, 0, 0, "version too long", "", nil, now)
			mc.writeResponse(StratumResponse{ID: id, Result: false, Error: newStratumError(stratumErrCodeInvalidRequest, "version too long")})
			return out, false
		}
		verVal, err := parseUint32BEHex(verStr)
		if err != nil {
			if validateFields {
				mc.recordShare(worker, false, 0, 0, "invalid version", "", nil, now)
				mc.writeResponse(StratumResponse{ID: id, Result: false, Error: newStratumError(stratumErrCodeInvalidRequest, "invalid version")})
				return out, false
			}
			verVal = 0
		}
		submittedVersion = verVal
	}

	out.worker = worker
	out.jobID = jobID
	out.extranonce2 = extranonce2
	out.ntime = ntime
	out.nonce = nonce
	out.submittedVersion = submittedVersion
	return out, true
}

func (mc *MinerConn) prepareSubmissionTaskFastBytes(reqID any, workerB, jobIDB, extranonce2B, ntimeB, nonceB, versionB []byte, haveVersion bool, now time.Time) (submissionTask, bool) {
	// Allocate only for worker/job_id; keep hex fields in bytes and pre-parse
	// ntime/nonce/version to uint32.
	validateFields := mc.cfg.ShareCheckParamFormat

	worker := string(workerB)
	if validateFields {
		worker = trimSpaceFast(worker)
	}
	if validateFields && len(worker) == 0 {
		mc.recordShare("", false, 0, 0, "empty worker", "", nil, now)
		mc.writeResponse(StratumResponse{ID: reqID, Result: false, Error: newStratumError(stratumErrCodeInvalidRequest, "worker name required")})
		return submissionTask{}, false
	}
	if validateFields && len(worker) > maxWorkerNameLen {
		logger.Debug("submit rejected: worker name too long", "remote", mc.id, "len", len(worker))
		mc.recordShare("", false, 0, 0, "worker name too long", "", nil, now)
		mc.writeResponse(StratumResponse{ID: reqID, Result: false, Error: newStratumError(stratumErrCodeInvalidRequest, "worker name too long")})
		return submissionTask{}, false
	}

	jobID := string(jobIDB)
	if validateFields {
		jobID = trimSpaceFast(jobID)
	}
	if validateFields && len(jobID) == 0 && mc.cfg.ShareRequireJobID {
		mc.recordShare(worker, false, 0, 0, "empty job id", "", nil, now)
		mc.writeResponse(StratumResponse{ID: reqID, Result: false, Error: newStratumError(stratumErrCodeInvalidRequest, "job id required")})
		return submissionTask{}, false
	}
	if validateFields && len(jobID) > maxJobIDLen {
		logger.Debug("submit rejected: job id too long", "remote", mc.id, "len", len(jobID))
		mc.recordShare(worker, false, 0, 0, "job id too long", "", nil, now)
		mc.writeResponse(StratumResponse{ID: reqID, Result: false, Error: newStratumError(stratumErrCodeInvalidRequest, "job id too long")})
		return submissionTask{}, false
	}

	submittedVersion := uint32(0)
	if haveVersion {
		if validateFields && len(versionB) == 0 {
			mc.recordShare(worker, false, 0, 0, "empty version", "", nil, now)
			mc.writeResponse(StratumResponse{ID: reqID, Result: false, Error: newStratumError(stratumErrCodeInvalidRequest, "version required")})
			return submissionTask{}, false
		}
		if validateFields && len(versionB) > maxVersionHexLen {
			logger.Debug("submit rejected: version too long", "remote", mc.id, "len", len(versionB))
			mc.recordShare(worker, false, 0, 0, "version too long", "", nil, now)
			mc.writeResponse(StratumResponse{ID: reqID, Result: false, Error: newStratumError(stratumErrCodeInvalidRequest, "version too long")})
			return submissionTask{}, false
		}
		verVal, err := parseUint32BEHexBytes(versionB)
		if err != nil {
			if validateFields {
				mc.recordShare(worker, false, 0, 0, "invalid version", "", nil, now)
				mc.writeResponse(StratumResponse{ID: reqID, Result: false, Error: newStratumError(stratumErrCodeInvalidRequest, "invalid version")})
				return submissionTask{}, false
			}
			verVal = 0
		}
		submittedVersion = verVal
	}

	params := submitParams{
		worker:           worker,
		jobID:            jobID,
		submittedVersion: submittedVersion,
	}

	return mc.prepareSubmissionTaskFromParsedBytes(reqID, params, extranonce2B, ntimeB, nonceB, now)
}

// prepareSubmissionTask validates a mining.submit request and, if valid, returns
// a fully-populated submissionTask. On any validation failure it writes the
// appropriate Stratum response and returns ok=false.
//
// This helper exists so benchmarks can include submit parsing/validation while
// still exercising the core share-processing path without extra goroutine
// scheduling noise.
func (mc *MinerConn) prepareSubmissionTask(req *StratumRequest, now time.Time) (submissionTask, bool) {
	params, ok := mc.parseSubmitParams(req, now)
	if !ok {
		return submissionTask{}, false
	}
	return mc.prepareSubmissionTaskFromParsed(req.ID, params, now)
}

func (mc *MinerConn) prepareSubmissionTaskFromParsedBytes(reqID any, params submitParams, extranonce2B, ntimeB, nonceB []byte, now time.Time) (submissionTask, bool) {
	worker := params.worker
	jobID := params.jobID
	submittedVersion := params.submittedVersion
	validateFields := mc.cfg.ShareCheckParamFormat

	if mc.cfg.ShareRequireAuthorizedConnection && !mc.authorized {
		logger.Debug("submit rejected: unauthorized", "remote", mc.id)
		mc.recordShare(worker, false, 0, 0, "unauthorized", "", nil, now)
		if mc.metrics != nil {
			mc.metrics.RecordSubmitError("unauthorized")
		}
		mc.writeResponse(StratumResponse{ID: reqID, Result: false, Error: newStratumError(stratumErrCodeUnauthorized, "unauthorized")})
		return submissionTask{}, false
	}

	authorizedWorker := mc.currentWorker()
	submitWorker := worker
	if mc.cfg.ShareRequireAuthorizedConnection && mc.cfg.ShareRequireWorkerMatch && authorizedWorker != "" && submitWorker != authorizedWorker {
		logger.Warn("submit rejected: worker mismatch", "remote", mc.id, "authorized", authorizedWorker, "submitted", submitWorker)
		mc.recordShare(authorizedWorker, false, 0, 0, "unauthorized worker", "", nil, now)
		if mc.metrics != nil {
			mc.metrics.RecordSubmitError("worker_mismatch")
		}
		mc.writeResponse(StratumResponse{ID: reqID, Result: false, Error: newStratumError(stratumErrCodeUnauthorized, "unauthorized")})
		return submissionTask{}, false
	}

	workerName := authorizedWorker
	if workerName == "" {
		workerName = worker
	}
	if mc.isBanned(now) {
		until, reason, _ := mc.banDetails()
		logger.Warn("submit rejected: banned", "miner", mc.minerName(workerName), "ban_until", until, "reason", reason)
		if mc.metrics != nil {
			mc.metrics.RecordSubmitError("banned")
		}
		mc.writeResponse(StratumResponse{ID: reqID, Result: false, Error: mc.bannedStratumError()})
		return submissionTask{}, false
	}

	job, curLast, curPrevHash, curHeight, ntimeBounds, notifiedScriptTime, ok := mc.jobForIDWithLast(jobID)
	usedFallbackJob := false
	if !ok || job == nil {
		if shareJobFreshnessChecksJobID(mc.cfg.ShareJobFreshnessMode) {
			logger.Debug("submit rejected: stale job", "remote", mc.id, "job", jobID)
			mc.rejectShareWithBan(&StratumRequest{ID: reqID, Method: "mining.submit"}, workerName, rejectStaleJob, stratumErrCodeJobNotFound, "job not found", now)
			return submissionTask{}, false
		}
		if curLast == nil {
			logger.Debug("submit rejected: no fallback job available", "remote", mc.id, "job", jobID)
			mc.rejectShareWithBan(&StratumRequest{ID: reqID, Method: "mining.submit"}, workerName, rejectStaleJob, stratumErrCodeJobNotFound, "job not found", now)
			return submissionTask{}, false
		}
		job = curLast
		usedFallbackJob = true
		if notifiedScriptTime == 0 {
			notifiedScriptTime = mc.scriptTimeForJob(job.JobID, job.ScriptTime)
		}
	}

	policyReject := submitPolicyReject{reason: rejectUnknown}
	if usedFallbackJob {
		// Even when job-id freshness checks are disabled, classify non-block
		// shares for unknown/expired job IDs as stale rather than lowdiff.
		policyReject = submitPolicyReject{reason: rejectStaleJob, errCode: stratumErrCodeJobNotFound, errMsg: "job not found"}
	}
	if shareJobFreshnessChecksPrevhash(mc.cfg.ShareJobFreshnessMode) && curLast != nil && (curPrevHash != job.Template.Previous || curHeight != job.Template.Height) {
		logger.Warn("submit: stale job mismatch (policy)", "remote", mc.id, "job", jobID, "expected_prev", job.Template.Previous, "expected_height", job.Template.Height, "current_prev", curPrevHash, "current_height", curHeight)
		policyReject = submitPolicyReject{reason: rejectStaleJob, errCode: stratumErrCodeJobNotFound, errMsg: "job not found"}
	}

	en2Small, en2Len, en2Large, err := decodeExtranonce2HexBytes(extranonce2B, validateFields, job.Extranonce2Size)
	if err != nil {
		logger.Debug("submit bad extranonce2", "remote", mc.id, "error", err)
		mc.rejectShareWithBan(&StratumRequest{ID: reqID, Method: "mining.submit"}, workerName, rejectInvalidExtranonce2, stratumErrCodeInvalidRequest, "invalid extranonce2", now)
		return submissionTask{}, false
	}

	if validateFields && len(ntimeB) != 8 {
		logger.Debug("submit invalid ntime length", "remote", mc.id, "len", len(ntimeB))
		mc.rejectShareWithBan(&StratumRequest{ID: reqID, Method: "mining.submit"}, workerName, rejectInvalidNTime, stratumErrCodeInvalidRequest, "invalid ntime", now)
		return submissionTask{}, false
	}
	ntimeVal, err := parseUint32BEHexBytes(ntimeB)
	if err != nil {
		logger.Debug("submit bad ntime", "remote", mc.id, "error", err)
		mc.rejectShareWithBan(&StratumRequest{ID: reqID, Method: "mining.submit"}, workerName, rejectInvalidNTime, stratumErrCodeInvalidRequest, "invalid ntime", now)
		return submissionTask{}, false
	}
	minNTime := ntimeBounds.min
	maxNTime := ntimeBounds.max
	if mc.cfg.ShareCheckNTimeWindow && (int64(ntimeVal) < minNTime || int64(ntimeVal) > maxNTime) {
		logger.Warn("submit ntime outside window (policy)", "remote", mc.id, "ntime", ntimeVal, "min", minNTime, "max", maxNTime)
		if policyReject.reason == rejectUnknown {
			policyReject = submitPolicyReject{reason: rejectInvalidNTime, errCode: stratumErrCodeInvalidRequest, errMsg: "invalid ntime"}
		}
	}

	if validateFields && len(nonceB) != 8 {
		logger.Debug("submit invalid nonce length", "remote", mc.id, "len", len(nonceB))
		mc.rejectShareWithBan(&StratumRequest{ID: reqID, Method: "mining.submit"}, workerName, rejectInvalidNonce, stratumErrCodeInvalidRequest, "invalid nonce", now)
		return submissionTask{}, false
	}
	nonceVal, err := parseUint32BEHexBytes(nonceB)
	if err != nil {
		logger.Debug("submit bad nonce", "remote", mc.id, "error", err)
		mc.rejectShareWithBan(&StratumRequest{ID: reqID, Method: "mining.submit"}, workerName, rejectInvalidNonce, stratumErrCodeInvalidRequest, "invalid nonce", now)
		return submissionTask{}, false
	}

	baseVersion := uint32(job.Template.Version)
	useVersion := baseVersion
	versionDiff := uint32(0)
	if submittedVersion != 0 {
		if submittedVersion&^mc.versionMask == 0 {
			useVersion = baseVersion ^ submittedVersion
			versionDiff = submittedVersion
		} else {
			useVersion = submittedVersion
			versionDiff = useVersion ^ baseVersion
		}
	}

	extranonce2 := ""
	ntime := ""
	nonce := ""
	versionHex := ""
	if debugLogging || verboseRuntimeLogging {
		extranonce2 = string(extranonce2B)
		ntime = string(ntimeB)
		nonce = string(nonceB)
		versionHex = uint32ToHex8Lower(useVersion)
	}

	if mc.cfg.ShareCheckVersionRolling && versionDiff != 0 {
		maskedDiff := versionDiff & mc.versionMask

		if !mc.versionRoll {
			logger.Warn("submit version rolling disabled (policy)", "remote", mc.id, "diff", uint32ToHex8Lower(versionDiff))
			if policyReject.reason == rejectUnknown {
				policyReject = submitPolicyReject{reason: rejectInvalidVersion, errCode: stratumErrCodeInvalidRequest, errMsg: "version rolling not enabled"}
			}
		}

		if versionDiff&^mc.versionMask != 0 {
			logger.Warn("submit version outside mask (policy)", "remote", mc.id, "version", uint32ToHex8Lower(useVersion), "mask", uint32ToHex8Lower(mc.versionMask))
			if policyReject.reason == rejectUnknown {
				policyReject = submitPolicyReject{reason: rejectInvalidVersionMask, errCode: stratumErrCodeInvalidRequest, errMsg: "invalid version mask"}
			}
		}

		if mc.minVerBits > 0 {
			usedBits := bits.OnesCount32(maskedDiff)
			if usedBits < mc.minVerBits {
				if !mc.cfg.ShareAllowDegradedVersionBits {
					logger.Warn("submit insufficient version rolling bits (policy)", "remote", mc.id, "version", uint32ToHex8Lower(useVersion), "required_bits", mc.minVerBits)
					if policyReject.reason == rejectUnknown {
						policyReject = submitPolicyReject{reason: rejectInsufficientVersionBits, errCode: stratumErrCodeInvalidRequest, errMsg: "insufficient version bits"}
					}
				} else {
					logger.Warn("submit: miner operating in degraded version rolling mode (allowed by BIP310)",
						"remote", mc.id, "version", uint32ToHex8Lower(useVersion),
						"used_bits", usedBits,
						"negotiated_minimum", mc.minVerBits)
				}
			}
		}
	}

	task := submissionTask{
		mc:               mc,
		reqID:            reqID,
		job:              job,
		jobID:            jobID,
		workerName:       workerName,
		extranonce2:      extranonce2,
		extranonce2Len:   en2Len,
		extranonce2Bytes: en2Small,
		extranonce2Large: en2Large,
		ntime:            ntime,
		ntimeVal:         ntimeVal,
		nonce:            nonce,
		nonceVal:         nonceVal,
		versionHex:       versionHex,
		useVersion:       useVersion,
		scriptTime:       notifiedScriptTime,
		policyReject:     policyReject,
		receivedAt:       now,
	}
	return task, true
}

func (mc *MinerConn) prepareSubmissionTaskFromParsed(reqID any, params submitParams, now time.Time) (submissionTask, bool) {
	worker := params.worker
	jobID := params.jobID
	extranonce2 := params.extranonce2
	ntime := params.ntime
	nonce := params.nonce
	submittedVersion := params.submittedVersion
	validateFields := mc.cfg.ShareCheckParamFormat

	if mc.cfg.ShareRequireAuthorizedConnection && !mc.authorized {
		logger.Debug("submit rejected: unauthorized", "remote", mc.id)
		mc.recordShare(worker, false, 0, 0, "unauthorized", "", nil, now)
		if mc.metrics != nil {
			mc.metrics.RecordSubmitError("unauthorized")
		}
		mc.writeResponse(StratumResponse{ID: reqID, Result: false, Error: newStratumError(stratumErrCodeUnauthorized, "unauthorized")})
		return submissionTask{}, false
	}

	authorizedWorker := mc.currentWorker()
	submitWorker := worker
	if mc.cfg.ShareRequireAuthorizedConnection && mc.cfg.ShareRequireWorkerMatch && authorizedWorker != "" && submitWorker != authorizedWorker {
		logger.Warn("submit rejected: worker mismatch", "remote", mc.id, "authorized", authorizedWorker, "submitted", submitWorker)
		mc.recordShare(authorizedWorker, false, 0, 0, "unauthorized worker", "", nil, now)
		if mc.metrics != nil {
			mc.metrics.RecordSubmitError("worker_mismatch")
		}
		mc.writeResponse(StratumResponse{ID: reqID, Result: false, Error: newStratumError(stratumErrCodeUnauthorized, "unauthorized")})
		return submissionTask{}, false
	}

	workerName := authorizedWorker
	if workerName == "" {
		workerName = worker
	}
	if mc.isBanned(now) {
		until, reason, _ := mc.banDetails()
		logger.Warn("submit rejected: banned", "miner", mc.minerName(workerName), "ban_until", until, "reason", reason)
		if mc.metrics != nil {
			mc.metrics.RecordSubmitError("banned")
		}
		mc.writeResponse(StratumResponse{ID: reqID, Result: false, Error: mc.bannedStratumError()})
		return submissionTask{}, false
	}

	job, curLast, curPrevHash, curHeight, ntimeBounds, notifiedScriptTime, ok := mc.jobForIDWithLast(jobID)
	usedFallbackJob := false
	if !ok || job == nil {
		if shareJobFreshnessChecksJobID(mc.cfg.ShareJobFreshnessMode) {
			logger.Debug("submit rejected: stale job", "remote", mc.id, "job", jobID)
			// Use "job not found" for missing/expired jobs.
			mc.rejectShareWithBan(&StratumRequest{ID: reqID, Method: "mining.submit"}, workerName, rejectStaleJob, stratumErrCodeJobNotFound, "job not found", now)
			return submissionTask{}, false
		}
		if curLast == nil {
			logger.Debug("submit rejected: no fallback job available", "remote", mc.id, "job", jobID)
			mc.rejectShareWithBan(&StratumRequest{ID: reqID, Method: "mining.submit"}, workerName, rejectStaleJob, stratumErrCodeJobNotFound, "job not found", now)
			return submissionTask{}, false
		}
		job = curLast
		usedFallbackJob = true
		if notifiedScriptTime == 0 {
			notifiedScriptTime = mc.scriptTimeForJob(job.JobID, job.ScriptTime)
		}
	}

	// Defensive: ensure the job template still matches what we advertised to this
	// connection (prevhash/height). If it changed underneath us, reject as stale.
	policyReject := submitPolicyReject{reason: rejectUnknown}
	if usedFallbackJob {
		// Even when job-id freshness checks are disabled, classify non-block
		// shares for unknown/expired job IDs as stale rather than lowdiff.
		policyReject = submitPolicyReject{reason: rejectStaleJob, errCode: stratumErrCodeJobNotFound, errMsg: "job not found"}
	}
	if shareJobFreshnessChecksPrevhash(mc.cfg.ShareJobFreshnessMode) && curLast != nil && (curPrevHash != job.Template.Previous || curHeight != job.Template.Height) {
		logger.Warn("submit: stale job mismatch (policy)", "remote", mc.id, "job", jobID, "expected_prev", job.Template.Previous, "expected_height", job.Template.Height, "current_prev", curPrevHash, "current_height", curHeight)
		policyReject = submitPolicyReject{reason: rejectStaleJob, errCode: stratumErrCodeJobNotFound, errMsg: "job not found"}
	}

	en2Small, en2Len, en2Large, err := decodeExtranonce2Hex(extranonce2, validateFields, job.Extranonce2Size)
	if err != nil {
		logger.Debug("submit bad extranonce2", "remote", mc.id, "error", err)
		mc.rejectShareWithBan(&StratumRequest{ID: reqID, Method: "mining.submit"}, workerName, rejectInvalidExtranonce2, stratumErrCodeInvalidRequest, "invalid extranonce2", now)
		return submissionTask{}, false
	}

	if validateFields && len(ntime) != 8 {
		logger.Debug("submit invalid ntime length", "remote", mc.id, "len", len(ntime))
		mc.rejectShareWithBan(&StratumRequest{ID: reqID, Method: "mining.submit"}, workerName, rejectInvalidNTime, stratumErrCodeInvalidRequest, "invalid ntime", now)
		return submissionTask{}, false
	}
	// Stratum pools send ntime as BIG-ENDIAN hex and parse it back with parseInt(hex, 16).
	ntimeVal, err := parseUint32BEHex(ntime)
	if err != nil {
		logger.Debug("submit bad ntime", "remote", mc.id, "error", err)
		mc.rejectShareWithBan(&StratumRequest{ID: reqID, Method: "mining.submit"}, workerName, rejectInvalidNTime, stratumErrCodeInvalidRequest, "invalid ntime", now)
		return submissionTask{}, false
	}
	// Tight ntime bounds: require ntime to be >= the template's curtime
	// (or mintime when provided) and allow it to roll forward only a short
	// distance from the template.
	minNTime := ntimeBounds.min
	maxNTime := ntimeBounds.max
	if mc.cfg.ShareCheckNTimeWindow && (int64(ntimeVal) < minNTime || int64(ntimeVal) > maxNTime) {
		// Policy-only: for safety we still run the PoW check and, if the share is
		// a real block, submit it even if ntime violates the pool's tighter window.
		logger.Warn("submit ntime outside window (policy)", "remote", mc.id, "ntime", ntimeVal, "min", minNTime, "max", maxNTime)
		if policyReject.reason == rejectUnknown {
			policyReject = submitPolicyReject{reason: rejectInvalidNTime, errCode: stratumErrCodeInvalidRequest, errMsg: "invalid ntime"}
		}
	}

	if validateFields && len(nonce) != 8 {
		logger.Debug("submit invalid nonce length", "remote", mc.id, "len", len(nonce))
		mc.rejectShareWithBan(&StratumRequest{ID: reqID, Method: "mining.submit"}, workerName, rejectInvalidNonce, stratumErrCodeInvalidRequest, "invalid nonce", now)
		return submissionTask{}, false
	}
	// Nonce is sent as BIG-ENDIAN hex in mining.notify.
	nonceVal, err := parseUint32BEHex(nonce)
	if err != nil {
		logger.Debug("submit bad nonce", "remote", mc.id, "error", err)
		mc.rejectShareWithBan(&StratumRequest{ID: reqID, Method: "mining.submit"}, workerName, rejectInvalidNonce, stratumErrCodeInvalidRequest, "invalid nonce", now)
		return submissionTask{}, false
	}

	// BIP320: reject version rolls outside the negotiated mask (docs/protocols/bip-0320.mediawiki).
	baseVersion := uint32(job.Template.Version)
	useVersion := baseVersion
	versionDiff := uint32(0)
	if submittedVersion != 0 {
		// ESP-Miner sends the delta (rolled_version ^ base_version), while other
		// miners send the full rolled version. Treat values that fit entirely
		// inside the negotiated mask as a delta, otherwise as a full version.
		if submittedVersion&^mc.versionMask == 0 {
			useVersion = baseVersion ^ submittedVersion
			versionDiff = submittedVersion
		} else {
			useVersion = submittedVersion
			versionDiff = useVersion ^ baseVersion
		}
	}

	versionHex := ""
	if debugLogging || verboseRuntimeLogging {
		versionHex = uint32ToHex8Lower(useVersion)
	}
	if mc.cfg.ShareCheckVersionRolling && versionDiff != 0 {
		maskedDiff := versionDiff & mc.versionMask

		if !mc.versionRoll {
			logger.Warn("submit version rolling disabled (policy)", "remote", mc.id, "diff", uint32ToHex8Lower(versionDiff))
			if policyReject.reason == rejectUnknown {
				policyReject = submitPolicyReject{reason: rejectInvalidVersion, errCode: stratumErrCodeInvalidRequest, errMsg: "version rolling not enabled"}
			}
		}

		if versionDiff&^mc.versionMask != 0 {
			logger.Warn("submit version outside mask (policy)", "remote", mc.id, "version", uint32ToHex8Lower(useVersion), "mask", uint32ToHex8Lower(mc.versionMask))
			if policyReject.reason == rejectUnknown {
				policyReject = submitPolicyReject{reason: rejectInvalidVersionMask, errCode: stratumErrCodeInvalidRequest, errMsg: "invalid version mask"}
			}
		}

		if mc.minVerBits > 0 {
			usedBits := bits.OnesCount32(maskedDiff)
			if usedBits < mc.minVerBits {
				if !mc.cfg.ShareAllowDegradedVersionBits {
					logger.Warn("submit insufficient version rolling bits (policy)", "remote", mc.id, "version", uint32ToHex8Lower(useVersion), "required_bits", mc.minVerBits)
					if policyReject.reason == rejectUnknown {
						policyReject = submitPolicyReject{reason: rejectInsufficientVersionBits, errCode: stratumErrCodeInvalidRequest, errMsg: "insufficient version bits"}
					}
				} else {
					// Log but don't reject (BIP310 permissive approach: allow degraded mode)
					logger.Warn("submit: miner operating in degraded version rolling mode (allowed by BIP310)",
						"remote", mc.id, "version", uint32ToHex8Lower(useVersion),
						"used_bits", usedBits,
						"negotiated_minimum", mc.minVerBits)
				}
			}
		}
	}

	task := submissionTask{
		mc:               mc,
		reqID:            reqID,
		job:              job,
		jobID:            jobID,
		workerName:       workerName,
		extranonce2:      extranonce2,
		extranonce2Len:   en2Len,
		extranonce2Bytes: en2Small,
		extranonce2Large: en2Large,
		ntime:            ntime,
		ntimeVal:         ntimeVal,
		nonce:            nonce,
		nonceVal:         nonceVal,
		versionHex:       versionHex,
		useVersion:       useVersion,
		scriptTime:       notifiedScriptTime,
		policyReject:     policyReject,
		receivedAt:       now,
	}
	return task, true
}
