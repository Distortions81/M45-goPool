package main

import (
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"math/bits"
	"sync/atomic"
	"time"
)

func (mc *MinerConn) recordActivity(now time.Time) {
	mc.lastActivity = now
}

func (mc *MinerConn) stratumMsgRateLimitExceeded(now time.Time, method string) bool {
	limit := mc.cfg.StratumMessagesPerMinute
	if limit <= 0 {
		return false
	}
	effectiveLimit := limit * stratumFloodLimitMultiplier
	if effectiveLimit <= 0 {
		return false
	}
	maxUnits := effectiveLimit * 2 // count in half-message units

	if mc.stratumMsgWindowStart.IsZero() || now.Sub(mc.stratumMsgWindowStart) >= time.Minute {
		mc.stratumMsgWindowStart = now
		mc.stratumMsgCount = 0
	}

	weightUnits := 2 // one full message
	if method == "mining.submit" && !mc.connectedAt.IsZero() && now.Sub(mc.connectedAt) < earlySubmitHalfWeightWindow {
		weightUnits = 1 // startup submit spam counts half until vardiff stabilizes
	}

	mc.stratumMsgCount += weightUnits
	return mc.stratumMsgCount > maxUnits
}

func (mc *MinerConn) idleExpired(now time.Time) (bool, string) {
	timeout := mc.cfg.ConnectionTimeout
	if timeout <= 0 {
		timeout = defaultConnectionTimeout
	}
	if timeout <= 0 || mc.lastActivity.IsZero() {
		return false, ""
	}
	if now.Sub(mc.lastActivity) > timeout {
		return true, "connection timeout"
	}
	return false, ""
}

// submitRejectReason classifies categories of invalid submissions. It is used
// for ban decisions while allowing human-readable reason strings to remain
// stable and centralized.
type submitRejectReason int

const (
	rejectUnknown submitRejectReason = iota
	rejectInvalidExtranonce2
	rejectInvalidNTime
	rejectInvalidNonce
	rejectInvalidCoinbase
	rejectInvalidMerkle
	rejectInvalidVersion
	rejectInvalidVersionMask
	rejectInsufficientVersionBits
	rejectStaleJob
	rejectDuplicateShare
	rejectLowDiff
)

func (r submitRejectReason) String() string {
	switch r {
	case rejectInvalidExtranonce2:
		return "invalid extranonce2"
	case rejectInvalidNTime:
		return "invalid ntime"
	case rejectInvalidNonce:
		return "invalid nonce"
	case rejectInvalidCoinbase:
		return "invalid coinbase"
	case rejectInvalidMerkle:
		return "invalid merkle"
	case rejectInvalidVersion:
		return "invalid version"
	case rejectInvalidVersionMask:
		return "invalid version mask"
	case rejectInsufficientVersionBits:
		return "insufficient version bits"
	case rejectStaleJob:
		return "stale job"
	case rejectDuplicateShare:
		return "duplicate share"
	case rejectLowDiff:
		return "lowDiff"
	default:
		return "unknown"
	}
}

func (mc *MinerConn) noteInvalidSubmit(now time.Time, reason submitRejectReason) (bool, int) {
	mc.stateMu.Lock()
	defer mc.stateMu.Unlock()
	window := mc.banInvalidWindow()
	mc.refreshBanCountersWindowLocked(now, window)
	mc.lastPenalty = now

	// Only treat clearly bogus submissions as ban-eligible. Normal mining
	// behavior like low-difficulty shares, stale jobs, or the occasional
	// duplicate share should never trigger a ban.
	switch reason {
	case rejectInvalidExtranonce2,
		rejectInvalidNTime,
		rejectInvalidNonce,
		rejectInvalidCoinbase,
		rejectInvalidMerkle,
		rejectInvalidVersion,
		rejectInvalidVersionMask,
		rejectInsufficientVersionBits:
		mc.invalidSubs++
	default:
		// Track the last penalty time but don't increment the ban counter.
		return false, mc.effectiveInvalidSubsLocked(mc.cfg.BanInvalidSubmissionsAfter)
	}

	// Require a burst of bad submissions in a short window before banning.
	threshold := mc.cfg.BanInvalidSubmissionsAfter
	if threshold <= 0 {
		threshold = defaultBanInvalidSubmissionsAfter
	}
	effectiveInvalid := mc.effectiveInvalidSubsLocked(threshold)
	if effectiveInvalid >= threshold {
		banDuration := mc.cfg.BanInvalidSubmissionsDuration
		if banDuration <= 0 {
			banDuration = defaultBanInvalidSubmissionsDuration
		}
		mc.banUntil = now.Add(banDuration)
		if mc.banReason == "" {
			mc.banReason = fmt.Sprintf("too many invalid submissions (%d in %s)", effectiveInvalid, window)
		}
		return true, effectiveInvalid
	}
	return false, effectiveInvalid
}

func (mc *MinerConn) noteValidSubmit(now time.Time) {
	mc.stateMu.Lock()
	defer mc.stateMu.Unlock()
	window := mc.banInvalidWindow()
	mc.refreshBanCountersWindowLocked(now, window)
	mc.lastPenalty = now
	if mc.validSubsForBan < 1000000 {
		mc.validSubsForBan++
	}
}

func (mc *MinerConn) banInvalidWindow() time.Duration {
	window := mc.cfg.BanInvalidSubmissionsWindow
	if window <= 0 {
		window = defaultBanInvalidSubmissionsWindow
	}
	return window
}

func (mc *MinerConn) refreshBanCountersWindowLocked(now time.Time, window time.Duration) {
	if window <= 0 {
		return
	}
	if now.Sub(mc.lastPenalty) > window {
		mc.invalidSubs = 0
		mc.validSubsForBan = 0
	}
}

func (mc *MinerConn) effectiveInvalidSubsLocked(threshold int) int {
	if threshold <= 0 {
		threshold = defaultBanInvalidSubmissionsAfter
	}
	if threshold <= 0 {
		threshold = 1
	}
	effective := mc.invalidSubs
	if effective <= 0 {
		return 0
	}
	forgiveUnits := mc.validSubsForBan / banInvalidForgiveSharesPerUnit
	maxForgive := max(int(float64(threshold)*banInvalidForgiveCapFraction), 1)
	if forgiveUnits > maxForgive {
		forgiveUnits = maxForgive
	}
	effective -= forgiveUnits
	if effective < 0 {
		effective = 0
	}
	return effective
}

