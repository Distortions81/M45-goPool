package main

import (
	"math"
	"sort"
	"strings"
	"time"
)

// statsWorker processes stats updates asynchronously from a buffered channel.
// This eliminates lock contention on the hot path (share submission).
func (mc *MinerConn) statsWorker() {
	defer mc.statsWg.Done()

	for update := range mc.statsUpdates {
		mc.statsMu.Lock()
		mc.ensureWindowLocked(update.timestamp)
		mc.ensureVardiffWindowLocked(update.timestamp)

		if update.worker != "" {
			if mc.stats.Worker != update.worker {
				mc.stats.Worker = update.worker
				mc.stats.WorkerSHA256 = workerNameHash(update.worker)
			} else if mc.stats.WorkerSHA256 == "" {
				mc.stats.WorkerSHA256 = workerNameHash(update.worker)
			}
		}

		mc.stats.WindowSubmissions++
		mc.vardiffWindowSubmissions++
		if update.accepted {
			mc.stats.Accepted++
			mc.stats.WindowAccepted++
			mc.vardiffWindowAccepted++
			if update.creditedDiff >= 0 {
				mc.stats.TotalDifficulty += update.creditedDiff
				mc.stats.WindowDifficulty += update.creditedDiff
				mc.vardiffWindowDifficulty += update.creditedDiff
				mc.updateHashrateLocked(update.creditedDiff, update.timestamp)
			}
		} else {
			mc.stats.Rejected++
		}
		mc.stats.LastShare = update.timestamp

		mc.lastShareHash = update.shareHash
		mc.lastShareAccepted = update.accepted
		mc.lastShareDifficulty = update.shareDiff
		mc.lastShareDetail = update.detail
		mc.observeNotifyFirstShareLocked(update.timestamp)
		mc.observeRecentSubmitOutcomeLocked(update.accepted, update.reason)
		if !update.accepted && update.reason != "" {
			mc.lastRejectReason = update.reason
		}
		mc.statsMu.Unlock()
	}
}

func (mc *MinerConn) minerName(fallback string) string {
	mc.statsMu.Lock()
	worker := mc.stats.Worker
	mc.statsMu.Unlock()
	if worker != "" {
		return worker
	}
	if fallback != "" {
		return fallback
	}
	return mc.id
}

func (mc *MinerConn) minerClientInfo() (minerType, name, version string) {
	mc.stateMu.Lock()
	minerType = mc.minerType
	name = mc.minerClientName
	version = mc.minerClientVersion
	mc.stateMu.Unlock()
	return minerType, name, version
}

func (mc *MinerConn) currentWorker() string {
	mc.statsMu.Lock()
	defer mc.statsMu.Unlock()
	return mc.stats.Worker
}

func (mc *MinerConn) currentWorkerHash() string {
	mc.statsMu.Lock()
	defer mc.statsMu.Unlock()
	return strings.TrimSpace(mc.stats.WorkerSHA256)
}

func (mc *MinerConn) currentSessionID() string {
	mc.stateMu.Lock()
	defer mc.stateMu.Unlock()
	return strings.TrimSpace(mc.sessionID)
}

func (mc *MinerConn) updateWorker(worker string) string {
	if worker == "" {
		return mc.minerName("")
	}
	mc.statsMu.Lock()
	if mc.stats.Worker != worker {
		mc.stats.Worker = worker
		mc.stats.WorkerSHA256 = workerNameHash(worker)
	} else if mc.stats.WorkerSHA256 == "" {
		mc.stats.WorkerSHA256 = workerNameHash(worker)
	}
	mc.statsMu.Unlock()
	return worker
}

func (mc *MinerConn) ensureWindowLocked(now time.Time) {
	if mc.stats.WindowStart.IsZero() {
		start := now
		if !mc.windowResetAnchor.IsZero() && now.After(mc.windowResetAnchor) {
			lagPct := mc.dynamicWindowStartLagPercentLocked(now)
			elapsed := now.Sub(mc.windowResetAnchor)
			start = mc.windowResetAnchor.Add(time.Duration((int64(elapsed) * int64(lagPct)) / 100))
		}
		mc.stats.WindowStart = start
		mc.windowResetAnchor = time.Time{}
		mc.stats.WindowDifficulty = 0
		return
	}
	// Keep status/confidence windows independent from vardiff retarget cadence.
	// Only reset after a long true idle gap to avoid carrying stale epochs.
	if !mc.stats.LastShare.IsZero() && now.Sub(mc.stats.LastShare) > statusWindowIdleReset {
		mc.stats.WindowStart = now
		mc.windowResetAnchor = time.Time{}
		mc.stats.WindowAccepted = 0
		mc.stats.WindowSubmissions = 0
		mc.stats.WindowDifficulty = 0
	}
}

