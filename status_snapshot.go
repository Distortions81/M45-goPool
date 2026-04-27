package main

import (
	"context"
	"encoding/hex"
	stdjson "encoding/json"
	"fmt"
	"html/template"
	"io"
	"math"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bytedance/sonic"
)

func shareRatePerMinute(stats MinerStats, now time.Time) float64 {
	if stats.WindowStart.IsZero() {
		return 0
	}
	window := now.Sub(stats.WindowStart)
	if window <= 0 {
		return 0
	}
	return float64(stats.WindowAccepted) / window.Minutes()
}

func modeledShareRatePerMinute(hashrate, diff float64) float64 {
	if hashrate <= 0 || diff <= 0 {
		return 0
	}
	return (hashrate / hashPerShare) * 60.0 / diff
}

func cumulativeHashrateEstimate(stats MinerStats, connectedAt, now time.Time) float64 {
	if connectedAt.IsZero() || !now.After(connectedAt) || stats.TotalDifficulty <= 0 {
		return 0
	}
	elapsed := now.Sub(connectedAt).Seconds()
	if elapsed <= 0 {
		return 0
	}
	return (stats.TotalDifficulty * hashPerShare) / elapsed
}

func cumulativeHashrateEstimateFromDifficultySum(sumDifficulty float64, startAt, now time.Time) float64 {
	if sumDifficulty <= 0 || startAt.IsZero() || !now.After(startAt) {
		return 0
	}
	elapsed := now.Sub(startAt).Seconds()
	if elapsed <= 0 {
		return 0
	}
	return (sumDifficulty * hashPerShare) / elapsed
}

func blendDisplayHashrate(stats MinerStats, connectedAt, now time.Time, ema, cumulativeLifetime, cumulativeRecent float64, useCumulative, useRecentCumulative bool) float64 {
	if !useCumulative {
		return ema
	}
	if ema <= 0 {
		if cumulativeRecent > 0 {
			return cumulativeRecent
		}
		return cumulativeLifetime
	}
	if cumulativeLifetime <= 0 && cumulativeRecent <= 0 {
		return ema
	}

	cumulative := cumulativeLifetime
	// Prefer a recent cumulative estimate (VarDiff retarget window) when it
	// indicates a materially higher hashrate than the lifetime cumulative.
	// This avoids slow upward convergence after early low-difficulty epochs.
	if useRecentCumulative && cumulativeRecent > 0 && (cumulative <= 0 || cumulativeRecent > cumulative*1.05) {
		cumulative = cumulativeRecent
	}

	// Favor cumulative estimator as samples grow: it is more accurate over
	// longer horizons and less sensitive to vardiff/window resets.
	w := float64(stats.Accepted) / 64.0
	if w < 0 {
		w = 0
	}
	if w > 1 {
		w = 1
	}
	// Time-based floor: even with noisy/low accepted-share counts, long-lived
	// steady-state connections should converge toward cumulative hashrate.
	//
	// We ramp this floor up gradually to avoid over-weighting cumulative too
	// early after connect/reconnect.
	const ageToFullCumulative = 20 * time.Minute
	if !connectedAt.IsZero() {
		age := now.Sub(connectedAt)
		if age > 0 {
			ageWeight := age.Seconds() / ageToFullCumulative.Seconds()
			if ageWeight < 0 {
				ageWeight = 0
			}
			if ageWeight > 1 {
				ageWeight = 1
			}
			if ageWeight > w {
				w = ageWeight
			}
		}
	}
	if w < 0 {
		w = 0
	}
	if w > 1 {
		w = 1
	}
	return (1-w)*ema + w*cumulative
}

func blendedShareRatePerMinute(stats MinerStats, now time.Time, rawRate, modeledRate float64) float64 {
	if rawRate <= 0 {
		return modeledRate
	}
	if modeledRate <= 0 {
		return rawRate
	}
	weight := float64(stats.WindowAccepted) / 16.0
	if weight < 0 {
		weight = 0
	}
	if weight > 1 {
		weight = 1
	}
	if !stats.WindowStart.IsZero() {
		if window := now.Sub(stats.WindowStart); window > 0 && window < 45*time.Second {
			weight *= window.Seconds() / 45.0
		}
	}
	if weight < 0 {
		weight = 0
	}
	if weight > 1 {
		weight = 1
	}
	return weight*rawRate + (1-weight)*modeledRate
}

func hasReliableRateEstimate(stats MinerStats, now time.Time, modeledRate float64, connectedAt time.Time) bool {
	minWindow, minEvidence, minCumulativeAccepted, minConnected := reliabilityThresholds(modeledRate)
	if modeledRate <= 0 {
		return false
	}
	if !stats.WindowStart.IsZero() {
		window := now.Sub(stats.WindowStart)
		if window >= minWindow {
			expected := modeledRate * window.Minutes()
			if expected > 0 {
				evidence := float64(stats.WindowAccepted)
				if expected < evidence {
					evidence = expected
				}
				if evidence >= minEvidence {
					return true
				}
			}
		}
	}
	// Fallback path for frequent vardiff resets: use cumulative accepted shares
	// and minimum connection age to avoid suppressing useful estimates forever.
	if stats.Accepted >= minCumulativeAccepted && !connectedAt.IsZero() && now.Sub(connectedAt) >= minConnected {
		return true
	}
	return false
}

