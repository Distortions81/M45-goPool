package main

import "testing"

func TestWorkerListStore_AddStoresHashIdentityAndCensoredDisplay(t *testing.T) {
	store, err := newWorkerListStore(t.TempDir() + "/saved_workers.sqlite")
	if err != nil {
		t.Fatalf("newWorkerListStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const (
		userID = "user_1"
		worker = "bc1qexampleaddress00000000000000000000000000.worker-01"
	)
	wantHash := workerNameHash(worker)
	if wantHash == "" {
		t.Fatalf("expected non-empty hash")
	}
	wantDisplay := shortWorkerName(worker, workerNamePrefix, workerNameSuffix)

	if err := store.Add(userID, worker); err != nil {
		t.Fatalf("store.Add: %v", err)
	}

	var gotWorker, gotHash, gotDisplay string
	if err := store.db.QueryRow(`
		SELECT COALESCE(worker, ''), COALESCE(worker_hash, ''), COALESCE(worker_display, '')
		FROM saved_workers
		WHERE user_id = ? AND worker_hash = ?
	`, userID, wantHash).Scan(&gotWorker, &gotHash, &gotDisplay); err != nil {
		t.Fatalf("query row: %v", err)
	}

	if gotWorker != wantHash {
		t.Fatalf("worker column should store hash identity: got %q want %q", gotWorker, wantHash)
	}
	if gotHash != wantHash {
		t.Fatalf("worker_hash mismatch: got %q want %q", gotHash, wantHash)
	}
	if gotDisplay != wantDisplay {
		t.Fatalf("worker_display mismatch: got %q want %q", gotDisplay, wantDisplay)
	}
	if gotWorker == worker {
		t.Fatalf("raw worker name should not be stored in worker column")
	}
}
