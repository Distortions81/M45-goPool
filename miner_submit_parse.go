package main

import (
	"fmt"
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

func normalizeSubmitExtranonce2Hex(extranonce2 string, expectedSize int) (string, error) {
	if len(extranonce2) == 0 {
		return "", fmt.Errorf("extranonce2 required")
	}
	for i := 0; i < len(extranonce2); i++ {
		if hexNibbleLUT[extranonce2[i]] == 0xff {
			return "", fmt.Errorf("invalid hex digit in %q", extranonce2)
		}
	}
	if expectedSize > 0 {
		wantLen := expectedSize * 2
		switch {
		case len(extranonce2) > wantLen:
			return extranonce2[:wantLen], nil
		case len(extranonce2) < wantLen:
			return extranonce2 + strings.Repeat("0", wantLen-len(extranonce2)), nil
		default:
			return extranonce2, nil
		}
	}
	if len(extranonce2)%2 != 0 {
		return "", fmt.Errorf("odd-length extranonce2 hex")
	}
	return extranonce2, nil
}

func normalizeSubmitNonceHex(nonce string) (string, error) {
	if len(nonce) == 0 {
		return "", fmt.Errorf("nonce required")
	}
	for i := 0; i < len(nonce); i++ {
		if hexNibbleLUT[nonce[i]] == 0xff {
			return "", fmt.Errorf("invalid hex digit in %q", nonce)
		}
	}
	if len(nonce) > maxNonceHexLen {
		return nonce[:maxNonceHexLen], nil
	}
	return nonce, nil
}

type submittedVersionResolution struct {
	useVersion          uint32
	versionDiff         uint32
	alternateUseVersion uint32
	hasAlternateVersion bool
}

// resolveSubmittedVersion prefers BIP310 replacement-bits semantics while
// retaining a legacy XOR-delta alternate for miners that historically used it.
func resolveSubmittedVersion(baseVersion, submittedVersion, versionMask uint32, versionRollingActive, allowMaskMismatch, versionProvided bool) submittedVersionResolution {
	if !versionProvided {
		return submittedVersionResolution{useVersion: baseVersion}
	}

	// BIP310 replacement semantics also apply to an active zero mask. In that
	// state the required explicit zero version_bits value preserves the complete
	// job version.
	if versionRollingActive && submittedVersion&^versionMask == 0 {
		bip310Version := (baseVersion &^ versionMask) | (submittedVersion & versionMask)
		xorVersion := baseVersion ^ submittedVersion
		out := submittedVersionResolution{
			useVersion:  bip310Version,
			versionDiff: bip310Version ^ baseVersion,
		}
		if xorVersion != bip310Version {
			out.alternateUseVersion = xorVersion
			out.hasAlternateVersion = true
		}
		return out
	}

	fullVersionDiff := submittedVersion ^ baseVersion
	if fullVersionDiff&^versionMask == 0 {
		return submittedVersionResolution{
			useVersion:  submittedVersion,
			versionDiff: fullVersionDiff,
		}
	}

	if allowMaskMismatch {
		return submittedVersionResolution{
			useVersion:  baseVersion ^ submittedVersion,
			versionDiff: submittedVersion,
		}
	}
	return submittedVersionResolution{
		useVersion:  submittedVersion,
		versionDiff: fullVersionDiff,
	}
}

type submittedVersionMaskPolicy struct {
	active bool
	mask   uint32
}

// blockOnlyRescueVersions returns every retained plausible supplied-version
// header not already covered by ordinary-share primary/alternate resolution.
// In addition to current and notify-time BIP310 replacement semantics, a miner
// can have used an intermediate connection-wide mask, a complete header
// version, or a legacy XOR delta. The common four-interpretation case stays in
// the inline array; retained intermediate masks spill into the extra slice.
func blockOnlyRescueVersions(
	baseVersion, submittedVersion uint32,
	ordinary submittedVersionResolution,
	current submittedVersionMaskPolicy,
	notified *submittedVersionMaskPolicy,
	historicalMasks []uint32,
) ([3]uint32, []uint32, int) {
	var versions [3]uint32
	var extra []uint32
	var count int
	candidateAt := func(index int) uint32 {
		if index < len(versions) {
			return versions[index]
		}
		return extra[index-len(versions)]
	}
	appendCandidate := func(candidate uint32) {
		if candidate == ordinary.useVersion || (ordinary.hasAlternateVersion && candidate == ordinary.alternateUseVersion) {
			return
		}
		for i := 0; i < count; i++ {
			if candidateAt(i) == candidate {
				return
			}
		}
		if count < len(versions) {
			versions[count] = candidate
		} else {
			extra = append(extra, candidate)
		}
		count++
	}

	if current.active {
		appendCandidate((baseVersion &^ current.mask) | (submittedVersion & current.mask))
	}
	if notified != nil && notified.active &&
		(notified.active != current.active || notified.mask != current.mask) {
		appendCandidate((baseVersion &^ notified.mask) | (submittedVersion & notified.mask))
	}
	for _, mask := range historicalMasks {
		appendCandidate((baseVersion &^ mask) | (submittedVersion & mask))
	}
	appendCandidate(submittedVersion)
	appendCandidate(baseVersion ^ submittedVersion)

	return versions, extra, count
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
	if len(jobID) == 0 {
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
	versionProvided := false
	if len(req.Params) == 6 {
		versionProvided = true
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
		verVal, err := parseUint32BEHexPadded(verStr)
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
	out.versionProvided = versionProvided
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
	params, ok := mc.parseSubmitParams(req, now)
	if !ok {
		return submissionTask{}, false
	}
	return mc.prepareSubmissionTaskFromParsed(req.ID, params, now)
}

func (mc *MinerConn) prepareSubmissionTaskFromParsed(reqID any, params submitParams, now time.Time) (submissionTask, bool) {
	worker := params.worker
	jobID := params.jobID
	extranonce2 := params.extranonce2
	ntime := params.ntime
	nonce := params.nonce
	submittedVersion := params.submittedVersion
	versionProvided := params.versionProvided
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
	workerName := authorizedWorker
	if workerName == "" {
		workerName = worker
	}
	var banPolicy *bannedSubmitPolicy
	if until, reason, _ := mc.banDetails(); now.Before(until) {
		banPolicy = &bannedSubmitPolicy{
			until:  until,
			reason: reason,
			err:    mc.bannedStratumError(),
		}
	}

	jobLookup := mc.jobForSubmissionWithLast(jobID)
	job := jobLookup.job
	curLast := jobLookup.lastJob
	curPrevHash := jobLookup.lastPrevHash
	curHeight := jobLookup.lastHeight
	ntimeBounds := jobLookup.ntimeBounds
	notifiedScriptTime := jobLookup.scriptTime
	notifiedCoinbase := jobLookup.coinbase
	coinbaseOK := jobLookup.coinbaseOK
	ok := jobLookup.found
	retiredJob := jobLookup.retired
	if coinbaseOK && notifiedCoinbase.worker != "" {
		workerName = notifiedCoinbase.worker
	}
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
	if usedFallbackJob || retiredJob {
		// Even when job-id freshness checks are disabled, classify non-block
		// shares for unknown/expired and retired job IDs as stale rather than
		// lowdiff. Retired jobs continue through PoW validation solely so a real
		// network-target block can be submitted exactly as advertised.
		policyReject = submitPolicyReject{reason: rejectStaleJob, errCode: stratumErrCodeJobNotFound, errMsg: "job not found"}
	}
	if shareJobFreshnessChecksPrevhash(mc.cfg.ShareJobFreshnessMode) && curLast != nil && (curPrevHash != job.Template.Previous || curHeight != job.Template.Height) {
		logger.Warn("submit: stale job mismatch (policy)", "remote", mc.id, "job", jobID, "expected_prev", job.Template.Previous, "expected_height", job.Template.Height, "current_prev", curPrevHash, "current_height", curHeight)
		policyReject = submitPolicyReject{reason: rejectStaleJob, errCode: stratumErrCodeJobNotFound, errMsg: "job not found"}
	}
	if mc.cfg.ShareRequireAuthorizedConnection && mc.cfg.ShareRequireWorkerMatch && workerName != "" && submitWorker != workerName {
		logger.Warn("submit worker mismatch (policy)",
			"remote", mc.id,
			"job", jobID,
			"expected", workerName,
			"submitted", submitWorker,
		)
		if mc.metrics != nil {
			mc.metrics.RecordSubmitError("worker_mismatch")
		}
		if policyReject.reason == rejectUnknown {
			// Keep validating PoW so a real block is never discarded solely for
			// a worker-label policy mismatch.
			policyReject = submitPolicyReject{reason: rejectUnauthorizedWorker, errCode: stratumErrCodeUnauthorized, errMsg: "unauthorized"}
		}
	}

	extranonce2, err := normalizeSubmitExtranonce2Hex(extranonce2, job.Extranonce2Size)
	if err != nil {
		logger.Debug("submit bad extranonce2", "remote", mc.id, "error", err)
		mc.rejectShareWithBan(&StratumRequest{ID: reqID, Method: "mining.submit"}, workerName, rejectInvalidExtranonce2, stratumErrCodeInvalidRequest, "invalid extranonce2", now)
		return submissionTask{}, false
	}
	en2Small, en2Len, en2Large, err := decodeExtranonce2Hex(extranonce2, validateFields, job.Extranonce2Size)
	if err != nil {
		logger.Debug("submit bad extranonce2", "remote", mc.id, "error", err)
		mc.rejectShareWithBan(&StratumRequest{ID: reqID, Method: "mining.submit"}, workerName, rejectInvalidExtranonce2, stratumErrCodeInvalidRequest, "invalid extranonce2", now)
		return submissionTask{}, false
	}

	if validateFields && (len(ntime) == 0 || len(ntime) > 8) {
		logger.Debug("submit invalid ntime length", "remote", mc.id, "len", len(ntime))
		mc.rejectShareWithBan(&StratumRequest{ID: reqID, Method: "mining.submit"}, workerName, rejectInvalidNTime, stratumErrCodeInvalidRequest, "invalid ntime", now)
		return submissionTask{}, false
	}
	// Stratum pools send ntime as BIG-ENDIAN hex and parse it back with parseInt(hex, 16).
	ntimeVal, err := parseUint32BEHexPadded(ntime)
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
	if !retiredJob && mc.cfg.ShareCheckNTimeWindow && (int64(ntimeVal) < minNTime || int64(ntimeVal) > maxNTime) {
		// Policy-only: for safety we still run the PoW check and, if the share is
		// a real block, submit it even if ntime violates the pool's tighter window.
		logger.Warn("submit ntime outside window (policy)", "remote", mc.id, "ntime", ntimeVal, "min", minNTime, "max", maxNTime)
		if policyReject.reason == rejectUnknown {
			policyReject = submitPolicyReject{reason: rejectInvalidNTime, errCode: stratumErrCodeInvalidRequest, errMsg: "invalid ntime"}
		}
	}

	nonce, err = normalizeSubmitNonceHex(nonce)
	if err != nil {
		logger.Debug("submit bad nonce", "remote", mc.id, "error", err)
		mc.rejectShareWithBan(&StratumRequest{ID: reqID, Method: "mining.submit"}, workerName, rejectInvalidNonce, stratumErrCodeInvalidRequest, "invalid nonce", now)
		return submissionTask{}, false
	}
	// Nonce is sent as BIG-ENDIAN hex in mining.notify.
	nonceVal, err := parseUint32BEHexPadded(nonce)
	if err != nil {
		logger.Debug("submit bad nonce", "remote", mc.id, "error", err)
		mc.rejectShareWithBan(&StratumRequest{ID: reqID, Method: "mining.submit"}, workerName, rejectInvalidNonce, stratumErrCodeInvalidRequest, "invalid nonce", now)
		return submissionTask{}, false
	}

	// BIP320: reject version rolls outside the negotiated mask (docs/protocols/bip-0320.mediawiki).
	baseVersion := uint32(job.Template.Version)
	// sendNotifyFor updates the connection-wide mask before writing
	// mining.set_version_mask. Synchronize with that wire sequence so a submit
	// cannot observe the new mask until the miner could have received it.
	mc.notifyMu.Lock()
	var versionMaskHistoryBuffer [defaultRecentJobs]uint32
	versionRollingActive, versionMask, versionMaskHistory := mc.versionRollingPolicyHistorySnapshot(versionMaskHistoryBuffer[:0])
	mc.notifyMu.Unlock()
	versionResolution := resolveSubmittedVersion(baseVersion, submittedVersion, versionMask, versionRollingActive, mc.cfg.ShareAllowOutOfMaskVersionBits, versionProvided)
	useVersion := versionResolution.useVersion
	versionDiff := versionResolution.versionDiff
	var blockRescueVersions [3]uint32
	var blockRescueExtra []uint32
	var blockRescueCount int
	if versionProvided {
		currentPolicy := submittedVersionMaskPolicy{active: versionRollingActive, mask: versionMask}
		var notifiedPolicy *submittedVersionMaskPolicy
		if coinbaseOK && notifiedCoinbase.versionRollingActive &&
			(notifiedCoinbase.versionMask != versionMask || notifiedCoinbase.versionRollingActive != versionRollingActive) {
			notifiedPolicy = &submittedVersionMaskPolicy{
				active: notifiedCoinbase.versionRollingActive,
				mask:   notifiedCoinbase.versionMask,
			}
		}
		blockRescueVersions, blockRescueExtra, blockRescueCount = blockOnlyRescueVersions(
			baseVersion,
			submittedVersion,
			versionResolution,
			currentPolicy,
			notifiedPolicy,
			versionMaskHistory,
		)
	}

	versionHex := ""
	if debugLogging || verboseRuntimeLogging {
		versionHex = uint32ToHex8Lower(useVersion)
	}
	// Preserve the existing full-version compatibility path for nonzero masks.
	// At an active zero mask, though, BIP310 requires the explicit value to be
	// zero; comparing only the resolved version diff would miss a full job
	// version supplied in the version_bits slot.
	rawVersionMaskMismatch := versionRollingActive && versionMask == 0 && versionProvided && submittedVersion != 0
	if mc.cfg.ShareCheckVersionRolling && rawVersionMaskMismatch {
		if !mc.cfg.ShareAllowOutOfMaskVersionBits {
			logger.Warn("submit version bits outside mask (policy)", "remote", mc.id, "version_bits", uint32ToHex8Lower(submittedVersion), "mask", uint32ToHex8Lower(versionMask))
			if policyReject.reason == rejectUnknown {
				policyReject = submitPolicyReject{reason: rejectInvalidVersionMask, errCode: stratumErrCodeInvalidRequest, errMsg: "invalid version mask"}
			}
		} else {
			logger.Debug("submit version bits outside mask allowed (compat)",
				"remote", mc.id,
				"version_bits", uint32ToHex8Lower(submittedVersion),
				"mask", uint32ToHex8Lower(versionMask))
		}
	}
	if mc.cfg.ShareCheckVersionRolling && versionDiff != 0 {
		if !versionRollingActive {
			if !mc.cfg.ShareAllowOutOfMaskVersionBits {
				logger.Warn("submit version rolling disabled (policy)", "remote", mc.id, "diff", uint32ToHex8Lower(versionDiff))
				if policyReject.reason == rejectUnknown {
					policyReject = submitPolicyReject{reason: rejectInvalidVersion, errCode: stratumErrCodeInvalidRequest, errMsg: "version rolling not enabled"}
				}
			} else {
				logger.Debug("submit version rolling disabled but version bits allowed (compat)",
					"remote", mc.id,
					"diff", uint32ToHex8Lower(versionDiff))
			}
		}

		if !rawVersionMaskMismatch && versionDiff&^versionMask != 0 {
			if !mc.cfg.ShareAllowOutOfMaskVersionBits {
				logger.Warn("submit version outside mask (policy)", "remote", mc.id, "version", uint32ToHex8Lower(useVersion), "mask", uint32ToHex8Lower(versionMask))
				if policyReject.reason == rejectUnknown {
					policyReject = submitPolicyReject{reason: rejectInvalidVersionMask, errCode: stratumErrCodeInvalidRequest, errMsg: "invalid version mask"}
				}
			} else {
				logger.Debug("submit version outside mask allowed (compat)",
					"remote", mc.id,
					"version", uint32ToHex8Lower(useVersion),
					"mask", uint32ToHex8Lower(versionMask))
			}
		}
	}

	task := submissionTask{
		mc:                  mc,
		reqID:               reqID,
		job:                 job,
		jobID:               jobID,
		workerName:          workerName,
		extranonce2:         extranonce2,
		extranonce2Len:      en2Len,
		extranonce2Bytes:    en2Small,
		extranonce2Large:    en2Large,
		ntime:               ntime,
		ntimeVal:            ntimeVal,
		nonce:               nonce,
		nonceVal:            nonceVal,
		versionHex:          versionHex,
		useVersion:          useVersion,
		alternateVersionHex: uint32ToHex8Lower(versionResolution.alternateUseVersion),
		alternateUseVersion: versionResolution.alternateUseVersion,
		hasAlternateVersion: versionResolution.hasAlternateVersion,
		blockRescueVersions: blockRescueVersions,
		blockRescueExtra:    blockRescueExtra,
		blockRescueCount:    blockRescueCount,
		notifiedCoinbase:    notifiedCoinbase,
		hasNotifiedCoinbase: coinbaseOK && len(notifiedCoinbase.prefix) > 0 && len(notifiedCoinbase.suffix) > 0,
		scriptTime:          notifiedScriptTime,
		assignedDifficulty:  mc.assignedDifficulty(jobID),
		policyReject:        policyReject,
		banPolicy:           banPolicy,
		receivedAt:          now,
	}
	return task, true
}
