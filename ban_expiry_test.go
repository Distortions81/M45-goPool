package main

import (
	"path/filepath"
	"testing"
	"time"
)

// TestBanListPersistPrunesExpired verifies that persistLocked drops expired
// bans from the in-memory map and from the on-disk file, while preserving
// permanent and still-active entries.
func TestBanListPersistPrunesExpired(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state", "workers.db")
	db, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("openStateDB: %v", err)
	}
	defer db.Close()

	// Set up the shared DB for this test
	cleanup := setSharedStateDBForTest(db)
	defer cleanup()

	now := time.Now()
	expiredTime := now.Add(-time.Hour)
	futureTime := now.Add(time.Hour)

	// Insert expired, permanent, and active bans.
	if _, err := db.Exec(`INSERT INTO bans (worker, worker_hash, until_unix, reason, updated_at_unix) VALUES (?, ?, ?, ?, ?)`,
		"expired", workerNameHash("expired"), expiredTime.Unix(), "too many invalid shares", now.Unix()); err != nil {
		t.Fatalf("insert expired ban: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO bans (worker, worker_hash, until_unix, reason, updated_at_unix) VALUES (?, ?, ?, ?, ?)`,
		"permanent", workerNameHash("permanent"), int64(0), "manual ban", now.Unix()); err != nil {
		t.Fatalf("insert permanent ban: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO bans (worker, worker_hash, until_unix, reason, updated_at_unix) VALUES (?, ?, ?, ?, ?)`,
		"active", workerNameHash("active"), futureTime.Unix(), "temporary ban", now.Unix()); err != nil {
		t.Fatalf("insert active ban: %v", err)
	}

	bans := &banStore{}
	if err := bans.cleanExpired(now); err != nil {
		t.Fatalf("cleanExpired error: %v", err)
	}

	seen := make(map[string]banEntry)
	for _, b := range bans.snapshot(now) {
		seen[b.Worker] = b
	}
	if _, ok := seen["expired"]; ok {
		t.Fatalf("expired ban should be pruned")
	}
	if p, ok := seen["permanent"]; !ok {
		t.Fatalf("permanent ban missing")
	} else if !p.Until.IsZero() {
		t.Fatalf("permanent ban should have zero Until, got %v", p.Until)
	}
	if a, ok := seen["active"]; !ok {
		t.Fatalf("active ban missing")
	} else if !a.Until.After(now) {
		t.Fatalf("active ban Until should be in the future, got %v (now %v)", a.Until, now)
	}
}
