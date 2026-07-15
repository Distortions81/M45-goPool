package main

import (
	"encoding/hex"
	"fmt"
	"time"
)

func (mc *MinerConn) processSubmissionTask(task submissionTask) {
	defer mc.recordSubmissionTaskRTT(task)
	mc.logSubmissionTask(task)
	prepared, ok := mc.prepareSubmissionForProcessing(task)
	if !ok {
		return
	}
	mc.processPreparedShare(prepared)
}

// processPreparedSubmissionTask completes a submission whose exact header and
// every plausible version interpretation were already hashed before ordinary
// queue admission.
func (mc *MinerConn) processPreparedSubmissionTask(prepared preparedSubmissionTask) {
	defer mc.recordSubmissionTaskRTT(prepared.task)
	mc.processPreparedShare(prepared)
}

func (mc *MinerConn) recordSubmissionTaskRTT(task submissionTask) {
	start := task.receivedAt
	if start.IsZero() {
		start = time.Now()
	}
	mc.recordSubmitRTT(time.Since(start))
}

func (mc *MinerConn) logSubmissionTask(task submissionTask) {
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
}

func (mc *MinerConn) prepareSubmissionForProcessing(task submissionTask) (preparedSubmissionTask, bool) {
	ctx, ok := mc.prepareShareContext(task)
	if !ok {
		return preparedSubmissionTask{}, false
	}
	return mc.resolveSubmissionContext(task, ctx), true
}

// resolveSubmissionContext selects the same ordinary-share primary/alternate
// interpretation as before, then hashes every retained block-only rescue
// version. It runs before queue admission so a network-target block can never
// sit behind ordinary work.
func (mc *MinerConn) resolveSubmissionContext(task submissionTask, ctx shareContext) preparedSubmissionTask {
	jobID := task.jobID
	now := task.receivedAt
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
		if altCtx, ok := mc.prepareVersionShareContext(altTask, ctx); ok {
			primaryAcceptable := mc.submissionMeetsAssignedDifficulty(ctx, thresholdDiff, now)
			alternateAcceptable := mc.submissionMeetsAssignedDifficulty(altCtx, thresholdDiff, now)
			if preferAlternateSubmissionContext(ctx, altCtx, primaryAcceptable, alternateAcceptable) {
				task = altTask
				ctx = altCtx
			}
		}
	}

	// BIP310 makes the latest connection-wide mask authoritative even for old
	// job IDs. For block safety, also test retained intermediate and notify-time
	// masks plus the raw-full and legacy-XOR interpretations of the supplied
	// value. Rescue candidates are block-only and can never make an ordinary
	// share valid.
	if !ctx.isBlock {
		for i := 0; i < task.blockRescueCount; i++ {
			rescueVersion, ok := task.blockRescueVersion(i)
			if !ok {
				break
			}
			rescueTask := task
			rescueTask.useVersion = rescueVersion
			rescueTask.versionHex = uint32ToHex8Lower(rescueTask.useVersion)
			if rescueCtx, ok := mc.prepareVersionShareContext(rescueTask, ctx); ok && rescueCtx.isBlock {
				task = rescueTask
				ctx = rescueCtx
				break
			}
		}
	}

	if !ctx.isBlock {
		// Rescue data is only needed while testing alternate headers. Proven
		// non-blocks keep cbTx separately when verbose share detail is enabled.
		ctx.rescueCoinbase = nil
		ctx.rescueMerkleRoot = [32]byte{}
	}
	return preparedSubmissionTask{
		task:          task,
		ctx:           ctx,
		assignedDiff:  assignedDiff,
		currentDiff:   currentDiff,
		creditedDiff:  creditedDiff,
		thresholdDiff: thresholdDiff,
		meetsAssigned: mc.submissionMeetsAssignedDifficulty(ctx, thresholdDiff, now),
	}
}

// processShare remains the direct/test entry point for an already-built
// primary context. Production asynchronous admission resolves the same context
// synchronously and calls processPreparedShare from the worker.
func (mc *MinerConn) processShare(task submissionTask, ctx shareContext) {
	mc.processPreparedShare(mc.resolveSubmissionContext(task, ctx))
}

func (mc *MinerConn) processPreparedShare(prepared preparedSubmissionTask) {
	task := prepared.task
	ctx := prepared.ctx
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
	assignedDiff := prepared.assignedDiff
	currentDiff := prepared.currentDiff
	creditedDiff := prepared.creditedDiff
	thresholdDiff := prepared.thresholdDiff

	// A miner that was already banned when this submit arrived may still have
	// been hashing work we advertised while it was authorized. Preserve the ban
	// for ordinary shares, but only after every plausible header has been checked
	// so a network-target block is never discarded. This direct response matches
	// the old pre-PoW ban path and deliberately does not add another ban strike.
	if !ctx.isBlock && task.banPolicy != nil {
		logger.Warn("submit rejected: banned",
			"miner", mc.minerName(workerName),
			"ban_until", task.banPolicy.until,
			"reason", task.banPolicy.reason,
		)
		if mc.metrics != nil {
			mc.metrics.RecordSubmitError("banned")
		}
		mc.writeResponse(StratumResponse{ID: reqID, Result: false, Error: task.banPolicy.err})
		return
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

	lowDiff := !prepared.meetsAssigned
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
