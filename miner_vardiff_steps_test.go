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

func TestSuggestedVardiff_UpwardCooldownBlocksBackToBackLargeUpshifts(t *testing.T) {
	now := time.Unix(1700000000, 0)
	mc := &MinerConn{
		cfg: Config{},
		vardiff: VarDiffConfig{
			MinDiff:            1,
			MaxDiff:            1 << 30,
			TargetSharesPerMin: 7,
			AdjustmentWindow:   70 * time.Second,
			Step:               2,
			DampingFactor:      0.7,
		},
	}
	mc.initialEMAWindowDone.Store(true)
	mc.vardiffAdjustments.Store(2)
	atomicStoreFloat64(&mc.difficulty, 1024)
	mc.lastDiffChange.Store(now.Add(-2 * time.Minute).UnixNano())

	snap := minerShareSnapshot{
		Stats: MinerStats{
			WindowStart:       now.Add(-2 * time.Minute),
			WindowAccepted:    24,
			WindowSubmissions: 24,
		},
		RollingHashrate: 90e12,
	}

	// Simulate a just-applied large jump, then ensure another upward move is
	// explicitly blocked until cooldown expires.
	mc.noteVardiffUpwardMove(now, 1024, 8192)
	atomicStoreFloat64(&mc.difficulty, 8192)
	if got := mc.suggestedVardiff(now.Add(90*time.Second), snap); got != 8192 {
		t.Fatalf("got %.8g want %.8g during upward cooldown", got, 8192.0)
	}
	if got := mc.suggestedVardiff(now.Add(vardiffLargeUpCooldown+time.Second), snap); got <= 8192 {
		t.Fatalf("got %.8g want > %.8g after upward cooldown", got, 8192.0)
	}
}

func TestSuggestedVardiff_PersistentHighWorkStartLatencyAddsDownwardBias(t *testing.T) {
	now := time.Unix(1700000000, 0)
	mc := &MinerConn{
		cfg: Config{},
		vardiff: VarDiffConfig{
			MinDiff:            1,
			MaxDiff:            1 << 30,
			TargetSharesPerMin: 7,
			AdjustmentWindow:   70 * time.Second,
			Step:               2,
			DampingFactor:      1,
		},
	}
	mc.initialEMAWindowDone.Store(true)
	mc.vardiffAdjustments.Store(1) // bypass debounce for deterministic comparison
	atomicStoreFloat64(&mc.difficulty, 1024)
	mc.lastDiffChange.Store(now.Add(-2 * time.Minute).UnixNano())

	rolling := (300.0 * hashPerShare * 7.0) / 60.0 // target diff ~= 300 (clear downward move)
	baseSnap := minerShareSnapshot{
		Stats: MinerStats{
			WindowStart:       now.Add(-2 * time.Minute),
			WindowAccepted:    12,
			WindowSubmissions: 12,
		},
		RollingHashrate: rolling,
	}
	baseline := mc.suggestedVardiff(now, baseSnap)

	highWarm := baseSnap
	highWarm.NotifyToFirstShareP95MS = vardiffHighWarmupP95MS + 5000
	highWarm.NotifyToFirstShareSamples = vardiffHighWarmupSamplesMin + 2

	got1 := mc.suggestedVardiff(now.Add(2*time.Minute), highWarm)
	got2 := mc.suggestedVardiff(now.Add(4*time.Minute), highWarm)
	got3 := mc.suggestedVardiff(now.Add(6*time.Minute), highWarm)
	if got3 >= baseline {
		t.Fatalf("got %.8g want lower than baseline %.8g after persistent high work-start latency", got3, baseline)
	}
	if got1 != baseline || got2 != baseline {
		t.Fatalf("expected bias to trigger only after persistence, got1 %.8g got2 %.8g baseline %.8g", got1, got2, baseline)
	}
}

