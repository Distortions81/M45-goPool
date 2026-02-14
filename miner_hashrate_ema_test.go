package main

import (
	"testing"
	"time"
)

func TestUpdateHashrateLocked_DoesNotUpdateBeforeTau(t *testing.T) {
	mc := &MinerConn{
		cfg: Config{
			HashrateEMATauSeconds: 600,
		},
	}
	base := time.Unix(1700000000, 0)

	mc.statsMu.Lock()
	for i := 0; i < 10; i++ {
		mc.updateHashrateLocked(1, base.Add(time.Duration(i)*time.Second))
	}
	if mc.rollingHashrateValue != 0 {
		t.Fatalf("rollingHashrateValue=%v, want 0 before tau elapses", mc.rollingHashrateValue)
	}
	if mc.hashrateSampleCount != 10 {
		t.Fatalf("hashrateSampleCount=%d, want 10", mc.hashrateSampleCount)
	}
	mc.statsMu.Unlock()
}

func TestUpdateHashrateLocked_UsesBootstrapWindowThenUpdatesIncrementally(t *testing.T) {
	mc := &MinerConn{
		cfg: Config{
			HashrateEMATauSeconds: 60,
		},
	}
	base := time.Unix(1700000000, 0)

	mc.statsMu.Lock()
	mc.updateHashrateLocked(1, base)
	if mc.rollingHashrateValue != 0 {
		t.Fatalf("rollingHashrateValue=%v, want 0 after first share", mc.rollingHashrateValue)
	}

	mc.updateHashrateLocked(1, base.Add(initialHashrateEMATau+time.Second))
	if mc.rollingHashrateValue <= 0 {
		t.Fatalf("rollingHashrateValue=%v, want > 0 once bootstrap tau elapsed", mc.rollingHashrateValue)
	}
	first := mc.rollingHashrateValue
	if mc.hashrateSampleCount != 0 {
		t.Fatalf("hashrateSampleCount=%d, want 0 after bootstrap tau update", mc.hashrateSampleCount)
	}

	// After the first EMA window, updates should apply incrementally each sample.
	mc.updateHashrateLocked(1, base.Add(90*time.Second)) // only 59s since last update
	if mc.rollingHashrateValue == first {
		t.Fatalf("rollingHashrateValue=%v, want change on incremental post-bootstrap update", mc.rollingHashrateValue)
	}
	mc.statsMu.Unlock()
}

func TestResetShareWindow_ResetsEMATauState(t *testing.T) {
	now := time.Unix(1700000000, 0)
	mc := &MinerConn{}
	mc.initialEMAWindowDone.Store(true)
	mc.stats.WindowStart = now.Add(-time.Minute)
	mc.stats.WindowAccepted = 12
	mc.stats.WindowSubmissions = 15
	mc.stats.WindowDifficulty = 42
	mc.lastHashrateUpdate = now.Add(-10 * time.Second)
	mc.rollingHashrateValue = 12345
	mc.hashrateSampleCount = 7
	mc.hashrateAccumulatedDiff = 9.5

	mc.resetShareWindow(now)

	if mc.initialEMAWindowDone.Load() {
		t.Fatalf("initialEMAWindowDone=true, want false after resetShareWindow")
	}
	if !mc.stats.WindowStart.IsZero() {
		t.Fatalf("WindowStart=%v want zero time so first share starts the window", mc.stats.WindowStart)
	}
	if mc.stats.WindowAccepted != 0 || mc.stats.WindowSubmissions != 0 || mc.stats.WindowDifficulty != 0 {
		t.Fatalf("window counters not cleared: accepted=%d submissions=%d difficulty=%v",
			mc.stats.WindowAccepted, mc.stats.WindowSubmissions, mc.stats.WindowDifficulty)
	}
	if !mc.lastHashrateUpdate.IsZero() || mc.rollingHashrateValue != 0 || mc.hashrateSampleCount != 0 || mc.hashrateAccumulatedDiff != 0 {
		t.Fatalf("hashrate state not cleared")
	}
}

func TestResetShareWindow_FirstShareAnchorsWindowByLagPercent(t *testing.T) {
	now := time.Unix(1700000000, 0)
	firstShare := now.Add(20 * time.Second)
	mc := &MinerConn{}

	mc.resetShareWindow(now)

	mc.statsMu.Lock()
	mc.ensureWindowLocked(firstShare)
	got := mc.stats.WindowStart
	mc.statsMu.Unlock()

	want := now.Add((20 * time.Second * windowStartLagPercent) / 100)
	if !got.Equal(want) {
		t.Fatalf("WindowStart=%v want %v with %d%% lag between reset and first share", got, want, windowStartLagPercent)
	}
}
