package main

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"math/bits"
	"strings"
	"time"
)

// Handle mining.submit.
// This is where we’d check PoW and submitblock.
type submitParams struct {
	worker           string
	jobID            string
	extranonce2      string
	ntime            string
	nonce            string
	submittedVersion uint32
}

// parseSubmitParams validates and extracts the core fields from a mining.submit
// request, recording and responding to any parameter errors. It returns params
// and ok=false when a response has already been sent.
func (mc *MinerConn) parseSubmitParams(req *StratumRequest, now time.Time) (submitParams, bool) {
	var out submitParams

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
	// Validate worker name length
	if len(worker) == 0 {
		mc.recordShare("", false, 0, 0, "empty worker", "", nil, now)
		mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(20, "worker name required")})
		return out, false
	}
	if len(worker) > maxWorkerNameLen {
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
	// Validate job ID length
	if len(jobID) == 0 {
		mc.recordShare(worker, false, 0, 0, "empty job id", "", nil, now)
		mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(20, "job id required")})
		return out, false
	}
	if len(jobID) > maxJobIDLen {
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
		// Validate version string length (should be 8-char hex for 4-byte value)
		if len(verStr) == 0 {
			mc.recordShare(worker, false, 0, 0, "empty version", "", nil, now)
			mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(20, "version required")})
			return out, false
		}
		if len(verStr) > maxVersionHexLen {
			logger.Warn("submit rejected: version too long", "remote", mc.id, "len", len(verStr))
			mc.recordShare(worker, false, 0, 0, "version too long", "", nil, now)
			mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(20, "version too long")})
			return out, false
		}
		// Stratum submit version is encoded as big-endian hex.
		verVal, err := parseUint32BEHex(verStr)
		if err != nil {
			mc.recordShare(worker, false, 0, 0, "invalid version", "", nil, now)
			mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(20, "invalid version")})
			return out, false
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

func (mc *MinerConn) handleSubmit(req *StratumRequest) {
	// Expect params like:
	// [worker_name, job_id, extranonce2, ntime, nonce]
	now := time.Now()

	task, ok := mc.prepareSubmissionTask(req, now)
	if !ok {
		return
	}
	if mc.cfg.DirectSubmitProcessing {
		mc.processSubmissionTask(task)
		return
	}
	ensureSubmissionWorkerPool()
	submissionWorkers.submit(task)
}

// prepareSubmissionTask validates a mining.submit request and, if valid, returns
// a fully-populated submissionTask. On any validation failure it writes the
// appropriate Stratum response and returns ok=false.
//
// This helper exists so benchmarks can include submit parsing/validation while
// still exercising the core share-processing path without extra goroutine
// scheduling noise.
func (mc *MinerConn) prepareSubmissionTask(req *StratumRequest, now time.Time) (submissionTask, bool) {
	if mc.cfg.SoloMode {
		return mc.prepareSubmissionTaskSolo(req, now)
	}
	return mc.prepareSubmissionTaskStrict(req, now)
}

