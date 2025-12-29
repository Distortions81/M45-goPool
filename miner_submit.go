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

	workerName := mc.updateWorker(worker)
	if mc.isBanned(now) {
		until, reason, _ := mc.banDetails()
		logger.Warn("submit rejected: banned", "miner", mc.minerName(workerName), "ban_until", until, "reason", reason)
		if mc.metrics != nil {
			mc.metrics.RecordSubmitError("banned")
		}
		mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(24, "banned")})
		return submissionTask{}, false
	}

	job, ok := mc.jobForID(jobID)
	if !ok || job == nil {
		logger.Warn("submit rejected: stale job", "remote", mc.id, "job", jobID)
		// Use "job not found" for missing/expired jobs.
		mc.rejectShareWithBan(req, workerName, rejectStaleJob, 21, "job not found", now)
		return submissionTask{}, false
	}

	// Defensive: ensure the job template still matches what we advertised to this
	// connection (prevhash/height). If it changed underneath us, reject as stale.
	mc.jobMu.Lock()
	curLast := mc.lastJob
	mc.jobMu.Unlock()
	if curLast != nil && curLast.Template.Previous != job.Template.Previous {
		logger.Warn("submit rejected: stale job prevhash mismatch", "remote", mc.id, "job", jobID, "expected_prev", job.Template.Previous, "current_prev", curLast.Template.Previous)
		mc.rejectShareWithBan(req, workerName, rejectStaleJob, 21, "job not found", now)
		return submissionTask{}, false
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
		logger.Warn("submit ntime outside window", "remote", mc.id, "ntime", ntimeVal, "min", minNTime, "max", maxNTime)
		mc.rejectShareWithBan(req, workerName, rejectInvalidNTime, 20, "invalid ntime", now)
		return submissionTask{}, false
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

	versionHex := fmt.Sprintf("%08x", useVersion)
	if versionDiff != 0 && !mc.versionRoll {
		logger.Warn("submit rejected: version rolling disabled", "remote", mc.id, "diff", fmt.Sprintf("%08x", versionDiff))
		mc.rejectShareWithBan(req, workerName, rejectInvalidVersion, 20, "version rolling not enabled", now)
		return submissionTask{}, false
	}
	if versionDiff&^mc.versionMask != 0 {
		logger.Warn("submit rejected: version outside mask", "remote", mc.id, "version", fmt.Sprintf("%08x", useVersion), "mask", fmt.Sprintf("%08x", mc.versionMask))
		mc.rejectShareWithBan(req, workerName, rejectInvalidVersionMask, 20, "invalid version mask", now)
		return submissionTask{}, false
	}
	if versionDiff != 0 && mc.minVerBits > 0 && bits.OnesCount32(versionDiff&mc.versionMask) < mc.minVerBits {
		logger.Warn("submit rejected: insufficient version rolling bits", "remote", mc.id, "version", fmt.Sprintf("%08x", useVersion), "required_bits", mc.minVerBits)
		mc.rejectShareWithBan(req, workerName, rejectInsufficientVersionBits, 20, "insufficient version bits", now)
		return submissionTask{}, false
	}

	if mc.isDuplicateShare(job.JobID, extranonce2, ntime, nonce, versionHex) {
		logger.Warn("duplicate share", "remote", mc.id, "job", jobID, "extranonce2", extranonce2, "ntime", ntime, "nonce", nonce, "version", versionHex)
		mc.rejectShareWithBan(req, workerName, rejectDuplicateShare, 22, "duplicate share", now)
		return submissionTask{}, false
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
		receivedAt:       now,
	}
	return task, true
}