func reliabilityThresholds(modeledRate float64) (minWindow time.Duration, minEvidence float64, minCumulativeAccepted int64, minConnected time.Duration) {
	// Base target: enough time for ~3 expected shares under modeled cadence.
	if modeledRate <= 0 {
		return 60 * time.Second, 4, 6, 90 * time.Second
	}
	targetWindow := max(time.Duration((3.0/modeledRate)*float64(time.Minute)), 30*time.Second)
	if targetWindow > 2*time.Minute {
		targetWindow = 2 * time.Minute
	}
	expectedInWindow := modeledRate * targetWindow.Minutes()
	if expectedInWindow < 2.5 {
		expectedInWindow = 2.5
	}
	if expectedInWindow > 6 {
		expectedInWindow = 6
	}
	minWindow = targetWindow
	minEvidence = expectedInWindow
	minCumulativeAccepted = max(int64(math.Ceil(expectedInWindow)), 3)
	if minCumulativeAccepted > 8 {
		minCumulativeAccepted = 8
	}
	minConnected = max(targetWindow+30*time.Second, 60*time.Second)
	if minConnected > 4*time.Minute {
		minConnected = 4 * time.Minute
	}
	return minWindow, minEvidence, minCumulativeAccepted, minConnected
}

func hashrateConfidenceLevel(stats MinerStats, now time.Time, modeledRate, estimatedHashrate float64, connectedAt time.Time) int {
	if modeledRate <= 0 || estimatedHashrate <= 0 {
		return 0
	}
	if !hasReliableRateEstimate(stats, now, modeledRate, connectedAt) {
		return 0
	}
	settlingWindowAgreement := hashrateAgreementWithinTolerance(stats, now, modeledRate, settlingHashrateMaxRelativeError, settlingHashrateMinExpectedShares)
	settlingCumulativeAgreement := hashrateEstimateAgreesWithCumulative(stats, now, connectedAt, estimatedHashrate, settlingHashrateCumulativeMaxRelativeError)
	hasCumulativeEvidence := hashrateHasCumulativeEvidence(stats, now, connectedAt)
	if !settlingWindowAgreement {
		// Frequent vardiff resets can keep the current window too short for
		// window-based agreement checks; allow cumulative agreement to settle
		// confidence once enough long-horizon evidence exists.
		if !(hasCumulativeEvidence && settlingCumulativeAgreement) {
			return 0
		}
	}
	if hasCumulativeEvidence && !settlingCumulativeAgreement {
		return 0
	}
	minWindow, minEvidence, minCumulativeAccepted, minConnected := reliabilityThresholds(modeledRate)
	highWindow := max(minWindow*2, 90*time.Second)
	if highWindow > 6*time.Minute {
		highWindow = 6 * time.Minute
	}
	highEvidence := minEvidence * 2
	if highEvidence < 6 {
		highEvidence = 6
	}
	if highEvidence > 20 {
		highEvidence = 20
	}
	highCum := max(minCumulativeAccepted*2, 8)
	if highCum > 24 {
		highCum = 24
	}
	highConn := max(minConnected+2*time.Minute, 3*time.Minute)
	if highConn > 10*time.Minute {
		highConn = 10 * time.Minute
	}
	if !stats.WindowStart.IsZero() {
		window := now.Sub(stats.WindowStart)
		if window >= highWindow {
			expected := modeledRate * window.Minutes()
			if expected > 0 {
				evidence := float64(stats.WindowAccepted)
				if expected < evidence {
					evidence = expected
				}
				if evidence >= highEvidence {
					if hashrateAgreementWithinTolerance(stats, now, modeledRate, stableHashrateMaxRelativeError, stableHashrateMinExpectedShares) &&
						hashrateEstimateAgreesWithCumulative(stats, now, connectedAt, estimatedHashrate, stableHashrateCumulativeMaxRelativeError) {
						if hashrateAgreementWithinTolerance(stats, now, modeledRate, veryStableHashrateMaxRelativeError, veryStableHashrateMinExpectedShares) &&
							hashrateEstimateAgreesWithCumulative(stats, now, connectedAt, estimatedHashrate, veryStableHashrateCumulativeMaxRelativeError) &&
							stats.Accepted >= 32 &&
							!connectedAt.IsZero() &&
							now.Sub(connectedAt) >= 20*time.Minute {
							return 3
						}
						return 2
					}
					return 1
				}
			}
		}
	}
	if stats.Accepted >= highCum && !connectedAt.IsZero() && now.Sub(connectedAt) >= highConn {
		if hashrateAgreementWithinTolerance(stats, now, modeledRate, stableHashrateMaxRelativeError, stableHashrateMinExpectedShares) &&
			hashrateEstimateAgreesWithCumulative(stats, now, connectedAt, estimatedHashrate, stableHashrateCumulativeMaxRelativeError) {
			if hashrateAgreementWithinTolerance(stats, now, modeledRate, veryStableHashrateMaxRelativeError, veryStableHashrateMinExpectedShares) &&
				hashrateEstimateAgreesWithCumulative(stats, now, connectedAt, estimatedHashrate, veryStableHashrateCumulativeMaxRelativeError) &&
				stats.Accepted >= 32 &&
				now.Sub(connectedAt) >= 20*time.Minute {
				return 3
			}
			return 2
		}
		return 1
	}
	if hashrateAgreementWithinTolerance(stats, now, modeledRate, stableHashrateMaxRelativeError, stableHashrateMinExpectedShares) &&
		hashrateEstimateAgreesWithCumulative(stats, now, connectedAt, estimatedHashrate, stableHashrateCumulativeMaxRelativeError) {
		if hashrateAgreementWithinTolerance(stats, now, modeledRate, veryStableHashrateMaxRelativeError, veryStableHashrateMinExpectedShares) &&
			hashrateEstimateAgreesWithCumulative(stats, now, connectedAt, estimatedHashrate, veryStableHashrateCumulativeMaxRelativeError) &&
			stats.Accepted >= 32 &&
			!connectedAt.IsZero() &&
			now.Sub(connectedAt) >= 20*time.Minute {
			return 3
		}
		return 2
	}
	return 1
}

