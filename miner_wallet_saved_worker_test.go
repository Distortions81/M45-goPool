package main

import (
	"sync"
	"testing"
	"time"
)

func TestMaybeUpdateSavedWorkerBestDiff_TracksAfterResync(t *testing.T) {
	store, err := newWorkerListStore(t.TempDir() + "/saved_workers.sqlite")
	if err != nil {
		t.Fatalf("newWorkerListStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const (
		userID   = "user-1"
		worker   = "bc1qexampleaddress00000000000000000000000000.worker-01"
		bestDiff = 123.45
	)
	hash := workerNameHash(worker)
	if hash == "" {
		t.Fatalf("expected worker hash")
	}

	mc := &MinerConn{
		savedWorkerStore:     store,
		registeredWorkerHash: hash,
		savedWorkerTracked:   false, // Simulate an already-connected miner before save.
	}

	// Not saved yet: lookup should miss and update should no-op.
	mc.maybeUpdateSavedWorkerBestDiff(bestDiff)
	if mc.savedWorkerTracked {
		t.Fatalf("worker should not be tracked before it is saved")
	}

	if err := store.Add(userID, worker); err != nil {
		t.Fatalf("store.Add: %v", err)
	}

	// After the worker is saved, a tracking resync (triggered by save/remove
	// handlers for live connections) enables updates without a reconnect.
	mc.syncSavedWorkerState(hash)
	mc.maybeUpdateSavedWorkerBestDiff(bestDiff)
	if !mc.savedWorkerTracked {
		t.Fatalf("worker should be tracked after save")
	}
	if mc.savedWorkerBestDiff != bestDiff {
		t.Fatalf("savedWorkerBestDiff = %v, want %v", mc.savedWorkerBestDiff, bestDiff)
	}

	list, err := store.List(userID)
	if err != nil {
		t.Fatalf("store.List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("len(list) = %d, want 1", len(list))
	}
	if list[0].BestDifficulty != bestDiff {
		t.Fatalf("BestDifficulty = %v, want %v", list[0].BestDifficulty, bestDiff)
	}
}

func TestSavedWorkerUpdatesPersistDuringStateSync(t *testing.T) {
	store, err := newWorkerListStore(t.TempDir() + "/saved_workers.sqlite")
	if err != nil {
		t.Fatalf("newWorkerListStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const (
		userID   = "user-sync-window"
		worker   = "bc1qexampleaddress00000000000000000000000000.sync-window"
		bestDiff = 321.5
	)
	if err := store.Add(userID, worker); err != nil {
		t.Fatalf("store.Add: %v", err)
	}
	hash := workerNameHash(worker)
	mc := &MinerConn{
		savedWorkerStore:     store,
		registeredWorker:     worker,
		registeredWorkerHash: hash,
	}

	// Hold the cache in the exact state published by registerWorker before its
	// store lookup returns. Submit-time updates must query the store instead of
	// treating the temporarily untracked cache as authoritative.
	mc.savedWorkerMu.Lock()
	generation := mc.beginSavedWorkerSyncLocked()
	mc.savedWorkerMu.Unlock()

	now := time.Now().UTC()
	mc.maybeUpdateSavedWorkerBestDiffFor(worker, bestDiff)
	mc.maybeUpdateSavedWorkerMinuteBestDiffFor(worker, bestDiff, now)

	gotBest, tracked, err := store.BestDifficultyForHash(hash)
	if err != nil {
		t.Fatalf("BestDifficultyForHash: %v", err)
	}
	if !tracked || gotBest != bestDiff {
		t.Fatalf("saved best = (%v, %v), want (%v, true)", gotBest, tracked, bestDiff)
	}
	if got := store.SavedWorkerMinuteBestDifficulty(hash, now); got <= 0 {
		t.Fatalf("saved minute best = %v, want positive", got)
	}

	// A lookup that started before the update must not lower the newer cached
	// best when it completes.
	if !mc.completeSavedWorkerSync(hash, generation, 1, true, true) {
		t.Fatal("matching saved-worker sync completion was rejected")
	}
	mc.savedWorkerMu.Lock()
	gotCacheBest := mc.savedWorkerBestDiff
	gotCacheTracked := mc.savedWorkerTracked
	gotCacheSyncing := mc.savedWorkerSyncing
	mc.savedWorkerMu.Unlock()
	if gotCacheBest != bestDiff || !gotCacheTracked || gotCacheSyncing {
		t.Fatalf("saved-worker cache = (best %v, tracked %v, syncing %v), want (%v, true, false)",
			gotCacheBest, gotCacheTracked, gotCacheSyncing, bestDiff)
	}
}

func TestSavedWorkerSyncRejectsStaleABAGeneration(t *testing.T) {
	const (
		workerA = "bc1qexampleaddress00000000000000000000000000.worker-a"
		workerB = "bc1qexampleaddress00000000000000000000000000.worker-b"
	)
	hashA := workerNameHash(workerA)
	hashB := workerNameHash(workerB)
	mc := &MinerConn{workerRegistry: newWorkerConnectionRegistry()}

	mc.savedWorkerMu.Lock()
	mc.registeredWorker = workerA
	mc.registeredWorkerHash = hashA
	firstAGeneration := mc.beginSavedWorkerSyncLocked()
	mc.registeredWorker = workerB
	mc.registeredWorkerHash = hashB
	_ = mc.beginSavedWorkerSyncLocked()
	mc.registeredWorker = workerA
	mc.registeredWorkerHash = hashA
	secondAGeneration := mc.beginSavedWorkerSyncLocked()
	mc.savedWorkerMu.Unlock()

	if mc.completeSavedWorkerSync(hashA, firstAGeneration, 999, true, true) {
		t.Fatal("stale first A generation applied after A -> B -> A")
	}
	mc.savedWorkerMu.Lock()
	gotGeneration := mc.savedWorkerSyncGen
	gotSyncing := mc.savedWorkerSyncing
	gotTracked := mc.savedWorkerTracked
	gotBest := mc.savedWorkerBestDiff
	mc.savedWorkerMu.Unlock()
	if gotGeneration != secondAGeneration || !gotSyncing || gotTracked || gotBest != 0 {
		t.Fatalf("cache after stale completion = (generation %d, syncing %v, tracked %v, best %v), want (%d, true, false, 0)",
			gotGeneration, gotSyncing, gotTracked, gotBest, secondAGeneration)
	}
	if !mc.completeSavedWorkerSync(hashA, secondAGeneration, 42, true, true) {
		t.Fatal("current second A generation was rejected")
	}

	// Closing the registration advances the generation so even a completion
	// with the same hash cannot repopulate state after unregister.
	mc.savedWorkerMu.Lock()
	closingGeneration := mc.beginSavedWorkerSyncLocked()
	mc.savedWorkerMu.Unlock()
	mc.unregisterRegisteredWorker()
	if mc.completeSavedWorkerSync(hashA, closingGeneration, 1000, true, true) {
		t.Fatal("sync completion applied after worker unregister")
	}
	mc.savedWorkerMu.Lock()
	defer mc.savedWorkerMu.Unlock()
	if mc.registeredWorkerHash != "" || mc.savedWorkerSyncing || mc.savedWorkerTracked || mc.savedWorkerBestDiff != 0 {
		t.Fatalf("cache survived unregister: hash=%q syncing=%v tracked=%v best=%v",
			mc.registeredWorkerHash, mc.savedWorkerSyncing, mc.savedWorkerTracked, mc.savedWorkerBestDiff)
	}
}

func TestSavedWorkerSyncClearsMatchingGenerationOnLookupExit(t *testing.T) {
	const worker = "bc1qexampleaddress00000000000000000000000000.lookup-exit"
	hash := workerNameHash(worker)

	tests := []struct {
		name  string
		store func(t *testing.T) *workerListStore
	}{
		{name: "nil store"},
		{
			name: "lookup error",
			store: func(t *testing.T) *workerListStore {
				store, err := newWorkerListStore(t.TempDir() + "/saved_workers.sqlite")
				if err != nil {
					t.Fatalf("newWorkerListStore: %v", err)
				}
				if err := store.Close(); err != nil {
					t.Fatalf("store.Close: %v", err)
				}
				return store
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var store *workerListStore
			if tt.store != nil {
				store = tt.store(t)
			}
			mc := &MinerConn{
				savedWorkerStore:     store,
				registeredWorker:     worker,
				registeredWorkerHash: hash,
			}
			mc.savedWorkerMu.Lock()
			generation := mc.beginSavedWorkerSyncLocked()
			mc.savedWorkerMu.Unlock()

			mc.lookupSavedWorkerState(hash, generation)

			mc.savedWorkerMu.Lock()
			defer mc.savedWorkerMu.Unlock()
			if mc.savedWorkerSyncing {
				t.Fatal("saved-worker cache remained syncing after lookup exit")
			}
			if mc.savedWorkerSyncGen != generation {
				t.Fatalf("generation = %d, want %d", mc.savedWorkerSyncGen, generation)
			}
		})
	}
}

func TestSavedWorkerUpdatesStayBoundDuringReauthorization(t *testing.T) {
	store, err := newWorkerListStore(t.TempDir() + "/saved_workers.sqlite")
	if err != nil {
		t.Fatalf("newWorkerListStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	mc := benchmarkMinerConnForSubmit(nil)
	workerA := mc.currentWorker()
	workerB, _, _ := generateTestWorker(t)
	const userID = "user-reauth"
	if err := store.Add(userID, workerA); err != nil {
		t.Fatalf("save worker A: %v", err)
	}
	if err := store.Add(userID, workerB); err != nil {
		t.Fatalf("save worker B: %v", err)
	}
	mc.savedWorkerStore = store
	mc.workerRegistry = newWorkerConnectionRegistry()
	mc.assignConnectionSeq()
	mc.registerWorker(workerA)

	now := time.Now().UTC()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			mc.updateWorker(workerB)
			mc.registerWorker(workerB)
			mc.updateWorker(workerA)
			mc.registerWorker(workerA)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			mc.maybeUpdateSavedWorkerBestDiffFor(workerA, 200)
			mc.maybeUpdateSavedWorkerMinuteBestDiffFor(workerA, 200, now)
		}
	}()
	wg.Wait()

	hashA := workerNameHash(workerA)
	hashB := workerNameHash(workerB)
	bestA, trackedA, err := store.BestDifficultyForHash(hashA)
	if err != nil {
		t.Fatalf("worker A best lookup: %v", err)
	}
	bestB, trackedB, err := store.BestDifficultyForHash(hashB)
	if err != nil {
		t.Fatalf("worker B best lookup: %v", err)
	}
	if !trackedA || !trackedB {
		t.Fatalf("saved tracking = (%v, %v), want both true", trackedA, trackedB)
	}
	if bestA != 200 {
		t.Fatalf("worker A best = %v, want 200", bestA)
	}
	if bestB != 0 {
		t.Fatalf("worker B received wallet A best difficulty: %v", bestB)
	}
	if got := store.SavedWorkerMinuteBestDifficulty(hashA, now); got <= 0 {
		t.Fatalf("worker A minute best = %v, want positive", got)
	}
	if got := store.SavedWorkerMinuteBestDifficulty(hashB, now); got != 0 {
		t.Fatalf("worker B received wallet A minute best: %v", got)
	}
}
