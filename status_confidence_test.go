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

func TestHashrateAccuracySymbol(t *testing.T) {
	if got := hashrateAccuracySymbol(0); got != "~" {
		t.Fatalf("level0 got %q want %q", got, "~")
	}
	if got := hashrateAccuracySymbol(1); got != "≈" {
		t.Fatalf("level1 got %q want %q", got, "≈")
	}
	if got := hashrateAccuracySymbol(2); got != "" {
		t.Fatalf("level2 got %q want empty", got)
	}
}