// noteProtocolViolation tracks protocol-level misbehavior (invalid JSON,
// oversized messages, unknown methods, etc.) and decides whether to ban the
// worker based on configurable thresholds. When BanProtocolViolationsAfter
// is zero, protocol bans are disabled.
func (mc *MinerConn) noteProtocolViolation(now time.Time) (bool, int) {
	mc.stateMu.Lock()
	defer mc.stateMu.Unlock()

	threshold := mc.cfg.BanInvalidSubmissionsAfter
	if threshold <= 0 {
		// When explicit protocol thresholds are not set, reuse the
		// invalid-submission threshold for simplicity.
		return false, mc.protoViolations
	}

	window := mc.cfg.BanInvalidSubmissionsWindow
	if window <= 0 {
		window = time.Minute
	}
	if now.Sub(mc.lastProtoViolation) > window {
		mc.protoViolations = 0
	}
	mc.lastProtoViolation = now
	mc.protoViolations++

	if mc.protoViolations >= threshold {
		banDuration := mc.cfg.BanInvalidSubmissionsDuration
		if banDuration <= 0 {
			banDuration = 15 * time.Minute
		}
		mc.banUntil = now.Add(banDuration)
		if mc.banReason == "" {
			mc.banReason = fmt.Sprintf("too many protocol violations (%d in %s)", mc.protoViolations, window)
		}
		return true, mc.protoViolations
	}
	return false, mc.protoViolations
}

// rejectShareWithBan records a rejected share, updates invalid-submission
// counters, and either bans the worker or returns a typed Stratum error
// depending on recent behavior. It centralizes the common pattern used for
// clearly invalid submissions (bad extranonce, ntime, nonce, etc.).
func (mc *MinerConn) rejectShareWithBan(req *StratumRequest, workerName string, reason submitRejectReason, errCode int, errMsg string, now time.Time) {
	reasonText := reason.String()
	mc.recordShare(workerName, false, 0, 0, reasonText, "", nil, now)
	if banned, invalids := mc.noteInvalidSubmit(now, reason); banned {
		mc.logBan(reasonText, workerName, invalids)
		mc.writeResponse(StratumResponse{
			ID:     req.ID,
			Result: false,
			Error:  newStratumError(24, "banned"),
		})
		return
	}
	mc.writeResponse(StratumResponse{
		ID:     req.ID,
		Result: false,
		Error:  newStratumError(errCode, errMsg),
	})
}

func (mc *MinerConn) currentDifficulty() float64 {
	return atomicLoadFloat64(&mc.difficulty)
}

func (mc *MinerConn) currentShareTarget() *big.Int {
	target := mc.shareTarget.Load()
	if target == nil || target.Sign() <= 0 {
		return nil
	}
	return new(big.Int).Set(target)
}

func (mc *MinerConn) shareTargetOrDefault() *big.Int {
	target := mc.currentShareTarget()
	if target != nil {
		return target
	}
	// Fall back to the pool minimum difficulty.
	fallbackDiff := mc.cfg.MinDifficulty
	if fallbackDiff <= 0 {
		fallbackDiff = defaultMinDifficulty
	}
	if fallbackDiff <= 0 {
		fallbackDiff = 1.0
	}
	fallback := targetFromDifficulty(fallbackDiff)
	oldTarget := mc.shareTarget.Load()
	if oldTarget == nil || oldTarget.Sign() <= 0 {
		mc.shareTarget.CompareAndSwap(oldTarget, new(big.Int).Set(fallback))
	}
	return fallback
}

func (mc *MinerConn) resetShareWindow(now time.Time) {
	mc.statsMu.Lock()
	// Reset only the VarDiff retarget window. Status/confidence windows and
	// hashrate EMAs continue accumulating across difficulty adjustments.
	mc.vardiffWindowStart = time.Time{}
	mc.vardiffWindowResetAnchor = now
	mc.vardiffWindowAccepted = 0
	mc.vardiffWindowSubmissions = 0
	mc.vardiffWindowDifficulty = 0
	mc.statsMu.Unlock()
}

func (mc *MinerConn) hashrateEMATau() time.Duration {
	if !mc.initialEMAWindowDone.Load() {
		return initialHashrateEMATau
	}

	tauSeconds := mc.cfg.HashrateEMATauSeconds
	if tauSeconds <= 0 {
		tauSeconds = defaultHashrateEMATauSeconds
	}
	if tauSeconds <= 0 {
		return time.Minute
	}
	return time.Duration(tauSeconds * float64(time.Second))
}

func (mc *MinerConn) hashrateControlTau() time.Duration {
	if !mc.initialEMAWindowDone.Load() {
		return initialHashrateEMATau
	}
	slow := mc.hashrateEMATau()
	fast := max(time.Duration(float64(slow)*hashrateControlTauFactor), hashrateControlTauMin)
	if fast > slow {
		fast = slow
	}
	return fast
}

func (mc *MinerConn) decayedHashratesLocked(now time.Time) (control, display float64) {
	control = mc.rollingHashrateControl
	display = mc.rollingHashrateValue
	if now.IsZero() || mc.lastHashrateUpdate.IsZero() || !now.After(mc.lastHashrateUpdate) {
		return control, display
	}
	idleSeconds := now.Sub(mc.lastHashrateUpdate).Seconds()
	if idleSeconds <= 0 {
		return control, display
	}
	controlTau := mc.hashrateControlTau().Seconds()
	displayTau := mc.hashrateEMATau().Seconds()
	if controlTau > 0 && control > 0 {
		control *= math.Exp(-idleSeconds / controlTau)
		if control < 0 || math.IsNaN(control) || math.IsInf(control, 0) {
			control = 0
		}
	}
	if displayTau > 0 && display > 0 {
		display *= math.Exp(-idleSeconds / displayTau)
		if display < 0 || math.IsNaN(display) || math.IsInf(display, 0) {
			display = 0
		}
	}
	return control, display
}

