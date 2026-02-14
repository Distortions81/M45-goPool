package main

import (
	"testing"
	"time"
)

func TestWorkerListStore_NormalizeSavedWorkersStorage_ScrubsRawWorkerNames(t *testing.T) {
	dbPath := t.TempDir() + "/workers.db"
	db, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("openStateDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	store := &workerListStore{db: db, ownsDB: false}

	userID := "user_1"
	worker := "TestWorker"
	expectedHash := workerNameHash(worker)
	if expectedHash == "" {
		t.Fatalf("expected non-empty workerNameHash")
	}

	if _, err := db.Exec("INSERT INTO saved_workers (user_id, worker, worker_hash, notify_enabled, best_difficulty) VALUES (?, ?, '', 1, 0)", userID, worker); err != nil {
		t.Fatalf("insert saved_worker: %v", err)
	}

	done := make(chan struct{})
	var gotErr error
	go func() {
		gotErr = normalizeSavedWorkersStorage(store.db)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("normalizeSavedWorkersStorage appears to be stuck (possible DB deadlock)")
	}
	if gotErr != nil {
		t.Fatalf("normalizeSavedWorkersStorage error: %v", gotErr)
	}

	var (
		backfilledHash string
		storedWorker   string
	)
	if err := db.QueryRow("SELECT COALESCE(worker_hash, ''), COALESCE(worker, '') FROM saved_workers WHERE user_id = ? AND worker_hash = ?", userID, expectedHash).Scan(&backfilledHash, &storedWorker); err != nil {
		t.Fatalf("query normalized row: %v", err)
	}
	if backfilledHash != expectedHash {
		t.Fatalf("worker_hash not backfilled: got %q want %q", backfilledHash, expectedHash)
	}
	if storedWorker != expectedHash {
		t.Fatalf("worker column was not scrubbed to hash: got %q want %q", storedWorker, expectedHash)
	}
}