func hashrateAgreementWithinTolerance(stats MinerStats, now time.Time, modeledRate, maxRelativeError, minExpectedShares float64) bool {
	if modeledRate <= 0 || maxRelativeError < 0 || minExpectedShares <= 0 || stats.WindowStart.IsZero() {
		return false
	}
	window := now.Sub(stats.WindowStart)
	if window <= 0 {
		return false
	}
	expectedShares := modeledRate * window.Minutes()
	if expectedShares < minExpectedShares {
		return false
	}
	observedShares := float64(stats.WindowAccepted)
	if observedShares <= 0 {
		return false
	}
	relativeError := math.Abs(observedShares-expectedShares) / expectedShares
	return relativeError <= maxRelativeError
}

func hashrateHasCumulativeEvidence(stats MinerStats, now, connectedAt time.Time) bool {
	if connectedAt.IsZero() || !now.After(connectedAt) {
		return false
	}
	if stats.Accepted < hashrateCumulativeAgreementMinAccepted {
		return false
	}
	return now.Sub(connectedAt) >= hashrateCumulativeAgreementMinConnected
}

func hashrateEstimateAgreesWithCumulative(stats MinerStats, now, connectedAt time.Time, estimateHashrate, maxRelativeError float64) bool {
	if estimateHashrate <= 0 || maxRelativeError < 0 || connectedAt.IsZero() || !now.After(connectedAt) {
		return false
	}
	cumulative := cumulativeHashrateEstimate(stats, connectedAt, now)
	if cumulative <= 0 {
		return false
	}
	relativeError := math.Abs(estimateHashrate-cumulative) / cumulative
	return relativeError <= maxRelativeError
}

func hashrateAccuracySymbol(level int) string {
	switch level {
	case 0:
		return "~"
	case 1:
		return "≈"
	default:
		// Stable hashrate estimates intentionally display no marker.
		return ""
	}
}

func workerHashrateEstimate(view WorkerView, now time.Time) float64 {
	if view.RollingHashrate > 0 {
		return view.RollingHashrate
	}
	if !view.WindowStart.IsZero() {
		window := now.Sub(view.WindowStart)
		if window <= 0 {
			return 0
		}
		// Keep startup behavior aligned with the EMA bootstrap horizon.
		if window < initialHashrateEMATau {
			return 0
		}
		if view.WindowDifficulty > 0 {
			return (view.WindowDifficulty * hashPerShare) / window.Seconds()
		}
	}
	if view.ShareRate > 0 && view.Difficulty > 0 {
		return (view.Difficulty * hashPerShare * view.ShareRate) / 60.0
	}
	return 0
}

