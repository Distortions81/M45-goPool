package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

const maxSavedWorkersPerUser = 64

type workerListStore struct {
	db *sql.DB
}

func newWorkerListStore(path string) (*workerListStore, error) {
	if path == "" {
		return nil, os.ErrInvalid
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", path+"?_foreign_keys=1&_journal=WAL")
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS saved_workers (
			user_id TEXT NOT NULL,
			worker TEXT NOT NULL,
			worker_hash TEXT,
			PRIMARY KEY(user_id, worker)
		)
	`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := addSavedWorkersHashColumn(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS saved_workers_hash_idx ON saved_workers (user_id, worker_hash)`); err != nil {
		_ = db.Close()
		return nil, err
	}

	return &workerListStore{db: db}, nil
}

func addSavedWorkersHashColumn(db *sql.DB) error {
	if db == nil {
		return nil
	}
	_, err := db.Exec("ALTER TABLE saved_workers ADD COLUMN worker_hash TEXT")
	if err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return err
	}
	return nil
}

func (s *workerListStore) Add(userID, worker string) error {
	if s == nil || s.db == nil {
		return nil
	}
	userID = strings.TrimSpace(userID)
	worker = strings.TrimSpace(worker)
	if userID == "" || worker == "" {
		return nil
	}
	if len(worker) > workerLookupMaxBytes {
		return nil
	}

	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM saved_workers WHERE user_id = ?", userID).Scan(&count); err != nil {
		return err
	}
	if count >= maxSavedWorkersPerUser {
		return nil
	}

	hash := workerNameHash(worker)
	if _, err := s.db.Exec("INSERT OR IGNORE INTO saved_workers (user_id, worker, worker_hash) VALUES (?, ?, ?)", userID, worker, hash); err != nil {
		return err
	}
	_, err := s.db.Exec("UPDATE saved_workers SET worker_hash = ? WHERE user_id = ? AND worker = ? AND (worker_hash IS NULL OR worker_hash = '')", hash, userID, worker)
	return err
}

func (s *workerListStore) List(userID string) ([]SavedWorkerEntry, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, nil
	}
	rows, err := s.db.Query("SELECT worker, worker_hash FROM saved_workers WHERE user_id = ? ORDER BY worker COLLATE NOCASE", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var workers []SavedWorkerEntry
	for rows.Next() {
		var entry SavedWorkerEntry
		if err := rows.Scan(&entry.Name, &entry.Hash); err != nil {
			return nil, err
		}
		entry.Hash = strings.TrimSpace(entry.Hash)
		if entry.Hash == "" {
			entry.Hash = workerNameHash(entry.Name)
			if entry.Hash != "" {
				_, _ = s.db.Exec("UPDATE saved_workers SET worker_hash = ? WHERE user_id = ? AND worker = ?", entry.Hash, userID, entry.Name)
			}
		}
		workers = append(workers, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return workers, nil
}

func (s *workerListStore) Remove(userID, worker string) error {
	if s == nil || s.db == nil {
		return nil
	}
	userID = strings.TrimSpace(userID)
	worker = strings.TrimSpace(worker)
	if userID == "" || worker == "" {
		return nil
	}
	if len(worker) > workerLookupMaxBytes {
		return nil
	}
	hash := workerNameHash(worker)
	if hash != "" {
		_, err := s.db.Exec("DELETE FROM saved_workers WHERE user_id = ? AND worker_hash = ?", userID, hash)
		return err
	}
	_, err := s.db.Exec("DELETE FROM saved_workers WHERE user_id = ? AND worker = ?", userID, worker)
	return err
}

func (s *workerListStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}
