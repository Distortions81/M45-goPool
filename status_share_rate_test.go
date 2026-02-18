package main

import (
	"testing"
	"time"
)

func TestModeledShareRatePerMinute(t *testing.T) {
	hashrate := 1.2e12
	diff := 2457.6
	got := modeledShareRatePerMinute(hashrate, diff)
	if got < 6.8 || got > 7.2 {
		t.Fatalf("got %.4f want around 7 shares/min", got)
	}
}

func TestBlendedShareRatePerMinute_PrefersModeledAtLowSamples(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	stats := MinerStats{
		WindowStart:    now.Add(-20 * time.Second),
		WindowAccepted: 1,
	}
	got := blendedShareRatePerMinute(stats, now, 20.0, 7.0)
	if got < 6.5 || got > 9.0 {
		t.Fatalf("got %.4f want close to modeled rate under tiny sample", got)
	}
}

func TestBlendedShareRatePerMinute_PrefersRawAtHighSamples(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	stats := MinerStats{
		WindowStart:    now.Add(-3 * time.Minute),
		WindowAccepted: 40,
	}
	got := blendedShareRatePerMinute(stats, now, 8.0, 6.0)
	if got < 7.8 || got > 8.0 {
		t.Fatalf("got %.4f want close to raw rate with high sample count", got)
	}
}

func TestBlendDisplayHashrate_LongLivedConnectionFavorsCumulative(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	connectedAt := now.Add(-30 * time.Minute)
	stats := MinerStats{
		Accepted: 1,
	}
	ema := 100.0
	cumulative := 200.0
	got := blendDisplayHashrate(stats, connectedAt, now, ema, cumulative, 0, true, true)
	if got < 190.0 || got > 200.0 {
		t.Fatalf("got %.4f want close to cumulative for long-lived connection", got)
	}
}

func TestBlendDisplayHashrate_YoungConnectionStaysNearEMA(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	connectedAt := now.Add(-time.Minute)
	stats := MinerStats{
		Accepted: 1,
	}
	ema := 100.0
	cumulative := 200.0
	got := blendDisplayHashrate(stats, connectedAt, now, ema, cumulative, 0, true, true)
	if got <= 100.0 || got >= 120.0 {
		t.Fatalf("got %.4f want near EMA for newly connected worker", got)
	}
}

func TestBlendDisplayHashrate_PrefersRecentCumulativeAfterDifficultyRamp(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	connectedAt := now.Add(-30 * time.Minute)
	stats := MinerStats{
		Accepted: 200,
	}
	ema := 1.0e12
	cumulativeLifetime := 2.0e11
	cumulativeRecent := 1.02e12
	got := blendDisplayHashrate(stats, connectedAt, now, ema, cumulativeLifetime, cumulativeRecent, true, true)
	if got < 9.5e11 || got > 1.02e12 {
		t.Fatalf("got %.6g want close to recent cumulative %.6g (not lifetime %.6g)", got, cumulativeRecent, cumulativeLifetime)
	}
}
