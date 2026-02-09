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

	mc.updateHashrateLocked(1, base.Add(31*time.Second))
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
