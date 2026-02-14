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
		{name: "first adjustment", adjustCnt: 0, want: 8},
		{name: "second adjustment", adjustCnt: 1, want: 8},
		{name: "third adjustment still far from target", adjustCnt: 2, want: 4},
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

func TestSuggestedVardiff_FarFromTargetBypassesDebounce(t *testing.T) {
	now := time.Unix(1700000000, 0)
	mc := &MinerConn{
		cfg: Config{},
		vardiff: VarDiffConfig{
			MinDiff:            1,
			MaxDiff:            1 << 30,
			TargetSharesPerMin: 7,
			AdjustmentWindow:   120 * time.Second,
			Step:               2,
			DampingFactor:      0.7,
		},
	}
	atomicStoreFloat64(&mc.difficulty, 1024)
	mc.initialEMAWindowDone.Store(true)
	mc.vardiffAdjustments.Store(3)
	mc.lastDiffChange.Store(now.Add(-2 * time.Minute).UnixNano())

	// Very high hashrate relative to current diff; ratio is far above 8x.
	snap := minerShareSnapshot{
		Stats: MinerStats{
			WindowStart:       now.Add(-time.Minute),
			WindowAccepted:    20,
			WindowSubmissions: 20,
		},
		RollingHashrate: 90e12,
	}

	got := mc.suggestedVardiff(now, snap)
	if got <= 1024 {
		t.Fatalf("got %.8g want > %.8g when far from target and post-bootstrap", got, 1024.0)
	}
}