func (mc *MinerConn) vardiffRetargetInterval(rollingHashrate, currentDiff, targetShares, staleRate float64) time.Duration {
	interval := mc.vardiff.AdjustmentWindow
	if interval <= 0 {
		interval = defaultVarDiffAdjustmentWindow
	}
	if interval <= 0 {
		interval = time.Minute
	}
	// Keep explicit non-default windows fixed; adaptive behavior applies to
	// the compiled default window only.
	if interval == defaultVarDiffAdjustmentWindow {
		interval = mc.adaptiveVardiffWindow(interval, rollingHashrate, currentDiff, targetShares)
	}
	interval = applyStaleRetargetSlowdown(interval, staleRate)

	// During bootstrap, enforce both the EMA warmup horizon and the vardiff
	// adjustment window by waiting for whichever is longer.
	if !mc.initialEMAWindowDone.Load() && interval < initialHashrateEMATau {
		interval = initialHashrateEMATau
	}
	if mc.vardiff.RetargetDelay > 0 && interval < mc.vardiff.RetargetDelay {
		interval = mc.vardiff.RetargetDelay
	}
	return interval
}

func applyStaleRetargetSlowdown(interval time.Duration, staleRate float64) time.Duration {
	if interval <= 0 || staleRate <= 0 {
		return interval
	}
	switch {
	case staleRate >= 0.12:
		interval = interval * 2
	case staleRate >= 0.06:
		interval = (interval * 16) / 10
	case staleRate >= 0.03:
		interval = (interval * 13) / 10
	}
	if interval < vardiffAdaptiveMinWindow {
		interval = vardiffAdaptiveMinWindow
	}
	if interval > vardiffAdaptiveMaxWindow {
		interval = vardiffAdaptiveMaxWindow
	}
	return interval
}

func (mc *MinerConn) adaptiveVardiffWindow(base time.Duration, rollingHashrate, currentDiff, targetShares float64) time.Duration {
	if base <= 0 || rollingHashrate <= 0 || currentDiff <= 0 || targetShares <= 0 {
		return base
	}
	sharesPerMin := (rollingHashrate / hashPerShare) * 60.0 / currentDiff
	if sharesPerMin <= 0 {
		return base
	}
	expectedShares := sharesPerMin * base.Minutes()
	switch {
	case expectedShares >= vardiffAdaptiveHighShareCount*2:
		base = base / 2
	case expectedShares >= vardiffAdaptiveHighShareCount:
		base = (base * 3) / 4
	case expectedShares <= vardiffAdaptiveLowShareCount/2:
		base = base * 2
	case expectedShares <= vardiffAdaptiveLowShareCount:
		base = (base * 3) / 2
	}
	if base < vardiffAdaptiveMinWindow {
		base = vardiffAdaptiveMinWindow
	}
	if base > vardiffAdaptiveMaxWindow {
		base = vardiffAdaptiveMaxWindow
	}
	return base
}

// updateHashrateLocked updates the per-connection hashrate using a simple
// exponential moving average (EMA) over time. It expects statsMu to be held
// by the caller.
func (mc *MinerConn) updateHashrateLocked(targetDiff float64, shareTime time.Time) {
	if targetDiff <= 0 || shareTime.IsZero() {
		return
	}

	// Update once we've reached the control EMA time window.
	controlTauSeconds := mc.hashrateControlTau().Seconds()
	displayTauSeconds := mc.hashrateEMATau().Seconds()

	if mc.lastHashrateUpdate.IsZero() {
		mc.lastHashrateUpdate = shareTime
		mc.hashrateSampleCount = 1
		mc.hashrateAccumulatedDiff = targetDiff
		return
	}

	mc.hashrateSampleCount++
	mc.hashrateAccumulatedDiff += targetDiff
	elapsed := shareTime.Sub(mc.lastHashrateUpdate).Seconds()
	if elapsed <= 0 {
		return
	}

	// Bootstrap: wait for the initial EMA window so startup doesn't jump on
	// one or two early shares. After bootstrap, update incrementally.
	if !mc.initialEMAWindowDone.Load() && elapsed < controlTauSeconds {
		return
	}

	sample := (mc.hashrateAccumulatedDiff * hashPerShare) / elapsed

	// Apply an EMA with a configurable time constant so that hashrate responds
	// quickly to changes but decays smoothly when shares slow down.
	alphaControl := 1 - math.Exp(-elapsed/controlTauSeconds)
	if alphaControl < 0 {
		alphaControl = 0
	}
	if alphaControl > 1 {
		alphaControl = 1
	}
	alphaDisplay := 1 - math.Exp(-elapsed/displayTauSeconds)
	if alphaDisplay < 0 {
		alphaDisplay = 0
	}
	if alphaDisplay > 1 {
		alphaDisplay = 1
	}

	if mc.rollingHashrateControl <= 0 {
		mc.rollingHashrateControl = sample
	} else {
		mc.rollingHashrateControl = mc.rollingHashrateControl + alphaControl*(sample-mc.rollingHashrateControl)
	}
	if mc.rollingHashrateValue <= 0 {
		mc.rollingHashrateValue = sample
	} else {
		mc.rollingHashrateValue = mc.rollingHashrateValue + alphaDisplay*(sample-mc.rollingHashrateValue)
	}
	mc.initialEMAWindowDone.Store(true)
	mc.lastHashrateUpdate = shareTime
	mc.hashrateSampleCount = 0
	mc.hashrateAccumulatedDiff = 0

	if mc.metrics != nil {
		connSeq := atomic.LoadUint64(&mc.connectionSeq)
		if connSeq != 0 {
			mc.metrics.UpdateConnectionHashrate(connSeq, mc.rollingHashrateValue)
		}
	}
}

