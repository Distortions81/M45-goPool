package main

import (
	"path/filepath"
	"testing"
)

func TestWorkerListStore_BestDifficultyForHashTracksSavedWorker(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "workers.db")
	store, err := newWorkerListStore(dbPath)
	if err != nil {
		t.Fatalf("newWorkerListStore: %v", err)
	}

	const (
		userID = "user1"
		worker = "bc1qexample.worker1"
	)
	if err := store.Add(userID, worker); err != nil {
		t.Fatalf("Add: %v", err)
	}

	hash := workerNameHash(worker)
	if hash == "" {
		t.Fatalf("workerNameHash(%q) empty", worker)
	}

	best, ok, err := store.BestDifficultyForHash(hash)
	if err != nil {
		t.Fatalf("BestDifficultyForHash: %v", err)
	}
	if !ok {
		t.Fatalf("BestDifficultyForHash ok=false; want true for saved worker")
	}
	if best != 0 {
		t.Fatalf("BestDifficultyForHash best=%v; want 0 initially", best)
	}

	if updated, err := store.UpdateSavedWorkerBestDifficulty(hash, 1234); err != nil {
		t.Fatalf("UpdateSavedWorkerBestDifficulty: %v", err)
	} else if !updated {
		t.Fatalf("UpdateSavedWorkerBestDifficulty updated=false; want true")
	}

	best, ok, err = store.BestDifficultyForHash(hash)
	if err != nil {
		t.Fatalf("BestDifficultyForHash after update: %v", err)
	}
	if !ok {
		t.Fatalf("BestDifficultyForHash ok=false after update; want true")
	}
	if best != 1234 {
		t.Fatalf("BestDifficultyForHash best=%v; want 1234 after update", best)
	}

	// Close flushes pending best difficulty to the DB.
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	store2, err := newWorkerListStore(dbPath)
	if err != nil {
		t.Fatalf("newWorkerListStore(reopen): %v", err)
	}
	defer store2.Close()

	list, err := store2.List(userID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List len=%d; want 1", len(list))
	}
	if list[0].BestDifficulty != 1234 {
		t.Fatalf("List BestDifficulty=%v; want 1234", list[0].BestDifficulty)
	}
}
