package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func setBackblazeTestClock(svc *backblazeBackupService, start time.Time) func(time.Duration) {
	now := start
	svc.now = func() time.Time { return now }
	return func(d time.Duration) { now = now.Add(d) }
}

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
	svc.RunOnce(context.Background(), "test", true)

	// Use the shared DB that was set up earlier
	ts, _, err := readLastBackupStampFromDB(db, backupStateKeyWorkerDBSnapshot)
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
	svc.RunOnce(context.Background(), "test", true)

	wantSnapshot := filepath.Join(cfg.DataDir, "snapshots", "workers.snapshot.db")
	if svc.snapshotPath != wantSnapshot {
		t.Fatalf("snapshotPath mismatch: got %q want %q", svc.snapshotPath, wantSnapshot)
	}
	if _, err := os.Stat(wantSnapshot); err != nil {
		t.Fatalf("snapshot file missing: %v", err)
	}
	// Use the shared DB that was set up earlier
	ts, _, err := readLastBackupStampFromDB(db, backupStateKeyWorkerDBSnapshot)
	if err != nil {
		t.Fatalf("read sqlite stamp: %v", err)
	}
	if ts.IsZero() {
		t.Fatalf("expected non-zero sqlite stamp time")
	}
}

func TestBackups_CloudEnabledWithoutCredentials_DoesNotStartServiceWhenNoLocalBackupConfigured(t *testing.T) {
	tmp := t.TempDir()
	cfg := defaultConfig()
	cfg.DataDir = tmp
	cfg.BackblazeBackupEnabled = true
	cfg.BackblazeKeepLocalCopy = false
	cfg.BackupSnapshotPath = ""
	cfg.BackblazeBackupIntervalSeconds = 1
	// Deliberately omit credentials so cloud backup is considered unconfigured.
	cfg.BackblazeBucket = ""
	cfg.BackblazeAccountID = ""
	cfg.BackblazeApplicationKey = ""

	dbPath := filepath.Join(cfg.DataDir, "state", "workers.db")
	createTestWorkerDB(t, dbPath)

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
	if svc != nil {
		t.Fatalf("expected nil service when cloud backup is incomplete and local backup is disabled")
	}
}

func TestBackups_RunOnInterval_EvenWhenDBUnchanged(t *testing.T) {
	tmp := t.TempDir()
	cfg := defaultConfig()
	cfg.DataDir = tmp
	cfg.BackblazeBackupEnabled = false
	cfg.BackblazeKeepLocalCopy = true
	cfg.BackblazeForceEveryInterval = true
	cfg.BackupSnapshotPath = ""
	cfg.BackblazeBackupIntervalSeconds = 1

	dbPath := filepath.Join(cfg.DataDir, "state", "workers.db")
	createTestWorkerDB(t, dbPath)

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
	advance := setBackblazeTestClock(svc, time.Unix(1_700_000_000, 0))

	// First backup.
	svc.RunOnce(context.Background(), "test", true)
	ts1, _, err := readLastBackupStampFromDB(db, backupStateKeyWorkerDBSnapshot)
	if err != nil {
		t.Fatalf("read sqlite stamp: %v", err)
	}
	if ts1.IsZero() {
		t.Fatalf("expected non-zero sqlite stamp time")
	}

	// Second backup with no DB writes. Ensure the stamp advances even if the DB
	// is unchanged (interval-driven backups).
	advance(1100 * time.Millisecond)
	svc.RunOnce(context.Background(), "test", true)
	ts2, _, err := readLastBackupStampFromDB(db, backupStateKeyWorkerDBSnapshot)
	if err != nil {
		t.Fatalf("read sqlite stamp: %v", err)
	}
	if !ts2.After(ts1) {
		t.Fatalf("expected stamp to advance: ts1=%v ts2=%v", ts1, ts2)
	}
}