func (mc *MinerConn) trackJob(job *Job, clean bool) {
	mc.jobMu.Lock()
	defer mc.jobMu.Unlock()
	// No longer clear old jobs on clean - preserve them for miners with latency
	// The eviction logic below will handle cleanup when we exceed maxRecentJobs
	if _, ok := mc.activeJobs[job.JobID]; !ok {
		mc.jobOrder = append(mc.jobOrder, job.JobID)
	}
	// Note: Don't clear shareCache on job re-send - coinbase is stable for a
	// given job (payouts configured at boot). Clearing would allow duplicate
	// shares after difficulty changes, wasting miner work.
	mc.activeJobs[job.JobID] = job
	mc.lastJob = job
	mc.lastJobPrevHash = job.Template.Previous
	mc.lastJobHeight = job.Template.Height
	mc.lastClean = clean
	if mc.cfg.ShareCheckNTimeWindow && mc.jobNTimeBounds != nil {
		minNTime := job.Template.CurTime
		if job.Template.Mintime > 0 && job.Template.Mintime > minNTime {
			minNTime = job.Template.Mintime
		}
		slack := mc.cfg.ShareNTimeMaxForwardSeconds
		if slack <= 0 {
			slack = defaultShareNTimeMaxForwardSeconds
		}
		mc.jobNTimeBounds[job.JobID] = jobNTimeBounds{
			min: minNTime,
			max: minNTime + int64(slack),
		}
	}

	// Evict oldest jobs if we exceed the max limit
	dupEnabled := mc.cfg.ShareCheckDuplicate
	now := time.Time{}
	for len(mc.jobOrder) > mc.maxRecentJobs && len(mc.jobOrder) > 0 {
		oldest := mc.jobOrder[0]
		mc.jobOrder = mc.jobOrder[1:]
		delete(mc.activeJobs, oldest)
		if mc.jobScriptTime != nil {
			delete(mc.jobScriptTime, oldest)
		}
		if mc.jobNotifyCoinbase != nil {
			delete(mc.jobNotifyCoinbase, oldest)
		}
		if mc.jobNTimeBounds != nil {
			delete(mc.jobNTimeBounds, oldest)
		}
		if dupEnabled {
			if cache := mc.shareCache[oldest]; cache != nil {
				if now.IsZero() {
					now = time.Now()
				}
				if mc.evictedShareCache == nil {
					mc.evictedShareCache = make(map[string]*evictedCacheEntry)
				}
				mc.evictedShareCache[oldest] = &evictedCacheEntry{
					cache:     cache,
					evictedAt: now,
				}
			}
		}
		delete(mc.shareCache, oldest)
		delete(mc.jobDifficulty, oldest)
	}

	if dupEnabled && mc.evictedShareCache != nil {
		if now.IsZero() {
			now = time.Now()
		}
		// Clean up expired evicted caches
		for jobID, entry := range mc.evictedShareCache {
			if now.Sub(entry.evictedAt) > evictedShareCacheGrace {
				delete(mc.evictedShareCache, jobID)
			}
		}
	}
}

func (mc *MinerConn) scriptTimeForJob(jobID string, fallback int64) int64 {
	if jobID == "" {
		return fallback
	}
	mc.jobMu.Lock()
	st, ok := mc.jobScriptTime[jobID]
	mc.jobMu.Unlock()
	if ok {
		return st
	}
	return fallback
}

// jobForIDWithLast returns the job for the given ID along with the current lastJob
// and the scriptTime used when this job was notified to this connection, all
// under a single lock acquisition to avoid race conditions.
func (mc *MinerConn) jobForIDWithLast(jobID string) (job *Job, lastJob *Job, lastPrevHash string, lastHeight int64, ntimeBounds jobNTimeBounds, scriptTime int64, ok bool) {
	mc.jobMu.Lock()
	defer mc.jobMu.Unlock()
	job, ok = mc.activeJobs[jobID]
	if mc.cfg.ShareCheckNTimeWindow && mc.jobNTimeBounds != nil {
		ntimeBounds = mc.jobNTimeBounds[jobID]
	}
	if mc.jobScriptTime != nil {
		scriptTime = mc.jobScriptTime[jobID]
	}
	return job, mc.lastJob, mc.lastJobPrevHash, mc.lastJobHeight, ntimeBounds, scriptTime, ok
}

func (mc *MinerConn) setJobDifficulty(jobID string, diff float64) {
	if jobID == "" || diff <= 0 {
		return
	}
	mc.jobMu.Lock()
	if mc.jobDifficulty == nil {
		mc.jobDifficulty = make(map[string]float64)
	}
	mc.jobDifficulty[jobID] = diff
	mc.jobMu.Unlock()
}

// assignedDifficulty returns the difficulty we assigned when the job was
// sent to the miner. Falls back to currentDifficulty if unknown.
func (mc *MinerConn) assignedDifficulty(jobID string) float64 {
	curDiff := mc.currentDifficulty()
	if jobID == "" {
		return curDiff
	}
	mc.jobMu.Lock()
	diff, ok := mc.jobDifficulty[jobID]
	mc.jobMu.Unlock()
	if ok && diff > 0 {
		return diff
	}

	return curDiff
}

// meetsPreDiffGrace returns true if the share difficulty is acceptable under
// the previous-difficulty grace period. This allows shares computed at the
// old difficulty to be accepted for a short window after a vardiff change.
func (mc *MinerConn) meetsPrevDiffGrace(shareDiff float64, now time.Time) bool {
	lastChange := time.Unix(0, mc.lastDiffChange.Load())
	if lastChange.IsZero() || now.Sub(lastChange) > previousDiffGracePeriod {
		return false
	}
	prevDiff := atomicLoadFloat64(&mc.previousDifficulty)
	if prevDiff <= 0 {
		return false
	}
	ratio := shareDiff / prevDiff
	return ratio >= 0.98
}

func (mc *MinerConn) cleanFlagFor(job *Job) bool {
	mc.jobMu.Lock()
	defer mc.jobMu.Unlock()
	if mc.lastJob == nil {
		return true
	}
	return mc.lastJobPrevHash != job.Template.Previous || mc.lastJobHeight != job.Template.Height
}