func workerViewFromConn(mc *MinerConn, now time.Time) WorkerView {
	estimatedRTT := estimateConnRTTMS(mc.conn)
	if estimatedRTT > 0 {
		mc.recordPingRTT(estimatedRTT)
	}
	snap := mc.snapshotShareInfo()
	stats := snap.Stats
	name := stats.Worker
	if name == "" {
		name = mc.id
	}
	displayName := shortWorkerName(name, workerNamePrefix, workerNameSuffix)
	workerHash := strings.TrimSpace(stats.WorkerSHA256)
	diff := mc.currentDifficulty()
	rawRate := shareRatePerMinute(stats, now)
	hashRate := workerHashrateEstimate(WorkerView{
		RollingHashrate:  snap.RollingHashrateDisplay,
		WindowStart:      stats.WindowStart,
		WindowDifficulty: stats.WindowDifficulty,
		ShareRate:        rawRate,
		Difficulty:       diff,
	}, now)
	lifetimeCumulative := cumulativeHashrateEstimate(stats, mc.connectedAt, now)
	recentCumulative := 0.0
	if snap.RetargetWindowAccepted >= 8 && !snap.RetargetWindowStart.IsZero() && now.After(snap.RetargetWindowStart) {
		if window := now.Sub(snap.RetargetWindowStart); window >= initialHashrateEMATau {
			recentCumulative = cumulativeHashrateEstimateFromDifficultySum(snap.RetargetWindowDifficulty, snap.RetargetWindowStart, now)
		}
	}
	hashRate = blendDisplayHashrate(stats, mc.connectedAt, now, hashRate, lifetimeCumulative, recentCumulative, mc.cfg.HashrateCumulativeEnabled, mc.cfg.HashrateRecentCumulativeEnabled)
	modeledRate := modeledShareRatePerMinute(hashRate, diff)
	accRate := blendedShareRatePerMinute(stats, now, rawRate, modeledRate)
	conf := hashrateConfidenceLevel(stats, now, modeledRate, hashRate, mc.connectedAt)
	addr, script, valid := mc.workerWalletData(stats.Worker)
	scriptHex := ""
	if len(script) > 0 {
		scriptHex = strings.ToLower(hex.EncodeToString(script))
	}
	lastShareHash := snap.LastShareHash
	displayHash := ""
	if lastShareHash != "" {
		displayHash = shortDisplayID(lastShareHash, hashPrefix, hashSuffix)
	}
	vardiff := mc.suggestedVardiff(now, snap)
	banned := mc.isBanned(now)
	until, reason, _ := mc.banDetails()
	minerType, minerName, minerVersion := mc.minerClientInfo()
	estPingP50 := snap.PingRTTP50MS
	estPingP95 := snap.PingRTTP95MS
	if estPingP95 <= 0 {
		estPingP50 = snap.SubmitRTTP50MS
		estPingP95 = snap.SubmitRTTP95MS
	}
	if estPingP95 <= 0 && estimatedRTT > 0 {
		estPingP50 = estimatedRTT
		estPingP95 = estimatedRTT
	}
	return WorkerView{
		Name:                      name,
		DisplayName:               displayName,
		WorkerSHA256:              workerHash,
		Accepted:                  uint64(stats.Accepted),
		Rejected:                  uint64(stats.Rejected),
		BalanceSats:               0,
		WalletAddress:             addr,
		WalletScript:              scriptHex,
		MinerType:                 minerType,
		MinerName:                 minerName,
		MinerVersion:              minerVersion,
		LastShare:                 stats.LastShare,
		LastShareHash:             lastShareHash,
		DisplayLastShare:          displayHash,
		LastShareAccepted:         snap.LastShareAccepted,
		LastShareDifficulty:       snap.LastShareDifficulty,
		LastShareDetail:           snap.LastShareDetail,
		Difficulty:                diff,
		Vardiff:                   vardiff,
		RollingHashrate:           hashRate,
		LastReject:                snap.LastReject,
		Banned:                    banned,
		BannedUntil:               until,
		BanReason:                 reason,
		WindowStart:               stats.WindowStart,
		WindowAccepted:            stats.WindowAccepted,
		WindowSubmissions:         stats.WindowSubmissions,
		WindowDifficulty:          stats.WindowDifficulty,
		ShareRate:                 accRate,
		HashrateAccuracy:          hashrateAccuracySymbol(conf),
		SubmitRTTP50MS:            snap.SubmitRTTP50MS,
		SubmitRTTP95MS:            snap.SubmitRTTP95MS,
		NotifyToFirstShareMinMS:   snap.NotifyToFirstShareMinMS,
		NotifyToFirstShareMS:      snap.NotifyToFirstShareMS,
		NotifyToFirstShareP50MS:   snap.NotifyToFirstShareP50MS,
		NotifyToFirstShareP95MS:   snap.NotifyToFirstShareP95MS,
		NotifyToFirstShareSamples: snap.NotifyToFirstShareSamples,
		EstimatedPingP50MS:        estPingP50,
		EstimatedPingP95MS:        estPingP95,
		ConnectionID:              mc.connectionIDString(),
		ConnectionSeq:             atomic.LoadUint64(&mc.connectionSeq),
		ConnectedAt:               mc.connectedAt,
		WalletValidated:           valid,
	}
}

func (s *StatusServer) snapshotWorkerViews(now time.Time) []WorkerView {
	if s.registry == nil {
		return nil
	}
	conns := s.registry.Snapshot()
	views := make([]WorkerView, 0, len(conns))
	for _, mc := range conns {
		views = append(views, workerViewFromConn(mc, now))
	}
	views = mergeWorkerViewsByHash(views)
	sort.Slice(views, func(i, j int) bool {
		return views[i].LastShare.After(views[j].LastShare)
	})
	return views
}