func TestSuggestedVardiff_StaleRateSlowsRetargetCadence(t *testing.T) {
	now := time.Unix(1700000000, 0)
	mc := &MinerConn{
		cfg: Config{},
		vardiff: VarDiffConfig{
			MinDiff:            1,
			MaxDiff:            1 << 30,
			TargetSharesPerMin: 1,
			AdjustmentWindow:   70 * time.Second, // non-default disables adaptive shrink
			Step:               2,
			DampingFactor:      1,
		},
	}
	mc.initialEMAWindowDone.Store(true)
	mc.vardiffAdjustments.Store(1)
	atomicStoreFloat64(&mc.difficulty, 1)
	mc.lastDiffChange.Store(now.Add(-80 * time.Second).UnixNano())

	snap := minerShareSnapshot{
		Stats: MinerStats{
			WindowStart:       now.Add(-2 * time.Minute),
			WindowAccepted:    10,
			WindowSubmissions: 10,
		},
		RollingHashrate: hashPerShare,
	}
	if got := mc.suggestedVardiff(now, snap); got <= 1 {
		t.Fatalf("got %.8g want > %.8g with low stale rate", got, 1.0)
	}

	snap.RecentStaleRate = 0.08 // stretches 70s interval to 112s
	if got := mc.suggestedVardiff(now, snap); got != 1 {
		t.Fatalf("got %.8g want %.8g while stale rate should delay retarget", got, 1.0)
	}
}

func TestSuggestedVardiff_UncertaintyCapsLargeMoveOnTinySample(t *testing.T) {
	now := time.Unix(1700000000, 0)
	mc := &MinerConn{
		cfg: Config{},
		vardiff: VarDiffConfig{
			MinDiff:            1,
			MaxDiff:            1 << 30,
			TargetSharesPerMin: 1,
			AdjustmentWindow:   60 * time.Second,
			Step:               2,
			DampingFactor:      1,
		},
	}
	mc.initialEMAWindowDone.Store(true)
	mc.lastDiffChange.Store(now.Add(-3 * time.Minute).UnixNano())
	atomicStoreFloat64(&mc.difficulty, 8192)

	// target diff ~= 65536 (8x higher), but with only one accepted share the
	// uncertainty cap should limit this move to 2x.
	rolling := (65536.0 * hashPerShare) / 60.0
	snap := minerShareSnapshot{
		Stats: MinerStats{
			WindowStart:       now.Add(-time.Minute),
			WindowAccepted:    1,
			WindowSubmissions: 1,
		},
		RollingHashrate: rolling,
	}
	if got := mc.suggestedVardiff(now, snap); got != 16384 {
		t.Fatalf("got %.8g want %.8g when uncertainty cap should restrict large move", got, 16384.0)
	}
}

func TestSuggestedVardiff_SteadyStateNearTargetNeedsManyConfirmations(t *testing.T) {
	now := time.Unix(1700000000, 0)
	mc := &MinerConn{
		cfg: Config{},
		vardiff: VarDiffConfig{
			MinDiff:            1,
			MaxDiff:            1 << 30,
			TargetSharesPerMin: 7,
			AdjustmentWindow:   70 * time.Second,
			Step:               2,
			DampingFactor:      1,
		},
	}
	mc.initialEMAWindowDone.Store(true)
	mc.vardiffAdjustments.Store(5) // enable strict steady-state debounce
	atomicStoreFloat64(&mc.difficulty, 1000)
	mc.lastDiffChange.Store(now.Add(-3 * time.Minute).UnixNano())

	// ratio ~= 1.23 (outside noise band, but near target enough to require many
	// consistent windows before any move).
	targetDiff := 1230.0
	rolling := (targetDiff * hashPerShare * 7.0) / 60.0
	snap := minerShareSnapshot{
		Stats: MinerStats{
			WindowStart:       now.Add(-2 * time.Minute),
			WindowAccepted:    100,
			WindowSubmissions: 100,
		},
		RollingHashrate: rolling,
	}

	for i := 0; i < 7; i++ {
		ts := now.Add(time.Duration(i) * 10 * time.Second)
		if got := mc.suggestedVardiff(ts, snap); got != 1000 {
			t.Fatalf("iteration %d got %.8g want %.8g before steady-state confirmation threshold", i+1, got, 1000.0)
		}
	}
	if got := mc.suggestedVardiff(now.Add(8*10*time.Second), snap); got <= 1000 {
		t.Fatalf("got %.8g want > %.8g once steady-state confirmation threshold reached", got, 1000.0)
	}
}