func (mc *MinerConn) ensureVardiffWindowLocked(now time.Time) {
	if mc.vardiffWindowStart.IsZero() {
		start := now
		if !mc.vardiffWindowResetAnchor.IsZero() && now.After(mc.vardiffWindowResetAnchor) {
			start = mc.vardiffWindowResetAnchor
		}
		mc.vardiffWindowStart = start
		mc.vardiffWindowResetAnchor = time.Time{}
		mc.vardiffWindowDifficulty = 0
		return
	}
	maxAge := mc.vardiff.AdjustmentWindow * 2
	if maxAge <= 0 {
		maxAge = defaultVarDiffAdjustmentWindow * 2
	}
	if now.Sub(mc.vardiffWindowStart) > maxAge {
		mc.vardiffWindowStart = now
		mc.vardiffWindowResetAnchor = time.Time{}
		mc.vardiffWindowAccepted = 0
		mc.vardiffWindowSubmissions = 0
		mc.vardiffWindowDifficulty = 0
	}
}

func (mc *MinerConn) dynamicWindowStartLagPercentLocked(now time.Time) int {
	lagPct := max(windowStartLagPercent, 0)
	if lagPct > 100 {
		lagPct = 100
	}
	if mc.windowResetAnchor.IsZero() || !now.After(mc.windowResetAnchor) {
		return lagPct
	}
	elapsedMS := now.Sub(mc.windowResetAnchor).Seconds() * 1000.0
	if elapsedMS <= 0 {
		return lagPct
	}

	// Use miner-response RTT only; this is less affected by share-luck noise
	// than notify->first-share latency.
	pingP50, pingP95 := submitRTTPercentilesLocked(mc.pingRTTSamplesMs, mc.pingRTTCount)
	setupMS := pingP95
	if setupMS <= 0 {
		setupMS = pingP50
	}
	if setupMS > 0 {
		// Convert request/response RTT into a conservative setup estimate and
		// add a fixed miner-side prep offset commonly observed in practice.
		setupMS = (setupMS * 2)
	}
	if setupMS <= 0 {
		return lagPct
	}

	dynamicPct := max(int(math.Round((setupMS/elapsedMS)*100.0)), 20)
	if dynamicPct > 85 {
		dynamicPct = 85
	}
	// Blend with baseline so estimates are stable even with noisy latency.
	blended := max((dynamicPct+lagPct)/2, 0)
	if blended > 100 {
		blended = 100
	}
	return blended
}

// recordShare updates accounting for a submitted share. creditedDiff is the
// target difficulty we assigned for this share (used for hashrate), while
// shareDiff is the difficulty implied by the submitted hash (used for
// display/detail). They may differ when vardiff changed between notify and
// submit; we always want hashrate to use the assigned target.
func (mc *MinerConn) recordShare(worker string, accepted bool, creditedDiff float64, shareDiff float64, reason string, shareHash string, detail *ShareDetail, now time.Time) {
	// Send update to async stats worker instead of blocking on mutex
	update := statsUpdate{
		worker:       worker,
		accepted:     accepted,
		creditedDiff: creditedDiff,
		shareDiff:    shareDiff,
		reason:       reason,
		shareHash:    shareHash,
		detail:       detail,
		timestamp:    now,
	}

	select {
	case mc.statsUpdates <- update:
		// Successfully queued for async processing
	default:
		// Channel full, process synchronously as fallback
		mc.recordShareSync(update)
	}

	if mc.metrics != nil {
		mc.metrics.RecordShare(accepted, reason)
	}
}

// recordShareSync is the fallback synchronous stats update (only when channel is full)
func (mc *MinerConn) recordShareSync(update statsUpdate) {
	mc.statsMu.Lock()
	mc.ensureWindowLocked(update.timestamp)
	mc.ensureVardiffWindowLocked(update.timestamp)
	if update.worker != "" {
		if mc.stats.Worker != update.worker {
			mc.stats.Worker = update.worker
			mc.stats.WorkerSHA256 = workerNameHash(update.worker)
		} else if mc.stats.WorkerSHA256 == "" {
			mc.stats.WorkerSHA256 = workerNameHash(update.worker)
		}
	}
	mc.stats.WindowSubmissions++
	mc.vardiffWindowSubmissions++
	if update.accepted {
		mc.stats.Accepted++
		mc.stats.WindowAccepted++
		mc.vardiffWindowAccepted++
		if update.creditedDiff >= 0 {
			mc.stats.TotalDifficulty += update.creditedDiff
			mc.stats.WindowDifficulty += update.creditedDiff
			mc.vardiffWindowDifficulty += update.creditedDiff
			mc.updateHashrateLocked(update.creditedDiff, update.timestamp)
		}
	} else {
		mc.stats.Rejected++
	}
	mc.stats.LastShare = update.timestamp

	mc.lastShareHash = update.shareHash
	mc.lastShareAccepted = update.accepted
	mc.lastShareDifficulty = update.shareDiff
	mc.lastShareDetail = update.detail
	mc.observeNotifyFirstShareLocked(update.timestamp)
	mc.observeRecentSubmitOutcomeLocked(update.accepted, update.reason)
	if !update.accepted && update.reason != "" {
		mc.lastRejectReason = update.reason
	}
	mc.statsMu.Unlock()
}

