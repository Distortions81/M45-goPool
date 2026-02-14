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
	if len(got) != 2 {
		t.Fatalf("unexpected history length: got %d want 2", len(got))
	}
	if got[0].Hashrate != 200 || got[0].BlockHeight != 800002 {
		t.Fatalf("unexpected first sample: %+v", got[0])
	}
	if got[1].Hashrate != 300 || got[1].BlockHeight != 800003 {
		t.Fatalf("unexpected second sample: %+v", got[1])
	}
}

func TestPoolHashrateHistorySnapshotDropsExpired(t *testing.T) {
	t.Parallel()

	base := time.Unix(1_700_000_000, 0).UTC()
	s := &StatusServer{}

	s.appendPoolHashrateHistory(150, 810000, base.Add(-5*time.Minute))
	got := s.poolHashrateHistorySnapshot(base.Add(2 * time.Minute))
	if len(got) != 0 {
		t.Fatalf("expected empty history after expiry, got %d samples", len(got))
	}
}
