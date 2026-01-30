package main

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestWorkerListStoreHandlesNullWorkerHash(t *testing.T) {
	db, err := openStateDB(filepath.Join(t.TempDir(), "workers.db"))
	if err != nil {
		t.Fatalf("openStateDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	const (
		userID  = "user_123"
		worker  = "Example.Worker"
		enabled = 1
		best    = 0.0
	)
	if _, err := db.Exec(
		"INSERT INTO saved_workers (user_id, worker, worker_hash, notify_enabled, best_difficulty) VALUES (?, ?, ?, ?, ?)",
		userID,
		worker,
		nil, // worker_hash intentionally NULL to simulate pre-migration rows.
		enabled,
		best,
	); err != nil {
		t.Fatalf("insert saved_workers: %v", err)
	}

	store := &workerListStore{db: db, ownsDB: false}

	records, err := store.ListAllSavedWorkers()
	if err != nil {
		t.Fatalf("ListAllSavedWorkers: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	expectedHash := workerNameHash(worker)
	if records[0].Hash != expectedHash {
		t.Fatalf("expected hash %q, got %q", expectedHash, records[0].Hash)
	}

	entries, err := store.List(userID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Hash != expectedHash {
		t.Fatalf("expected hash %q, got %q", expectedHash, entries[0].Hash)
	}

	var dbHash sql.NullString
	if err := db.QueryRow("SELECT worker_hash FROM saved_workers WHERE user_id = ? AND worker = ?", userID, worker).Scan(&dbHash); err != nil {
		t.Fatalf("select worker_hash: %v", err)
	}
	if !dbHash.Valid || dbHash.String != expectedHash {
		t.Fatalf("expected persisted worker_hash %q, got valid=%v value=%q", expectedHash, dbHash.Valid, dbHash.String)
	}
}