func (mc *MinerConn) isDuplicateShare(jobID string, extranonce2 []byte, ntime, nonce uint32, version uint32) bool {
	// Skip duplicate checking if disabled (default for solo pools)
	if !mc.cfg.ShareCheckDuplicate {
		return false
	}

	// Build the key outside the connection lock to minimize contention.
	var dk duplicateShareKey
	makeDuplicateShareKeyDecoded(&dk, extranonce2, ntime, nonce, version)

	mc.jobMu.Lock()

	if mc.shareCache == nil {
		// Allocate lazily so disabling duplicate checks avoids per-connection maps.
		mc.shareCache = make(map[string]*duplicateShareSet, mc.maxRecentJobs)
	}
	if mc.evictedShareCache == nil {
		mc.evictedShareCache = make(map[string]*evictedCacheEntry, mc.maxRecentJobs)
	}

	// Check active job cache first
	cache := mc.shareCache[jobID]
	if cache != nil {
		mc.jobMu.Unlock()
		return cache.seenOrAdd(dk)
	}

	// Check evicted job cache (for late shares on evicted jobs)
	if entry := mc.evictedShareCache[jobID]; entry != nil {
		cache = entry.cache
		mc.jobMu.Unlock()
		return cache.seenOrAdd(dk)
	}

	// No cache exists - create new one in active cache
	cache = &duplicateShareSet{
		m:     make(map[duplicateShareKey]struct{}, duplicateShareHistory),
		order: make([]duplicateShareKey, 0, duplicateShareHistory),
	}
	mc.shareCache[jobID] = cache
	mc.jobMu.Unlock()
	return cache.seenOrAdd(dk)
}

func (mc *MinerConn) maybeAdjustDifficulty(now time.Time) bool {
	varDiffEnabled := mc.cfg.VarDiffEnabled || mc.cfg.TargetSharesPerMin <= 0
	// If this connection is locked to a static difficulty, skip VarDiff.
	if !varDiffEnabled || mc.lockDifficulty {
		return false
	}

	snap := mc.snapshotShareInfo()
	newDiff := mc.suggestedVardiff(now, snap)

	currentDiff := atomicLoadFloat64(&mc.difficulty)
	if profiler := getMinerProfileCollector(); profiler != nil {
		profiler.ObserveVardiff(mc, now, snap, currentDiff, newDiff)
	}

	if newDiff == 0 || math.Abs(newDiff-currentDiff) < 1e-6 {
		return false
	}

	mc.resetShareWindow(now)
	if logger.Enabled(logLevelInfo) {
		accRate := 0.0
		if snap.RollingHashrate > 0 {
			accRate = (snap.RollingHashrate / hashPerShare) * 60
		}
		logger.Info("vardiff adjust",
			"miner", mc.minerName(""),
			"shares_per_min", accRate,
			"old_diff", currentDiff,
			"new_diff", newDiff,
		)
	}
	if mc.metrics != nil {
		dir := "down"
		if newDiff > currentDiff {
			dir = "up"
		}
		mc.metrics.RecordVardiffMove(dir)
	}
	mc.setDifficulty(newDiff)
	mc.noteVardiffUpwardMove(now, currentDiff, newDiff)
	mc.vardiffAdjustments.Add(1)
	mc.resetVardiffPending()
	return true
}

func (mc *MinerConn) noteVardiffUpwardMove(now time.Time, oldDiff, newDiff float64) {
	if newDiff <= oldDiff || oldDiff <= 0 || now.IsZero() {
		return
	}
	if newDiff/oldDiff < vardiffLargeUpJumpFactor {
		return
	}
	mc.vardiffUpwardCooldownUntil.Store(now.Add(vardiffLargeUpCooldown).UnixNano())
}

// suggestedVardiff returns the difficulty VarDiff would select based on the
// current stats, without applying any changes.
func (mc *MinerConn) suggestedVardiff(now time.Time, snap minerShareSnapshot) float64 {
	windowStart := snap.RetargetWindowStart
	windowAccepted := snap.RetargetWindowAccepted
	windowSubmissions := snap.RetargetWindowSubmissions
	windowDifficulty := snap.RetargetWindowDifficulty
	// Backward-compatibility for tests/paths that construct snapshots with only
	// Stats populated.
	if windowStart.IsZero() && !snap.Stats.WindowStart.IsZero() {
		windowStart = snap.Stats.WindowStart
	}
	if windowAccepted == 0 && snap.Stats.WindowAccepted > 0 {
		windowAccepted = snap.Stats.WindowAccepted
	}
	if windowSubmissions == 0 && snap.Stats.WindowSubmissions > 0 {
		windowSubmissions = snap.Stats.WindowSubmissions
	}
	if windowDifficulty <= 0 && snap.Stats.WindowDifficulty > 0 {
		windowDifficulty = snap.Stats.WindowDifficulty
	}

	lastChange := time.Unix(0, mc.lastDiffChange.Load())
	currentDiff := atomicLoadFloat64(&mc.difficulty)

	if currentDiff <= 0 {
		currentDiff = mc.vardiff.MinDiff
	}
	targetShares := mc.vardiff.TargetSharesPerMin
	if targetShares <= 0 {
		targetShares = defaultVarDiff.TargetSharesPerMin
	}
	if targetShares <= 0 {
		targetShares = 6
	}
	if guarded := mc.timeoutRiskDownshift(now, currentDiff, snap.Stats.LastShare, lastChange, targetShares, snap.RollingHashrate); guarded > 0 && math.Abs(guarded-currentDiff) >= 1e-6 {
		return guarded
	}
	if windowSubmissions == 0 || windowStart.IsZero() {
		return currentDiff
	}
	if windowAccepted == 0 {
		return currentDiff
	}

	rollingHashrate := snap.RollingHashrate
	if rollingHashrate <= 0 {
		if windowStart.IsZero() || !now.After(windowStart) || windowDifficulty <= 0 {
			return currentDiff
		}
		windowSeconds := now.Sub(windowStart).Seconds()
		if windowSeconds <= 0 {
			return currentDiff
		}
		rollingHashrate = (windowDifficulty * hashPerShare) / windowSeconds
		if rollingHashrate <= 0 {
			return currentDiff
		}
	}

	interval := mc.vardiffRetargetInterval(rollingHashrate, currentDiff, targetShares, snap.RecentStaleRate)
	guardStart := lastChange
	if !mc.initialEMAWindowDone.Load() && windowStart.After(guardStart) {
		guardStart = windowStart
	}
	if !guardStart.IsZero() && now.Sub(guardStart) < interval {
		return currentDiff
	}
	targetDiff := (rollingHashrate / hashPerShare) * 60 / targetShares
	if targetDiff <= 0 || math.IsNaN(targetDiff) || math.IsInf(targetDiff, 0) {
		return currentDiff
	}

	// Aim directly at computed target share cadence.
	if mc.vardiff.MaxDiff > 0 && targetDiff > mc.vardiff.MaxDiff {
		targetDiff = mc.vardiff.MaxDiff
	}
	if targetDiff < mc.vardiff.MinDiff {
		targetDiff = mc.vardiff.MinDiff
	}
	if mc.cfg.MaxDifficulty > 0 && targetDiff > mc.cfg.MaxDifficulty {
		targetDiff = mc.cfg.MaxDifficulty
	}

	ratio := targetDiff / currentDiff
	band := mc.vardiffNoiseBand(windowAccepted)
	if ratio >= 1-band && ratio <= 1+band {
		mc.resetVardiffPending()
		return currentDiff
	}
	dir := int32(1)
	if ratio < 1 {
		dir = -1
	}
	if dir > 0 && mc.upwardVardiffCooldownActive(now) {
		return currentDiff
	}
	if dir < 0 && mc.shouldHoldLowHashrateDownshift(windowAccepted, rollingHashrate, currentDiff, interval) {
		return currentDiff
	}

	if dir < 0 && mc.shouldApplyWarmupDownwardBias(snap.NotifyToFirstShareP95MS, snap.NotifyToFirstShareSamples) {
		targetDiff *= vardiffWarmupDownwardBias
		if targetDiff < mc.vardiff.MinDiff {
			targetDiff = mc.vardiff.MinDiff
		}
		ratio = targetDiff / currentDiff
	}

	if mc.shouldDelayVardiffAdjustment(dir, ratio) {
		return currentDiff
	}

	dampingFactor := mc.vardiff.DampingFactor
	if dampingFactor <= 0 || dampingFactor > 1 {
		dampingFactor = 0.7
	}

	newDiff := currentDiff + dampingFactor*(targetDiff-currentDiff)
	if newDiff <= 0 || math.IsNaN(newDiff) || math.IsInf(newDiff, 0) {
		return currentDiff
	}

	factor := newDiff / currentDiff
	step := mc.vardiff.Step
	if step <= 1 {
		step = 2
	}
	maxFactor := mc.vardiffAdjustmentCap(step, ratio)
	maxFactor = mc.vardiffUncertaintyCap(maxFactor, step, ratio, windowAccepted, interval, rollingHashrate, currentDiff)
	minFactor := 1 / maxFactor
	if factor > maxFactor {
		factor = maxFactor
	}
	if factor < minFactor {
		factor = minFactor
	}
	newDiff = currentDiff * factor

	newDiff = mc.applyVardiffShareRateSafety(newDiff, rollingHashrate)
	if newDiff == 0 || math.Abs(newDiff-currentDiff) < 1e-6 {
		return currentDiff
	}
	return mc.clampDifficulty(newDiff)
}

