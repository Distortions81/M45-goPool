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

func TestResetShareWindow_PreservesRollingHashrateState(t *testing.T) {
	now := time.Unix(1700000000, 0)
	mc := &MinerConn{}
	mc.initialEMAWindowDone.Store(true)
	mc.stats.WindowStart = now.Add(-time.Minute)
	mc.stats.WindowAccepted = 12
	mc.stats.WindowSubmissions = 15
	mc.stats.WindowDifficulty = 42
	mc.vardiffWindowStart = now.Add(-time.Minute)
	mc.vardiffWindowAccepted = 12
	mc.vardiffWindowSubmissions = 15
	mc.vardiffWindowDifficulty = 42
	mc.lastHashrateUpdate = now.Add(-10 * time.Second)
	mc.rollingHashrateValue = 12345
	mc.rollingHashrateControl = 23456
	mc.hashrateSampleCount = 7
	mc.hashrateAccumulatedDiff = 9.5

	mc.resetShareWindow(now)

	if !mc.initialEMAWindowDone.Load() {
		t.Fatalf("initialEMAWindowDone=false, want true preserved after resetShareWindow")
	}
	if mc.stats.WindowStart.IsZero() {
		t.Fatalf("WindowStart unexpectedly zero; status window should be preserved across vardiff reset")
	}
	if mc.stats.WindowAccepted != 12 || mc.stats.WindowSubmissions != 15 || mc.stats.WindowDifficulty != 42 {
		t.Fatalf("status window counters changed: accepted=%d submissions=%d difficulty=%v",
			mc.stats.WindowAccepted, mc.stats.WindowSubmissions, mc.stats.WindowDifficulty)
	}
	if !mc.vardiffWindowStart.IsZero() {
		t.Fatalf("vardiffWindowStart=%v want zero time so first share starts the vardiff window", mc.vardiffWindowStart)
	}
	if mc.vardiffWindowAccepted != 0 || mc.vardiffWindowSubmissions != 0 || mc.vardiffWindowDifficulty != 0 {
		t.Fatalf("vardiff window counters not cleared: accepted=%d submissions=%d difficulty=%v",
			mc.vardiffWindowAccepted, mc.vardiffWindowSubmissions, mc.vardiffWindowDifficulty)
	}
	if mc.lastHashrateUpdate.IsZero() || mc.hashrateSampleCount == 0 || mc.hashrateAccumulatedDiff == 0 {
		t.Fatalf("hashrate accumulator state should be preserved")
	}
	if mc.rollingHashrateValue != 12345 || mc.rollingHashrateControl != 23456 {
		t.Fatalf("rolling hashrates should be preserved across reset: display=%v control=%v", mc.rollingHashrateValue, mc.rollingHashrateControl)
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
	baseControl := mc.rollingHashrateControl
	baseDisplay := mc.rollingHashrateValue

	// Introduce a sharp hashrate rise; fast/control EMA should move farther.
	mc.updateHashrateLocked(4, base.Add(initialHashrateEMATau+61*time.Second))
	deltaControl := mc.rollingHashrateControl - baseControl
	deltaDisplay := mc.rollingHashrateValue - baseDisplay
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
	mc.initialEMAWindowDone.Store(true)
	mc.statsMu.Lock()
	mc.lastHashrateUpdate = now.Add(-5 * time.Minute)
	mc.rollingHashrateControl = 1.0e12
	mc.rollingHashrateValue = 1.0e12
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
	got := mc.vardiffWindowStart
	mc.statsMu.Unlock()

	want := now
	if !got.Equal(want) {
		t.Fatalf("vardiffWindowStart=%v want %v anchored at reset time", got, want)
	}
}
