package main

import (
	"encoding/hex"
	"fmt"
	"time"
)

func (mc *MinerConn) processSubmissionTask(task submissionTask) {
	start := task.receivedAt
	if start.IsZero() {
		start = time.Now()
	}
	defer func() {
		mc.recordSubmitRTT(time.Since(start))
	}()

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

	if !mc.useStrictSubmitPath() {
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

	if !ctx.isBlock && mc.cfg.ShareCheckDuplicate && mc.isDuplicateShare(jobID, (&task).extranonce2Decoded(), task.ntimeVal, task.nonceVal, task.useVersion) {
		ex2Log := extranonce2
		if ex2Log == "" {
			ex2Log = hex.EncodeToString((&task).extranonce2Decoded())
		}
		ntimeLog := ntime
		if ntimeLog == "" {
			ntimeLog = fmt.Sprintf("%08x", task.ntimeVal)
		}
		nonceLog := nonce
		if nonceLog == "" {
			nonceLog = fmt.Sprintf("%08x", task.nonceVal)
		}
		verLog := versionHex
		if verLog == "" {
			verLog = fmt.Sprintf("%08x", task.useVersion)
		}
		logger.Warn("duplicate share", "remote", mc.id, "job", jobID, "extranonce2", ex2Log, "ntime", ntimeLog, "nonce", nonceLog, "version", verLog)
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
			detail = mc.buildShareDetailFromCoinbase(job, ctx.cbTx)
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
				Error:  []any{23, fmt.Sprintf("low difficulty share (%.6g expected %.6g)", ctx.shareDiff, assignedDiff), nil},
			})
		}
		return
	}

	shareHash := ctx.hashHex
	var detail *ShareDetail
	if debugLogging || verboseLogging {
		detail = mc.buildShareDetailFromCoinbase(job, ctx.cbTx)
	}

	if ctx.isBlock {
		mc.noteValidSubmit(now)
		mc.handleBlockShare(reqID, job, workerName, (&task).extranonce2Decoded(), fmt.Sprintf("%08x", task.ntimeVal), fmt.Sprintf("%08x", task.nonceVal), task.useVersion, ctx.hashHex, ctx.shareDiff, now)
		mc.trackBestShare(workerName, shareHash, ctx.shareDiff, now)
		mc.maybeUpdateSavedWorkerBestDiff(ctx.shareDiff)
		return
	}

	mc.noteValidSubmit(now)
	mc.recordShare(workerName, true, creditedDiff, ctx.shareDiff, "", shareHash, detail, now)
	mc.trackBestShare(workerName, shareHash, ctx.shareDiff, now)
	mc.maybeUpdateSavedWorkerBestDiff(ctx.shareDiff)

	// Respond first; any vardiff adjustment and follow-up notify can happen after
	// the submit is acknowledged to minimize perceived submit latency.
	mc.writeTrueResponse(reqID)

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
		mc.noteValidSubmit(now)
		mc.handleBlockShare(reqID, job, workerName, (&task).extranonce2Decoded(), fmt.Sprintf("%08x", task.ntimeVal), fmt.Sprintf("%08x", task.nonceVal), task.useVersion, ctx.hashHex, ctx.shareDiff, now)
		mc.trackBestShare(workerName, ctx.hashHex, ctx.shareDiff, now)
		mc.maybeUpdateSavedWorkerBestDiff(ctx.shareDiff)
		return
	}

	mc.noteValidSubmit(now)
	shareHash := ctx.hashHex
	mc.recordShare(workerName, true, creditedDiff, ctx.shareDiff, "", shareHash, nil, now)
	mc.trackBestShare(workerName, shareHash, ctx.shareDiff, now)
	mc.maybeUpdateSavedWorkerBestDiff(ctx.shareDiff)

	// Respond first; vardiff adjustments and notifies can follow.
	mc.writeTrueResponse(reqID)

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
}
