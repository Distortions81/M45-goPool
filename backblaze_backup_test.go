package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func createTestWorkerDB(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir db dir: %v", err)
	}
	db, err := sql.Open("sqlite", path+"?_foreign_keys=1")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS test_table (id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO test_table(v) VALUES ("a")`); err != nil {
		t.Fatalf("insert: %v", err)
	}
}

func TestBackups_WriteStampAndLocalSnapshot_DefaultPath(t *testing.T) {
	tmp := t.TempDir()
	cfg := defaultConfig()
	cfg.DataDir = tmp
	cfg.BackblazeBackupEnabled = false
	cfg.BackblazeKeepLocalCopy = true
	cfg.BackupSnapshotPath = ""
	cfg.BackblazeBackupIntervalSeconds = 1

	dbPath := filepath.Join(cfg.DataDir, "state", "workers.db")
	createTestWorkerDB(t, dbPath)

	// Set up the shared DB for this test
	db, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("openStateDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	cleanup := setSharedStateDBForTest(db)
	t.Cleanup(cleanup)

	svc, err := newBackblazeBackupService(context.Background(), cfg, dbPath)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	if svc == nil {
		t.Fatalf("expected service")
	}
	svc.run(context.Background())

	// Use the shared DB that was set up earlier
	ts, _, err := readLastBackupStampFromDB(db, backupStateKeyWorkerDB)
	if err != nil {
		t.Fatalf("read sqlite stamp: %v", err)
	}
	if ts.IsZero() {
		t.Fatalf("expected non-zero sqlite stamp time")
	}

	if svc.snapshotPath == "" {
		t.Fatalf("expected snapshotPath")
	}
	if _, err := os.Stat(svc.snapshotPath); err != nil {
		t.Fatalf("snapshot file missing: %v", err)
	}
}

func TestBackups_SnapshotPathOverride_RelativeToDataDir(t *testing.T) {
	tmp := t.TempDir()
	cfg := defaultConfig()
	cfg.DataDir = tmp
	cfg.BackblazeBackupEnabled = false
	cfg.BackblazeKeepLocalCopy = false
	cfg.BackupSnapshotPath = filepath.Join("snapshots", "workers.snapshot.db")
	cfg.BackblazeBackupIntervalSeconds = 1

	dbPath := filepath.Join(cfg.DataDir, "state", "workers.db")
	createTestWorkerDB(t, dbPath)

	// Set up the shared DB for this test
	db, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("openStateDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	cleanup := setSharedStateDBForTest(db)
	t.Cleanup(cleanup)

	svc, err := newBackblazeBackupService(context.Background(), cfg, dbPath)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	if svc == nil {
		t.Fatalf("expected service")
	}
	svc.run(context.Background())

	wantSnapshot := filepath.Join(cfg.DataDir, "snapshots", "workers.snapshot.db")
	if svc.snapshotPath != wantSnapshot {
		t.Fatalf("snapshotPath mismatch: got %q want %q", svc.snapshotPath, wantSnapshot)
	}
	if _, err := os.Stat(wantSnapshot); err != nil {
		t.Fatalf("snapshot file missing: %v", err)
	}
	// Use the shared DB that was set up earlier
	ts, _, err := readLastBackupStampFromDB(db, backupStateKeyWorkerDB)
	if err != nil {
		t.Fatalf("read sqlite stamp: %v", err)
	}
	if ts.IsZero() {
		t.Fatalf("expected non-zero sqlite stamp time")
	}
}

func TestSnapshotWorkerDB_CreatesCopy(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "state", "workers.db")
	createTestWorkerDB(t, dbPath)

	snap, _, err := snapshotWorkerDB(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("snapshotWorkerDB: %v", err)
	}
	defer os.Remove(snap)
	if _, err := os.Stat(snap); err != nil {
		t.Fatalf("snapshot missing: %v", err)
	}
}
