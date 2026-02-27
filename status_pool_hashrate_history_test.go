package main

import (
	"testing"
	"time"
)

func hasPoolHistoryBit(bits []uint16, idx int) bool {
	if idx < 0 {
		return false
	}
	bi := idx / 8
	if bi < 0 || bi >= len(bits) {
		return false
	}
	return (bits[bi] & (1 << uint(idx%8))) != 0
}

func countPoolHistoryBits(bits []uint16, n int) int {
	if n <= 0 {
		return 0
	}
	count := 0
	for i := 0; i < n; i++ {
		if hasPoolHistoryBit(bits, i) {
			count++
		}
	}
	return count
}

func TestPoolHashrateHistoryQuantizedSnapshotTrimsAndOrders(t *testing.T) {
	t.Parallel()

	base := time.Unix(1_700_000_000, 0).UTC()
	s := &StatusServer{}

	s.appendPoolHashrateHistory(100, 800000, base.Add(-7*time.Minute))
	s.appendPoolHashrateHistory(0, 800001, base.Add(-5*time.Minute)) // ignored
	s.appendPoolHashrateHistory(200, 800002, base.Add(-5*time.Minute))
	s.appendPoolHashrateHistory(300, 800003, base.Add(-time.Minute))

	got := s.poolHashrateHistoryQuantizedSnapshot(base)
	if got == nil {
		t.Fatalf("expected non-nil quantized snapshot")
	}
	if got.N <= 0 || got.I <= 0 {
		t.Fatalf("invalid quantized metadata: %+v", got)
	}
	if len(got.P) == 0 || len(got.HQ) == 0 {
		t.Fatalf("expected quantized arrays in snapshot: %+v", got)
	}
	if count := countPoolHistoryBits(got.P, got.N); count < 2 {
		t.Fatalf("expected at least 2 present buckets, got %d (%+v)", count, got.P)
	}
	if got.H0 <= 0 || got.H1 <= 0 || got.H1 < got.H0 {
		t.Fatalf("unexpected hashrate quantization bounds: h0=%v h1=%v", got.H0, got.H1)
	}
}

func TestPoolHashrateHistoryQuantizedSnapshotDropsExpired(t *testing.T) {
	t.Parallel()

	base := time.Unix(1_700_000_000, 0).UTC()
	s := &StatusServer{}

	s.appendPoolHashrateHistory(150, 810000, base.Add(-5*time.Minute))
	got := s.poolHashrateHistoryQuantizedSnapshot(base.Add(2 * time.Minute))
	if got == nil {
		t.Fatalf("expected non-nil quantized snapshot")
	}
	if len(got.P) != 0 || len(got.HQ) != 0 {
		t.Fatalf("expected empty quantized arrays after expiry, got %+v", got)
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
