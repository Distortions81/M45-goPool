package main

import (
	"testing"
	"time"
)

func TestPoolHashrateHistorySnapshotTrimsAndOrders(t *testing.T) {
	t.Parallel()

	base := time.Unix(1_700_000_000, 0).UTC()
	s := &StatusServer{}

	s.appendPoolHashrateHistory(100, 800000, base.Add(-7*time.Minute))
	s.appendPoolHashrateHistory(0, 800001, base.Add(-5*time.Minute)) // ignored
	s.appendPoolHashrateHistory(200, 800002, base.Add(-5*time.Minute))
	s.appendPoolHashrateHistory(300, 800003, base.Add(-time.Minute))

	got := s.poolHashrateHistorySnapshot(base)
	if len(got) < 2 {
		t.Fatalf("expected at least 2 history points, got %d", len(got))
	}
	first := decodeHashrateSI8(uint8(got[0]))
	last := decodeHashrateSI8(uint8(got[len(got)-1]))
	if first <= 0 || last <= 0 {
		t.Fatalf("expected positive decoded values, got first=%v last=%v", first, last)
	}
	if first >= last {
		t.Fatalf("expected increasing sample order (first < last), got first=%v last=%v", first, last)
	}
}

func TestPoolHashrateHistorySnapshotDropsExpired(t *testing.T) {
	t.Parallel()

	base := time.Unix(1_700_000_000, 0).UTC()
	s := &StatusServer{}

	s.appendPoolHashrateHistory(150, 810000, base.Add(-5*time.Minute))
	got := s.poolHashrateHistorySnapshot(base.Add(2 * time.Minute))
	if len(got) != 0 {
		t.Fatalf("expected empty history after expiry, got len=%d", len(got))
	}
}

func TestLatestPoolHashrateHistorySinceReturnsNewestInRange(t *testing.T) {
	t.Parallel()

	base := time.Unix(1_700_000_000, 0).UTC()
	s := &StatusServer{}
	s.appendPoolHashrateHistory(120, 820000, base.Add(-4*time.Minute))
	s.appendPoolHashrateHistory(220, 820001, base.Add(-90*time.Second))
	s.appendPoolHashrateHistory(320, 820002, base.Add(-30*time.Second))

	hashrate, height, ok := s.latestPoolHashrateHistorySince(base, 2*time.Minute)
	if !ok {
		t.Fatalf("expected recent hashrate fallback")
	}
	if hashrate != 320 || height != 820002 {
		t.Fatalf("unexpected fallback sample: hashrate=%v height=%d", hashrate, height)
	}
}

func TestLatestPoolHashrateHistorySinceRespectsMaxAge(t *testing.T) {
	t.Parallel()

	base := time.Unix(1_700_000_000, 0).UTC()
	s := &StatusServer{}
	s.appendPoolHashrateHistory(220, 820001, base.Add(-3*time.Minute))

	if _, _, ok := s.latestPoolHashrateHistorySince(base, 2*time.Minute); ok {
		t.Fatalf("expected no fallback sample outside max age")
	}
}
