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
			notify_enabled INTEGER NOT NULL DEFAULT 1,
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
	if err := addSavedWorkersNotifyEnabledColumn(db); err != nil {
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
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS discord_worker_state (
			user_id TEXT NOT NULL,
			worker_hash TEXT NOT NULL,
			online INTEGER NOT NULL,
			since INTEGER NOT NULL,
			seen_online INTEGER NOT NULL,
			seen_offline INTEGER NOT NULL,
			offline_notified INTEGER NOT NULL,
			recovery_eligible INTEGER NOT NULL,
			recovery_notified INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY(user_id, worker_hash)
		)
	`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS discord_worker_state_user_idx ON discord_worker_state (user_id)`); err != nil {
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
	if _, err := s.db.Exec("INSERT OR IGNORE INTO saved_workers (user_id, worker, worker_hash, notify_enabled) VALUES (?, ?, ?, 1)", userID, worker, hash); err != nil {
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
	rows, err := s.db.Query("SELECT worker, worker_hash, notify_enabled FROM saved_workers WHERE user_id = ? ORDER BY worker COLLATE NOCASE", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var workers []SavedWorkerEntry
	for rows.Next() {
		var entry SavedWorkerEntry
		var notifyEnabledInt int
		if err := rows.Scan(&entry.Name, &entry.Hash, &notifyEnabledInt); err != nil {
			return nil, err
		}
		entry.NotifyEnabled = notifyEnabledInt != 0
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

func (s *workerListStore) SetSavedWorkerNotifyEnabled(userID, workerHash string, enabled bool, now time.Time) error {
	if s == nil || s.db == nil {
		return nil
	}
	userID = strings.TrimSpace(userID)
	workerHash = strings.ToLower(strings.TrimSpace(workerHash))
	if userID == "" || workerHash == "" {
		return nil
	}
	if len(workerHash) != 64 {
		return nil
	}
	val := 0
	if enabled {
		val = 1
	}
	_, err := s.db.Exec("UPDATE saved_workers SET notify_enabled = ? WHERE user_id = ? AND worker_hash = ?", val, userID, workerHash)
	return err
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

func (s *workerListStore) IsDiscordLinkEnabled(userID string) (bool, error) {
	if s == nil || s.db == nil {
		return false, nil
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return false, nil
	}
	var enabledInt int
	err := s.db.QueryRow("SELECT enabled FROM discord_links WHERE user_id = ?", userID).Scan(&enabledInt)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	return enabledInt != 0, nil
}

func (s *workerListStore) LoadDiscordWorkerStates(userID string) (map[string]workerNotifyState, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, nil
	}
	rows, err := s.db.Query(`
		SELECT worker_hash, online, since, seen_online, seen_offline, offline_notified, recovery_eligible, recovery_notified
		FROM discord_worker_state
		WHERE user_id = ?
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]workerNotifyState)
	for rows.Next() {
		var (
			hash            string
			onlineInt       int
			sinceUnix       int64
			seenOnlineInt   int
			seenOfflineInt  int
			offlineNotInt   int
			recoveryEligInt int
			recoveryNotInt  int
		)
		if err := rows.Scan(&hash, &onlineInt, &sinceUnix, &seenOnlineInt, &seenOfflineInt, &offlineNotInt, &recoveryEligInt, &recoveryNotInt); err != nil {
			return nil, err
		}
		hash = strings.TrimSpace(hash)
		if hash == "" {
			continue
		}
		st := workerNotifyState{
			Online:           onlineInt != 0,
			SeenOnline:       seenOnlineInt != 0,
			SeenOffline:      seenOfflineInt != 0,
			OfflineNotified:  offlineNotInt != 0,
			RecoveryEligible: recoveryEligInt != 0,
			RecoveryNotified: recoveryNotInt != 0,
		}
		if sinceUnix > 0 {
			st.Since = time.Unix(sinceUnix, 0)
		}
		out[hash] = st
	}
	return out, nil
}

func (s *workerListStore) PersistDiscordWorkerStates(userID string, upserts map[string]workerNotifyState, deletes []string, now time.Time) error {
	if s == nil || s.db == nil {
		return nil
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil
	}
	if len(upserts) == 0 && len(deletes) == 0 {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if len(deletes) > 0 {
		stmt, err := tx.Prepare("DELETE FROM discord_worker_state WHERE user_id = ? AND worker_hash = ?")
		if err != nil {
			return err
		}
		for _, h := range deletes {
			h = strings.TrimSpace(h)
			if h == "" {
				continue
			}
			if _, err := stmt.Exec(userID, h); err != nil {
				_ = stmt.Close()
				return err
			}
		}
		_ = stmt.Close()
	}

	if len(upserts) > 0 {
		stmt, err := tx.Prepare(`
			INSERT INTO discord_worker_state (
				user_id, worker_hash, online, since,
				seen_online, seen_offline,
				offline_notified, recovery_eligible, recovery_notified,
				updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(user_id, worker_hash) DO UPDATE SET
				online = excluded.online,
				since = excluded.since,
				seen_online = excluded.seen_online,
				seen_offline = excluded.seen_offline,
				offline_notified = excluded.offline_notified,
				recovery_eligible = excluded.recovery_eligible,
				recovery_notified = excluded.recovery_notified,
				updated_at = excluded.updated_at
		`)
		if err != nil {
			return err
		}
		ts := now.Unix()
		for h, st := range upserts {
			h = strings.TrimSpace(h)
			if h == "" {
				continue
			}
			onlineInt := 0
			if st.Online {
				onlineInt = 1
			}
			sinceUnix := ts
			if !st.Since.IsZero() {
				sinceUnix = st.Since.Unix()
			}
			seenOnline := 0
			if st.SeenOnline {
				seenOnline = 1
			}
			seenOffline := 0
			if st.SeenOffline {
				seenOffline = 1
			}
			offlineNot := 0
			if st.OfflineNotified {
				offlineNot = 1
			}
			recoveryElig := 0
			if st.RecoveryEligible {
				recoveryElig = 1
			}
			recoveryNot := 0
			if st.RecoveryNotified {
				recoveryNot = 1
			}
			if _, err := stmt.Exec(
				userID, h, onlineInt, sinceUnix,
				seenOnline, seenOffline,
				offlineNot, recoveryElig, recoveryNot,
				ts,
			); err != nil {
				_ = stmt.Close()
				return err
			}
		}
		_ = stmt.Close()
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

func (s *workerListStore) ClearDiscordWorkerStates(userID string) error {
	if s == nil || s.db == nil {
		return nil
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil
	}
	_, err := s.db.Exec("DELETE FROM discord_worker_state WHERE user_id = ?", userID)
	return err
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
