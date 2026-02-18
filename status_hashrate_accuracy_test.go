package main

import (
	"testing"
	"time"
)

func TestCumulativeHashrateEstimate(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	connectedAt := now.Add(-2 * time.Minute)
	targetHashrate := 1.2e12
	totalDiff := (targetHashrate * now.Sub(connectedAt).Seconds()) / hashPerShare

	got := cumulativeHashrateEstimate(MinerStats{TotalDifficulty: totalDiff}, connectedAt, now)
	if got < targetHashrate*0.98 || got > targetHashrate*1.02 {
		t.Fatalf("got %.6g want around %.6g", got, targetHashrate)
	}
}

func TestBlendDisplayHashrateMovesTowardCumulativeWithEvidence(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	connectedAt := now.Add(-5 * time.Minute)
	stats := MinerStats{Accepted: 96}
	ema := 1.0e12
	cumulative := 1.2e12
	got := blendDisplayHashrate(stats, connectedAt, now, ema, cumulative, 0, true, true)
	if got < 1.15e12 || got > 1.2e12 {
		t.Fatalf("got %.6g want closer to cumulative %.6g than ema %.6g", got, cumulative, ema)
	}
}
