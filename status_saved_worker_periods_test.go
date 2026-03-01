package main

import (
	"testing"
	"time"
)

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

func TestRecordSavedOnlineWorkerPeriodsRecordsPoolHashrate(t *testing.T) {
	store, err := newWorkerListStore(t.TempDir() + "/workers.db")
	if err != nil {
		t.Fatalf("newWorkerListStore: %v", err)
	}
	defer store.Close()

	now := time.Unix(1_700_000_000, 0).UTC().Add(30 * time.Second)
	s := &StatusServer{
		workerLists:        store,
		savedWorkerPeriods: make(map[string]*savedWorkerPeriodRing),
	}
	workers := []WorkerView{
		{WorkerSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", RollingHashrate: 1500},
		{WorkerSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", RollingHashrate: 2500},
	}
	s.recordSavedOnlineWorkerPeriods(workers, now)

	bucket := now.UTC().Truncate(savedWorkerPeriodBucket)
	currentMinute := savedWorkerUnixMinute(bucket)
	if currentMinute <= uint32(savedWorkerPeriodBucketMinutes) {
		t.Fatalf("currentMinute unexpectedly small: %d", currentMinute)
	}
	sampleMinute := currentMinute - uint32(savedWorkerPeriodBucketMinutes)
	idx := savedWorkerRingIndex(sampleMinute)

	s.savedWorkerPeriodsMu.Lock()
	defer s.savedWorkerPeriodsMu.Unlock()
	ring := s.savedWorkerPeriods[savedWorkerPeriodPoolKey]
	if ring == nil {
		t.Fatalf("missing %q history ring", savedWorkerPeriodPoolKey)
	}
	if ring.minutes[idx] != sampleMinute {
		t.Fatalf("pool ring minute=%d want %d", ring.minutes[idx], sampleMinute)
	}
	if ring.bestDifficultyQ[idx] != 0 {
		t.Fatalf("pool bestDifficultyQ=%d want 0", ring.bestDifficultyQ[idx])
	}
	got := decodeHashrateSI16(ring.hashrateQ[idx])
	if got < 3000 || got > 5000 {
		t.Fatalf("pool hashrate decoded=%v, expected around 4000", got)
	}
}

func TestRecordSavedOnlineWorkerPeriodsRecordsPoolBestShareForSampledMinuteOnly(t *testing.T) {
	store, err := newWorkerListStore(t.TempDir() + "/workers.db")
	if err != nil {
		t.Fatalf("newWorkerListStore: %v", err)
	}
	defer store.Close()

	now := time.Unix(1_700_000_000, 0).UTC().Add(30 * time.Second)
	bucket := now.UTC().Truncate(savedWorkerPeriodBucket)
	sampleBucket := bucket.Add(-savedWorkerPeriodBucket)

	s := &StatusServer{
		workerLists:        store,
		savedWorkerPeriods: make(map[string]*savedWorkerPeriodRing),
	}
	workers := []WorkerView{
		{
			WorkerSHA256:        "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			RollingHashrate:     1500,
			LastShare:           sampleBucket.Add(10 * time.Second),
			LastShareDifficulty: 1200,
		},
		{
			WorkerSHA256:        "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			RollingHashrate:     2500,
			LastShare:           sampleBucket.Add(20 * time.Second),
			LastShareDifficulty: 3400,
		},
		{
			WorkerSHA256:        "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
			RollingHashrate:     900,
			LastShare:           sampleBucket.Add(-10 * time.Second), // outside sampled minute
			LastShareDifficulty: 9900,
		},
	}
	s.recordSavedOnlineWorkerPeriods(workers, now)

	currentMinute := savedWorkerUnixMinute(bucket)
	if currentMinute <= uint32(savedWorkerPeriodBucketMinutes) {
		t.Fatalf("currentMinute unexpectedly small: %d", currentMinute)
	}
	sampleMinute := currentMinute - uint32(savedWorkerPeriodBucketMinutes)
	idx := savedWorkerRingIndex(sampleMinute)

	s.savedWorkerPeriodsMu.Lock()
	defer s.savedWorkerPeriodsMu.Unlock()
	ring := s.savedWorkerPeriods[savedWorkerPeriodPoolKey]
	if ring == nil {
		t.Fatalf("missing %q history ring", savedWorkerPeriodPoolKey)
	}
	gotBest := decodeBestShareSI16(ring.bestDifficultyQ[idx])
	if gotBest < 3000 || gotBest > 3800 {
		t.Fatalf("pool best-share decoded=%v, expected around 3400", gotBest)
	}
}