func (mc *MinerConn) vardiffAdjustmentCap(baseStep, targetRatio float64) float64 {
	if baseStep <= 1 {
		return 1
	}
	absRatio := targetRatio
	if absRatio <= 0 || math.IsNaN(absRatio) || math.IsInf(absRatio, 0) {
		return baseStep
	}
	if absRatio < 1 {
		absRatio = 1 / absRatio
	}
	if mc.vardiffAdjustments.Load() < 2 {
		// Startup: allow more aggressive catch-up while we are clearly far off.
		if absRatio >= math.Pow(baseStep, 4) {
			return baseStep * baseStep * baseStep
		}
		return baseStep * baseStep
	}
	// Post-bootstrap: scale allowed move by how far off target we still are.
	if absRatio >= math.Pow(baseStep, 6) {
		return baseStep * baseStep * baseStep
	}
	if absRatio >= math.Pow(baseStep, 4) {
		return baseStep * baseStep
	}
	return baseStep
}

func (mc *MinerConn) vardiffUncertaintyCap(capFactor, baseStep, targetRatio float64, windowAccepted int, interval time.Duration, rollingHashrate, currentDiff float64) float64 {
	if capFactor <= 1 || baseStep <= 1 || windowAccepted <= 0 || interval <= 0 || rollingHashrate <= 0 || currentDiff <= 0 {
		return capFactor
	}
	absRatio := targetRatio
	if absRatio < 0 {
		absRatio = -absRatio
	}
	if absRatio < 1 {
		absRatio = 1 / absRatio
	}
	if absRatio < vardiffUncertaintyAbsRatio {
		return capFactor
	}

	// Estimate effective evidence in this window from both observed and
	// expected shares. Low evidence implies high Poisson noise.
	sharesPerMin := (rollingHashrate / hashPerShare) * 60.0 / currentDiff
	expectedShares := sharesPerMin * interval.Minutes()
	evidence := float64(windowAccepted)
	if expectedShares > 0 && expectedShares < evidence {
		evidence = expectedShares
	}
	if evidence >= vardiffUncertaintyMinSamples {
		return capFactor
	}
	if absRatio >= 8 {
		limit := baseStep * baseStep
		if capFactor > limit {
			capFactor = limit
		}
	}
	if absRatio >= 4 {
		limit := baseStep
		if capFactor > limit {
			capFactor = limit
		}
	}
	if capFactor < 1 {
		return 1
	}
	return capFactor
}

func (mc *MinerConn) shouldDelayVardiffAdjustment(dir int32, targetRatio float64) bool {
	if dir != -1 && dir != 1 {
		return false
	}
	// Keep startup corrections fast.
	if mc.vardiffAdjustments.Load() < 2 || !mc.initialEMAWindowDone.Load() {
		return false
	}
	absRatio := targetRatio
	if absRatio < 0 {
		absRatio = -absRatio
	}
	// When clearly far from target, don't debounce; prioritize catch-up.
	if absRatio >= 8 {
		return false
	}
	required := int32(2)
	// Near target, require more consistent evidence before moving.
	if absRatio < 2 {
		required = 3
	}
	if absRatio < 1.5 {
		required = 4
	}
	// Once we've already converged through several adjustments, strongly
	// suppress near-target retarget churn from Poisson randomness.
	if mc.vardiffAdjustments.Load() >= 4 {
		if absRatio < 1.35 {
			required = 6
		}
		if absRatio < 1.25 {
			required = 8
		}
	}
	prevDir := mc.vardiffPendingDirection.Load()
	if prevDir != dir {
		mc.vardiffPendingDirection.Store(dir)
		mc.vardiffPendingCount.Store(1)
		return true
	}
	count := mc.vardiffPendingCount.Add(1)
	if count < required {
		return true
	}
	return false
}

