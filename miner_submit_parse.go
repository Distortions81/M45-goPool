package main

import (
	"encoding/hex"
	"fmt"
	"math/bits"
	"strings"
	"time"
)

func (mc *MinerConn) useStrictSubmitPath() bool {
	return mc.cfg.ShareRequireWorkerMatch ||
		shareJobFreshnessChecksPrevhash(mc.cfg.ShareJobFreshnessMode) ||
		mc.cfg.ShareCheckNTimeWindow ||
		mc.cfg.ShareCheckVersionRolling
}

// parseSubmitParams validates and extracts the core fields from a mining.submit
// request, recording and responding to any parameter errors. It returns params
// and ok=false when a response has already been sent.
func (mc *MinerConn) parseSubmitParams(req *StratumRequest, now time.Time) (submitParams, bool) {
	var out submitParams
	validateFields := mc.cfg.ShareCheckParamFormat

	if len(req.Params) < 5 || len(req.Params) > 6 {
		logger.Warn("submit invalid params", "remote", mc.id, "params", req.Params)
		mc.recordShare("", false, 0, 0, "invalid params", "", nil, now)
		mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(20, "invalid params")})
		return out, false
	}

	worker, ok := req.Params[0].(string)
	if !ok {
		mc.recordShare("", false, 0, 0, "invalid worker", "", nil, now)
		mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(20, "invalid worker")})
		return out, false
	}
	if validateFields && len(worker) == 0 {
		mc.recordShare("", false, 0, 0, "empty worker", "", nil, now)
		mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(20, "worker name required")})
		return out, false
	}
	if validateFields && len(worker) > maxWorkerNameLen {
		logger.Warn("submit rejected: worker name too long", "remote", mc.id, "len", len(worker))
		mc.recordShare("", false, 0, 0, "worker name too long", "", nil, now)
		mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(20, "worker name too long")})
		return out, false
	}

	jobID, ok := req.Params[1].(string)
	if !ok {
		mc.recordShare(worker, false, 0, 0, "invalid job id", "", nil, now)
		mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(20, "invalid job id")})
		return out, false
	}
	if validateFields && len(jobID) == 0 && mc.cfg.ShareRequireJobID {
		mc.recordShare(worker, false, 0, 0, "empty job id", "", nil, now)
		mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(20, "job id required")})
		return out, false
	}
	if validateFields && len(jobID) > maxJobIDLen {
		logger.Warn("submit rejected: job id too long", "remote", mc.id, "len", len(jobID))
		mc.recordShare(worker, false, 0, 0, "job id too long", "", nil, now)
		mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(20, "job id too long")})
		return out, false
	}
	extranonce2, ok := req.Params[2].(string)
	if !ok {
		mc.recordShare(worker, false, 0, 0, "invalid extranonce2", "", nil, now)
		mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(20, "invalid extranonce2")})
		return out, false
	}
	ntime, ok := req.Params[3].(string)
	if !ok {
		mc.recordShare(worker, false, 0, 0, "invalid ntime", "", nil, now)
		mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(20, "invalid ntime")})
		return out, false
	}
	nonce, ok := req.Params[4].(string)
	if !ok {
		mc.recordShare(worker, false, 0, 0, "invalid nonce", "", nil, now)
		mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(20, "invalid nonce")})
		return out, false
	}

	submittedVersion := uint32(0)
	if len(req.Params) == 6 {
		verStr, ok := req.Params[5].(string)
		if !ok {
			mc.recordShare(worker, false, 0, 0, "invalid version", "", nil, now)
			mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(20, "invalid version")})
			return out, false
		}
		if validateFields && len(verStr) == 0 {
			mc.recordShare(worker, false, 0, 0, "empty version", "", nil, now)
			mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(20, "version required")})
			return out, false
		}
		if validateFields && len(verStr) > maxVersionHexLen {
			logger.Warn("submit rejected: version too long", "remote", mc.id, "len", len(verStr))
			mc.recordShare(worker, false, 0, 0, "version too long", "", nil, now)
			mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(20, "version too long")})
			return out, false
		}
		verVal, err := parseUint32BEHex(verStr)
		if err != nil {
			if validateFields {
				mc.recordShare(worker, false, 0, 0, "invalid version", "", nil, now)
				mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(20, "invalid version")})
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
		logger.Warn("submit invalid params", "remote", mc.id, "params", params)
		mc.recordShare("", false, 0, 0, "invalid params", "", nil, now)
		mc.writeResponse(StratumResponse{ID: id, Result: false, Error: newStratumError(20, "invalid params")})
		return out, false
	}

	worker := params[0]
	if validateFields && len(worker) == 0 {
		mc.recordShare("", false, 0, 0, "empty worker", "", nil, now)
		mc.writeResponse(StratumResponse{ID: id, Result: false, Error: newStratumError(20, "worker name required")})
		return out, false
	}
	if validateFields && len(worker) > maxWorkerNameLen {
		logger.Warn("submit rejected: worker name too long", "remote", mc.id, "len", len(worker))
		mc.recordShare("", false, 0, 0, "worker name too long", "", nil, now)
		mc.writeResponse(StratumResponse{ID: id, Result: false, Error: newStratumError(20, "worker name too long")})
		return out, false
	}

	jobID := params[1]
	if validateFields && len(jobID) == 0 && mc.cfg.ShareRequireJobID {
		mc.recordShare(worker, false, 0, 0, "empty job id", "", nil, now)
		mc.writeResponse(StratumResponse{ID: id, Result: false, Error: newStratumError(20, "job id required")})
		return out, false
	}
	if validateFields && len(jobID) > maxJobIDLen {
		logger.Warn("submit rejected: job id too long", "remote", mc.id, "len", len(jobID))
		mc.recordShare(worker, false, 0, 0, "job id too long", "", nil, now)
		mc.writeResponse(StratumResponse{ID: id, Result: false, Error: newStratumError(20, "job id too long")})
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
			mc.writeResponse(StratumResponse{ID: id, Result: false, Error: newStratumError(20, "version required")})
			return out, false
		}
		if validateFields && len(verStr) > maxVersionHexLen {
			logger.Warn("submit rejected: version too long", "remote", mc.id, "len", len(verStr))
			mc.recordShare(worker, false, 0, 0, "version too long", "", nil, now)
			mc.writeResponse(StratumResponse{ID: id, Result: false, Error: newStratumError(20, "version too long")})
			return out, false
		}
		verVal, err := parseUint32BEHex(verStr)
		if err != nil {
			if validateFields {
				mc.recordShare(worker, false, 0, 0, "invalid version", "", nil, now)
				mc.writeResponse(StratumResponse{ID: id, Result: false, Error: newStratumError(20, "invalid version")})
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

// prepareSubmissionTask validates a mining.submit request and, if valid, returns
// a fully-populated submissionTask. On any validation failure it writes the
// appropriate Stratum response and returns ok=false.
//
// This helper exists so benchmarks can include submit parsing/validation while
// still exercising the core share-processing path without extra goroutine
// scheduling noise.
func (mc *MinerConn) prepareSubmissionTask(req *StratumRequest, now time.Time) (submissionTask, bool) {
	if !mc.useStrictSubmitPath() {
		return mc.prepareSubmissionTaskSolo(req, now)
	}
	return mc.prepareSubmissionTaskStrict(req, now)
}

func (mc *MinerConn) prepareSubmissionTaskSolo(req *StratumRequest, now time.Time) (submissionTask, bool) {
	params, ok := mc.parseSubmitParams(req, now)
	if !ok {
		return submissionTask{}, false
	}
	return mc.prepareSubmissionTaskSoloParsed(req.ID, params, now)
}

func (mc *MinerConn) prepareSubmissionTaskSoloParsed(reqID any, params submitParams, now time.Time) (submissionTask, bool) {
	worker := params.worker
	jobID := params.jobID
	extranonce2 := params.extranonce2
	ntime := params.ntime
	nonce := params.nonce
	submittedVersion := params.submittedVersion
	validateFields := mc.cfg.ShareCheckParamFormat

	if mc.cfg.ShareRequireAuthorizedConnection && !mc.authorized {
		logger.Warn("submit rejected: unauthorized", "remote", mc.id)
		mc.recordShare(worker, false, 0, 0, "unauthorized", "", nil, now)
		if mc.metrics != nil {
			mc.metrics.RecordSubmitError("unauthorized")
		}
		mc.writeResponse(StratumResponse{ID: reqID, Result: false, Error: newStratumError(24, "unauthorized")})
		return submissionTask{}, false
	}

	// Solo mode: trust the authorized worker identity for this connection and
	// avoid per-submit worker-mismatch checks. If the connection worker is not
	// known (shouldn't happen after authorize), fall back to the submit param.
	workerName := mc.currentWorker()
	if workerName == "" {
		workerName = worker
	}
	if mc.isBanned(now) {
		until, reason, _ := mc.banDetails()
		logger.Warn("submit rejected: banned", "miner", mc.minerName(workerName), "ban_until", until, "reason", reason)
		if mc.metrics != nil {
			mc.metrics.RecordSubmitError("banned")
		}
		mc.writeResponse(StratumResponse{ID: reqID, Result: false, Error: newStratumError(24, "banned")})
		return submissionTask{}, false
	}

	job, curLast, notifiedScriptTime, ok := mc.jobForIDWithLast(jobID)
	if !ok || job == nil {
		if shareJobFreshnessChecksJobID(mc.cfg.ShareJobFreshnessMode) {
			logger.Warn("submit rejected: stale job", "remote", mc.id, "job", jobID)
			// Use "job not found" for missing/expired jobs.
			mc.rejectShareWithBan(&StratumRequest{ID: reqID, Method: "mining.submit"}, workerName, rejectStaleJob, 21, "job not found", now)
			return submissionTask{}, false
		}
		if curLast == nil {
			logger.Warn("submit rejected: no fallback job available", "remote", mc.id, "job", jobID)
			mc.rejectShareWithBan(&StratumRequest{ID: reqID, Method: "mining.submit"}, workerName, rejectStaleJob, 21, "job not found", now)
			return submissionTask{}, false
		}
		job = curLast
		if notifiedScriptTime == 0 {
			notifiedScriptTime = mc.scriptTimeForJob(job.JobID, job.ScriptTime)
		}
	}

	// Solo-mode: we only validate inputs enough to reconstruct the header and
	// compute PoW/difficulty. We intentionally skip pool policy checks.
	policyReject := submitPolicyReject{reason: rejectUnknown}

	if validateFields && len(extranonce2) != job.Extranonce2Size*2 {
		logger.Warn("submit invalid extranonce2 length", "remote", mc.id, "got", len(extranonce2)/2, "expected", job.Extranonce2Size)
		mc.rejectShareWithBan(&StratumRequest{ID: reqID, Method: "mining.submit"}, workerName, rejectInvalidExtranonce2, 20, "invalid extranonce2", now)
		return submissionTask{}, false
	}
	en2, err := hex.DecodeString(extranonce2)
	if err != nil {
		logger.Warn("submit bad extranonce2", "remote", mc.id, "error", err)
		mc.rejectShareWithBan(&StratumRequest{ID: reqID, Method: "mining.submit"}, workerName, rejectInvalidExtranonce2, 20, "invalid extranonce2", now)
		return submissionTask{}, false
	}

	if validateFields && len(ntime) != 8 {
		logger.Warn("submit invalid ntime length", "remote", mc.id, "len", len(ntime))
		mc.rejectShareWithBan(&StratumRequest{ID: reqID, Method: "mining.submit"}, workerName, rejectInvalidNTime, 20, "invalid ntime", now)
		return submissionTask{}, false
	}
	ntimeVal, err := parseUint32BEHex(ntime)
	if err != nil {
		logger.Warn("submit bad ntime", "remote", mc.id, "error", err)
		mc.rejectShareWithBan(&StratumRequest{ID: reqID, Method: "mining.submit"}, workerName, rejectInvalidNTime, 20, "invalid ntime", now)
		return submissionTask{}, false
	}

	if validateFields && len(nonce) != 8 {
		logger.Warn("submit invalid nonce length", "remote", mc.id, "len", len(nonce))
		mc.rejectShareWithBan(&StratumRequest{ID: reqID, Method: "mining.submit"}, workerName, rejectInvalidNonce, 20, "invalid nonce", now)
		return submissionTask{}, false
	}
	nonceVal, err := parseUint32BEHex(nonce)
	if err != nil {
		logger.Warn("submit bad nonce", "remote", mc.id, "error", err)
		mc.rejectShareWithBan(&StratumRequest{ID: reqID, Method: "mining.submit"}, workerName, rejectInvalidNonce, 20, "invalid nonce", now)
		return submissionTask{}, false
	}

	// Version parsing is needed to build the correct header. We interpret
	// submitted versions using the negotiated mask when possible, but do not
	// enforce BIP320 policy in solo mode.
	baseVersion := uint32(job.Template.Version)
	useVersion := baseVersion
	if submittedVersion != 0 {
		if submittedVersion&^mc.versionMask == 0 {
			useVersion = baseVersion ^ submittedVersion
		} else {
			useVersion = submittedVersion
		}
	}

	versionHex := ""
	if debugLogging || verboseLogging {
		versionHex = fmt.Sprintf("%08x", useVersion)
	}

	task := submissionTask{
		mc:               mc,
		reqID:            reqID,
		job:              job,
		jobID:            jobID,
		workerName:       workerName,
		extranonce2:      extranonce2,
		extranonce2Bytes: en2,
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

func (mc *MinerConn) prepareSubmissionTaskStrict(req *StratumRequest, now time.Time) (submissionTask, bool) {
	params, ok := mc.parseSubmitParams(req, now)
	if !ok {
		return submissionTask{}, false
	}
	return mc.prepareSubmissionTaskStrictParsed(req.ID, params, now)
}

func (mc *MinerConn) prepareSubmissionTaskStrictParsed(reqID any, params submitParams, now time.Time) (submissionTask, bool) {
	worker := params.worker
	jobID := params.jobID
	extranonce2 := params.extranonce2
	ntime := params.ntime
	nonce := params.nonce
	submittedVersion := params.submittedVersion
	validateFields := mc.cfg.ShareCheckParamFormat

	if mc.cfg.ShareRequireAuthorizedConnection && !mc.authorized {
		logger.Warn("submit rejected: unauthorized", "remote", mc.id)
		mc.recordShare(worker, false, 0, 0, "unauthorized", "", nil, now)
		if mc.metrics != nil {
			mc.metrics.RecordSubmitError("unauthorized")
		}
		mc.writeResponse(StratumResponse{ID: reqID, Result: false, Error: newStratumError(24, "unauthorized")})
		return submissionTask{}, false
	}

	authorizedWorker := strings.TrimSpace(mc.currentWorker())
	submitWorker := strings.TrimSpace(worker)
	if mc.cfg.ShareRequireAuthorizedConnection && mc.cfg.ShareRequireWorkerMatch && authorizedWorker != "" && submitWorker != authorizedWorker {
		logger.Warn("submit rejected: worker mismatch", "remote", mc.id, "authorized", authorizedWorker, "submitted", submitWorker)
		mc.recordShare(authorizedWorker, false, 0, 0, "unauthorized worker", "", nil, now)
		if mc.metrics != nil {
			mc.metrics.RecordSubmitError("worker_mismatch")
		}
		mc.writeResponse(StratumResponse{ID: reqID, Result: false, Error: newStratumError(24, "unauthorized")})
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
		mc.writeResponse(StratumResponse{ID: reqID, Result: false, Error: newStratumError(24, "banned")})
		return submissionTask{}, false
	}

	job, curLast, notifiedScriptTime, ok := mc.jobForIDWithLast(jobID)
	if !ok || job == nil {
		if shareJobFreshnessChecksJobID(mc.cfg.ShareJobFreshnessMode) {
			logger.Warn("submit rejected: stale job", "remote", mc.id, "job", jobID)
			// Use "job not found" for missing/expired jobs.
			mc.rejectShareWithBan(&StratumRequest{ID: reqID, Method: "mining.submit"}, workerName, rejectStaleJob, 21, "job not found", now)
			return submissionTask{}, false
		}
		if curLast == nil {
			logger.Warn("submit rejected: no fallback job available", "remote", mc.id, "job", jobID)
			mc.rejectShareWithBan(&StratumRequest{ID: reqID, Method: "mining.submit"}, workerName, rejectStaleJob, 21, "job not found", now)
			return submissionTask{}, false
		}
		job = curLast
		if notifiedScriptTime == 0 {
			notifiedScriptTime = mc.scriptTimeForJob(job.JobID, job.ScriptTime)
		}
	}

	// Defensive: ensure the job template still matches what we advertised to this
	// connection (prevhash/height). If it changed underneath us, reject as stale.
	policyReject := submitPolicyReject{reason: rejectUnknown}
	if shareJobFreshnessChecksPrevhash(mc.cfg.ShareJobFreshnessMode) && curLast != nil && curLast.Template.Previous != job.Template.Previous {
		logger.Warn("submit: stale job prevhash mismatch (policy)", "remote", mc.id, "job", jobID, "expected_prev", job.Template.Previous, "current_prev", curLast.Template.Previous)
		policyReject = submitPolicyReject{reason: rejectStaleJob, errCode: 21, errMsg: "job not found"}
	}

	if validateFields && len(extranonce2) != job.Extranonce2Size*2 {
		logger.Warn("submit invalid extranonce2 length", "remote", mc.id, "got", len(extranonce2)/2, "expected", job.Extranonce2Size)
		mc.rejectShareWithBan(&StratumRequest{ID: reqID, Method: "mining.submit"}, workerName, rejectInvalidExtranonce2, 20, "invalid extranonce2", now)
		return submissionTask{}, false
	}
	en2, err := hex.DecodeString(extranonce2)
	if err != nil {
		logger.Warn("submit bad extranonce2", "remote", mc.id, "error", err)
		mc.rejectShareWithBan(&StratumRequest{ID: reqID, Method: "mining.submit"}, workerName, rejectInvalidExtranonce2, 20, "invalid extranonce2", now)
		return submissionTask{}, false
	}

	if validateFields && len(ntime) != 8 {
		logger.Warn("submit invalid ntime length", "remote", mc.id, "len", len(ntime))
		mc.rejectShareWithBan(&StratumRequest{ID: reqID, Method: "mining.submit"}, workerName, rejectInvalidNTime, 20, "invalid ntime", now)
		return submissionTask{}, false
	}
	// Stratum pools send ntime as BIG-ENDIAN hex and parse it back with parseInt(hex, 16).
	ntimeVal, err := parseUint32BEHex(ntime)
	if err != nil {
		logger.Warn("submit bad ntime", "remote", mc.id, "error", err)
		mc.rejectShareWithBan(&StratumRequest{ID: reqID, Method: "mining.submit"}, workerName, rejectInvalidNTime, 20, "invalid ntime", now)
		return submissionTask{}, false
	}
	// Tight ntime bounds: require ntime to be >= the template's curtime
	// (or mintime when provided) and allow it to roll forward only a short
	// distance from the template.
	minNTime := job.Template.CurTime
	if job.Template.Mintime > 0 && job.Template.Mintime > minNTime {
		minNTime = job.Template.Mintime
	}
	ntimeForwardSlack := mc.cfg.ShareNTimeMaxForwardSeconds
	if ntimeForwardSlack <= 0 {
		ntimeForwardSlack = defaultShareNTimeMaxForwardSeconds
	}
	maxNTime := minNTime + int64(ntimeForwardSlack)
	if mc.cfg.ShareCheckNTimeWindow && (int64(ntimeVal) < minNTime || int64(ntimeVal) > maxNTime) {
		// Policy-only: for safety we still run the PoW check and, if the share is
		// a real block, submit it even if ntime violates the pool's tighter window.
		logger.Warn("submit ntime outside window (policy)", "remote", mc.id, "ntime", ntimeVal, "min", minNTime, "max", maxNTime)
		if policyReject.reason == rejectUnknown {
			policyReject = submitPolicyReject{reason: rejectInvalidNTime, errCode: 20, errMsg: "invalid ntime"}
		}
	}

	if validateFields && len(nonce) != 8 {
		logger.Warn("submit invalid nonce length", "remote", mc.id, "len", len(nonce))
		mc.rejectShareWithBan(&StratumRequest{ID: reqID, Method: "mining.submit"}, workerName, rejectInvalidNonce, 20, "invalid nonce", now)
		return submissionTask{}, false
	}
	// Nonce is sent as BIG-ENDIAN hex in mining.notify.
	nonceVal, err := parseUint32BEHex(nonce)
	if err != nil {
		logger.Warn("submit bad nonce", "remote", mc.id, "error", err)
		mc.rejectShareWithBan(&StratumRequest{ID: reqID, Method: "mining.submit"}, workerName, rejectInvalidNonce, 20, "invalid nonce", now)
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
	if debugLogging || verboseLogging {
		versionHex = fmt.Sprintf("%08x", useVersion)
	}
	if mc.cfg.ShareCheckVersionRolling && versionDiff != 0 && !mc.versionRoll {
		logger.Warn("submit version rolling disabled (policy)", "remote", mc.id, "diff", fmt.Sprintf("%08x", versionDiff))
		if policyReject.reason == rejectUnknown {
			policyReject = submitPolicyReject{reason: rejectInvalidVersion, errCode: 20, errMsg: "version rolling not enabled"}
		}
	}
	if mc.cfg.ShareCheckVersionRolling && versionDiff&^mc.versionMask != 0 {
		logger.Warn("submit version outside mask (policy)", "remote", mc.id, "version", fmt.Sprintf("%08x", useVersion), "mask", fmt.Sprintf("%08x", mc.versionMask))
		if policyReject.reason == rejectUnknown {
			policyReject = submitPolicyReject{reason: rejectInvalidVersionMask, errCode: 20, errMsg: "invalid version mask"}
		}
	}
	if mc.cfg.ShareCheckVersionRolling && versionDiff != 0 && mc.minVerBits > 0 && bits.OnesCount32(versionDiff&mc.versionMask) < mc.minVerBits {
		if !mc.cfg.ShareAllowDegradedVersionBits {
			logger.Warn("submit insufficient version rolling bits (policy)", "remote", mc.id, "version", fmt.Sprintf("%08x", useVersion), "required_bits", mc.minVerBits)
			if policyReject.reason == rejectUnknown {
				policyReject = submitPolicyReject{reason: rejectInsufficientVersionBits, errCode: 20, errMsg: "insufficient version bits"}
			}
		} else {
			// Log but don't reject (BIP310 permissive approach: allow degraded mode)
			logger.Warn("submit: miner operating in degraded version rolling mode (allowed by BIP310)",
				"remote", mc.id, "version", fmt.Sprintf("%08x", useVersion),
				"used_bits", bits.OnesCount32(versionDiff&mc.versionMask),
				"negotiated_minimum", mc.minVerBits)
		}
	}

	task := submissionTask{
		mc:               mc,
		reqID:            reqID,
		job:              job,
		jobID:            jobID,
		workerName:       workerName,
		extranonce2:      extranonce2,
		extranonce2Bytes: en2,
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
