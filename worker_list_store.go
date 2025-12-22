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
			PRIMARY KEY(user_id, worker)
		)
	`); err != nil {
		_ = db.Close()
		return nil, err
	}

	return &workerListStore{db: db}, nil
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

	_, err := s.db.Exec("INSERT OR IGNORE INTO saved_workers (user_id, worker) VALUES (?, ?)", userID, worker)
	return err
}

func (s *workerListStore) List(userID string) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, nil
	}
	rows, err := s.db.Query("SELECT worker FROM saved_workers WHERE user_id = ? ORDER BY worker COLLATE NOCASE", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var workers []string
	for rows.Next() {
		var worker string
		if err := rows.Scan(&worker); err != nil {
			return nil, err
		}
		workers = append(workers, worker)
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
	_, err := s.db.Exec("DELETE FROM saved_workers WHERE user_id = ? AND worker = ?", userID, worker)
	return err
}

func (s *workerListStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}