func TestBackups_DefaultSkipsWhenDBUnchanged(t *testing.T) {
	tmp := t.TempDir()
	cfg := defaultConfig()
	cfg.DataDir = tmp
	cfg.BackblazeBackupEnabled = false
	cfg.BackblazeKeepLocalCopy = true
	cfg.BackblazeForceEveryInterval = false
	cfg.BackupSnapshotPath = ""
	cfg.BackblazeBackupIntervalSeconds = 1

	dbPath := filepath.Join(cfg.DataDir, "state", "workers.db")
	createTestWorkerDB(t, dbPath)

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
	advance := setBackblazeTestClock(svc, time.Unix(1_700_000_100, 0))

	// First backup.
	svc.RunOnce(context.Background(), "test", true)
	ts1, dv1, err := readLastBackupStampFromDB(db, backupStateKeyWorkerDBSnapshot)
	if err != nil {
		t.Fatalf("read sqlite stamp: %v", err)
	}
	if ts1.IsZero() || dv1 == 0 {
		t.Fatalf("expected non-zero stamp and data_version, got ts=%v dv=%d", ts1, dv1)
	}

	// Second run after interval with no DB writes should be skipped.
	advance(1100 * time.Millisecond)
	svc.RunOnce(context.Background(), "test", false)
	ts2, dv2, err := readLastBackupStampFromDB(db, backupStateKeyWorkerDBSnapshot)
	if err != nil {
		t.Fatalf("read sqlite stamp: %v", err)
	}
	if !ts2.Equal(ts1) {
		t.Fatalf("expected stamp unchanged when DB unchanged: ts1=%v ts2=%v", ts1, ts2)
	}
	if dv2 != dv1 {
		t.Fatalf("expected data_version unchanged when DB unchanged: dv1=%d dv2=%d", dv1, dv2)
	}
}

func TestBackups_DefaultRunsWhenDBChanged(t *testing.T) {
	tmp := t.TempDir()
	cfg := defaultConfig()
	cfg.DataDir = tmp
	cfg.BackblazeBackupEnabled = false
	cfg.BackblazeKeepLocalCopy = true
	cfg.BackblazeForceEveryInterval = false
	cfg.BackupSnapshotPath = ""
	cfg.BackblazeBackupIntervalSeconds = 1

	dbPath := filepath.Join(cfg.DataDir, "state", "workers.db")
	createTestWorkerDB(t, dbPath)

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
	advance := setBackblazeTestClock(svc, time.Unix(1_700_000_200, 0))

	// First backup.
	svc.RunOnce(context.Background(), "test", true)
	ts1, dv1, err := readLastBackupStampFromDB(db, backupStateKeyWorkerDBSnapshot)
	if err != nil {
		t.Fatalf("read sqlite stamp: %v", err)
	}
	if ts1.IsZero() || dv1 == 0 {
		t.Fatalf("expected non-zero stamp and data_version, got ts=%v dv=%d", ts1, dv1)
	}

	// Update tracked state and expect a non-forced run to snapshot again.
	if _, err := db.Exec(`INSERT INTO bans(worker, worker_hash, until_unix, reason, updated_at_unix) VALUES (?, ?, ?, ?, ?)`,
		"worker-1", "hash-1", time.Now().Add(5*time.Minute).Unix(), "test", time.Now().Unix()); err != nil {
		t.Fatalf("insert ban row: %v", err)
	}

	advance(1100 * time.Millisecond)
	svc.RunOnce(context.Background(), "test", false)
	ts2, dv2, err := readLastBackupStampFromDB(db, backupStateKeyWorkerDBSnapshot)
	if err != nil {
		t.Fatalf("read sqlite stamp: %v", err)
	}
	if dv2 == dv1 {
		t.Fatalf("expected data_version token to change when DB changed: dv1=%d dv2=%d", dv1, dv2)
	}
	if ts2.Before(ts1) {
		t.Fatalf("expected snapshot stamp to stay same or advance when DB changed: ts1=%v ts2=%v", ts1, ts2)
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
