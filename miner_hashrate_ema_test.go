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
	for i := range 10 {
		mc.updateHashrateLocked(1, base.Add(time.Duration(i)*time.Second))
	}
	if mc.vardiffState.rollingHashrateValue != 0 {
		t.Fatalf("rollingHashrateValue=%v, want 0 before tau elapses", mc.vardiffState.rollingHashrateValue)
	}
	if mc.vardiffState.hashrateSampleCount != 10 {
		t.Fatalf("hashrateSampleCount=%d, want 10", mc.vardiffState.hashrateSampleCount)
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
	if mc.vardiffState.rollingHashrateValue != 0 {
		t.Fatalf("rollingHashrateValue=%v, want 0 after first share", mc.vardiffState.rollingHashrateValue)
	}

	mc.updateHashrateLocked(1, base.Add(initialHashrateEMATau+time.Second))
	if mc.vardiffState.rollingHashrateValue <= 0 {
		t.Fatalf("rollingHashrateValue=%v, want > 0 once bootstrap tau elapsed", mc.vardiffState.rollingHashrateValue)
	}
	first := mc.vardiffState.rollingHashrateValue
	if mc.vardiffState.hashrateSampleCount != 0 {
		t.Fatalf("hashrateSampleCount=%d, want 0 after bootstrap tau update", mc.vardiffState.hashrateSampleCount)
	}

	// After the first EMA window, updates should apply incrementally each sample.
	mc.updateHashrateLocked(1, base.Add(90*time.Second)) // only 59s since last update
	if mc.vardiffState.rollingHashrateValue == first {
		t.Fatalf("rollingHashrateValue=%v, want change on incremental post-bootstrap update", mc.vardiffState.rollingHashrateValue)
	}
	mc.statsMu.Unlock()
}

func TestResetShareWindow_PreservesRollingHashrateState(t *testing.T) {
	now := time.Unix(1700000000, 0)
	mc := &MinerConn{}
	mc.vardiffState.initialEMAWindowDone.Store(true)
	mc.stats.WindowStart = now.Add(-time.Minute)
	mc.stats.WindowAccepted = 12
	mc.stats.WindowSubmissions = 15
	mc.stats.WindowDifficulty = 42
	mc.vardiffState.vardiffWindowStart = now.Add(-time.Minute)
	mc.vardiffState.vardiffWindowAccepted = 12
	mc.vardiffState.vardiffWindowSubmissions = 15
	mc.vardiffState.vardiffWindowDifficulty = 42
	mc.vardiffState.lastHashrateUpdate = now.Add(-10 * time.Second)
	mc.vardiffState.rollingHashrateValue = 12345
	mc.vardiffState.rollingHashrateControl = 23456
	mc.vardiffState.hashrateSampleCount = 7
	mc.vardiffState.hashrateAccumulatedDiff = 9.5

	mc.resetShareWindow(now)

	if !mc.vardiffState.initialEMAWindowDone.Load() {
		t.Fatalf("initialEMAWindowDone=false, want true preserved after resetShareWindow")
	}
	if mc.stats.WindowStart.IsZero() {
		t.Fatalf("WindowStart unexpectedly zero; status window should be preserved across vardiff reset")
	}
	if mc.stats.WindowAccepted != 12 || mc.stats.WindowSubmissions != 15 || mc.stats.WindowDifficulty != 42 {
		t.Fatalf("status window counters changed: accepted=%d submissions=%d difficulty=%v",
			mc.stats.WindowAccepted, mc.stats.WindowSubmissions, mc.stats.WindowDifficulty)
	}
	if !mc.vardiffState.vardiffWindowStart.IsZero() {
		t.Fatalf("vardiffWindowStart=%v want zero time so first share starts the vardiff window", mc.vardiffState.vardiffWindowStart)
	}
	if mc.vardiffState.vardiffWindowAccepted != 0 || mc.vardiffState.vardiffWindowSubmissions != 0 || mc.vardiffState.vardiffWindowDifficulty != 0 {
		t.Fatalf("vardiff window counters not cleared: accepted=%d submissions=%d difficulty=%v",
			mc.vardiffState.vardiffWindowAccepted, mc.vardiffState.vardiffWindowSubmissions, mc.vardiffState.vardiffWindowDifficulty)
	}
	if mc.vardiffState.lastHashrateUpdate.IsZero() || mc.vardiffState.hashrateSampleCount == 0 || mc.vardiffState.hashrateAccumulatedDiff == 0 {
		t.Fatalf("hashrate accumulator state should be preserved")
	}
	if mc.vardiffState.rollingHashrateValue != 12345 || mc.vardiffState.rollingHashrateControl != 23456 {
		t.Fatalf("rolling hashrates should be preserved across reset: display=%v control=%v", mc.vardiffState.rollingHashrateValue, mc.vardiffState.rollingHashrateControl)
	}
}

