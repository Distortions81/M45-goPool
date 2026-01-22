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

// validateWorkerWallet checks whether the given worker name appears to be a
// valid Bitcoin address for the current network using local address
// validation (btcsuite).
func validateWorkerWallet(_ rpcCaller, _ *AccountStore, worker string) bool {
	base := workerBaseAddress(worker)
	if base == "" {
		return false
	}
	_, err := scriptForAddress(base, ChainParams())
	return err == nil
}

func (mc *MinerConn) recordActivity(now time.Time) {
	mc.lastActivity = now
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
	// Determine the window for counting invalid submissions. Prefer the
	// explicit ban window from config when set, otherwise fall back to the
	// VarDiff burst window, and finally a 60s default.
	window := mc.cfg.BanInvalidSubmissionsWindow
	if window <= 0 {
		window = mc.vardiff.BurstWindow
		if window <= 0 {
			window = time.Minute
		}
	}
	if now.Sub(mc.lastPenalty) > window {
		mc.invalidSubs = 0
	}
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
		return false, mc.invalidSubs
	}

	// Require a burst of bad submissions in a short window before banning.
	threshold := mc.cfg.BanInvalidSubmissionsAfter
	if threshold <= 0 {
		threshold = mc.vardiff.MaxBurstShares
		if threshold <= 0 {
			threshold = 300
		}
	}
	if mc.invalidSubs >= threshold {
		banDuration := mc.cfg.BanInvalidSubmissionsDuration
		if banDuration <= 0 {
			banDuration = 15 * time.Minute
		}
		mc.banUntil = now.Add(banDuration)
		if mc.banReason == "" {
			mc.banReason = fmt.Sprintf("too many invalid submissions (%d in %s)", mc.invalidSubs, window)
		}
		return true, mc.invalidSubs
	}
	return false, mc.invalidSubs
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

func (mc *MinerConn) shareRates(now time.Time) (float64, float64) {
	mc.statsMu.Lock()
	defer mc.statsMu.Unlock()
	if mc.stats.WindowStart.IsZero() {
		return 0, 0
	}
	window := now.Sub(mc.stats.WindowStart)
	if window <= 0 {
		return 0, 0
	}
	accRate := float64(mc.stats.WindowAccepted) / window.Minutes()
	subRate := float64(mc.stats.WindowSubmissions) / window.Minutes()
	return accRate, subRate
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
	fallback := targetFromDifficulty(mc.cfg.MinDifficulty)
	oldTarget := mc.shareTarget.Load()
	if oldTarget == nil || oldTarget.Sign() <= 0 {
		mc.shareTarget.CompareAndSwap(oldTarget, new(big.Int).Set(fallback))
	}
	return fallback
}

func (mc *MinerConn) resetShareWindow(now time.Time) {
	connSeq := atomic.LoadUint64(&mc.connectionSeq)

	mc.statsMu.Lock()
	mc.stats.WindowStart = now
	mc.stats.WindowAccepted = 0
	mc.stats.WindowSubmissions = 0
	mc.stats.WindowDifficulty = 0
	mc.lastHashrateUpdate = time.Time{}
	mc.rollingHashrateValue = 0
	mc.hashrateSampleCount = 0
	mc.hashrateAccumulatedDiff = 0
	mc.statsMu.Unlock()

	if mc.metrics != nil && connSeq != 0 {
		mc.metrics.UpdateConnectionHashrate(connSeq, 0)
	}
}

