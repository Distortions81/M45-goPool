package main

import (
	"testing"
	"time"
)

func TestSuggestedVardiff_FirstTwoAdjustmentsUseTwoSteps(t *testing.T) {
	now := time.Unix(1700000000, 0)
	mc := &MinerConn{
		cfg: Config{},
		vardiff: VarDiffConfig{
			MinDiff:            1,
			MaxDiff:            1024,
			TargetSharesPerMin: 1,
			AdjustmentWindow:   10 * time.Second,
			Step:               2,
			DampingFactor:      1,
		},
	}
	atomicStoreFloat64(&mc.difficulty, 1)

	snap := minerShareSnapshot{
		Stats: MinerStats{
			WindowStart:       now.Add(-time.Minute),
			WindowAccepted:    10,
			WindowSubmissions: 10,
		},
		RollingHashrate: hashPerShare,
	}

	tests := []struct {
		name      string
		adjustCnt int32
		want      float64
	}{
		{name: "first adjustment", adjustCnt: 0, want: 4},
		{name: "second adjustment", adjustCnt: 1, want: 4},
		{name: "third adjustment", adjustCnt: 2, want: 2},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mc.vardiffAdjustments.Store(tc.adjustCnt)
			got := mc.suggestedVardiff(now, snap)
			if got != tc.want {
				t.Fatalf("adjustCnt=%d got %.8g want %.8g", tc.adjustCnt, got, tc.want)
			}
		})
	}
}

func TestSuggestedVardiff_UsesAdjustmentWindowAfterBootstrap(t *testing.T) {
	now := time.Unix(1700000000, 0)
	mc := &MinerConn{
		cfg: Config{
			HashrateEMATauSeconds: 120,
		},
		vardiff: VarDiffConfig{
			MinDiff:            1,
			MaxDiff:            1024,
			TargetSharesPerMin: 1,
			AdjustmentWindow:   90 * time.Second,
			Step:               2,
			DampingFactor:      1,
		},
	}
	atomicStoreFloat64(&mc.difficulty, 1)

	snap := minerShareSnapshot{
		Stats: MinerStats{
			WindowStart:       now.Add(-3 * time.Minute),
			WindowAccepted:    10,
			WindowSubmissions: 10,
		},
		RollingHashrate: hashPerShare,
	}

	mc.initialEMAWindowDone.Store(true)
	mc.lastDiffChange.Store(now.Add(-60 * time.Second).UnixNano())
	if got := mc.suggestedVardiff(now, snap); got != 1 {
		t.Fatalf("got %.8g want %.8g while vardiff adjustment window has not elapsed", got, 1.0)
	}

	mc.lastDiffChange.Store(now.Add(-90 * time.Second).UnixNano())
	if got := mc.suggestedVardiff(now, snap); got != 4 {
		t.Fatalf("got %.8g want %.8g once vardiff adjustment window elapsed", got, 4.0)
	}
}

func TestSuggestedVardiff_UsesBootstrap30SecondIntervalFirst(t *testing.T) {
	now := time.Unix(1700000000, 0)
	mc := &MinerConn{
		cfg: Config{
			HashrateEMATauSeconds: 120,
		},
		vardiff: VarDiffConfig{
			MinDiff:            1,
			MaxDiff:            1024,
			TargetSharesPerMin: 1,
			AdjustmentWindow:   10 * time.Second,
			Step:               2,
			DampingFactor:      1,
		},
	}
	atomicStoreFloat64(&mc.difficulty, 1)

	snap := minerShareSnapshot{
		Stats: MinerStats{
			WindowStart:       now.Add(-3 * time.Minute),
			WindowAccepted:    10,
			WindowSubmissions: 10,
		},
		RollingHashrate: hashPerShare,
	}

	bootstrapInterval := initialHashrateEMATau
	mc.lastDiffChange.Store(now.Add(-bootstrapInterval + time.Second).UnixNano())
	if got := mc.suggestedVardiff(now, snap); got != 1 {
		t.Fatalf("got %.8g want %.8g before bootstrap interval", got, 1.0)
	}

	mc.lastDiffChange.Store(now.Add(-bootstrapInterval).UnixNano())
	if got := mc.suggestedVardiff(now, snap); got != 4 {
		t.Fatalf("got %.8g want %.8g once bootstrap interval elapsed", got, 4.0)
	}
}

