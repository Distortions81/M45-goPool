package main

import (
	"testing"
	"time"
)

func TestWorkerListStore_ListAllSavedWorkers_DoesNotDeadlockOnHashBackfill(t *testing.T) {
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
		_, gotErr = store.ListAllSavedWorkers()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("ListAllSavedWorkers appears to be stuck (possible DB deadlock)")
	}
	if gotErr != nil {
		t.Fatalf("ListAllSavedWorkers error: %v", gotErr)
	}

	var backfilled string
	if err := db.QueryRow("SELECT COALESCE(worker_hash, '') FROM saved_workers WHERE user_id = ? AND worker = ?", userID, worker).Scan(&backfilled); err != nil {
		t.Fatalf("query backfilled hash: %v", err)
	}
	if backfilled != expectedHash {
		t.Fatalf("worker_hash not backfilled: got %q want %q", backfilled, expectedHash)
	}
}
