package main

import "testing"

func TestMaybeUpdateSavedWorkerBestDiff_PicksUpWorkerSavedAfterConnect(t *testing.T) {
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

	// After the worker is saved, the next accepted share should lazily start
	// tracking and update the saved best difficulty without a reconnect.
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
