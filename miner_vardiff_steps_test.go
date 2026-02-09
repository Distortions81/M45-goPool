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

func TestSuggestedVardiffFine_FirstTwoAdjustmentsUseTwoSteps(t *testing.T) {
	now := time.Unix(1700000000, 0)
	mc := &MinerConn{
		cfg: Config{
			VardiffFine: true,
		},
		vardiff: VarDiffConfig{
			MinDiff:            1,
			MaxDiff:            1024,
			TargetSharesPerMin: 1,
			Step:               2,
		},
	}
	atomicStoreFloat64(&mc.difficulty, 1)

	snap := minerShareSnapshot{
		Stats: MinerStats{
			WindowStart:       now.Add(-time.Minute),
			WindowAccepted:    25,
			WindowSubmissions: 25,
		},
		RollingHashrate: hashPerShare,
	}

	tests := []struct {
		name      string
		adjustCnt int32
		want      float64
	}{
		{name: "first adjustment", adjustCnt: 0, want: 2},
		{name: "second adjustment", adjustCnt: 1, want: 2},
		{name: "third adjustment", adjustCnt: 2, want: 1.4142135623730951},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mc.vardiffAdjustments.Store(tc.adjustCnt)
			got := mc.suggestedVardiff(now, snap)
			if !almostEqualFloat64(got, tc.want, 1e-12) {
				t.Fatalf("adjustCnt=%d got %.16g want %.16g", tc.adjustCnt, got, tc.want)
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

	mc.lastDiffChange.Store(now.Add(-20 * time.Second).UnixNano())
	if got := mc.suggestedVardiff(now, snap); got != 1 {
		t.Fatalf("got %.8g want %.8g before 30s bootstrap interval", got, 1.0)
	}

	mc.lastDiffChange.Store(now.Add(-30 * time.Second).UnixNano())
	if got := mc.suggestedVardiff(now, snap); got != 4 {
		t.Fatalf("got %.8g want %.8g once 30s bootstrap interval elapsed", got, 4.0)
	}
}

func almostEqualFloat64(a, b, eps float64) bool {
	if a > b {
		return a-b <= eps
	}
	return b-a <= eps
}
