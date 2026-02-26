package main

import "testing"

func TestBackfillSavedWorkerOfflineGapLocked(t *testing.T) {
	if savedWorkerPeriodBucketMinutes <= 0 {
		t.Fatal("savedWorkerPeriodBucketMinutes must be > 0")
	}
	step := uint32(savedWorkerPeriodBucketMinutes)
	base := uint32(1_000_000)

	ring := &savedWorkerPeriodRing{}
	lastOnline := base
	lastIdx := savedWorkerRingIndex(lastOnline)
	ring.minutes[lastIdx] = lastOnline
	ring.hashrateQ[lastIdx] = 111
	ring.bestDifficultyQ[lastIdx] = 222
	ring.lastMinute = lastOnline

	sampleMinute := base + (4 * step)
	s := &StatusServer{}
	s.backfillSavedWorkerOfflineGapLocked(ring, sampleMinute)

	for i := uint32(1); i < 4; i++ {
		m := base + (i * step)
		idx := savedWorkerRingIndex(m)
		if ring.minutes[idx] != m {
			t.Fatalf("gap minute %d missing", m)
		}
		if ring.hashrateQ[idx] != 0 {
			t.Fatalf("gap minute %d hashrateQ = %d, want 0", m, ring.hashrateQ[idx])
		}
		if ring.bestDifficultyQ[idx] != 0 {
			t.Fatalf("gap minute %d bestDifficultyQ = %d, want 0", m, ring.bestDifficultyQ[idx])
		}
	}
	if ring.lastMinute != lastOnline {
		t.Fatalf("lastMinute changed = %d, want %d", ring.lastMinute, lastOnline)
	}
	if ring.hashrateQ[lastIdx] != 111 || ring.bestDifficultyQ[lastIdx] != 222 {
		t.Fatal("last online sample was modified")
	}
}