func TestSuggestedVardiff_BootstrapIntervalAnchorsToFirstShareAfterDiffChange(t *testing.T) {
	now := time.Unix(1700000000, 0)
	firstShare := now.Add(-20 * time.Second)
	mc := &MinerConn{
		cfg: Config{
			HashrateEMATauSeconds: 120,
		},
		vardiff: VarDiffConfig{
			MinDiff:            1,
			MaxDiff:            1024,
			TargetSharesPerMin: 1,
			AdjustmentWindow:   10 * time.Second,
			Step:               2,
			DampingFactor:      1,
		},
	}
	atomicStoreFloat64(&mc.difficulty, 1)

	snap := minerShareSnapshot{
		Stats: MinerStats{
			WindowStart:       firstShare,
			WindowAccepted:    10,
			WindowSubmissions: 10,
		},
		RollingHashrate: hashPerShare,
	}

	// Diff changed long enough ago, but first post-change share arrived recently.
	// Bootstrap should wait full interval from the first sampled share.
	mc.lastDiffChange.Store(now.Add(-2 * initialHashrateEMATau).UnixNano())
	if got := mc.suggestedVardiff(now, snap); got != 1 {
		t.Fatalf("got %.8g want %.8g before bootstrap interval from first share", got, 1.0)
	}

	if got := mc.suggestedVardiff(firstShare.Add(initialHashrateEMATau), snap); got != 4 {
		t.Fatalf("got %.8g want %.8g once bootstrap interval from first share elapsed", got, 4.0)
	}
}

func TestSuggestedVardiff_BootstrapAlsoRespectsAdjustmentWindow(t *testing.T) {
	now := time.Unix(1700000000, 0)
	firstShare := now.Add(-50 * time.Second)
	mc := &MinerConn{
		cfg: Config{
			HashrateEMATauSeconds: 120,
		},
		vardiff: VarDiffConfig{
			MinDiff:            1,
			MaxDiff:            1024,
			TargetSharesPerMin: 1,
			AdjustmentWindow:   90 * time.Second,
			Step:               2,
			DampingFactor:      1,
		},
	}
	atomicStoreFloat64(&mc.difficulty, 1)

	snap := minerShareSnapshot{
		Stats: MinerStats{
			WindowStart:       firstShare,
			WindowAccepted:    10,
			WindowSubmissions: 10,
		},
		RollingHashrate: hashPerShare,
	}

	// Diff changed long ago, first share is 50s ago:
	// - bootstrap tau (45s) has elapsed
	// - adjustment window (90s) has not
	mc.lastDiffChange.Store(now.Add(-5 * time.Minute).UnixNano())
	if got := mc.suggestedVardiff(now, snap); got != 1 {
		t.Fatalf("got %.8g want %.8g before adjustment window elapsed during bootstrap", got, 1.0)
	}

	if got := mc.suggestedVardiff(firstShare.Add(90*time.Second), snap); got != 4 {
		t.Fatalf("got %.8g want %.8g once adjustment window elapsed during bootstrap", got, 4.0)
	}
}

func TestSuggestedVardiff_UsesWindowDifficultyWhenRollingIsZero(t *testing.T) {
	now := time.Unix(1700000000, 0)
	mc := &MinerConn{
		cfg: Config{},
		vardiff: VarDiffConfig{
			MinDiff:            1,
			MaxDiff:            1024,
			TargetSharesPerMin: 1,
			Step:               2,
			DampingFactor:      1,
		},
	}
	atomicStoreFloat64(&mc.difficulty, 1)

	snap := minerShareSnapshot{
		Stats: MinerStats{
			WindowStart:       now.Add(-time.Minute),
			WindowAccepted:    10,
			WindowSubmissions: 10,
			WindowDifficulty:  60,
		},
		RollingHashrate: 0,
	}

	if got := mc.suggestedVardiff(now, snap); got != 4 {
		t.Fatalf("got %.8g want %.8g when rolling hashrate is zero but window difficulty is available", got, 4.0)
	}
}

func almostEqualFloat64(a, b, eps float64) bool {
	if a > b {
		return a-b <= eps
	}
	return b-a <= eps
}
