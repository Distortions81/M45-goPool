package main

import (
	"database/sql"
	"os"
	"strings"
	"time"
)

func newWorkerListStore(path string) (*workerListStore, error) {
	// Prefer the shared state DB to avoid multiple concurrent connections to
	// the same SQLite file (modernc.org/sqlite can corrupt the page cache).
	if db := getSharedStateDB(); db != nil {
		store := &workerListStore{db: db, ownsDB: false}
		store.startBestDiffWorker(10 * time.Second)
		return store, nil
	}

	if strings.TrimSpace(path) == "" {
		return nil, os.ErrInvalid
	}
	db, err := openStateDB(path)
	if err != nil {
		return nil, err
	}
	store := &workerListStore{db: db, ownsDB: true}
	store.startBestDiffWorker(10 * time.Second)
	return store, nil
}

func addSavedWorkersHashColumn(db *sql.DB) error {
	if db == nil {
		return nil
	}
	_, err := db.Exec("ALTER TABLE saved_workers ADD COLUMN worker_hash TEXT")
	if err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return err
	}
	// Backfill existing rows created before worker_hash existed.
	if _, err := db.Exec("UPDATE saved_workers SET worker_hash = '' WHERE worker_hash IS NULL"); err != nil {
		return err
	}
	return nil
}

func addSavedWorkersNotifyEnabledColumn(db *sql.DB) error {
	if db == nil {
		return nil
	}
	_, err := db.Exec("ALTER TABLE saved_workers ADD COLUMN notify_enabled INTEGER NOT NULL DEFAULT 1")
	if err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return err
	}
	return nil
}

func addSavedWorkersBestDifficultyColumn(db *sql.DB) error {
	if db == nil {
		return nil
	}
	_, err := db.Exec("ALTER TABLE saved_workers ADD COLUMN best_difficulty REAL NOT NULL DEFAULT 0")
	if err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return err
	}
	return nil
}

func addDiscordWorkerStateOfflineEligibleColumn(db *sql.DB) error {
	if db == nil {
		return nil
	}
	_, err := db.Exec("ALTER TABLE discord_worker_state ADD COLUMN offline_eligible INTEGER NOT NULL DEFAULT 0")
	if err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return err
	}
	return nil
}