func (mc *MinerConn) processSubmissionTask(task submissionTask) {
	job := task.job
	workerName := task.workerName
	jobID := task.jobID
	extranonce2 := task.extranonce2
	en2 := task.extranonce2Bytes
	ntime := task.ntime
	nonce := task.nonce
	versionHex := task.versionHex
	useVersion := task.useVersion
	reqID := task.reqID
	now := task.receivedAt

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

	var (
		headerHash       []byte
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
				job.ScriptTime,
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
				job.ScriptTime,
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
			job.ScriptTime,
		)
		if err != nil || len(cbTxid) != 32 {
			logger.Warn("submit coinbase rebuild failed", "remote", mc.id, "error", err)
			mc.recordShare(workerName, false, 0, 0, rejectInvalidCoinbase.String(), "", nil, now)
			mc.writeResponse(StratumResponse{
				ID:     reqID,
				Result: false,
				Error:  newStratumError(20, "invalid coinbase"),
			})
			return
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
			return
		}
	}

	expectedMerkle := computeMerkleRootFromBranches(cbTxid, job.MerkleBranches)
	if merkleRoot == nil || expectedMerkle == nil || !bytes.Equal(merkleRoot, expectedMerkle) {
		logger.Warn("submit merkle mismatch", "remote", mc.id, "worker", workerName, "job", jobID)
		detail := &ShareDetail{
			Header:         hex.EncodeToString(header),
			ShareHash:      hex.EncodeToString(reverseBytes(headerHash)),
			MerkleBranches: append([]string{}, job.MerkleBranches...),
			MerkleRootBE:   hex.EncodeToString(expectedMerkle),
			MerkleRootLE:   hex.EncodeToString(reverseBytes(expectedMerkle)),
			Coinbase:       hex.EncodeToString(cbTx),
		}
		detail.DecodeCoinbaseFields()
		mc.recordShare(workerName, false, 0, 0, rejectInvalidMerkle.String(), "", detail, now)
		if banned, invalids := mc.noteInvalidSubmit(now, rejectInvalidMerkle); banned {
			mc.logBan(rejectInvalidMerkle.String(), workerName, invalids)
			mc.writeResponse(StratumResponse{ID: reqID, Result: false, Error: newStratumError(24, "banned")})
		} else {
			mc.writeResponse(StratumResponse{ID: reqID, Result: false, Error: newStratumError(20, "invalid merkle")})
		}
		return
	}

	headerHashArray := doubleSHA256Array(header)
	headerHash = headerHashArray[:]

	var headerHashLE [32]byte
	copy(headerHashLE[:], headerHashArray[:])
	reverseBytes32(&headerHashLE)

	hashNum := bigIntPool.Get().(*big.Int)
	hashNum.SetBytes(headerHashLE[:])
	defer bigIntPool.Put(hashNum)

	hashLE := headerHashLE[:]
	shareDiff := difficultyFromHash(headerHash)
	hashHex := hex.EncodeToString(hashLE)
	assignedDiff := mc.assignedDifficulty(jobID)
	currentDiff := mc.currentDifficulty()
	creditedDiff := assignedDiff
	if creditedDiff <= 0 {
		creditedDiff = currentDiff
	}

	isBlock := hashNum.Cmp(job.Target) <= 0

	lowDiff := false
	thresholdDiff := assignedDiff
	if thresholdDiff <= 0 {
		thresholdDiff = currentDiff
	}
	if !isBlock && thresholdDiff > 0 {
		ratio := shareDiff / thresholdDiff
		if ratio < 0.98 {
			lowDiff = true
		}
	}

	if lowDiff {
		if debugLogging || verboseLogging {
			logger.Info("share rejected",
				"share_diff", shareDiff,
				"required_diff", thresholdDiff,
				"assigned_diff", assignedDiff,
				"current_diff", currentDiff,
			)
			logger.Warn("submit rejected: lowDiff",
				"miner", mc.minerName(workerName),
				"hash", hashHex,
			)
		}
		detail := mc.buildShareDetail(job, workerName, header, hashLE, nil, extranonce2, merkleRoot)
		acceptedForStats := false
		mc.recordShare(workerName, acceptedForStats, 0, shareDiff, "lowDiff", hashHex, detail, now)

		if banned, invalids := mc.noteInvalidSubmit(now, rejectLowDiff); banned {
			mc.logBan(rejectLowDiff.String(), workerName, invalids)
			mc.writeResponse(StratumResponse{ID: reqID, Result: false, Error: newStratumError(24, "banned")})
		} else {
			mc.writeResponse(StratumResponse{
				ID:     reqID,
				Result: false,
				Error:  []interface{}{23, fmt.Sprintf("low difficulty share (%.6g expected %.6g)", shareDiff, assignedDiff), nil},
			})
		}
		return
	}

	shareHash := hashHex
	detail := mc.buildShareDetail(job, workerName, header, hashLE, job.Target, extranonce2, merkleRoot)

	if isBlock {
		mc.handleBlockShare(reqID, job, workerName, en2, ntime, nonce, useVersion, hashHex, shareDiff, now)
		mc.trackBestShare(workerName, shareHash, shareDiff, now)
		return
	}

	mc.recordShare(workerName, true, creditedDiff, shareDiff, "", shareHash, detail, now)
	mc.trackBestShare(workerName, shareHash, shareDiff, now)
	if mc.maybeAdjustDifficulty(now) {
		mc.sendNotifyFor(job, true)
	}

	accRate, subRate := mc.shareRates(now)
	stats := mc.snapshotStats()
	logger.Info("share accepted",
		"miner", mc.minerName(workerName),
		"difficulty", shareDiff,
		"hash", hashHex,
		"accepted_total", stats.Accepted,
		"rejected_total", stats.Rejected,
		"worker_difficulty", stats.TotalDifficulty,
		"accept_rate_per_min", accRate,
		"submit_rate_per_min", subRate,
	)
	mc.writeResponse(StratumResponse{ID: reqID, Result: true, Error: nil})
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
				job.ScriptTime,
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
				job.ScriptTime,
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
		blockHex, _, _, _, err = buildBlock(job, mc.extranonce1, en2, ntime, nonce, int32(useVersion))
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
	if workerName != "" && job != nil && job.CoinbaseValue > 0 {
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

	stats := mc.snapshotStats()
	mc.logFoundBlock(job, workerName, hashHex, shareDiff)
	logger.Info("block found",
		"miner", mc.minerName(workerName),
		"height", job.Template.Height,
		"hash", hashHex,
		"accepted_total", stats.Accepted,
		"rejected_total", stats.Rejected,
		"worker_difficulty", stats.TotalDifficulty,
	)
	mc.writeResponse(StratumResponse{ID: reqID, Result: true, Error: nil})
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