func (mc *MinerConn) trackBestShare(worker, hash string, difficulty float64, now time.Time) {
	if mc.metrics == nil {
		return
	}
	mc.metrics.TrackBestShare(worker, hash, difficulty, now)
}

func (mc *MinerConn) snapshotStats() MinerStats {
	mc.statsMu.Lock()
	defer mc.statsMu.Unlock()
	return mc.stats
}

func (mc *MinerConn) snapshotStatsWithRates(now time.Time) (stats MinerStats, acceptRatePerMin float64, submitRatePerMin float64) {
	mc.statsMu.Lock()
	defer mc.statsMu.Unlock()
	stats = mc.stats
	if stats.WindowStart.IsZero() {
		return stats, 0, 0
	}
	window := now.Sub(stats.WindowStart)
	if window <= 0 {
		return stats, 0, 0
	}
	acceptRatePerMin = float64(stats.WindowAccepted) / window.Minutes()
	submitRatePerMin = float64(stats.WindowSubmissions) / window.Minutes()
	return stats, acceptRatePerMin, submitRatePerMin
}

type minerShareSnapshot struct {
	Stats                     MinerStats
	RetargetWindowStart       time.Time
	RetargetWindowAccepted    int
	RetargetWindowSubmissions int
	RetargetWindowDifficulty  float64
	RollingHashrate           float64
	RollingHashrateDisplay    float64
	SubmitRTTP50MS            float64
	SubmitRTTP95MS            float64
	PingRTTP50MS              float64
	PingRTTP95MS              float64
	NotifyToFirstShareMinMS   float64
	NotifyToFirstShareMS      float64
	NotifyToFirstShareP50MS   float64
	NotifyToFirstShareP95MS   float64
	NotifyToFirstShareSamples int
	RecentStaleRate           float64
	LastShareHash             string
	LastShareAccepted         bool
	LastShareDifficulty       float64
	LastShareDetail           *ShareDetail
	LastReject                string
}

func (mc *MinerConn) snapshotShareInfo() minerShareSnapshot {
	mc.statsMu.Lock()
	defer mc.statsMu.Unlock()
	now := time.Now()
	p50, p95 := submitRTTPercentilesLocked(mc.submitRTTSamplesMs, mc.submitRTTCount)
	pingP50, pingP95 := submitRTTPercentilesLocked(mc.pingRTTSamplesMs, mc.pingRTTCount)
	warmMin := submitRTTMinLocked(mc.notifyToFirstSamplesMs, mc.notifyToFirstCount)
	warmP50, warmP95 := submitRTTPercentilesLocked(mc.notifyToFirstSamplesMs, mc.notifyToFirstCount)
	workStartMS := mc.lastNotifyToFirstShareMs
	if mc.notifyAwaitingFirstShare && !mc.notifySentAt.IsZero() {
		if elapsed := time.Since(mc.notifySentAt).Seconds() * 1000.0; elapsed > workStartMS {
			workStartMS = elapsed
		}
	}
	controlHashrate, displayHashrate := mc.decayedHashratesLocked(now)
	if controlHashrate <= 0 {
		controlHashrate = displayHashrate
	}
	return minerShareSnapshot{
		Stats:                     mc.stats,
		RetargetWindowStart:       mc.vardiffWindowStart,
		RetargetWindowAccepted:    mc.vardiffWindowAccepted,
		RetargetWindowSubmissions: mc.vardiffWindowSubmissions,
		RetargetWindowDifficulty:  mc.vardiffWindowDifficulty,
		RollingHashrate:           controlHashrate,
		RollingHashrateDisplay:    displayHashrate,
		SubmitRTTP50MS:            p50,
		SubmitRTTP95MS:            p95,
		PingRTTP50MS:              pingP50,
		PingRTTP95MS:              pingP95,
		NotifyToFirstShareMinMS:   warmMin,
		NotifyToFirstShareMS:      workStartMS,
		NotifyToFirstShareP50MS:   warmP50,
		NotifyToFirstShareP95MS:   warmP95,
		NotifyToFirstShareSamples: mc.notifyToFirstCount,
		RecentStaleRate:           mc.recentStaleRateLocked(),
		LastShareHash:             mc.lastShareHash,
		LastShareAccepted:         mc.lastShareAccepted,
		LastShareDifficulty:       mc.lastShareDifficulty,
		LastShareDetail:           mc.lastShareDetail,
		LastReject:                mc.lastRejectReason,
	}
}

