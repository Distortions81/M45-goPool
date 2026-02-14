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
	"os"
	"path/filepath"
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

func blendDisplayHashrate(stats MinerStats, connectedAt, now time.Time, ema, cumulative float64) float64 {
	if ema <= 0 {
		return cumulative
	}
	if cumulative <= 0 {
		return ema
	}
	// Favor cumulative estimator as samples grow: it is more accurate over
	// longer horizons and less sensitive to vardiff/window resets.
	w := float64(stats.Accepted) / 16.0
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
	targetWindow := time.Duration((3.0 / modeledRate) * float64(time.Minute))
	if targetWindow < 30*time.Second {
		targetWindow = 30 * time.Second
	}
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
	minCumulativeAccepted = int64(math.Ceil(expectedInWindow))
	if minCumulativeAccepted < 3 {
		minCumulativeAccepted = 3
	}
	if minCumulativeAccepted > 8 {
		minCumulativeAccepted = 8
	}
	minConnected = targetWindow + 30*time.Second
	if minConnected < 60*time.Second {
		minConnected = 60 * time.Second
	}
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
	highWindow := minWindow * 2
	if highWindow < 90*time.Second {
		highWindow = 90 * time.Second
	}
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
	highCum := minCumulativeAccepted * 2
	if highCum < 8 {
		highCum = 8
	}
	if highCum > 24 {
		highCum = 24
	}
	highConn := minConnected + 2*time.Minute
	if highConn < 3*time.Minute {
		highConn = 3 * time.Minute
	}
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
	case 2:
		return "≈+"
	default:
		return "✓"
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
	hashRate = blendDisplayHashrate(stats, mc.connectedAt, now, hashRate, cumulativeHashrateEstimate(stats, mc.connectedAt, now))
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
	units := []string{"H/s", "KH/s", "MH/s", "GH/s", "TH/s", "PH/s"}
	unit := units[0]
	val := h
	for i := 0; i < len(units)-1 && val >= 1000; i++ {
		val /= 1000
		unit = units[i+1]
	}
	return fmt.Sprintf("%.3f %s", val, unit)
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
		"formatDiff": func(d float64) string {
			if d <= 0 {
				return "0"
			}
			if d < 1 {
				// Display small difficulties as decimals (e.g. 0.5) instead of rounding to 0.
				//
				// We intentionally truncate instead of round so values slightly below 1 don't
				// display as "1" due to formatting.
				prec := int(math.Ceil(-math.Log10(d))) + 2
				if prec < 3 {
					prec = 3
				}
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
			case d >= 1_000_000_000_000:
				return fmt.Sprintf("%.1fP", d/1_000_000_000_000.0)
			case d >= 1_000_000_000:
				return fmt.Sprintf("%.1fG", d/1_000_000_000.0)
			default:
				return fmt.Sprintf("%.1fM", d/1_000_000.0)
			}
		},
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

// loadTemplates loads and parses all HTML templates from the specified data directory.
// It returns a fully configured template or an error if any template fails to load or parse.
func loadTemplates(dataDir string) (*template.Template, error) {
	funcs := buildTemplateFuncs()

	// Build template paths
	layoutPath := filepath.Join(dataDir, "templates", "layout.tmpl")
	statusPath := filepath.Join(dataDir, "templates", "overview.tmpl")
	statusBoxesPath := filepath.Join(dataDir, "templates", "status_boxes.tmpl")
	hashrateGraphPath := filepath.Join(dataDir, "templates", "hashrate_graph.tmpl")
	hashrateGraphScriptPath := filepath.Join(dataDir, "templates", "hashrate_graph_script.tmpl")
	serverInfoPath := filepath.Join(dataDir, "templates", "server.tmpl")
	workerLoginPath := filepath.Join(dataDir, "templates", "worker_login.tmpl")
	signInPath := filepath.Join(dataDir, "templates", "sign_in.tmpl")
	savedWorkersPath := filepath.Join(dataDir, "templates", "saved_workers.tmpl")
	workerStatusPath := filepath.Join(dataDir, "templates", "worker_status.tmpl")
	workerWalletSearchPath := filepath.Join(dataDir, "templates", "worker_wallet_search.tmpl")
	nodeInfoPath := filepath.Join(dataDir, "templates", "node.tmpl")
	poolInfoPath := filepath.Join(dataDir, "templates", "pool.tmpl")
	aboutPath := filepath.Join(dataDir, "templates", "about.tmpl")
	helpPath := filepath.Join(dataDir, "templates", "help.tmpl")
	adminPath := filepath.Join(dataDir, "templates", "admin.tmpl")
	adminMinersPath := filepath.Join(dataDir, "templates", "admin_miners.tmpl")
	adminLoginsPath := filepath.Join(dataDir, "templates", "admin_logins.tmpl")
	adminBansPath := filepath.Join(dataDir, "templates", "admin_bans.tmpl")
	adminOperatorPath := filepath.Join(dataDir, "templates", "admin_operator.tmpl")
	adminLogsPath := filepath.Join(dataDir, "templates", "admin_logs.tmpl")
	errorPath := filepath.Join(dataDir, "templates", "error.tmpl")

	// Load template files
	layoutHTML, err := os.ReadFile(layoutPath)
	if err != nil {
		return nil, fmt.Errorf("load layout template: %w", err)
	}
	statusHTML, err := os.ReadFile(statusPath)
	if err != nil {
		return nil, fmt.Errorf("load status template: %w", err)
	}
	statusBoxesHTML, err := os.ReadFile(statusBoxesPath)
	if err != nil {
		return nil, fmt.Errorf("load status boxes template: %w", err)
	}
	hashrateGraphHTML, err := os.ReadFile(hashrateGraphPath)
	if err != nil {
		return nil, fmt.Errorf("load hashrate graph template: %w", err)
	}
	hashrateGraphScriptHTML, err := os.ReadFile(hashrateGraphScriptPath)
	if err != nil {
		return nil, fmt.Errorf("load hashrate graph script template: %w", err)
	}
	serverInfoHTML, err := os.ReadFile(serverInfoPath)
	if err != nil {
		return nil, fmt.Errorf("load server info template: %w", err)
	}
	workerLoginHTML, err := os.ReadFile(workerLoginPath)
	if err != nil {
		return nil, fmt.Errorf("load worker login template: %w", err)
	}
	signInHTML, err := os.ReadFile(signInPath)
	if err != nil {
		return nil, fmt.Errorf("load sign in template: %w", err)
	}
	savedWorkersHTML, err := os.ReadFile(savedWorkersPath)
	if err != nil {
		return nil, fmt.Errorf("load saved workers template: %w", err)
	}
	workerStatusHTML, err := os.ReadFile(workerStatusPath)
	if err != nil {
		return nil, fmt.Errorf("load worker status template: %w", err)
	}
	workerWalletSearchHTML, err := os.ReadFile(workerWalletSearchPath)
	if err != nil {
		return nil, fmt.Errorf("load worker wallet search template: %w", err)
	}
	nodeInfoHTML, err := os.ReadFile(nodeInfoPath)
	if err != nil {
		return nil, fmt.Errorf("load node info template: %w", err)
	}
	poolInfoHTML, err := os.ReadFile(poolInfoPath)
	if err != nil {
		return nil, fmt.Errorf("load pool info template: %w", err)
	}
	aboutHTML, err := os.ReadFile(aboutPath)
	if err != nil {
		return nil, fmt.Errorf("load about template: %w", err)
	}
	helpHTML, err := os.ReadFile(helpPath)
	if err != nil {
		return nil, fmt.Errorf("load help template: %w", err)
	}
	adminHTML, err := os.ReadFile(adminPath)
	if err != nil {
		return nil, fmt.Errorf("load admin template: %w", err)
	}
	adminMinersHTML, err := os.ReadFile(adminMinersPath)
	if err != nil {
		return nil, fmt.Errorf("load admin miners template: %w", err)
	}
	adminLoginsHTML, err := os.ReadFile(adminLoginsPath)
	if err != nil {
		return nil, fmt.Errorf("load admin logins template: %w", err)
	}
	adminBansHTML, err := os.ReadFile(adminBansPath)
	if err != nil {
		return nil, fmt.Errorf("load admin bans template: %w", err)
	}
	adminOperatorHTML, err := os.ReadFile(adminOperatorPath)
	if err != nil {
		return nil, fmt.Errorf("load admin operator template: %w", err)
	}
	adminLogsHTML, err := os.ReadFile(adminLogsPath)
	if err != nil {
		return nil, fmt.Errorf("load admin logs template: %w", err)
	}
	errorHTML, err := os.ReadFile(errorPath)
	if err != nil {
		return nil, fmt.Errorf("load error template: %w", err)
	}

	// Parse templates
	tmpl := template.New("layout").Funcs(funcs)
	if _, err := tmpl.Parse(string(layoutHTML)); err != nil {
		return nil, fmt.Errorf("parse layout template: %w", err)
	}
	if _, err := tmpl.New("overview").Parse(string(statusHTML)); err != nil {
		return nil, fmt.Errorf("parse status template: %w", err)
	}
	if _, err := tmpl.New("status_boxes").Parse(string(statusBoxesHTML)); err != nil {
		return nil, fmt.Errorf("parse status boxes template: %w", err)
	}
	if _, err := tmpl.New("hashrate_graph").Parse(string(hashrateGraphHTML)); err != nil {
		return nil, fmt.Errorf("parse hashrate graph template: %w", err)
	}
	if _, err := tmpl.New("hashrate_graph_script").Parse(string(hashrateGraphScriptHTML)); err != nil {
		return nil, fmt.Errorf("parse hashrate graph script template: %w", err)
	}
	if _, err := tmpl.New("server").Parse(string(serverInfoHTML)); err != nil {
		return nil, fmt.Errorf("parse server info template: %w", err)
	}
	if _, err := tmpl.New("worker_login").Parse(string(workerLoginHTML)); err != nil {
		return nil, fmt.Errorf("parse worker login template: %w", err)
	}
	if _, err := tmpl.New("sign_in").Parse(string(signInHTML)); err != nil {
		return nil, fmt.Errorf("parse sign in template: %w", err)
	}
	if _, err := tmpl.New("saved_workers").Parse(string(savedWorkersHTML)); err != nil {
		return nil, fmt.Errorf("parse saved workers template: %w", err)
	}
	if _, err := tmpl.New("worker_status").Parse(string(workerStatusHTML)); err != nil {
		return nil, fmt.Errorf("parse worker status template: %w", err)
	}
	if _, err := tmpl.New("worker_wallet_search").Parse(string(workerWalletSearchHTML)); err != nil {
		return nil, fmt.Errorf("parse worker wallet search template: %w", err)
	}
	if _, err := tmpl.New("node").Parse(string(nodeInfoHTML)); err != nil {
		return nil, fmt.Errorf("parse node info template: %w", err)
	}
	if _, err := tmpl.New("pool").Parse(string(poolInfoHTML)); err != nil {
		return nil, fmt.Errorf("parse pool template: %w", err)
	}
	if _, err := tmpl.New("about").Parse(string(aboutHTML)); err != nil {
		return nil, fmt.Errorf("parse about template: %w", err)
	}
	if _, err := tmpl.New("help").Parse(string(helpHTML)); err != nil {
		return nil, fmt.Errorf("parse help template: %w", err)
	}
	if _, err := tmpl.New("admin").Parse(string(adminHTML)); err != nil {
		return nil, fmt.Errorf("parse admin template: %w", err)
	}
	if _, err := tmpl.New("admin_miners").Parse(string(adminMinersHTML)); err != nil {
		return nil, fmt.Errorf("parse admin miners template: %w", err)
	}
	if _, err := tmpl.New("admin_logins").Parse(string(adminLoginsHTML)); err != nil {
		return nil, fmt.Errorf("parse admin logins template: %w", err)
	}
	if _, err := tmpl.New("admin_bans").Parse(string(adminBansHTML)); err != nil {
		return nil, fmt.Errorf("parse admin bans template: %w", err)
	}
	if _, err := tmpl.New("admin_operator").Parse(string(adminOperatorHTML)); err != nil {
		return nil, fmt.Errorf("parse admin operator template: %w", err)
	}
	if _, err := tmpl.New("admin_logs").Parse(string(adminLogsHTML)); err != nil {
		return nil, fmt.Errorf("parse admin logs template: %w", err)
	}
	if _, err := tmpl.New("error").Parse(string(errorHTML)); err != nil {
		return nil, fmt.Errorf("parse error template: %w", err)
	}

	return tmpl, nil
}

func NewStatusServer(ctx context.Context, jobMgr *JobManager, metrics *PoolMetrics, registry *MinerRegistry, workerRegistry *workerConnectionRegistry, accounting *AccountStore, rpc *RPCClient, cfg Config, start time.Time, clerk *ClerkVerifier, workerLists *workerListStore, configPath, adminConfigPath, tuningPath string, shutdown func()) *StatusServer {
	// Load HTML templates from data_dir/templates so operators can customize the
	// UI without recompiling. These are treated as required assets.
	tmpl, err := loadTemplates(cfg.DataDir)
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
		configPath:          configPath,
		adminConfigPath:     adminConfigPath,
		tuningPath:          tuningPath,
		adminSessions:       make(map[string]time.Time),
		requestShutdown:     shutdown,
	}
	server.UpdateConfig(cfg)
	server.scheduleNodeInfoRefresh()
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

// ReloadTemplates reloads all HTML templates from disk. This allows operators
// to update templates without restarting the pool server. It's designed to be
// called in response to SIGUSR1 or other reload triggers.
func (s *StatusServer) ReloadTemplates() error {
	if s == nil {
		return fmt.Errorf("status server is nil")
	}

	tmpl, err := loadTemplates(s.Config().DataDir)
	if err != nil {
		return err
	}

	// Atomically replace the template
	s.tmplMu.Lock()
	s.tmpl = tmpl
	s.tmplMu.Unlock()
	s.clearPageCache()
	logger.Info("templates reloaded successfully")
	return nil
}

// handleRPCResult is registered as an RPCClient result hook to opportunistically
// warm cached node info based on normal RPC traffic. It never changes how
// callers use the RPC client; it only updates StatusServer's own cache.
func (s *StatusServer) handleRPCResult(method string, params interface{}, raw stdjson.RawMessage) {
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
		args, ok := params.([]interface{})
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