func (mc *MinerConn) prepareSubmissionTaskSolo(req *StratumRequest, now time.Time) (submissionTask, bool) {
	params, ok := mc.parseSubmitParams(req, now)
	if !ok {
		return submissionTask{}, false
	}

	worker := params.worker
	jobID := params.jobID
	extranonce2 := params.extranonce2
	ntime := params.ntime
	nonce := params.nonce
	submittedVersion := params.submittedVersion

	if !mc.authorized {
		logger.Warn("submit rejected: unauthorized", "remote", mc.id)
		mc.recordShare(worker, false, 0, 0, "unauthorized", "", nil, now)
		if mc.metrics != nil {
			mc.metrics.RecordSubmitError("unauthorized")
		}
		mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(24, "unauthorized")})
		return submissionTask{}, false
	}

	authorizedWorker := strings.TrimSpace(mc.currentWorker())
	submitWorker := strings.TrimSpace(worker)
	if authorizedWorker != "" && submitWorker != authorizedWorker {
		logger.Warn("submit rejected: worker mismatch", "remote", mc.id, "authorized", authorizedWorker, "submitted", submitWorker)
		mc.recordShare(authorizedWorker, false, 0, 0, "unauthorized worker", "", nil, now)
		if mc.metrics != nil {
			mc.metrics.RecordSubmitError("worker_mismatch")
		}
		mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(24, "unauthorized")})
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
		mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(24, "banned")})
		return submissionTask{}, false
	}

	job, _, notifiedScriptTime, ok := mc.jobForIDWithLast(jobID)
	if !ok || job == nil {
		logger.Warn("submit rejected: stale job", "remote", mc.id, "job", jobID)
		// Use "job not found" for missing/expired jobs.
		mc.rejectShareWithBan(req, workerName, rejectStaleJob, 21, "job not found", now)
		return submissionTask{}, false
	}

	// Solo-mode: we only validate inputs enough to reconstruct the header and
	// compute PoW/difficulty. We intentionally skip pool policy checks.
	policyReject := submitPolicyReject{reason: rejectUnknown}

	if len(extranonce2) != job.Extranonce2Size*2 {
		logger.Warn("submit invalid extranonce2 length", "remote", mc.id, "got", len(extranonce2)/2, "expected", job.Extranonce2Size)
		mc.rejectShareWithBan(req, workerName, rejectInvalidExtranonce2, 20, "invalid extranonce2", now)
		return submissionTask{}, false
	}
	en2, err := hex.DecodeString(extranonce2)
	if err != nil {
		logger.Warn("submit bad extranonce2", "remote", mc.id, "error", err)
		mc.rejectShareWithBan(req, workerName, rejectInvalidExtranonce2, 20, "invalid extranonce2", now)
		return submissionTask{}, false
	}

	if len(ntime) != 8 {
		logger.Warn("submit invalid ntime length", "remote", mc.id, "len", len(ntime))
		mc.rejectShareWithBan(req, workerName, rejectInvalidNTime, 20, "invalid ntime", now)
		return submissionTask{}, false
	}
	if _, err := parseUint32BEHex(ntime); err != nil {
		logger.Warn("submit bad ntime", "remote", mc.id, "error", err)
		mc.rejectShareWithBan(req, workerName, rejectInvalidNTime, 20, "invalid ntime", now)
		return submissionTask{}, false
	}

	if len(nonce) != 8 {
		logger.Warn("submit invalid nonce length", "remote", mc.id, "len", len(nonce))
		mc.rejectShareWithBan(req, workerName, rejectInvalidNonce, 20, "invalid nonce", now)
		return submissionTask{}, false
	}
	if _, err := parseUint32BEHex(nonce); err != nil {
		logger.Warn("submit bad nonce", "remote", mc.id, "error", err)
		mc.rejectShareWithBan(req, workerName, rejectInvalidNonce, 20, "invalid nonce", now)
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
		reqID:            req.ID,
		job:              job,
		jobID:            jobID,
		workerName:       workerName,
		extranonce2:      extranonce2,
		extranonce2Bytes: en2,
		ntime:            ntime,
		nonce:            nonce,
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

	worker := params.worker
	jobID := params.jobID
	extranonce2 := params.extranonce2
	ntime := params.ntime
	nonce := params.nonce
	submittedVersion := params.submittedVersion

	if !mc.authorized {
		logger.Warn("submit rejected: unauthorized", "remote", mc.id)
		mc.recordShare(worker, false, 0, 0, "unauthorized", "", nil, now)
		if mc.metrics != nil {
			mc.metrics.RecordSubmitError("unauthorized")
		}
		mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(24, "unauthorized")})
		return submissionTask{}, false
	}

	authorizedWorker := strings.TrimSpace(mc.currentWorker())
	submitWorker := strings.TrimSpace(worker)
	if authorizedWorker != "" && submitWorker != authorizedWorker {
		logger.Warn("submit rejected: worker mismatch", "remote", mc.id, "authorized", authorizedWorker, "submitted", submitWorker)
		mc.recordShare(authorizedWorker, false, 0, 0, "unauthorized worker", "", nil, now)
		if mc.metrics != nil {
			mc.metrics.RecordSubmitError("worker_mismatch")
		}
		mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(24, "unauthorized")})
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
		mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(24, "banned")})
		return submissionTask{}, false
	}

	job, curLast, notifiedScriptTime, ok := mc.jobForIDWithLast(jobID)
	if !ok || job == nil {
		logger.Warn("submit rejected: stale job", "remote", mc.id, "job", jobID)
		// Use "job not found" for missing/expired jobs.
		mc.rejectShareWithBan(req, workerName, rejectStaleJob, 21, "job not found", now)
		return submissionTask{}, false
	}

	// Defensive: ensure the job template still matches what we advertised to this
	// connection (prevhash/height). If it changed underneath us, reject as stale.
	policyReject := submitPolicyReject{reason: rejectUnknown}
	if curLast != nil && curLast.Template.Previous != job.Template.Previous {
		logger.Warn("submit: stale job prevhash mismatch (policy)", "remote", mc.id, "job", jobID, "expected_prev", job.Template.Previous, "current_prev", curLast.Template.Previous)
		policyReject = submitPolicyReject{reason: rejectStaleJob, errCode: 21, errMsg: "job not found"}
	}

	if len(extranonce2) != job.Extranonce2Size*2 {
		logger.Warn("submit invalid extranonce2 length", "remote", mc.id, "got", len(extranonce2)/2, "expected", job.Extranonce2Size)
		mc.rejectShareWithBan(req, workerName, rejectInvalidExtranonce2, 20, "invalid extranonce2", now)
		return submissionTask{}, false
	}
	en2, err := hex.DecodeString(extranonce2)
	if err != nil {
		logger.Warn("submit bad extranonce2", "remote", mc.id, "error", err)
		mc.rejectShareWithBan(req, workerName, rejectInvalidExtranonce2, 20, "invalid extranonce2", now)
		return submissionTask{}, false
	}

	if len(ntime) != 8 {
		logger.Warn("submit invalid ntime length", "remote", mc.id, "len", len(ntime))
		mc.rejectShareWithBan(req, workerName, rejectInvalidNTime, 20, "invalid ntime", now)
		return submissionTask{}, false
	}
	// Stratum pools send ntime as BIG-ENDIAN hex and parse it back with parseInt(hex, 16).
	ntimeVal, err := parseUint32BEHex(ntime)
	if err != nil {
		logger.Warn("submit bad ntime", "remote", mc.id, "error", err)
		mc.rejectShareWithBan(req, workerName, rejectInvalidNTime, 20, "invalid ntime", now)
		return submissionTask{}, false
	}
	// Tight ntime bounds: require ntime to be >= the template's curtime
	// (or mintime when provided) and allow it to roll forward only a short
	// distance from the template.
	minNTime := job.Template.CurTime
	if job.Template.Mintime > 0 && job.Template.Mintime > minNTime {
		minNTime = job.Template.Mintime
	}
	ntimeForwardSlack := mc.cfg.NTimeForwardSlackSeconds
	if ntimeForwardSlack <= 0 {
		ntimeForwardSlack = defaultNTimeForwardSlackSeconds
	}
	maxNTime := minNTime + int64(ntimeForwardSlack)
	if int64(ntimeVal) < minNTime || int64(ntimeVal) > maxNTime {
		// Policy-only: for safety we still run the PoW check and, if the share is
		// a real block, submit it even if ntime violates the pool's tighter window.
		logger.Warn("submit ntime outside window (policy)", "remote", mc.id, "ntime", ntimeVal, "min", minNTime, "max", maxNTime)
		if policyReject.reason == rejectUnknown {
			policyReject = submitPolicyReject{reason: rejectInvalidNTime, errCode: 20, errMsg: "invalid ntime"}
		}
	}

	if len(nonce) != 8 {
		logger.Warn("submit invalid nonce length", "remote", mc.id, "len", len(nonce))
		mc.rejectShareWithBan(req, workerName, rejectInvalidNonce, 20, "invalid nonce", now)
		return submissionTask{}, false
	}
	// Nonce is sent as BIG-ENDIAN hex in mining.notify.
	if _, err := parseUint32BEHex(nonce); err != nil {
		logger.Warn("submit bad nonce", "remote", mc.id, "error", err)
		mc.rejectShareWithBan(req, workerName, rejectInvalidNonce, 20, "invalid nonce", now)
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
	if mc.cfg.CheckDuplicateShares || debugLogging || verboseLogging {
		versionHex = fmt.Sprintf("%08x", useVersion)
	}
	if versionDiff != 0 && !mc.versionRoll {
		logger.Warn("submit version rolling disabled (policy)", "remote", mc.id, "diff", fmt.Sprintf("%08x", versionDiff))
		if policyReject.reason == rejectUnknown {
			policyReject = submitPolicyReject{reason: rejectInvalidVersion, errCode: 20, errMsg: "version rolling not enabled"}
		}
	}
	if versionDiff&^mc.versionMask != 0 {
		logger.Warn("submit version outside mask (policy)", "remote", mc.id, "version", fmt.Sprintf("%08x", useVersion), "mask", fmt.Sprintf("%08x", mc.versionMask))
		if policyReject.reason == rejectUnknown {
			policyReject = submitPolicyReject{reason: rejectInvalidVersionMask, errCode: 20, errMsg: "invalid version mask"}
		}
	}
	if versionDiff != 0 && mc.minVerBits > 0 && bits.OnesCount32(versionDiff&mc.versionMask) < mc.minVerBits {
		if !mc.cfg.IgnoreMinVersionBits {
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
		reqID:            req.ID,
		job:              job,
		jobID:            jobID,
		workerName:       workerName,
		extranonce2:      extranonce2,
		extranonce2Bytes: en2,
		ntime:            ntime,
		nonce:            nonce,
		versionHex:       versionHex,
		useVersion:       useVersion,
		scriptTime:       notifiedScriptTime,
		policyReject:     policyReject,
		receivedAt:       now,
	}
	return task, true
}

type shareContext struct {
	header     []byte
	cbTx       []byte
	merkleRoot []byte
	hashLE     []byte
	hashHex    string
	shareDiff  float64
	isBlock    bool
}

func (mc *MinerConn) processSubmissionTask(task submissionTask) {
	workerName := task.workerName
	jobID := task.jobID
	extranonce2 := task.extranonce2
	ntime := task.ntime
	nonce := task.nonce
	versionHex := task.versionHex

	if debugLogging || verboseLogging {
		logger.Info("submit received",
			"remote", mc.id,
			"worker", workerName,
			"job", jobID,
			"extranonce2", extranonce2,
			"ntime", ntime,
			"nonce", nonce,
			"version", versionHex,
		)
	}

	if mc.cfg.SoloMode {
		ctx, ok := mc.prepareShareContextSolo(task)
		if !ok {
			return
		}
		mc.processSoloShare(task, ctx)
		return
	}
	ctx, ok := mc.prepareShareContextStrict(task)
	if !ok {
		return
	}
	mc.processRegularShare(task, ctx)
}

func (mc *MinerConn) prepareShareContextSolo(task submissionTask) (shareContext, bool) {
	// Solo mode keeps this hot path minimal: build header, compute hash/diff, and
	// detect block candidates. We intentionally skip strict verification and
	// avoid returning large buffers not needed for stats/accounting.
	job := task.job
	workerName := task.workerName
	jobID := task.jobID
	ntime := task.ntime
	nonce := task.nonce
	useVersion := task.useVersion
	scriptTime := task.scriptTime
	en2 := task.extranonce2Bytes
	reqID := task.reqID
	now := task.receivedAt

	if scriptTime == 0 {
		scriptTime = mc.scriptTimeForJob(jobID, job.ScriptTime)
	}

	var (
		header []byte
		cbTxid []byte
		err    error
	)

	// Rebuild coinbase+header. Dual/Triple payout paths are kept because they
	// affect the coinbase txid (and thus merkle root and header hash).
	if poolScript, workerScript, totalValue, feePercent, ok := mc.dualPayoutParams(job, workerName); ok {
		var merkleRoot []byte
		if job.OperatorDonationPercent > 0 && len(job.DonationScript) > 0 {
			_, cbTxid, err = serializeTripleCoinbaseTxPredecoded(
				job.Template.Height,
				mc.extranonce1,
				en2,
				job.TemplateExtraNonce2Size,
				poolScript,
				job.DonationScript,
				workerScript,
				totalValue,
				feePercent,
				job.OperatorDonationPercent,
				job.witnessCommitScript,
				job.coinbaseFlagsBytes,
				job.CoinbaseMsg,
				scriptTime,
			)
		} else {
			_, cbTxid, err = serializeDualCoinbaseTxPredecoded(
				job.Template.Height,
				mc.extranonce1,
				en2,
				job.TemplateExtraNonce2Size,
				poolScript,
				workerScript,
				totalValue,
				feePercent,
				job.witnessCommitScript,
				job.coinbaseFlagsBytes,
				job.CoinbaseMsg,
				scriptTime,
			)
		}
		if err == nil && len(cbTxid) == 32 {
			merkleRoot = computeMerkleRootFromBranches(cbTxid, job.MerkleBranches)
			header, err = job.buildBlockHeader(merkleRoot, ntime, nonce, int32(useVersion))
		}
	}

	if header == nil || err != nil || len(cbTxid) != 32 {
		_, cbTxid, err = serializeCoinbaseTxPredecoded(
			job.Template.Height,
			mc.extranonce1,
			en2,
			job.TemplateExtraNonce2Size,
			job.PayoutScript,
			job.CoinbaseValue,
			job.witnessCommitScript,
			job.coinbaseFlagsBytes,
			job.CoinbaseMsg,
			scriptTime,
		)
		if err != nil || len(cbTxid) != 32 {
			logger.Warn("submit coinbase rebuild failed", "remote", mc.id, "error", err)
			mc.recordShare(workerName, false, 0, 0, rejectInvalidCoinbase.String(), "", nil, now)
			mc.writeResponse(StratumResponse{
				ID:     reqID,
				Result: false,
				Error:  newStratumError(20, "invalid coinbase"),
			})
			return shareContext{}, false
		}
		merkleRoot := computeMerkleRootFromBranches(cbTxid, job.MerkleBranches)
		header, err = job.buildBlockHeader(merkleRoot, ntime, nonce, int32(useVersion))
		if err != nil {
			logger.Error("submit header build error", "remote", mc.id, "error", err)
			mc.recordShare(workerName, false, 0, 0, err.Error(), "", nil, now)
			if banned, invalids := mc.noteInvalidSubmit(now, rejectInvalidCoinbase); banned {
				mc.logBan(rejectInvalidCoinbase.String(), workerName, invalids)
				mc.writeResponse(StratumResponse{ID: reqID, Result: false, Error: newStratumError(24, "banned")})
			} else {
				mc.writeResponse(StratumResponse{ID: reqID, Result: false, Error: newStratumError(20, err.Error())})
			}
			return shareContext{}, false
		}
	}

	headerHashArray := doubleSHA256Array(header)

	var headerHashLE [32]byte
	copy(headerHashLE[:], headerHashArray[:])
	reverseBytes32(&headerHashLE)

	hashNum := bigIntPool.Get().(*big.Int)
	hashNum.SetBytes(headerHashLE[:])
	isBlock := hashNum.Cmp(job.Target) <= 0
	bigIntPool.Put(hashNum)

	hashHex := hex.EncodeToString(headerHashLE[:])

	return shareContext{
		hashHex:   hashHex,
		shareDiff: difficultyFromHash(headerHashArray[:]),
		isBlock:   isBlock,
	}, true
}

func (mc *MinerConn) prepareShareContextStrict(task submissionTask) (shareContext, bool) {
	job := task.job
	workerName := task.workerName
	jobID := task.jobID
	ntime := task.ntime
	nonce := task.nonce
	useVersion := task.useVersion
	scriptTime := task.scriptTime
	en2 := task.extranonce2Bytes
	reqID := task.reqID
	now := task.receivedAt

	if scriptTime == 0 {
		scriptTime = mc.scriptTimeForJob(jobID, job.ScriptTime)
	}

	var (
		header           []byte
		merkleRoot       []byte
		cbTx             []byte
		cbTxid           []byte
		usedDualCoinbase bool
		err              error
	)

	if poolScript, workerScript, totalValue, feePercent, ok := mc.dualPayoutParams(job, workerName); ok {
		if job.OperatorDonationPercent > 0 && len(job.DonationScript) > 0 {
			cbTx, cbTxid, err = serializeTripleCoinbaseTxPredecoded(
				job.Template.Height,
				mc.extranonce1,
				en2,
				job.TemplateExtraNonce2Size,
				poolScript,
				job.DonationScript,
				workerScript,
				totalValue,
				feePercent,
				job.OperatorDonationPercent,
				job.witnessCommitScript,
				job.coinbaseFlagsBytes,
				job.CoinbaseMsg,
				scriptTime,
			)
		} else {
			cbTx, cbTxid, err = serializeDualCoinbaseTxPredecoded(
				job.Template.Height,
				mc.extranonce1,
				en2,
				job.TemplateExtraNonce2Size,
				poolScript,
				workerScript,
				totalValue,
				feePercent,
				job.witnessCommitScript,
				job.coinbaseFlagsBytes,
				job.CoinbaseMsg,
				scriptTime,
			)
		}
		if err == nil && len(cbTxid) == 32 {
			merkleRoot = computeMerkleRootFromBranches(cbTxid, job.MerkleBranches)
			header, err = job.buildBlockHeader(merkleRoot, ntime, nonce, int32(useVersion))
			if err == nil {
				usedDualCoinbase = true
			}
		}
	}

	if header == nil || merkleRoot == nil || err != nil || len(cbTxid) != 32 {
		if err != nil && usedDualCoinbase {
			logger.Warn("dual-payout header build failed, falling back to single-output header",
				"error", err,
				"worker", workerName,
			)
		}
		cbTx, cbTxid, err = serializeCoinbaseTxPredecoded(
			job.Template.Height,
			mc.extranonce1,
			en2,
			job.TemplateExtraNonce2Size,
			job.PayoutScript,
			job.CoinbaseValue,
			job.witnessCommitScript,
			job.coinbaseFlagsBytes,
			job.CoinbaseMsg,
			scriptTime,
		)
		if err != nil || len(cbTxid) != 32 {
			logger.Warn("submit coinbase rebuild failed", "remote", mc.id, "error", err)
			mc.recordShare(workerName, false, 0, 0, rejectInvalidCoinbase.String(), "", nil, now)
			mc.writeResponse(StratumResponse{
				ID:     reqID,
				Result: false,
				Error:  newStratumError(20, "invalid coinbase"),
			})
			return shareContext{}, false
		}
		merkleRoot = computeMerkleRootFromBranches(cbTxid, job.MerkleBranches)
		header, err = job.buildBlockHeader(merkleRoot, ntime, nonce, int32(useVersion))
		if err != nil {
			logger.Error("submit header build error", "remote", mc.id, "error", err)
			mc.recordShare(workerName, false, 0, 0, err.Error(), "", nil, now)
			if banned, invalids := mc.noteInvalidSubmit(now, rejectInvalidCoinbase); banned {
				mc.logBan(rejectInvalidCoinbase.String(), workerName, invalids)
				mc.writeResponse(StratumResponse{ID: reqID, Result: false, Error: newStratumError(24, "banned")})
			} else {
				mc.writeResponse(StratumResponse{ID: reqID, Result: false, Error: newStratumError(20, err.Error())})
			}
			return shareContext{}, false
		}
	}

	expectedMerkle := computeMerkleRootFromBranches(cbTxid, job.MerkleBranches)
	if merkleRoot == nil || expectedMerkle == nil || !bytes.Equal(merkleRoot, expectedMerkle) {
		logger.Warn("submit merkle mismatch", "remote", mc.id, "worker", workerName, "job", jobID)
		var detail *ShareDetail
		if debugLogging || verboseLogging {
			detail = mc.buildShareDetailFromCoinbase(job, workerName, header, nil, nil, expectedMerkle, cbTx)
		}
		mc.recordShare(workerName, false, 0, 0, rejectInvalidMerkle.String(), "", detail, now)
		if banned, invalids := mc.noteInvalidSubmit(now, rejectInvalidMerkle); banned {
			mc.logBan(rejectInvalidMerkle.String(), workerName, invalids)
			mc.writeResponse(StratumResponse{ID: reqID, Result: false, Error: newStratumError(24, "banned")})
		} else {
			mc.writeResponse(StratumResponse{ID: reqID, Result: false, Error: newStratumError(20, "invalid merkle")})
		}
		return shareContext{}, false
	}

	headerHashArray := doubleSHA256Array(header)

	var headerHashLE [32]byte
	copy(headerHashLE[:], headerHashArray[:])
	reverseBytes32(&headerHashLE)

	hashNum := bigIntPool.Get().(*big.Int)
	hashNum.SetBytes(headerHashLE[:])
	isBlock := hashNum.Cmp(job.Target) <= 0
	bigIntPool.Put(hashNum)

	hashHex := hex.EncodeToString(headerHashLE[:])

	ctx := shareContext{
		hashHex:   hashHex,
		shareDiff: difficultyFromHash(headerHashArray[:]),
		isBlock:   isBlock,
	}
	// Only keep large buffers when detail logging is enabled.
	if debugLogging || verboseLogging {
		hashLE := make([]byte, len(headerHashLE))
		copy(hashLE, headerHashLE[:])
		ctx.header = header
		ctx.cbTx = cbTx
		ctx.merkleRoot = append([]byte(nil), merkleRoot...)
		ctx.hashLE = hashLE
	}
	return ctx, true
}

func (mc *MinerConn) processRegularShare(task submissionTask, ctx shareContext) {
	job := task.job
	workerName := task.workerName
	jobID := task.jobID
	policyReject := task.policyReject
	reqID := task.reqID
	now := task.receivedAt
	extranonce2 := task.extranonce2
	ntime := task.ntime
	nonce := task.nonce
	versionHex := task.versionHex

	assignedDiff := mc.assignedDifficulty(jobID)
	currentDiff := mc.currentDifficulty()
	creditedDiff := assignedDiff
	if creditedDiff <= 0 {
		creditedDiff = currentDiff
	}

	if !ctx.isBlock && policyReject.reason != rejectUnknown {
		mc.rejectShareWithBan(&StratumRequest{ID: reqID, Method: "mining.submit"}, workerName, policyReject.reason, policyReject.errCode, policyReject.errMsg, now)
		return
	}

	if !ctx.isBlock && mc.cfg.CheckDuplicateShares && mc.isDuplicateShare(jobID, extranonce2, ntime, nonce, versionHex) {
		logger.Warn("duplicate share", "remote", mc.id, "job", jobID, "extranonce2", extranonce2, "ntime", ntime, "nonce", nonce, "version", versionHex)
		mc.rejectShareWithBan(&StratumRequest{ID: reqID, Method: "mining.submit"}, workerName, rejectDuplicateShare, 22, "duplicate share", now)
		return
	}

	thresholdDiff := assignedDiff
	if thresholdDiff <= 0 {
		thresholdDiff = currentDiff
	}
	lowDiff := false
	if !ctx.isBlock && thresholdDiff > 0 {
		ratio := ctx.shareDiff / thresholdDiff
		if ratio < 0.98 {
			if !mc.meetsPrevDiffGrace(ctx.shareDiff, now) {
				lowDiff = true
			}
		}
	}

	if lowDiff {
		if debugLogging || verboseLogging {
			logger.Info("share rejected",
				"share_diff", ctx.shareDiff,
				"required_diff", thresholdDiff,
				"assigned_diff", assignedDiff,
				"current_diff", currentDiff,
			)
			logger.Warn("submit rejected: lowDiff",
				"miner", mc.minerName(workerName),
				"hash", ctx.hashHex,
			)
		}
		var detail *ShareDetail
		if debugLogging || verboseLogging {
			detail = mc.buildShareDetailFromCoinbase(job, workerName, ctx.header, ctx.hashLE, nil, ctx.merkleRoot, ctx.cbTx)
		}
		acceptedForStats := false
		mc.recordShare(workerName, acceptedForStats, 0, ctx.shareDiff, "lowDiff", ctx.hashHex, detail, now)

		if banned, invalids := mc.noteInvalidSubmit(now, rejectLowDiff); banned {
			mc.logBan(rejectLowDiff.String(), workerName, invalids)
			mc.writeResponse(StratumResponse{ID: reqID, Result: false, Error: newStratumError(24, "banned")})
		} else {
			mc.writeResponse(StratumResponse{
				ID:     reqID,
				Result: false,
				Error:  []interface{}{23, fmt.Sprintf("low difficulty share (%.6g expected %.6g)", ctx.shareDiff, assignedDiff), nil},
			})
		}
		return
	}

	shareHash := ctx.hashHex
	var detail *ShareDetail
	if debugLogging || verboseLogging {
		detail = mc.buildShareDetailFromCoinbase(job, workerName, ctx.header, ctx.hashLE, job.Target, ctx.merkleRoot, ctx.cbTx)
	}

	if ctx.isBlock {
		mc.handleBlockShare(reqID, job, workerName, task.extranonce2Bytes, task.ntime, task.nonce, task.useVersion, ctx.hashHex, ctx.shareDiff, now)
		mc.trackBestShare(workerName, shareHash, ctx.shareDiff, now)
		return
	}

	mc.recordShare(workerName, true, creditedDiff, ctx.shareDiff, "", shareHash, detail, now)
	mc.trackBestShare(workerName, shareHash, ctx.shareDiff, now)
	if mc.maybeAdjustDifficulty(now) {
		mc.sendNotifyFor(job, true)
	}

	if logger.Enabled(logLevelInfo) {
		stats, accRate, subRate := mc.snapshotStatsWithRates(now)
		miner := stats.Worker
		if miner == "" {
			miner = workerName
			if miner == "" {
				miner = mc.id
			}
		}
		logger.Info("share accepted",
			"miner", miner,
			"difficulty", ctx.shareDiff,
			"hash", ctx.hashHex,
			"accepted_total", stats.Accepted,
			"rejected_total", stats.Rejected,
			"worker_difficulty", stats.TotalDifficulty,
			"accept_rate_per_min", accRate,
			"submit_rate_per_min", subRate,
		)
	}
	mc.writeTrueResponse(reqID)
}

func (mc *MinerConn) processSoloShare(task submissionTask, ctx shareContext) {
	job := task.job
	workerName := task.workerName
	reqID := task.reqID
	now := task.receivedAt

	assignedDiff := mc.assignedDifficulty(task.jobID)
	currentDiff := mc.currentDifficulty()
	creditedDiff := assignedDiff
	if creditedDiff <= 0 {
		creditedDiff = currentDiff
	}

	if ctx.isBlock {
		mc.handleBlockShare(reqID, job, workerName, task.extranonce2Bytes, task.ntime, task.nonce, task.useVersion, ctx.hashHex, ctx.shareDiff, now)
		mc.trackBestShare(workerName, ctx.hashHex, ctx.shareDiff, now)
		return
	}

	shareHash := ctx.hashHex
	mc.recordShare(workerName, true, creditedDiff, ctx.shareDiff, "", shareHash, nil, now)
	mc.trackBestShare(workerName, shareHash, ctx.shareDiff, now)
	if mc.maybeAdjustDifficulty(now) {
		mc.sendNotifyFor(job, true)
	}

	if logger.Enabled(logLevelInfo) {
		stats, accRate, subRate := mc.snapshotStatsWithRates(now)
		miner := stats.Worker
		if miner == "" {
			miner = workerName
			if miner == "" {
				miner = mc.id
			}
		}
		logger.Info("share accepted",
			"miner", miner,
			"difficulty", ctx.shareDiff,
			"hash", ctx.hashHex,
			"accepted_total", stats.Accepted,
			"rejected_total", stats.Rejected,
			"worker_difficulty", stats.TotalDifficulty,
			"accept_rate_per_min", accRate,
			"submit_rate_per_min", subRate,
		)
	}
	mc.writeTrueResponse(reqID)
}

// handleBlockShare processes a share that satisfies the network target. It
// builds the full block (reusing any dual-payout header/coinbase when
// available), submits it via RPC, logs the reward split and found-block
// record, and sends the final Stratum response.
func (mc *MinerConn) handleBlockShare(reqID interface{}, job *Job, workerName string, en2 []byte, ntime string, nonce string, useVersion uint32, hashHex string, shareDiff float64, now time.Time) {
	var (
		blockHex  string
		submitRes interface{}
		err       error
	)
	scriptTime := mc.scriptTimeForJob(job.JobID, job.ScriptTime)

	// Only construct the full block (including all non-coinbase transactions)
	// when the share actually satisfies the network target.
	if poolScript, workerScript, totalValue, feePercent, ok := mc.dualPayoutParams(job, workerName); ok {
		var cbTx, cbTxid []byte
		var err error
		if job.OperatorDonationPercent > 0 && len(job.DonationScript) > 0 {
			cbTx, cbTxid, err = serializeTripleCoinbaseTxPredecoded(
				job.Template.Height,
				mc.extranonce1,
				en2,
				job.TemplateExtraNonce2Size,
				poolScript,
				job.DonationScript,
				workerScript,
				totalValue,
				feePercent,
				job.OperatorDonationPercent,
				job.witnessCommitScript,
				job.coinbaseFlagsBytes,
				job.CoinbaseMsg,
				scriptTime,
			)
		} else {
			cbTx, cbTxid, err = serializeDualCoinbaseTxPredecoded(
				job.Template.Height,
				mc.extranonce1,
				en2,
				job.TemplateExtraNonce2Size,
				poolScript,
				workerScript,
				totalValue,
				feePercent,
				job.witnessCommitScript,
				job.coinbaseFlagsBytes,
				job.CoinbaseMsg,
				scriptTime,
			)
		}
		if err == nil && len(cbTxid) == 32 {
			merkleRoot := computeMerkleRootFromBranches(cbTxid, job.MerkleBranches)
			header, err := job.buildBlockHeader(merkleRoot, ntime, nonce, int32(useVersion))
			if err == nil {
				buf := blockBufferPool.Get().(*bytes.Buffer)
				buf.Reset()
				defer blockBufferPool.Put(buf)

				buf.Write(header)
				writeVarInt(buf, uint64(1+len(job.Transactions)))
				buf.Write(cbTx)
				for _, tx := range job.Transactions {
					raw, derr := hex.DecodeString(tx.Data)
					if derr != nil {
						err = fmt.Errorf("decode tx data: %w", derr)
						break
					}
					buf.Write(raw)
				}
				if err == nil {
					blockHex = hex.EncodeToString(buf.Bytes())
				}
			}
		}
	}
	if blockHex == "" {
		// Fallback to single-output block build if dual-payout params are
		// unavailable or any step fails. This reuses the existing helper that
		// constructs a canonical block for submission.
		blockHex, _, _, _, err = buildBlockWithScriptTime(job, mc.extranonce1, en2, ntime, nonce, int32(useVersion), scriptTime)
		if err != nil {
			if mc.metrics != nil {
				mc.metrics.RecordBlockSubmission("error")
				mc.metrics.RecordErrorEvent("submitblock", err.Error(), now)
			}
			logger.Error("submitblock build error", "remote", mc.id, "error", err)
			mc.writeResponse(StratumResponse{ID: reqID, Result: false, Error: newStratumError(20, err.Error())})
			return
		}
	}

	// Submit the block via RPC using an aggressive, no-backoff retry loop
	// so we race the rest of the network as hard as possible. This path is
	// intentionally not tied to the miner or process context so shutdown
	// signals do not cancel in-flight submissions.
	err = mc.submitBlockWithFastRetry(job, workerName, hashHex, blockHex, &submitRes)
	if err != nil {
		if mc.metrics != nil {
			mc.metrics.RecordBlockSubmission("error")
			mc.metrics.RecordErrorEvent("submitblock", err.Error(), time.Now())
		}
		logger.Error("submitblock error", "error", err)
		// Best-effort: record this block for manual or future retry when the
		// node RPC is unavailable or submitblock fails. This does not imply
		// that the block was accepted; it only preserves the data needed for
		// a later submitblock attempt.
		mc.logPendingSubmission(job, workerName, hashHex, blockHex, err)
		mc.writeResponse(StratumResponse{ID: reqID, Result: false, Error: newStratumError(20, err.Error())})
		return
	}
	if mc.metrics != nil {
		mc.metrics.RecordBlockSubmission("accepted")
	}

	// For solo mining, treat the worker that submitted the block as the
	// beneficiary of the block reward. We always split the reward between
	// the pool fee and worker payout for logging purposes.
	if logger.Enabled(logLevelInfo) && workerName != "" && job != nil && job.CoinbaseValue > 0 {
		total := job.CoinbaseValue
		feePct := mc.cfg.PoolFeePercent
		if feePct < 0 {
			feePct = 0
		}
		if feePct > 99.99 {
			feePct = 99.99
		}
		poolFee := int64(math.Round(float64(total) * feePct / 100.0))
		if poolFee < 0 {
			poolFee = 0
		}
		if poolFee > total {
			poolFee = total
		}
		minerAmt := total - poolFee
		if minerAmt > 0 {
			logger.Info("block reward split",
				"miner", mc.minerName(workerName),
				"worker_address", workerName,
				"height", job.Template.Height,
				"block_value_sats", total,
				"pool_fee_sats", poolFee,
				"worker_payout_sats", minerAmt,
				"fee_percent", feePct,
			)
		}
	}

	var stats MinerStats
	if logger.Enabled(logLevelInfo) {
		stats = mc.snapshotStats()
	}
	mc.logFoundBlock(job, workerName, hashHex, shareDiff)
	if logger.Enabled(logLevelInfo) {
		logger.Info("block found",
			"miner", mc.minerName(workerName),
			"height", job.Template.Height,
			"hash", hashHex,
			"accepted_total", stats.Accepted,
			"rejected_total", stats.Rejected,
			"worker_difficulty", stats.TotalDifficulty,
		)
	}
	mc.writeTrueResponse(reqID)
}

// logFoundBlock appends a JSON line describing a found block to a log file in
// the data directory. This is purely for operator audit/debugging and is best
// effort; failures are logged but do not affect pool operation.
func (mc *MinerConn) logFoundBlock(job *Job, worker, hashHex string, shareDiff float64) {
	dir := mc.cfg.DataDir
	if dir == "" {
		dir = defaultDataDir
	}
	// Compute a simple view of the payout split used for this block. In
	// dual-payout mode with a validated worker script, the coinbase uses a
	// pool-fee + worker output; otherwise the entire reward is logically
	// treated as a worker payout in single mode, or sent to the pool in
	// dual-payout fallback cases.
	total := job.Template.CoinbaseValue
	feePct := mc.cfg.PoolFeePercent
	if feePct < 0 {
		feePct = 0
	}
	if feePct > 99.99 {
		feePct = 99.99
	}
	poolFee := int64(math.Round(float64(total) * feePct / 100.0))
	if poolFee < 0 {
		poolFee = 0
	}
	if poolFee > total {
		poolFee = total
	}
	workerAmt := total - poolFee
	// If dual payout is disabled, treat the full reward as a worker payout
	// ("Single" mode = miner only). When dual payout is enabled but the
	// worker has no cached script or the worker wallet equals the pool
	// payout address, treat this block as pool-only and record the full
	// amount as pool_fee_sats with dual_payout_fallback=true.
	dualFallback := false
	workerAddr := ""
	if worker != "" {
		raw := strings.TrimSpace(worker)
		if parts := strings.SplitN(raw, ".", 2); len(parts) > 1 {
			raw = parts[0]
		}
		workerAddr = sanitizePayoutAddress(raw)
	}
	// Check if we fell back to single-output coinbase (worker wallet matches pool wallet)
	if len(mc.workerPayoutScript(worker)) == 0 || (workerAddr != "" && strings.EqualFold(workerAddr, mc.cfg.PayoutAddress)) {
		poolFee = total
		workerAmt = 0
		dualFallback = true
	}

	rec := map[string]interface{}{
		"timestamp":            time.Now().UTC(),
		"height":               job.Template.Height,
		"hash":                 hashHex,
		"worker":               mc.minerName(worker),
		"share_diff":           shareDiff,
		"job_id":               job.JobID,
		"payout_address":       mc.cfg.PayoutAddress,
		"coinbase_value_sats":  total,
		"pool_fee_sats":        poolFee,
		"worker_payout_sats":   workerAmt,
		"dual_payout_fallback": dualFallback,
	}
	data, err := fastJSONMarshal(rec)
	if err != nil {
		logger.Warn("found block log marshal", "error", err)
		return
	}
	line := append(data, '\n')
	select {
	case foundBlockLogCh <- foundBlockLogEntry{Dir: dir, Line: line}:
	default:
		// If the queue is full, drop the log entry rather than blocking
		// the submit path; this log is best-effort operator metadata.
		logger.Warn("found block log queue full; dropping entry")
	}
}

// logPendingSubmission appends a JSON line describing a block that failed
// submitblock to a log file in the data directory. This allows operators to
// manually retry submission with bitcoin-cli or future tooling when the node
// RPC is down or returns an error. It is best effort only.
func (mc *MinerConn) logPendingSubmission(job *Job, worker, hashHex, blockHex string, submitErr error) {
	if job == nil || blockHex == "" {
		return
	}
	rec := pendingSubmissionRecord{
		Timestamp:  time.Now().UTC(),
		Height:     job.Template.Height,
		Hash:       hashHex,
		Worker:     mc.minerName(worker),
		BlockHex:   blockHex,
		RPCError:   submitErr.Error(),
		RPCURL:     mc.cfg.RPCURL,
		PayoutAddr: mc.cfg.PayoutAddress,
		Status:     "pending",
	}
	appendPendingSubmissionRecord(pendingSubmissionsPath(mc.cfg), rec)
}