func mergeWorkerViewsByHash(views []WorkerView) []WorkerView {
	if len(views) <= 1 {
		return views
	}
	merged := make(map[string]WorkerView, len(views))
	order := make([]string, 0, len(views))
	for _, w := range views {
		key := w.WorkerSHA256
		if key == "" {
			key = "conn:" + w.ConnectionID
		}
		current, exists := merged[key]
		if !exists {
			merged[key] = w
			order = append(order, key)
			continue
		}
		current.Accepted += w.Accepted
		current.Rejected += w.Rejected
		current.BalanceSats += w.BalanceSats
		current.RollingHashrate += w.RollingHashrate
		current.WindowAccepted += w.WindowAccepted
		current.WindowSubmissions += w.WindowSubmissions
		current.WindowDifficulty += w.WindowDifficulty
		current.ShareRate += w.ShareRate
		if w.SubmitRTTP50MS > current.SubmitRTTP50MS {
			current.SubmitRTTP50MS = w.SubmitRTTP50MS
		}
		if w.SubmitRTTP95MS > current.SubmitRTTP95MS {
			current.SubmitRTTP95MS = w.SubmitRTTP95MS
		}
		if w.NotifyToFirstShareMS > current.NotifyToFirstShareMS {
			current.NotifyToFirstShareMS = w.NotifyToFirstShareMS
		}
		if w.NotifyToFirstShareMinMS > 0 && (current.NotifyToFirstShareMinMS <= 0 || w.NotifyToFirstShareMinMS < current.NotifyToFirstShareMinMS) {
			current.NotifyToFirstShareMinMS = w.NotifyToFirstShareMinMS
		}
		if w.NotifyToFirstShareP50MS > current.NotifyToFirstShareP50MS {
			current.NotifyToFirstShareP50MS = w.NotifyToFirstShareP50MS
		}
		if w.NotifyToFirstShareP95MS > current.NotifyToFirstShareP95MS {
			current.NotifyToFirstShareP95MS = w.NotifyToFirstShareP95MS
		}
		if w.NotifyToFirstShareSamples > current.NotifyToFirstShareSamples {
			current.NotifyToFirstShareSamples = w.NotifyToFirstShareSamples
		}
		if w.EstimatedPingP50MS > current.EstimatedPingP50MS {
			current.EstimatedPingP50MS = w.EstimatedPingP50MS
		}
		if w.EstimatedPingP95MS > current.EstimatedPingP95MS {
			current.EstimatedPingP95MS = w.EstimatedPingP95MS
		}
		if w.LastShare.After(current.LastShare) {
			current.LastShare = w.LastShare
			current.LastShareHash = w.LastShareHash
			current.DisplayLastShare = w.DisplayLastShare
			current.LastShareAccepted = w.LastShareAccepted
			current.LastShareDifficulty = w.LastShareDifficulty
			current.LastShareDetail = w.LastShareDetail
			current.LastReject = w.LastReject
			current.Difficulty = w.Difficulty
			current.Vardiff = w.Vardiff
		}
		if w.Banned {
			current.Banned = true
			if w.BannedUntil.After(current.BannedUntil) {
				current.BannedUntil = w.BannedUntil
				current.BanReason = w.BanReason
			}
		}
		if current.ConnectedAt.IsZero() || (!w.ConnectedAt.IsZero() && w.ConnectedAt.Before(current.ConnectedAt)) {
			current.ConnectedAt = w.ConnectedAt
		}
		if w.ConnectionSeq > current.ConnectionSeq {
			current.ConnectionSeq = w.ConnectionSeq
		}
		merged[key] = current
	}
	out := make([]WorkerView, 0, len(order))
	for _, key := range order {
		out = append(out, merged[key])
	}
	return out
}

func (s *StatusServer) computePoolHashrate() float64 {
	if s.metrics != nil {
		return s.metrics.PoolHashrate()
	}
	if s.registry == nil {
		return 0
	}
	var total float64
	for _, mc := range s.registry.Snapshot() {
		snap := mc.snapshotShareInfo()
		if snap.RollingHashrate > 0 {
			total += snap.RollingHashrate
		}
	}
	return total
}

func (s *StatusServer) findWorkerViewByHash(hash string) (WorkerView, bool) {
	if hash == "" {
		return WorkerView{}, false
	}
	data := s.statusDataView()
	lookup := workerLookupFromStatusData(data)
	if lookup == nil {
		return WorkerView{}, false
	}
	if w, ok := lookup[hash]; ok {
		return w, true
	}
	return WorkerView{}, false
}

// findAllWorkerViewsByHash returns all individual worker views for a given hash (unmerged).
// This is useful for showing all connections for the same worker separately.
func (s *StatusServer) findAllWorkerViewsByHash(hash string, now time.Time) []WorkerView {
	if hash == "" || s.workerRegistry == nil {
		return nil
	}

	// Use the efficient lookup to get only connections for this worker
	conns := s.workerRegistry.getConnectionsByHash(hash)
	if len(conns) == 0 {
		return nil
	}

	views := make([]WorkerView, 0, len(conns))
	for _, mc := range conns {
		views = append(views, workerViewFromConn(mc, now))
	}

	return views
}

func formatHashrateValue(h float64) string {
	units := []string{"H/s", "KH/s", "MH/s", "GH/s", "TH/s", "PH/s", "EH/s"}
	unit := units[0]
	val := h
	for i := 0; i < len(units)-1 && val >= 1000; i++ {
		val /= 1000
		unit = units[i+1]
	}
	return fmt.Sprintf("%.3f %s", val, unit)
}

