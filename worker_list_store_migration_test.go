package main

import "testing"

func TestNormalizeSavedWorkersStorage_DedupesAndScrubsEntries(t *testing.T) {
	dbPath := t.TempDir() + "/workers.db"
	db, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("openStateDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	userID := "user_1"
	worker := "bc1qexampleaddress00000000000000000000000000.workerA"
	hash := workerNameHash(worker)
	if hash == "" {
		t.Fatalf("expected non-empty worker hash")
	}
	censored := shortWorkerName(worker, workerNamePrefix, workerNameSuffix)

	// Old-style row: raw worker in `worker`, missing hash/display.
	if _, err := db.Exec(
		"INSERT INTO saved_workers (user_id, worker, worker_hash, worker_display, notify_enabled, best_difficulty) VALUES (?, ?, '', '', 0, 5.0)",
		userID, worker,
	); err != nil {
		t.Fatalf("insert old-style row: %v", err)
	}
	// New-style duplicate row for same hash with stronger flags/stats.
	if _, err := db.Exec(
		"INSERT INTO saved_workers (user_id, worker, worker_hash, worker_display, notify_enabled, best_difficulty) VALUES (?, ?, ?, '', 1, 12.5)",
		userID, hash, hash,
	); err != nil {
		t.Fatalf("insert duplicate hash row: %v", err)
	}

	if err := normalizeSavedWorkersStorage(db); err != nil {
		t.Fatalf("normalizeSavedWorkersStorage: %v", err)
	}

	var (
		count       int
		gotWorker   string
		gotHash     string
		gotDisplay  string
		gotNotify   int
		gotBestDiff float64
	)
	if err := db.QueryRow(`
		SELECT COUNT(*), COALESCE(worker, ''), COALESCE(worker_hash, ''), COALESCE(worker_display, ''), notify_enabled, best_difficulty
		FROM saved_workers
		WHERE user_id = ? AND worker_hash = ?
	`, userID, hash).Scan(&count, &gotWorker, &gotHash, &gotDisplay, &gotNotify, &gotBestDiff); err != nil {
		t.Fatalf("query normalized row: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 normalized row, got %d", count)
	}
	if gotWorker != hash {
		t.Fatalf("worker column should be hash identity: got %q want %q", gotWorker, hash)
	}
	if gotHash != hash {
		t.Fatalf("worker_hash mismatch: got %q want %q", gotHash, hash)
	}
	if gotDisplay != censored {
		t.Fatalf("worker_display mismatch: got %q want %q", gotDisplay, censored)
	}
	if gotNotify != 1 {
		t.Fatalf("notify_enabled merge mismatch: got %d want 1", gotNotify)
	}
	if gotBestDiff != 12.5 {
		t.Fatalf("best_difficulty merge mismatch: got %v want 12.5", gotBestDiff)
	}

	var rawCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM saved_workers WHERE worker LIKE '%.%'").Scan(&rawCount); err != nil {
		t.Fatalf("query raw worker rows: %v", err)
	}
	if rawCount != 0 {
		t.Fatalf("expected no raw wallet.worker strings in saved_workers.worker, found %d", rawCount)
	}
}
