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