func (mc *MinerConn) resetVardiffPending() {
	mc.vardiffPendingDirection.Store(0)
	mc.vardiffPendingCount.Store(0)
}

func (mc *MinerConn) upwardVardiffCooldownActive(now time.Time) bool {
	if now.IsZero() {
		return false
	}
	until := time.Unix(0, mc.vardiffUpwardCooldownUntil.Load())
	return !until.IsZero() && now.Before(until)
}

func (mc *MinerConn) shouldApplyWarmupDownwardBias(warmupP95MS float64, samples int) bool {
	if samples < vardiffHighWarmupSamplesMin || warmupP95MS <= 0 {
		mc.vardiffWarmupHighLatencyStreak.Store(0)
		return false
	}
	if warmupP95MS >= vardiffHighWarmupP95MS {
		return mc.vardiffWarmupHighLatencyStreak.Add(1) >= vardiffHighWarmupStreakNeed
	}
	mc.vardiffWarmupHighLatencyStreak.Store(0)
	return false
}

func (mc *MinerConn) timeoutRiskDownshift(now time.Time, currentDiff float64, lastShare, lastChange time.Time, targetShares, rollingHashrate float64) float64 {
	if now.IsZero() || currentDiff <= 0 || lastChange.IsZero() || !now.After(lastChange) {
		return 0
	}
	prevDiff := atomicLoadFloat64(&mc.previousDifficulty)
	if prevDiff <= 0 || prevDiff >= currentDiff {
		// Only apply this guard after an upward move; otherwise normal VarDiff
		// logic should decide.
		return 0
	}
	if !lastShare.IsZero() && !lastChange.After(lastShare) {
		// We already received shares after the last diff change.
		return 0
	}

	timeout := mc.cfg.ConnectionTimeout
	if timeout <= 0 {
		timeout = defaultConnectionTimeout
	}
	if timeout <= 0 {
		return 0
	}
	quietThreshold := max(time.Duration(float64(timeout)*vardiffTimeoutGuardThreshold), vardiffTimeoutGuardMinQuiet)
	maxQuiet := timeout - vardiffTimeoutGuardLead
	if maxQuiet > 0 && quietThreshold > maxQuiet {
		quietThreshold = maxQuiet
	}
	quiet := now.Sub(lastChange)
	if quiet < quietThreshold {
		return 0
	}
	if !mc.timeoutQuietStatisticallyUnlikely(quiet, targetShares, rollingHashrate, currentDiff) {
		return 0
	}

	downFactor := 0.75
	if quiet >= (timeout*9)/10 {
		downFactor = 0.5
	} else if quiet >= (timeout*4)/5 {
		downFactor = 0.65
	}
	newDiff := currentDiff * downFactor
	if newDiff > prevDiff {
		// Prefer stepping back to the pre-jump diff floor when possible.
		newDiff = prevDiff
	}
	newDiff = mc.clampDifficulty(newDiff)
	if newDiff <= 0 || newDiff >= currentDiff {
		return 0
	}
	return newDiff
}

func (mc *MinerConn) timeoutQuietStatisticallyUnlikely(quiet time.Duration, targetShares, rollingHashrate, currentDiff float64) bool {
	if quiet <= 0 || currentDiff <= 0 {
		return false
	}
	expectedRate := targetShares
	if expectedRate <= 0 {
		expectedRate = defaultVarDiffTargetSharesPerMin
	}
	if rollingHashrate > 0 {
		observedRate := (rollingHashrate / hashPerShare) * 60.0 / currentDiff
		if observedRate > 0 && (expectedRate <= 0 || observedRate < expectedRate) {
			expectedRate = observedRate
		}
	}
	if expectedRate <= 0 {
		return false
	}
	expectedShares := expectedRate * quiet.Minutes()
	if expectedShares <= 0 {
		return false
	}
	pZero := math.Exp(-expectedShares)
	return pZero <= vardiffTimeoutGuardMaxPZero
}

func (mc *MinerConn) vardiffNoiseBand(windowAccepted int) float64 {
	const baseBand = 0.2
	if windowAccepted <= 0 {
		return baseBand
	}
	// Approximate 95% Poisson confidence around observed share rate.
	noiseBand := 2.0 / math.Sqrt(float64(windowAccepted))
	if noiseBand < baseBand {
		return baseBand
	}
	// Cap so we still react to large clear mismatches.
	if noiseBand > 0.6 {
		return 0.6
	}
	return noiseBand
}

func (mc *MinerConn) applyVardiffShareRateSafety(diff, rollingHashrate float64) float64 {
	if diff <= 0 || rollingHashrate <= 0 {
		return diff
	}
	// shares/min = (H/hashPerShare)*60/diff
	nominal := (rollingHashrate / hashPerShare) * 60.0
	if nominal <= 0 {
		return diff
	}
	// Guard rails to avoid both long no-share gaps and share-flood spam.
	minDiffForMaxShares := nominal / vardiffSafetyMaxSharesPerMin
	maxDiffForMinShares := nominal / vardiffSafetyMinSharesPerMin
	if minDiffForMaxShares > 0 && diff < minDiffForMaxShares {
		diff = minDiffForMaxShares
	}
	if maxDiffForMinShares > 0 && diff > maxDiffForMinShares {
		diff = maxDiffForMinShares
	}
	return diff
}

func (mc *MinerConn) shouldHoldLowHashrateDownshift(windowAccepted int, rollingHashrate, currentDiff float64, interval time.Duration) bool {
	if windowAccepted < 0 || rollingHashrate <= 0 || currentDiff <= 0 || interval <= 0 {
		return false
	}
	sharesPerMin := (rollingHashrate / hashPerShare) * 60.0 / currentDiff
	expectedShares := sharesPerMin * interval.Minutes()
	if expectedShares >= vardiffLowHashrateExpectedShares {
		return false
	}
	return windowAccepted < vardiffLowHashrateMinAccepted
}

