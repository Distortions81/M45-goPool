package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const maxSavedWorkersPerUser = 64

type workerListStore struct {
	db *sql.DB
}

type discordLink struct {
	UserID        string
	DiscordUserID string
	Enabled       bool
	LinkedAt      time.Time
	UpdatedAt     time.Time
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
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS discord_links (
			user_id TEXT PRIMARY KEY,
			discord_user_id TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			linked_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)
	`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS discord_links_discord_user_idx ON discord_links (discord_user_id)`); err != nil {
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
		} else {
			lower := strings.ToLower(entry.Hash)
			if lower != entry.Hash {
				entry.Hash = lower
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

func (s *workerListStore) UpsertDiscordLink(userID, discordUserID string, enabled bool, now time.Time) error {
	if s == nil || s.db == nil {
		return nil
	}
	userID = strings.TrimSpace(userID)
	discordUserID = strings.TrimSpace(discordUserID)
	if userID == "" || discordUserID == "" {
		return nil
	}
	enabledInt := 0
	if enabled {
		enabledInt = 1
	}
	ts := now.Unix()
	_, err := s.db.Exec(`
		INSERT INTO discord_links (user_id, discord_user_id, enabled, linked_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			discord_user_id = excluded.discord_user_id,
			enabled = excluded.enabled,
			updated_at = excluded.updated_at
	`, userID, discordUserID, enabledInt, ts, ts)
	return err
}

func (s *workerListStore) DisableDiscordLinkByDiscordUserID(discordUserID string, now time.Time) error {
	if s == nil || s.db == nil {
		return nil
	}
	discordUserID = strings.TrimSpace(discordUserID)
	if discordUserID == "" {
		return nil
	}
	_, err := s.db.Exec("UPDATE discord_links SET enabled = 0, updated_at = ? WHERE discord_user_id = ?", now.Unix(), discordUserID)
	return err
}

func (s *workerListStore) ListEnabledDiscordLinks() ([]discordLink, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	rows, err := s.db.Query("SELECT user_id, discord_user_id, enabled, linked_at, updated_at FROM discord_links WHERE enabled = 1 ORDER BY updated_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []discordLink
	for rows.Next() {
		var (
			entry         discordLink
			enabledInt    int
			linkedAtUnix  int64
			updatedAtUnix int64
		)
		if err := rows.Scan(&entry.UserID, &entry.DiscordUserID, &enabledInt, &linkedAtUnix, &updatedAtUnix); err != nil {
			return nil, err
		}
		entry.UserID = strings.TrimSpace(entry.UserID)
		entry.DiscordUserID = strings.TrimSpace(entry.DiscordUserID)
		entry.Enabled = enabledInt != 0
		if linkedAtUnix > 0 {
			entry.LinkedAt = time.Unix(linkedAtUnix, 0)
		}
		if updatedAtUnix > 0 {
			entry.UpdatedAt = time.Unix(updatedAtUnix, 0)
		}
		out = append(out, entry)
	}
	return out, nil
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
