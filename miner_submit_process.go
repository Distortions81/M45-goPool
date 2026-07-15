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

	if debugLogging || verboseRuntimeLogging {
		logger.Debug("submit received",
			"component", "miner",
			"kind", "submit",
			"remote", mc.id,
			"worker", workerName,
			"job", jobID,
			"extranonce2", extranonce2,
			"ntime", ntime,
			"nonce", nonce,
			"version", versionHex,
		)
	}

	ctx, ok := mc.prepareShareContext(task)
	if !ok {
		return
	}
	mc.processShare(task, ctx)
}

func (mc *MinerConn) processShare(task submissionTask, ctx shareContext) {
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

	assignedDiff := task.assignedDifficulty
	if assignedDiff <= 0 {
		assignedDiff = mc.assignedDifficulty(jobID)
	}
	currentDiff := mc.currentDifficulty()
	creditedDiff := assignedDiff
	if creditedDiff <= 0 {
		creditedDiff = currentDiff
	}

	thresholdDiff := assignedDiff
	if thresholdDiff <= 0 {
		thresholdDiff = currentDiff
	}

	if task.hasAlternateVersion {
		altTask := task
		altTask.useVersion = task.alternateUseVersion
		altTask.versionHex = task.alternateVersionHex
		if altCtx, ok := mc.prepareShareContext(altTask); ok {
			primaryAcceptable := mc.submissionMeetsAssignedDifficulty(ctx, thresholdDiff, now)
			alternateAcceptable := mc.submissionMeetsAssignedDifficulty(altCtx, thresholdDiff, now)
			if preferAlternateSubmissionContext(ctx, altCtx, primaryAcceptable, alternateAcceptable) {
				task = altTask
				ctx = altCtx
				policyReject = task.policyReject
				versionHex = task.versionHex
			}
		}
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
			ntimeLog = uint32ToHex8Lower(task.ntimeVal)
		}
		nonceLog := nonce
		if nonceLog == "" {
			nonceLog = uint32ToHex8Lower(task.nonceVal)
		}
		verLog := versionHex
		if verLog == "" {
			verLog = uint32ToHex8Lower(task.useVersion)
		}
		logger.Info("duplicate share",
			"component", "miner",
			"kind", "reject",
			"remote", mc.id,
			"job", jobID,
			"extranonce2", ex2Log,
			"ntime", ntimeLog,
			"nonce", nonceLog,
			"version", verLog,
		)
		mc.rejectShareWithBan(&StratumRequest{ID: reqID, Method: "mining.submit"}, workerName, rejectDuplicateShare, stratumErrCodeDuplicateShare, "duplicate share", now)
		return
	}

	lowDiff := !mc.submissionMeetsAssignedDifficulty(ctx, thresholdDiff, now)
	if lowDiff {
		if debugLogging || verboseRuntimeLogging {
			logger.Info("share rejected",
				"component", "miner",
				"kind", "reject",
				"share_diff", ctx.shareDiff,
				"required_diff", thresholdDiff,
				"assigned_diff", assignedDiff,
				"current_diff", currentDiff,
			)
			logger.Info("submit rejected: lowDiff",
				"component", "miner",
				"kind", "reject",
				"miner", mc.minerName(workerName),
				"hash", ctx.hashHex,
			)
		}
		var detail *ShareDetail
		if debugLogging || verboseRuntimeLogging {
			detail = mc.buildShareDetailFromCoinbase(job, ctx.cbTx)
		}
		acceptedForStats := false
		mc.recordShare(workerName, acceptedForStats, 0, ctx.shareDiff, "lowDiff", ctx.hashHex, detail, now)

		if banned, invalids := mc.noteInvalidSubmit(now, rejectLowDiff); banned {
			mc.logBan(rejectLowDiff.String(), workerName, invalids)
			mc.writeResponse(StratumResponse{ID: reqID, Result: false, Error: mc.bannedStratumError()})
		} else {
			mc.writeResponse(StratumResponse{
				ID:     reqID,
				Result: false,
				Error:  []any{stratumErrCodeLowDiffShare, fmt.Sprintf("low difficulty share (%.6g expected %.6g)", ctx.shareDiff, assignedDiff), nil},
			})
		}
		return
	}

	shareHash := ctx.hashHex
	var detail *ShareDetail
	if debugLogging || verboseRuntimeLogging {
		detail = mc.buildShareDetailFromCoinbase(job, ctx.cbTx)
	}

	if ctx.isBlock {
		mc.noteValidSubmit(now)
		mc.handleBlockShare(reqID, job, task.jobID, workerName, (&task).extranonce2Decoded(), uint32ToHex8Lower(task.ntimeVal), uint32ToHex8Lower(task.nonceVal), task.useVersion, task.scriptTime, ctx.header, ctx.cbTx, ctx.hashHex, ctx.shareDiff, now)
		mc.trackBestShare(workerName, shareHash, ctx.shareDiff, now)
		mc.maybeUpdateSavedWorkerMinuteBestDiffFor(workerName, ctx.shareDiff, now)
		mc.maybeUpdateSavedWorkerBestDiffFor(workerName, ctx.shareDiff)
		return
	}

	mc.noteValidSubmit(now)
	mc.recordShare(workerName, true, creditedDiff, ctx.shareDiff, "", shareHash, detail, now)

	// Respond first; any vardiff adjustment and follow-up notify can happen after
	// the submit is acknowledged to minimize perceived submit latency.
	mc.writeTrueResponse(reqID)

	mc.trackBestShare(workerName, shareHash, ctx.shareDiff, now)
	mc.maybeUpdateSavedWorkerMinuteBestDiffFor(workerName, ctx.shareDiff, now)
	mc.maybeUpdateSavedWorkerBestDiffFor(workerName, ctx.shareDiff)

	if mc.maybeAdjustDifficulty(now) {
		mc.sendNotifyFor(job, true)
	}

	if (debugLogging || verboseRuntimeLogging) && logger.Enabled(logLevelInfo) {
		stats, accRate, subRate := mc.snapshotStatsWithRates(now)
		miner := stats.Worker
		if miner == "" {
			miner = workerName
			if miner == "" {
				miner = mc.id
			}
		}
		logger.Info("share accepted",
			"component", "miner",
			"kind", "share",
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

func (mc *MinerConn) submissionMeetsAssignedDifficulty(ctx shareContext, thresholdDiff float64, now time.Time) bool {
	if ctx.isBlock {
		return true
	}
	if thresholdDiff <= 0 {
		return true
	}
	ratio := ctx.shareDiff / thresholdDiff
	return ratio >= 0.98 || mc.meetsPrevDiffGrace(ctx.shareDiff, now)
}

func preferAlternateSubmissionContext(primaryCtx, alternateCtx shareContext, primaryAcceptable, alternateAcceptable bool) bool {
	if alternateCtx.isBlock && !primaryCtx.isBlock {
		return true
	}
	if primaryCtx.isBlock {
		return false
	}
	return !primaryAcceptable && alternateAcceptable
}