func formatLatencyMS(ms float64) string {
	if ms <= 0 || math.IsNaN(ms) || math.IsInf(ms, 0) {
		return "—"
	}
	if ms < 1 {
		us := math.Round(ms * 1000)
		if us < 1 {
			us = 1
		}
		return fmt.Sprintf("%.0fus", us)
	}
	if ms < 1000 {
		return fmt.Sprintf("%.0fms", math.Round(ms))
	}
	sec := ms / 1000
	if sec < 60 {
		return fmt.Sprintf("%.1fs", sec)
	}
	return fmt.Sprintf("%.1fm", sec/60)
}

func formatDiffValue(d float64) string {
	if d <= 0 || math.IsNaN(d) || math.IsInf(d, 0) {
		return "0"
	}
	if d < 1 {
		// Display small difficulties as decimals (e.g. 0.5) instead of rounding to 0.
		//
		// We intentionally truncate instead of round so values slightly below 1 don't
		// display as "1" due to formatting.
		prec := max(int(math.Ceil(-math.Log10(d)))+2, 3)
		if prec > 8 {
			prec = 8
		}
		scale := math.Pow10(prec)
		trunc := math.Trunc(d*scale) / scale
		s := strconv.FormatFloat(trunc, 'f', prec, 64)
		s = strings.TrimRight(s, "0")
		s = strings.TrimRight(s, ".")
		if s == "" || s == "0" {
			// Extremely small values may truncate to 0 at our precision cap.
			return strconv.FormatFloat(d, 'g', 3, 64)
		}
		return s
	}
	if d < 1_000_000 {
		return fmt.Sprintf("%.0f", math.Round(d))
	}
	switch {
	case d >= 1_000_000_000_000_000:
		return fmt.Sprintf("%.1fP", d/1_000_000_000_000_000.0)
	case d >= 1_000_000_000_000:
		return fmt.Sprintf("%.1fT", d/1_000_000_000_000.0)
	case d >= 1_000_000_000:
		return fmt.Sprintf("%.1fG", d/1_000_000_000.0)
	default:
		return fmt.Sprintf("%.1fM", d/1_000_000.0)
	}
}

func formatDiffDetailValue(d float64) string {
	if d <= 0 || math.IsNaN(d) || math.IsInf(d, 0) {
		return "0"
	}
	if d < 1 {
		return formatDiffValue(d)
	}
	s := strconv.FormatFloat(d, 'f', 8, 64)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	if s == "" {
		return "0"
	}
	return s
}

// buildTemplateFuncs returns the template.FuncMap used for all HTML templates.
func buildTemplateFuncs() template.FuncMap {
	return template.FuncMap{
		"humanDuration": func(d time.Duration) string {
			if d < 0 {
				return "0s"
			}
			return d.Round(time.Second).String()
		},
		"shortID": func(s string) string {
			// Shorten IDs / hashes to a stable, display-safe form.
			return shortDisplayID(s, hashPrefix, hashSuffix)
		},
		"join": func(ss []string, sep string) string {
			return strings.Join(ss, sep)
		},
		"formatHashrate": formatHashrateValue,
		"formatWorkerHashrate": func(h float64, accuracy string) string {
			if h <= 0 {
				return "—"
			}
			base := formatHashrateValue(h)
			marker := strings.TrimSpace(accuracy)
			if marker == "" || marker == "≈+" || marker == "✓" {
				return base
			}
			return marker + " " + base
		},
		"formatLatencyMS": formatLatencyMS,
		"formatWorkStartLatencyMS": func(minMS, p50MS, lastMS float64) string {
			if minMS > 0 {
				return formatLatencyMS(minMS)
			}
			if p50MS > 0 {
				return formatLatencyMS(p50MS)
			}
			return formatLatencyMS(lastMS)
		},
		"formatDiff":       formatDiffValue,
		"formatDiffDetail": formatDiffDetailValue,
		"formatTime": func(t time.Time) string {
			if t.IsZero() {
				return "—"
			}
			s := humanShortDuration(time.Since(t))
			if s == "just now" {
				return "Just now"
			}
			return s + " ago"
		},
		"formatTimeUTC": func(t time.Time) string {
			if t.IsZero() {
				return "—"
			}
			return t.UTC().Format("2006-01-02 15:04:05 UTC")
		},
		"addrPort": func(addr string) string {
			if addr == "" {
				return "—"
			}
			_, port, err := net.SplitHostPort(addr)
			if err != nil {
				return addr
			}
			return port
		},
		"formatShareRate": func(r float64) string {
			if r < 0 {
				r = 0
			}
			units := []string{"", "K", "M", "G"}
			val := r
			unit := units[0]
			for i := 0; i < len(units)-1 && val >= 1000; i++ {
				val /= 1000
				unit = units[i+1]
			}
			if unit == "" {
				return fmt.Sprintf("%.2f", val)
			}
			return fmt.Sprintf("%.2f %s", val, unit)
		},
		"formatBTCShort": func(sats int64) string {
			btc := float64(sats) / 1e8
			return fmt.Sprintf("%.8f BTC", btc)
		},
		"formatFiat": func(sats int64, price float64, currency string) string {
			if sats == 0 || price <= 0 {
				return ""
			}
			btc := float64(sats) / 1e8
			amt := btc * price
			cur := strings.ToUpper(strings.TrimSpace(currency))
			if cur == "" {
				cur = "USD"
			}
			return fmt.Sprintf("≈ %.2f %s", amt, cur)
		},
	}
}