// quantizeDifficulty snaps a difficulty value to 2^(k/granularity) levels
// within [min, max] (if max > 0). granularity=1 is power-of-two only.
func quantizeDifficulty(diff, min, max float64, granularity int) float64 {
	if diff <= 0 {
		diff = min
	}
	if diff <= 0 {
		return diff
	}
	if granularity <= 0 {
		granularity = 1
	}

	log2 := math.Log2(diff) * float64(granularity)
	if math.IsNaN(log2) || math.IsInf(log2, 0) {
		return diff
	}

	exp := math.Round(log2)
	cand := math.Pow(2, exp/float64(granularity))

	// Ensure candidate lies within [min, max] by snapping up/down as needed.
	if cand < min && min > 0 {
		exp = math.Ceil(math.Log2(min) * float64(granularity))
		cand = math.Pow(2, exp/float64(granularity))
	}
	if max > 0 && cand > max {
		exp = math.Floor(math.Log2(max) * float64(granularity))
		cand = math.Pow(2, exp/float64(granularity))
	}

	if cand < min {
		cand = min
	}
	if max > 0 && cand > max {
		cand = max
	}
	return cand
}

func (mc *MinerConn) clampDifficulty(diff float64) float64 {
	// Determine the tightest enforceable bounds from both pool config and vardiff.
	min := mc.cfg.MinDifficulty
	if min < 0 {
		min = 0
	}
	if min > 0 && mc.vardiff.MinDiff > min {
		min = mc.vardiff.MinDiff
	}

	max := mc.cfg.MaxDifficulty
	if max < 0 {
		max = 0
	}
	if max > 0 && mc.vardiff.MaxDiff > 0 && mc.vardiff.MaxDiff < max {
		max = mc.vardiff.MaxDiff
	}

	if max > 0 && max < min {
		max = min
	}

	if diff < min {
		diff = min
	}
	if max > 0 && diff > max {
		diff = max
	}
	granularity := mc.cfg.DifficultyStepGranularity
	if granularity <= 0 {
		granularity = defaultDifficultyStepGranularity
	}
	// Snap the final difficulty to the configured logarithmic step grid.
	return quantizeDifficulty(diff, min, max, granularity)
}

func (mc *MinerConn) setDifficulty(diff float64) {
	requested := diff
	diff = mc.clampDifficulty(diff)
	now := time.Now()

	// Atomically update difficulty fields
	oldDiff := atomicLoadFloat64(&mc.difficulty)
	atomicStoreFloat64(&mc.previousDifficulty, oldDiff)
	atomicStoreFloat64(&mc.difficulty, diff)
	mc.shareTarget.Store(targetFromDifficulty(diff))
	mc.lastDiffChange.Store(now.UnixNano())

	target := mc.shareTarget.Load()
	if logger.Enabled(logLevelInfo) {
		logger.Info("set difficulty",
			"miner", mc.minerName(""),
			"requested_diff", requested,
			"clamped_diff", diff,
			"share_target", formatBigIntHex64(target),
		)
	}

	msg := map[string]any{
		"id":     nil,
		"method": "mining.set_difficulty",
		"params": []any{diff},
	}
	if err := mc.writeJSON(msg); err != nil {
		logger.Error("difficulty write error", "remote", mc.id, "error", err)
	}
}

// startupPrimedDifficulty applies a bounded startup bias toward slightly lower
// difficulty so early hashrate/share-rate estimates converge faster. It is
// only active before the miner submits its first accepted share.
func (mc *MinerConn) startupPrimedDifficulty(diff float64) float64 {
	if diff <= 0 {
		return diff
	}
	if mc.cfg.LockSuggestedDifficulty {
		return diff
	}
	mc.statsMu.Lock()
	accepted := mc.stats.Accepted
	mc.statsMu.Unlock()
	if accepted > 0 {
		return diff
	}
	primed := diff * startupDiffPrimingFactor
	minAllowed := diff * startupDiffPrimingMinFactor
	if primed < minAllowed {
		primed = minAllowed
	}
	primed = mc.clampDifficulty(primed)
	if primed <= 0 || primed >= diff {
		return diff
	}
	return primed
}

func (mc *MinerConn) sendVersionMask() {
	msg := map[string]any{
		"id":     nil,
		"method": "mining.set_version_mask",
		"params": []any{uint32ToHex8Lower(mc.versionMask)},
	}
	if err := mc.writeJSON(msg); err != nil {
		logger.Error("version mask write error", "remote", mc.id, "error", err)
	}
}

func (mc *MinerConn) updateVersionMask(poolMask uint32) bool {
	changed := false
	if mc.poolMask != poolMask {
		mc.poolMask = poolMask
		changed = true
	}

	if !mc.versionRoll {
		if mc.minerMask != 0 {
			final := poolMask & mc.minerMask
			if final != 0 {
				available := bits.OnesCount32(final)
				if mc.minVerBits <= 0 {
					mc.minVerBits = 1
				}
				if mc.minVerBits > available {
					mc.minVerBits = available
					changed = true
				}
				if mc.versionMask != final {
					changed = true
				}
				mc.versionMask = final
				mc.versionRoll = true
				return changed
			}
		}
		if mc.versionMask != poolMask {
			changed = true
		}
		mc.versionMask = poolMask
		return changed
	}

	finalMask := poolMask & mc.minerMask
	if finalMask == 0 {
		if mc.versionMask != 0 {
			changed = true
		}
		mc.versionMask = 0
		mc.versionRoll = false
		return changed
	}

	available := bits.OnesCount32(finalMask)
	if mc.minVerBits > available {
		mc.minVerBits = available
		changed = true
	}
	if mc.versionMask != finalMask {
		changed = true
	}
	mc.versionMask = finalMask
	return changed
}

func (mc *MinerConn) sendSetExtranonce(ex1 string, en2Size int) {
	msg := map[string]any{
		"id":     nil,
		"method": "mining.set_extranonce",
		"params": []any{ex1, en2Size},
	}
	if err := mc.writeJSON(msg); err != nil {
		logger.Error("set_extranonce write error", "remote", mc.id, "error", err)
	}
}

func (mc *MinerConn) handleExtranonceSubscribe(req *StratumRequest) {
	mc.extranonceSubscribed = true
	mc.writeTrueResponse(req.ID)

	ex1 := hex.EncodeToString(mc.extranonce1)
	en2Size := mc.cfg.Extranonce2Size
	if en2Size <= 0 {
		en2Size = 4
	}
	mc.sendSetExtranonce(ex1, en2Size)
}