func TestUpdateHashrateLocked_ControlEMARespondsFasterThanDisplay(t *testing.T) {
	mc := &MinerConn{
		cfg: Config{
			HashrateEMATauSeconds: 300,
		},
	}
	base := time.Unix(1700000000, 0)

	mc.statsMu.Lock()
	// First update initializes hashrate after bootstrap.
	mc.updateHashrateLocked(1, base)
	mc.updateHashrateLocked(1, base.Add(initialHashrateEMATau+time.Second))
	baseControl := mc.vardiffState.rollingHashrateControl
	baseDisplay := mc.vardiffState.rollingHashrateValue

	// Introduce a sharp hashrate rise; fast/control EMA should move farther.
	mc.updateHashrateLocked(4, base.Add(initialHashrateEMATau+61*time.Second))
	deltaControl := mc.vardiffState.rollingHashrateControl - baseControl
	deltaDisplay := mc.vardiffState.rollingHashrateValue - baseDisplay
	mc.statsMu.Unlock()

	if deltaControl <= deltaDisplay {
		t.Fatalf("control EMA did not respond faster: deltaControl=%v deltaDisplay=%v", deltaControl, deltaDisplay)
	}
}

func TestDecayedHashratesLocked_DecaysDuringIdle(t *testing.T) {
	now := time.Unix(1700000000, 0)
	mc := &MinerConn{
		cfg: Config{
			HashrateEMATauSeconds: 300,
		},
	}
	mc.vardiffState.initialEMAWindowDone.Store(true)
	mc.statsMu.Lock()
	mc.vardiffState.lastHashrateUpdate = now.Add(-5 * time.Minute)
	mc.vardiffState.rollingHashrateControl = 1.0e12
	mc.vardiffState.rollingHashrateValue = 1.0e12
	control, display := mc.decayedHashratesLocked(now)
	mc.statsMu.Unlock()

	if control <= 0 || display <= 0 {
		t.Fatalf("expected positive decayed values, got control=%v display=%v", control, display)
	}
	if control >= 1.0e12 || display >= 1.0e12 {
		t.Fatalf("expected decay below original value, got control=%v display=%v", control, display)
	}
	// Control tau is faster, so control estimate should decay more.
	if control >= display {
		t.Fatalf("expected control decay stronger than display decay, got control=%v display=%v", control, display)
	}
}

func TestResetShareWindow_AnchorsVardiffWindowAtResetTime(t *testing.T) {
	now := time.Unix(1700000000, 0)
	firstShare := now.Add(20 * time.Second)
	mc := &MinerConn{}

	mc.resetShareWindow(now)

	mc.statsMu.Lock()
	mc.ensureVardiffWindowLocked(firstShare)
	got := mc.vardiffState.vardiffWindowStart
	mc.statsMu.Unlock()

	want := now
	if !got.Equal(want) {
		t.Fatalf("vardiffWindowStart=%v want %v anchored at reset time", got, want)
	}
}