// loadTemplates loads and parses all embedded HTML templates.
// It returns a fully configured template or an error if any template fails to load or parse.
func loadTemplates() (*template.Template, error) {
	assets, err := newUIAssetLoader()
	if err != nil {
		return nil, err
	}
	return loadTemplatesFromAssets(assets)
}

func loadTemplatesFromAssets(assets *uiAssetLoader) (*template.Template, error) {
	funcs := buildTemplateFuncs()

	templateFiles := []struct {
		name  string
		path  string
		label string
	}{
		{"layout", "layout.tmpl", "layout template"},
		{"overview", "overview.tmpl", "status template"},
		{"status_boxes", "status_boxes.tmpl", "status boxes template"},
		{"hashrate_graph", "hashrate_graph.tmpl", "hashrate graph template"},
		{"hashrate_graph_script", "hashrate_graph_script.tmpl", "hashrate graph script template"},
		{"server", "server.tmpl", "server info template"},
		{"worker_login", "worker_login.tmpl", "worker login template"},
		{"sign_in", "sign_in.tmpl", "sign in template"},
		{"saved_workers", "saved_workers.tmpl", "saved workers template"},
		{"worker_status", "worker_status.tmpl", "worker status template"},
		{"worker_wallet_search", "worker_wallet_search.tmpl", "worker wallet search template"},
		{"node", "node.tmpl", "node info template"},
		{"pool", "pool.tmpl", "pool template"},
		{"about", "about.tmpl", "about template"},
		{"help", "help.tmpl", "help template"},
		{"node_down", "node_down.tmpl", "node down template"},
		{"admin", "admin.tmpl", "admin template"},
		{"admin_miners", "admin_miners.tmpl", "admin miners template"},
		{"admin_logins", "admin_logins.tmpl", "admin logins template"},
		{"admin_bans", "admin_bans.tmpl", "admin bans template"},
		{"admin_operator", "admin_operator.tmpl", "admin operator template"},
		{"admin_config", "admin_config.tmpl", "admin config template"},
		{"admin_logs", "admin_logs.tmpl", "admin logs template"},
		{"error", "error.tmpl", "error template"},
	}

	tmpl := template.New("layout").Funcs(funcs)
	for _, item := range templateFiles {
		payload, err := assets.readTemplate(item.path)
		if err != nil {
			return nil, fmt.Errorf("load %s: %w", item.label, err)
		}
		if item.name == "layout" {
			if _, err := tmpl.Parse(string(payload)); err != nil {
				return nil, fmt.Errorf("parse %s: %w", item.label, err)
			}
			continue
		}
		if _, err := tmpl.New(item.name).Parse(string(payload)); err != nil {
			return nil, fmt.Errorf("parse %s: %w", item.label, err)
		}
	}

	return tmpl, nil
}

func NewStatusServer(ctx context.Context, jobMgr *JobManager, metrics *PoolMetrics, registry *MinerRegistry, workerRegistry *workerConnectionRegistry, accounting *AccountStore, rpc *RPCClient, cfg Config, start time.Time, clerk *ClerkVerifier, workerLists *workerListStore, configPath, adminConfigPath string, shutdown func()) *StatusServer {
	tmpl, err := loadTemplates()
	if err != nil {
		fatal("load templates", err)
	}

	if ctx == nil {
		ctx = context.Background()
	}

	server := &StatusServer{
		tmpl:                tmpl,
		jobMgr:              jobMgr,
		metrics:             metrics,
		registry:            registry,
		workerRegistry:      workerRegistry,
		accounting:          accounting,
		rpc:                 rpc,
		ctx:                 ctx,
		start:               start,
		clerk:               clerk,
		workerLookupLimiter: newWorkerLookupRateLimiter(workerLookupRateLimitMax, workerLookupRateLimitWindow),
		workerLists:         workerLists,
		priceSvc:            NewPriceService(),
		jsonCache:           make(map[string]cachedJSONResponse),
		poolHashrateHistory: make([]poolHashrateHistorySample, 0, int(poolHashrateHistoryWindow/poolHashrateTTL)+1),
		savedWorkerPeriods:  make(map[string]*savedWorkerPeriodRing),
		configPath:          configPath,
		adminConfigPath:     adminConfigPath,
		adminSessions:       make(map[string]time.Time),
		requestShutdown:     shutdown,
	}
	server.UpdateConfig(cfg)
	if n, err := server.loadSavedWorkerPeriodsSnapshot(); err != nil {
		logger.Warn("load saved worker period history snapshot", "error", err)
	} else if n > 0 {
		logger.Info("loaded saved worker period history snapshot", "workers", n, "path", server.savedWorkerPeriodsSnapshotPath())
	}
	server.scheduleNodeInfoRefresh()
	go server.runSavedWorkerPeriodsSnapshotFlusher(ctx)
	go server.runSavedWorkerPeriodSampler(ctx)
	return server
}