func TestSuggestedVardiff_TimeoutRiskGuardSkipsLikelyNoShareStreaksAtTarget(t *testing.T) {
	now := time.Unix(1700000000, 0)
	mc := &MinerConn{
		cfg: Config{
			ConnectionTimeout: 30 * time.Second,
		},
		vardiff: VarDiffConfig{
			MinDiff:            1,
			MaxDiff:            1 << 30,
			TargetSharesPerMin: 7,
			AdjustmentWindow:   60 * time.Second,
			Step:               2,
			DampingFactor:      0.7,
		},
	}
	atomicStoreFloat64(&mc.previousDifficulty, 1024)
	atomicStoreFloat64(&mc.difficulty, 8192)
	mc.lastDiffChange.Store(now.Add(-26 * time.Second).UnixNano())

	snap := minerShareSnapshot{
		Stats: MinerStats{
			LastShare:         now.Add(-50 * time.Second), // no share since upjump
			WindowStart:       now.Add(-26 * time.Second),
			WindowAccepted:    0,
			WindowSubmissions: 0,
		},
	}
	// At 7 shares/min and 26s quiet time, P(no share) is still non-trivial,
	// so the timeout guard should not downshift on this alone.
	if got := mc.suggestedVardiff(now, snap); got != 8192 {
		t.Fatalf("got %.8g want %.8g when no-share streak is still statistically plausible", got, 8192.0)
	}
}

func TestSuggestedVardiff_TimeoutRiskDownshiftsWhenNoShareIsStatisticallyUnlikely(t *testing.T) {
	now := time.Unix(1700000000, 0)
	mc := &MinerConn{
		cfg: Config{
			ConnectionTimeout: 30 * time.Second,
		},
		vardiff: VarDiffConfig{
			MinDiff:            1,
			MaxDiff:            1 << 30,
			TargetSharesPerMin: 30,
			AdjustmentWindow:   60 * time.Second,
			Step:               2,
			DampingFactor:      0.7,
		},
	}
	atomicStoreFloat64(&mc.previousDifficulty, 1024)
	atomicStoreFloat64(&mc.difficulty, 8192)
	mc.lastDiffChange.Store(now.Add(-26 * time.Second).UnixNano())

	snap := minerShareSnapshot{
		Stats: MinerStats{
			LastShare:         now.Add(-50 * time.Second), // no share since upjump
			WindowStart:       now.Add(-26 * time.Second),
			WindowAccepted:    0,
			WindowSubmissions: 0,
		},
	}
	if got := mc.suggestedVardiff(now, snap); got >= 8192 {
		t.Fatalf("got %.8g want downshift below %.8g when no-share streak is statistically unlikely", got, 8192.0)
	}
}

func TestSuggestedVardiff_TimeoutRiskGuardDoesNotTriggerAfterRecentShare(t *testing.T) {
	now := time.Unix(1700000000, 0)
	mc := &MinerConn{
		cfg: Config{
			ConnectionTimeout: 30 * time.Second,
		},
		vardiff: VarDiffConfig{
			MinDiff:            1,
			MaxDiff:            1 << 30,
			TargetSharesPerMin: 7,
			AdjustmentWindow:   60 * time.Second,
			Step:               2,
			DampingFactor:      0.7,
		},
	}
	atomicStoreFloat64(&mc.previousDifficulty, 1024)
	atomicStoreFloat64(&mc.difficulty, 8192)
	mc.lastDiffChange.Store(now.Add(-26 * time.Second).UnixNano())

	snap := minerShareSnapshot{
		Stats: MinerStats{
			LastShare:         now.Add(-10 * time.Second), // share arrived after upjump
			WindowStart:       now.Add(-10 * time.Second),
			WindowAccepted:    1,
			WindowSubmissions: 1,
		},
		RollingHashrate: 90e12,
	}
	if got := mc.suggestedVardiff(now, snap); got < 8192 {
		t.Fatalf("got %.8g want no timeout-risk downshift below %.8g after recent share", got, 8192.0)
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

func TestAdaptiveVardiffWindow_AdjustsForShareDensity(t *testing.T) {
	mc := &MinerConn{
		vardiff: VarDiffConfig{
			AdjustmentWindow: defaultVarDiffAdjustmentWindow,
		},
	}
	base := defaultVarDiffAdjustmentWindow
	tests := []struct {
		name       string
		rolling    float64
		currentDiff float64
		wantSign   int // -1 means shorter, +1 means longer
	}{
		{name: "high density shortens", rolling: 90e12, currentDiff: 1024, wantSign: -1},
		{name: "low density lengthens", rolling: 200e3, currentDiff: 1, wantSign: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mc.adaptiveVardiffWindow(base, tc.rolling, tc.currentDiff, 7)
			if tc.wantSign < 0 && got >= base {
				t.Fatalf("got %s want < %s", got, base)
			}
			if tc.wantSign > 0 && got <= base {
				t.Fatalf("got %s want > %s", got, base)
			}
		})
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