func TestSuggestedVardiff_UsesSingleStepCapWhenCloserToTarget(t *testing.T) {
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
	mc.vardiffAdjustments.Store(3)

	// Use a hashrate where step cap (not share-rate safety clamp) governs the
	// move size so we can validate near-target cap behavior.
	atomicStoreFloat64(&mc.difficulty, 32)
	snap := minerShareSnapshot{
		Stats: MinerStats{
			WindowStart:       now.Add(-time.Minute),
			WindowAccepted:    10,
			WindowSubmissions: 10,
		},
		RollingHashrate: 3 * hashPerShare,
	}

	got := mc.suggestedVardiff(now, snap)
	want := 64.0
	if got != want {
		t.Fatalf("got %.8g want %.8g when closer to target after bootstrap", got, want)
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
	if got := mc.suggestedVardiff(now, snap); got != 8 {
		t.Fatalf("got %.8g want %.8g once vardiff adjustment window elapsed", got, 8.0)
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
	if got := mc.suggestedVardiff(now, snap); got != 8 {
		t.Fatalf("got %.8g want %.8g once bootstrap interval elapsed", got, 8.0)
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

	if got := mc.suggestedVardiff(firstShare.Add(initialHashrateEMATau), snap); got != 8 {
		t.Fatalf("got %.8g want %.8g once bootstrap interval from first share elapsed", got, 8.0)
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

	if got := mc.suggestedVardiff(firstShare.Add(90*time.Second), snap); got != 8 {
		t.Fatalf("got %.8g want %.8g once adjustment window elapsed during bootstrap", got, 8.0)
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
	mc.initialEMAWindowDone.Store(true)

	snap := minerShareSnapshot{
		Stats: MinerStats{
			WindowStart:       now.Add(-time.Minute),
			WindowAccepted:    10,
			WindowSubmissions: 10,
			WindowDifficulty:  60,
		},
		RollingHashrate: 0,
	}

	if got := mc.suggestedVardiff(now, snap); got != 8 {
		t.Fatalf("got %.8g want %.8g when rolling hashrate is zero but window difficulty is available", got, 8.0)
	}
}

func TestSuggestedVardiff_NoiseBandSuppressesTinySampleMoves(t *testing.T) {
	now := time.Unix(1700000000, 0)
	mc := &MinerConn{
		cfg: Config{},
		vardiff: VarDiffConfig{
			MinDiff:            1,
			MaxDiff:            1024,
			TargetSharesPerMin: 7,
			AdjustmentWindow:   120 * time.Second,
			Step:               2,
			DampingFactor:      0.7,
		},
	}
	atomicStoreFloat64(&mc.difficulty, 1000)
	mc.initialEMAWindowDone.Store(true)
	mc.vardiffAdjustments.Store(3)
	mc.lastDiffChange.Store(now.Add(-3 * time.Minute).UnixNano())

	// Small sample window (2 accepted shares) with modest ratio drift that
	// should be ignored as Poisson noise.
	snap := minerShareSnapshot{
		Stats: MinerStats{
			WindowStart:       now.Add(-time.Minute),
			WindowAccepted:    2,
			WindowSubmissions: 2,
		},
		// Gives target/current ratio around 1.4 for diff=1000.
		RollingHashrate: (1000 * hashPerShare * 7.0 / 60.0) * 1.4,
	}
	if got := mc.suggestedVardiff(now, snap); got != 1000 {
		t.Fatalf("got %.8g want %.8g for tiny-sample noise window", got, 1000.0)
	}
}

func TestSuggestedVardiff_ShareRateSafetyClamp(t *testing.T) {
	now := time.Unix(1700000000, 0)
	mc := &MinerConn{
		cfg: Config{
			MinDifficulty: 1,
		},
		vardiff: VarDiffConfig{
			MinDiff:            1,
			MaxDiff:            0,
			TargetSharesPerMin: 7,
			AdjustmentWindow:   120 * time.Second,
			Step:               2,
			DampingFactor:      1,
		},
	}
	atomicStoreFloat64(&mc.difficulty, 1)
	mc.initialEMAWindowDone.Store(true)
	mc.vardiffAdjustments.Store(3)
	mc.lastDiffChange.Store(now.Add(-3 * time.Minute).UnixNano())

	rolling := 90e12
	snap := minerShareSnapshot{
		Stats: MinerStats{
			WindowStart:       now.Add(-time.Minute),
			WindowAccepted:    100,
			WindowSubmissions: 100,
		},
		RollingHashrate: rolling,
	}

	got := mc.suggestedVardiff(now, snap)
	maxSafeDiff := ((rolling / hashPerShare) * 60.0) / vardiffSafetyMinSharesPerMin
	if got > maxSafeDiff*1.001 {
		t.Fatalf("got %.8g exceeds max safe diff %.8g", got, maxSafeDiff)
	}
}

func TestAdaptiveVardiffWindow_ShortensForHighShareDensity(t *testing.T) {
	mc := &MinerConn{
		vardiff: VarDiffConfig{
			AdjustmentWindow: defaultVarDiffAdjustmentWindow,
		},
	}
	base := defaultVarDiffAdjustmentWindow
	rolling := 90e12
	got := mc.adaptiveVardiffWindow(base, rolling, 1024, 7)
	if got >= base {
		t.Fatalf("got %s want < %s for high share-density miner", got, base)
	}
}

func TestAdaptiveVardiffWindow_LengthensForLowShareDensity(t *testing.T) {
	mc := &MinerConn{
		vardiff: VarDiffConfig{
			AdjustmentWindow: defaultVarDiffAdjustmentWindow,
		},
	}
	base := defaultVarDiffAdjustmentWindow
	rolling := 200e3
	got := mc.adaptiveVardiffWindow(base, rolling, 1, 7)
	if got <= base {
		t.Fatalf("got %s want > %s for low share-density miner", got, base)
	}
}

func TestSuggestedVardiff_LowHashrateDownshiftNeedsMinimumAcceptedShares(t *testing.T) {
	now := time.Unix(1700000000, 0)
	mc := &MinerConn{
		cfg: Config{
			MinDifficulty: 1e-8,
		},
		vardiff: VarDiffConfig{
			MinDiff:            1e-8,
			MaxDiff:            0,
			TargetSharesPerMin: 7,
			AdjustmentWindow:   defaultVarDiffAdjustmentWindow,
			RetargetDelay:      0,
			Step:               2,
			DampingFactor:      0.7,
		},
	}
	atomicStoreFloat64(&mc.difficulty, 0.01)
	mc.initialEMAWindowDone.Store(true)
	mc.vardiffAdjustments.Store(3)
	mc.lastDiffChange.Store(now.Add(-5 * time.Minute).UnixNano())

	rolling := 200e3
	snap := minerShareSnapshot{
		Stats: MinerStats{
			WindowStart:       now.Add(-defaultVarDiffAdjustmentWindow),
			WindowAccepted:    1,
			WindowSubmissions: 1,
		},
		RollingHashrate: rolling,
	}
	got := mc.suggestedVardiff(now, snap)
	if got != 0.01 {
		t.Fatalf("got %.8g want %.8g when low-hashrate downshift sample is too small", got, 0.01)
	}
}

func almostEqualFloat64(a, b, eps float64) bool {
	if a > b {
		return a-b <= eps
	}
	return b-a <= eps
}