func (s *StatusServer) executeTemplate(w io.Writer, name string, data any) error {
	if s == nil {
		return fmt.Errorf("status server is nil")
	}
	s.tmplMu.RLock()
	tmpl := s.tmpl
	s.tmplMu.RUnlock()
	if tmpl == nil {
		return fmt.Errorf("templates not initialized")
	}
	return tmpl.ExecuteTemplate(w, name, data)
}

// ReloadTemplates reloads the embedded HTML templates and clears cached pages.
func (s *StatusServer) ReloadTemplates() error {
	if s == nil {
		return fmt.Errorf("status server is nil")
	}

	tmpl, err := loadTemplates()
	if err != nil {
		return err
	}

	// Atomically replace the template
	s.tmplMu.Lock()
	s.tmpl = tmpl
	s.tmplMu.Unlock()
	s.clearPageCache()
	logger.Info("embedded templates reloaded successfully")
	return nil
}

// handleRPCResult is registered as an RPCClient result hook to opportunistically
// warm cached node info based on normal RPC traffic. It never changes how
// callers use the RPC client; it only updates StatusServer's own cache.
func (s *StatusServer) handleRPCResult(method string, params any, raw stdjson.RawMessage) {
	if s == nil {
		return
	}

	switch method {
	case "getblockchaininfo":
		var bc struct {
			Chain                string  `json:"chain"`
			Blocks               int64   `json:"blocks"`
			Headers              int64   `json:"headers"`
			InitialBlockDownload bool    `json:"initialblockdownload"`
			Pruned               bool    `json:"pruned"`
			SizeOnDisk           float64 `json:"size_on_disk"`
		}
		if err := sonic.Unmarshal(raw, &bc); err != nil {
			return
		}
		s.nodeInfoMu.Lock()
		defer s.nodeInfoMu.Unlock()
		now := time.Now()
		if s.nodeInfo.fetchedAt.IsZero() || now.Sub(s.nodeInfo.fetchedAt) >= nodeInfoTTL {
			var info cachedNodeInfo = s.nodeInfo
			chain := strings.ToLower(strings.TrimSpace(bc.Chain))
			switch chain {
			case "main", "mainnet", "":
				info.network = "mainnet"
			case "test", "testnet", "testnet3", "testnet4":
				info.network = "testnet"
			case "signet":
				info.network = "signet"
			case "regtest":
				info.network = "regtest"
			default:
				info.network = bc.Chain
			}
			info.blocks = bc.Blocks
			info.headers = bc.Headers
			info.ibd = bc.InitialBlockDownload
			info.pruned = bc.Pruned
			if bc.SizeOnDisk > 0 {
				info.sizeOnDisk = uint64(bc.SizeOnDisk)
			}
			info.fetchedAt = now
			s.nodeInfo = info
		}
	case "getnetworkinfo":
		var netInfo struct {
			Subversion     string `json:"subversion"`
			Connections    int    `json:"connections"`
			ConnectionsIn  int    `json:"connections_in"`
			ConnectionsOut int    `json:"connections_out"`
		}
		if err := sonic.Unmarshal(raw, &netInfo); err != nil {
			return
		}
		s.nodeInfoMu.Lock()
		defer s.nodeInfoMu.Unlock()
		now := time.Now()
		if s.nodeInfo.fetchedAt.IsZero() || now.Sub(s.nodeInfo.fetchedAt) >= nodeInfoTTL {
			var info cachedNodeInfo = s.nodeInfo
			info.subversion = strings.TrimSpace(netInfo.Subversion)
			info.conns = netInfo.Connections
			info.connsIn = netInfo.ConnectionsIn
			info.connsOut = netInfo.ConnectionsOut
			info.fetchedAt = now
			s.nodeInfo = info
		}
	case "getblockhash":
		// Only care about genesis hash (height 0) to avoid polluting cache
		// with unrelated getblockhash calls.
		args, ok := params.([]any)
		if !ok || len(args) != 1 {
			return
		}
		h, ok := args[0].(float64)
		if !ok || int64(h) != 0 {
			return
		}
		var genesis string
		if err := sonic.Unmarshal(raw, &genesis); err != nil {
			return
		}
		genesis = strings.TrimSpace(genesis)
		if genesis == "" {
			return
		}
		s.nodeInfoMu.Lock()
		if s.nodeInfo.genesisHash == "" {
			s.nodeInfo.genesisHash = genesis
		}
		s.nodeInfoMu.Unlock()
	case "getbestblockhash":
		var best string
		if err := sonic.Unmarshal(raw, &best); err != nil {
			return
		}
		best = strings.TrimSpace(best)
		if best == "" {
			return
		}
		s.nodeInfoMu.Lock()
		s.nodeInfo.bestHash = best
		s.nodeInfoMu.Unlock()
	}
}

// SetJobManager attaches a JobManager after the status server has started.