// updateHashrateLocked updates the per-connection hashrate using a simple
// exponential moving average (EMA) over time. It expects statsMu to be held
// by the caller.
func (mc *MinerConn) updateHashrateLocked(targetDiff float64, shareTime time.Time) {
	if targetDiff <= 0 || shareTime.IsZero() {
		return
	}

	samplesNeeded := mc.cfg.HashrateEMAMinShares
	if samplesNeeded < minHashrateEMAMinShares {
		samplesNeeded = minHashrateEMAMinShares
	}

	if mc.lastHashrateUpdate.IsZero() {
		mc.lastHashrateUpdate = shareTime
		mc.hashrateSampleCount = 1
		mc.hashrateAccumulatedDiff = targetDiff
		return
	}

	mc.hashrateSampleCount++
	mc.hashrateAccumulatedDiff += targetDiff
	if mc.hashrateSampleCount < samplesNeeded {
		return
	}

	elapsed := shareTime.Sub(mc.lastHashrateUpdate).Seconds()
	if elapsed <= 0 {
		mc.lastHashrateUpdate = shareTime
		mc.hashrateSampleCount = 1
		mc.hashrateAccumulatedDiff = targetDiff
		return
	}

	sample := (mc.hashrateAccumulatedDiff * hashPerShare) / elapsed

	// Apply an EMA with a configurable time constant so that hashrate responds
	// quickly to changes but decays smoothly when shares slow down.
	tauSeconds := mc.cfg.HashrateEMATauSeconds
	if tauSeconds <= 0 {
		tauSeconds = defaultHashrateEMATauSeconds
	}
	alpha := 1 - math.Exp(-elapsed/tauSeconds)
	if alpha < 0 {
		alpha = 0
	}
	if alpha > 1 {
		alpha = 1
	}

	if mc.rollingHashrateValue <= 0 {
		mc.rollingHashrateValue = sample
	} else {
		mc.rollingHashrateValue = mc.rollingHashrateValue + alpha*(sample-mc.rollingHashrateValue)
	}
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
	mc.lastClean = clean

	// Evict oldest jobs if we exceed the max limit
	dupEnabled := mc.cfg.CheckDuplicateShares
	now := time.Time{}
	for len(mc.jobOrder) > mc.maxRecentJobs && len(mc.jobOrder) > 0 {
		oldest := mc.jobOrder[0]
		mc.jobOrder = mc.jobOrder[1:]
		delete(mc.activeJobs, oldest)
		if mc.jobScriptTime != nil {
			delete(mc.jobScriptTime, oldest)
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

func (mc *MinerConn) jobForID(jobID string) (*Job, bool) {
	mc.jobMu.Lock()
	defer mc.jobMu.Unlock()
	job, ok := mc.activeJobs[jobID]
	return job, ok
}

// jobForIDWithLast returns the job for the given ID along with the current lastJob
// and the scriptTime used when this job was notified to this connection, all
// under a single lock acquisition to avoid race conditions.
func (mc *MinerConn) jobForIDWithLast(jobID string) (job *Job, lastJob *Job, scriptTime int64, ok bool) {
	mc.jobMu.Lock()
	defer mc.jobMu.Unlock()
	job, ok = mc.activeJobs[jobID]
	if mc.jobScriptTime != nil {
		scriptTime = mc.jobScriptTime[jobID]
	}
	return job, mc.lastJob, scriptTime, ok
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
	return mc.lastJob.Template.Previous != job.Template.Previous || mc.lastJob.Template.Height != job.Template.Height
}

func (mc *MinerConn) isDuplicateShare(jobID, extranonce2, ntime, nonce, versionHex string) bool {
	// Skip duplicate checking if disabled (default for solo pools)
	if !mc.cfg.CheckDuplicateShares {
		return false
	}

	mc.jobMu.Lock()
	defer mc.jobMu.Unlock()

	if mc.shareCache == nil {
		// Allocate lazily so disabling duplicate checks avoids per-connection maps.
		mc.shareCache = make(map[string]*duplicateShareSet, mc.maxRecentJobs)
	}
	if mc.evictedShareCache == nil {
		mc.evictedShareCache = make(map[string]*evictedCacheEntry)
	}

	var dk duplicateShareKey
	makeDuplicateShareKey(&dk, extranonce2, ntime, nonce, versionHex)

	// Check active job cache first
	cache := mc.shareCache[jobID]
	if cache != nil {
		return cache.seenOrAdd(dk)
	}

	// Check evicted job cache (for late shares on evicted jobs)
	if entry := mc.evictedShareCache[jobID]; entry != nil {
		return entry.cache.seenOrAdd(dk)
	}

	// No cache exists - create new one in active cache
	cache = &duplicateShareSet{}
	mc.shareCache[jobID] = cache
	return cache.seenOrAdd(dk)
}

func (mc *MinerConn) maybeAdjustDifficulty(now time.Time) bool {
	// If this connection is locked to a static difficulty, skip VarDiff.
	if mc.lockDifficulty {
		return false
	}

	snap := mc.snapshotShareInfo()
	newDiff := mc.suggestedVardiff(now, snap)

	currentDiff := atomicLoadFloat64(&mc.difficulty)

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
	return true
}

// suggestedVardiff returns the difficulty VarDiff would select based on the
// current stats, without applying any changes.
func (mc *MinerConn) suggestedVardiff(now time.Time, snap minerShareSnapshot) float64 {
	windowStart := snap.Stats.WindowStart
	windowAccepted := snap.Stats.WindowAccepted
	windowSubmissions := snap.Stats.WindowSubmissions

	lastChange := time.Unix(0, mc.lastDiffChange.Load())
	currentDiff := atomicLoadFloat64(&mc.difficulty)

	if currentDiff <= 0 {
		currentDiff = mc.vardiff.MinDiff
	}
	if windowSubmissions == 0 || windowStart.IsZero() {
		return currentDiff
	}
	if !lastChange.IsZero() && now.Sub(lastChange) < minDiffChangeInterval {
		return currentDiff
	}
	if windowAccepted == 0 {
		return currentDiff
	}

	rollingHashrate := snap.RollingHashrate
	if rollingHashrate <= 0 {
		return currentDiff
	}

	targetShares := mc.vardiff.TargetSharesPerMin
	if targetShares <= 0 {
		targetShares = defaultVarDiff.TargetSharesPerMin
	}
	if targetShares <= 0 {
		targetShares = 6
	}
	targetDiff := (rollingHashrate / hashPerShare) * 60 / targetShares
	if targetDiff <= 0 || math.IsNaN(targetDiff) || math.IsInf(targetDiff, 0) {
		return currentDiff
	}
	// Aim one step lower than the computed target to reduce timeouts.
	stepFactor := mc.vardiff.Step
	if stepFactor <= 1 {
		stepFactor = 2
	}
	targetDiff = targetDiff / stepFactor
	if targetDiff > mc.vardiff.MaxDiff {
		targetDiff = mc.vardiff.MaxDiff
	}
	if targetDiff < mc.vardiff.MinDiff {
		targetDiff = mc.vardiff.MinDiff
	}
	if mc.cfg.MaxDifficulty > 0 && targetDiff > mc.cfg.MaxDifficulty {
		targetDiff = mc.cfg.MaxDifficulty
	}

	ratio := targetDiff / currentDiff
	const band = 0.5
	if ratio >= 1-band && ratio <= 1+band {
		return currentDiff
	}

	dampingFactor := mc.vardiff.DampingFactor
	if dampingFactor <= 0 || dampingFactor > 1 {
		dampingFactor = 0.5
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
	maxFactor := step
	minFactor := 1 / step
	if factor > maxFactor {
		factor = maxFactor
	}
	if factor < minFactor {
		factor = minFactor
	}
	newDiff = currentDiff * factor

	if newDiff == 0 || math.Abs(newDiff-currentDiff) < 1e-6 {
		return currentDiff
	}
	return mc.clampDifficulty(newDiff)
}

// quantizeDifficultyToPowerOfTwo snaps a difficulty value to a power-of-two
// level within [min, max] (if max > 0). This keeps stratum difficulty levels
// on clean power-of-two boundaries.
func quantizeDifficultyToPowerOfTwo(diff, min, max float64) float64 {
	if diff <= 0 {
		diff = min
	}
	if diff <= 0 {
		return diff
	}

	log2 := math.Log2(diff)
	if math.IsNaN(log2) || math.IsInf(log2, 0) {
		return diff
	}

	exp := math.Round(log2)
	cand := math.Pow(2, exp)

	// Ensure candidate lies within [min, max] by snapping up/down as needed.
	if cand < min && min > 0 {
		exp = math.Ceil(math.Log2(min))
		cand = math.Pow(2, exp)
	}
	if max > 0 && cand > max {
		exp = math.Floor(math.Log2(max))
		cand = math.Pow(2, exp)
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
	if min <= 0 {
		min = mc.vardiff.MinDiff
	} else if mc.vardiff.MinDiff > min {
		min = mc.vardiff.MinDiff
	}

	max := mc.cfg.MaxDifficulty
	if max <= 0 {
		max = mc.vardiff.MaxDiff
	} else if mc.vardiff.MaxDiff < max {
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
	// Snap the final difficulty to a power-of-two level within [min, max].
	return quantizeDifficultyToPowerOfTwo(diff, min, max)
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
			"share_target", fmt.Sprintf("%064x", target),
		)
	}

	msg := map[string]interface{}{
		"id":     nil,
		"method": "mining.set_difficulty",
		"params": []interface{}{diff},
	}
	if err := mc.writeJSON(msg); err != nil {
		logger.Error("difficulty write error", "remote", mc.id, "error", err)
	}
}

func (mc *MinerConn) sendVersionMask() {
	msg := map[string]interface{}{
		"id":     nil,
		"method": "mining.set_version_mask",
		"params": []interface{}{fmt.Sprintf("%08x", mc.versionMask)},
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
	msg := map[string]interface{}{
		"id":     nil,
		"method": "mining.set_extranonce",
		"params": []interface{}{ex1, en2Size},
	}
	if err := mc.writeJSON(msg); err != nil {
		logger.Error("set_extranonce write error", "remote", mc.id, "error", err)
	}
}

func (mc *MinerConn) handleExtranonceSubscribe(req *StratumRequest) {
	mc.extranonceSubscribed = true
	result := StratumResponse{
		ID:     req.ID,
		Result: true,
		Error:  nil,
	}
	mc.writeResponse(result)

	ex1 := hex.EncodeToString(mc.extranonce1)
	en2Size := mc.cfg.Extranonce2Size
	if en2Size <= 0 {
		en2Size = 4
	}
	mc.sendSetExtranonce(ex1, en2Size)
}
