package main

import (
	"testing"
	"time"
)

func TestHasReliableRateEstimate(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	if hasReliableRateEstimate(MinerStats{}, now, 7, now.Add(-2*time.Minute)) {
		t.Fatalf("expected false with zero window")
	}
	if hasReliableRateEstimate(MinerStats{WindowStart: now.Add(-20 * time.Second), WindowAccepted: 10}, now, 7, now.Add(-2*time.Minute)) {
		t.Fatalf("expected false with short window")
	}
	if hasReliableRateEstimate(MinerStats{WindowStart: now.Add(-time.Minute), WindowAccepted: 1}, now, 7, now.Add(-30*time.Second)) {
		t.Fatalf("expected false with low evidence")
	}
	if !hasReliableRateEstimate(MinerStats{WindowStart: now.Add(-time.Minute), WindowAccepted: 8}, now, 7, now.Add(-2*time.Minute)) {
		t.Fatalf("expected true with sufficient evidence")
	}
	// Frequent vardiff resets can shorten WindowStart; cumulative evidence
	// should still allow display after enough accepted shares and runtime.
	if !hasReliableRateEstimate(MinerStats{Accepted: 7}, now, 7, now.Add(-2*time.Minute)) {
		t.Fatalf("expected true with cumulative evidence fallback")
	}
}

func TestReliabilityThresholdsAreBoundedAndAdaptive(t *testing.T) {
	wFast, eFast, cFast, dFast := reliabilityThresholds(30)
	wSlow, eSlow, cSlow, dSlow := reliabilityThresholds(0.5)

	if wFast < 30*time.Second || wFast > 2*time.Minute || wSlow < 30*time.Second || wSlow > 2*time.Minute {
		t.Fatalf("window bounds violated: fast=%s slow=%s", wFast, wSlow)
	}
	if eFast < 2.5 || eFast > 6 || eSlow < 2.5 || eSlow > 6 {
		t.Fatalf("evidence bounds violated: fast=%.2f slow=%.2f", eFast, eSlow)
	}
	if cFast < 3 || cFast > 8 || cSlow < 3 || cSlow > 8 {
		t.Fatalf("cumulative bounds violated: fast=%d slow=%d", cFast, cSlow)
	}
	if dFast < 60*time.Second || dFast > 4*time.Minute || dSlow < 60*time.Second || dSlow > 4*time.Minute {
		t.Fatalf("connected bounds violated: fast=%s slow=%s", dFast, dSlow)
	}
	if !(wFast <= wSlow) {
		t.Fatalf("expected faster cadence to need no more window time: fast=%s slow=%s", wFast, wSlow)
	}
}

func TestHashrateConfidenceLevel_DemotesLargeMismatch(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	modeledRate := 10.0 // shares/min
	connectedAt := now.Add(-4 * time.Minute)
	stats := MinerStats{
		WindowStart:    now.Add(-2 * time.Minute),
		WindowAccepted: 15, // 25% below modeled expectation (20)
		Accepted:       40,
	}

	estimatedHashrate := 10.0 * hashPerShare / 60.0
	if got := hashrateConfidenceLevel(stats, now, modeledRate, estimatedHashrate, connectedAt); got != 0 {
		t.Fatalf("confidence=%d want 0 for >=25%% mismatch", got)
	}
}

func TestHashrateConfidenceLevel_RequiresTighterAgreementForStable(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	modeledRate := 10.0 // shares/min
	connectedAt := now.Add(-4 * time.Minute)
	stats := MinerStats{
		WindowStart:    now.Add(-2 * time.Minute),
		WindowAccepted: 16, // 20% below modeled expectation (20)
		Accepted:       40,
	}

	estimatedHashrate := 10.0 * hashPerShare / 60.0
	if got := hashrateConfidenceLevel(stats, now, modeledRate, estimatedHashrate, connectedAt); got != 1 {
		t.Fatalf("confidence=%d want 1 when settling threshold passes but stable threshold fails", got)
	}
}

func TestHashrateConfidenceLevel_DemotesWhenCumulativeDisagrees(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	connectedAt := now.Add(-10 * time.Minute)
	modeledRate := 10.0 // shares/min
	estimatedHashrate := (modeledRate * hashPerShare) / 60.0
	// Cumulative implies 7.5 shares/min (25% lower), so the estimator should not
	// be treated as settled when enough long-horizon evidence exists.
	totalDiff := 7.5 * now.Sub(connectedAt).Minutes()
	stats := MinerStats{
		WindowStart:     now.Add(-2 * time.Minute),
		WindowAccepted:  20,
		Accepted:        60,
		TotalDifficulty: totalDiff,
	}

	if got := hashrateConfidenceLevel(stats, now, modeledRate, estimatedHashrate, connectedAt); got != 0 {
		t.Fatalf("confidence=%d want 0 when cumulative disagrees by settling threshold", got)
	}
}

func TestHashrateConfidenceLevel_AllowsCumulativeSettlingWhenWindowTooShort(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	connectedAt := now.Add(-8 * time.Minute)
	modeledRate := 2.0 // shares/min
	estimatedHashrate := (modeledRate * hashPerShare) / 60.0
	stats := MinerStats{
		WindowStart:     now.Add(-30 * time.Second),
		WindowAccepted:  1, // expected shares too low for settling window threshold
		Accepted:        16,
		TotalDifficulty: modeledRate * now.Sub(connectedAt).Minutes(),
	}

	if got := hashrateConfidenceLevel(stats, now, modeledRate, estimatedHashrate, connectedAt); got != 1 {
		t.Fatalf("confidence=%d want 1 when cumulative evidence settles a short window", got)
	}
}

func TestHashrateConfidenceLevel_VeryStableTier(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	connectedAt := now.Add(-30 * time.Minute)
	modeledRate := 10.0 // shares/min
	estimatedHashrate := (modeledRate * hashPerShare) / 60.0
	stats := MinerStats{
		WindowStart:     now.Add(-10 * time.Minute),
		WindowAccepted:  100,
		Accepted:        120,
		TotalDifficulty: modeledRate * now.Sub(connectedAt).Minutes(),
	}

	if got := hashrateConfidenceLevel(stats, now, modeledRate, estimatedHashrate, connectedAt); got != 3 {
		t.Fatalf("confidence=%d want 3 for very stable evidence and agreement", got)
	}
}

func TestHashrateAccuracySymbol_MultiLevel(t *testing.T) {
	if got := hashrateAccuracySymbol(0); got != "~" {
		t.Fatalf("symbol=%q want %q for level 0", got, "~")
	}
	if got := hashrateAccuracySymbol(1); got != "≈" {
		t.Fatalf("symbol=%q want %q for level 1", got, "≈")
	}
	if got := hashrateAccuracySymbol(2); got != "≈+" {
		t.Fatalf("symbol=%q want %q for level 2", got, "≈+")
	}
	if got := hashrateAccuracySymbol(3); got != "✓" {
		t.Fatalf("symbol=%q want %q for level 3", got, "✓")
	}
}