func (mc *MinerConn) recordSubmitRTT(d time.Duration) {
	if d <= 0 {
		return
	}
	ms := d.Seconds() * 1000.0
	if ms <= 0 {
		return
	}
	mc.statsMu.Lock()
	mc.submitRTTSamplesMs[mc.submitRTTIndex] = ms
	mc.submitRTTIndex = (mc.submitRTTIndex + 1) % len(mc.submitRTTSamplesMs)
	if mc.submitRTTCount < len(mc.submitRTTSamplesMs) {
		mc.submitRTTCount++
	}
	mc.statsMu.Unlock()
}

func (mc *MinerConn) recordNotifySent(at time.Time) {
	if at.IsZero() {
		return
	}
	mc.statsMu.Lock()
	mc.notifySentAt = at
	mc.notifyAwaitingFirstShare = true
	mc.statsMu.Unlock()
}

func (mc *MinerConn) recordPingRTT(ms float64) {
	if ms <= 0 {
		return
	}
	mc.statsMu.Lock()
	mc.pingRTTSamplesMs[mc.pingRTTIndex] = ms
	mc.pingRTTIndex = (mc.pingRTTIndex + 1) % len(mc.pingRTTSamplesMs)
	if mc.pingRTTCount < len(mc.pingRTTSamplesMs) {
		mc.pingRTTCount++
	}
	mc.statsMu.Unlock()
}

func (mc *MinerConn) observeNotifyFirstShareLocked(shareAt time.Time) {
	if !mc.notifyAwaitingFirstShare || mc.notifySentAt.IsZero() || shareAt.IsZero() {
		return
	}
	if shareAt.Before(mc.notifySentAt) {
		return
	}
	mc.lastNotifyToFirstShareMs = shareAt.Sub(mc.notifySentAt).Seconds() * 1000.0
	if mc.lastNotifyToFirstShareMs > 0 {
		mc.notifyToFirstSamplesMs[mc.notifyToFirstIndex] = mc.lastNotifyToFirstShareMs
		mc.notifyToFirstIndex = (mc.notifyToFirstIndex + 1) % len(mc.notifyToFirstSamplesMs)
		if mc.notifyToFirstCount < len(mc.notifyToFirstSamplesMs) {
			mc.notifyToFirstCount++
		}
	}
	mc.notifyAwaitingFirstShare = false
}

func (mc *MinerConn) observeRecentSubmitOutcomeLocked(accepted bool, reason string) {
	isStale := !accepted && isStaleRejectReason(reason)
	slot := mc.recentSubmissionIndex
	if mc.recentSubmissionCount >= len(mc.recentSubmissionKinds) {
		if mc.recentSubmissionKinds[slot] == 1 && mc.recentStaleRejectCount > 0 {
			mc.recentStaleRejectCount--
		}
	} else {
		mc.recentSubmissionCount++
	}
	if isStale {
		mc.recentSubmissionKinds[slot] = 1
		mc.recentStaleRejectCount++
	} else {
		mc.recentSubmissionKinds[slot] = 0
	}
	mc.recentSubmissionIndex = (slot + 1) % len(mc.recentSubmissionKinds)
}

func (mc *MinerConn) recentStaleRateLocked() float64 {
	if mc.recentSubmissionCount <= 0 {
		return 0
	}
	return float64(mc.recentStaleRejectCount) / float64(mc.recentSubmissionCount)
}

func isStaleRejectReason(reason string) bool {
	reason = strings.ToLower(strings.TrimSpace(reason))
	return reason == "stale job" || reason == "stale"
}

func submitRTTPercentilesLocked(samples [64]float64, count int) (p50, p95 float64) {
	if count <= 0 {
		return 0, 0
	}
	if count > len(samples) {
		count = len(samples)
	}
	vals := make([]float64, 0, count)
	for i := 0; i < count; i++ {
		v := samples[i]
		if v > 0 {
			vals = append(vals, v)
		}
	}
	if len(vals) == 0 {
		return 0, 0
	}
	sort.Float64s(vals)
	idx50 := (len(vals) - 1) * 50 / 100
	idx95 := (len(vals) - 1) * 95 / 100
	return vals[idx50], vals[idx95]
}

func submitRTTMinLocked(samples [64]float64, count int) float64 {
	if count <= 0 {
		return 0
	}
	if count > len(samples) {
		count = len(samples)
	}
	min := 0.0
	for i := 0; i < count; i++ {
		v := samples[i]
		if v <= 0 {
			continue
		}
		if min <= 0 || v < min {
			min = v
		}
	}
	return min
}
