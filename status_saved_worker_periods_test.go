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

	hashA := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hashB := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	hashC := "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

	// Register A and B as saved workers; C is intentionally omitted so its
	// ring-buffer entry (if any) is ignored by the pool aggregation.
	for _, h := range []string{hashA, hashB} {
		if _, err := store.db.Exec(
			"INSERT OR IGNORE INTO saved_workers (user_id, worker, worker_hash, worker_display, notify_enabled) VALUES (?, ?, ?, ?, 1)",
			"testuser", h, h, h[:8],
		); err != nil {
			t.Fatalf("insert saved worker: %v", err)
		}
	}

	now := time.Now().UTC().Truncate(savedWorkerPeriodBucket).Add(30 * time.Second)
	bucket := now.UTC().Truncate(savedWorkerPeriodBucket)
	sampleBucket := bucket.Add(-savedWorkerPeriodBucket)

	// Pre-populate the minute-best ring buffer: this is what the new pool
	// best-share path reads via ConsumeSavedWorkerMinuteBestDifficulty.
	store.UpdateSavedWorkerMinuteBestDifficulty(hashA, 1200, sampleBucket.Add(10*time.Second))
	store.UpdateSavedWorkerMinuteBestDifficulty(hashB, 3400, sampleBucket.Add(20*time.Second))
	// C has a high difficulty but is not a saved worker — must not affect pool best.
	store.UpdateSavedWorkerMinuteBestDifficulty(hashC, 9900, sampleBucket.Add(5*time.Second))

	s := &StatusServer{
		workerLists:        store,
		savedWorkerPeriods: make(map[string]*savedWorkerPeriodRing),
	}
	workers := []WorkerView{
		{WorkerSHA256: hashA, RollingHashrate: 1500},
		{WorkerSHA256: hashB, RollingHashrate: 2500},
		{WorkerSHA256: hashC, RollingHashrate: 900},
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
