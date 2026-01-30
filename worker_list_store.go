package main

import (
	"database/sql"
	"os"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const maxSavedWorkersPerUser = 64

type workerListStore struct {
	db     *sql.DB
	ownsDB bool

	bestDiffMu      sync.Mutex
	bestDiffPending map[string]float64
	bestDiffCh      chan bestDiffUpdate
	bestDiffStop    chan struct{}
	bestDiffWg      sync.WaitGroup
}

type discordLink struct {
	UserID        string
	DiscordUserID string
	Enabled       bool
	LinkedAt      time.Time
	UpdatedAt     time.Time
}

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

func (s *workerListStore) BestDifficultyForHash(hash string) (float64, bool, error) {
	if s == nil || s.db == nil {
		return 0, false, nil
	}
	hash = strings.ToLower(strings.TrimSpace(hash))
	if hash == "" {
		return 0, false, nil
	}

	pending := 0.0
	s.bestDiffMu.Lock()
	if s.bestDiffPending != nil {
		pending = s.bestDiffPending[hash]
	}
	s.bestDiffMu.Unlock()

	var (
		count int
		best  sql.NullFloat64
	)
	if err := s.db.QueryRow("SELECT COUNT(*), MAX(best_difficulty) FROM saved_workers WHERE worker_hash = ?", hash).Scan(&count, &best); err != nil {
		return 0, false, err
	}

	dbBest := 0.0
	if best.Valid && best.Float64 > 0 {
		dbBest = best.Float64
	}

	out := dbBest
	if pending > out {
		out = pending
	}
	return out, count > 0, nil
}

func (s *workerListStore) UpdateSavedWorkerBestDifficulty(hash string, diff float64) (bool, error) {
	if s == nil || s.db == nil {
		return false, nil
	}
	hash = strings.ToLower(strings.TrimSpace(hash))
	if hash == "" || diff <= 0 {
		return false, nil
	}

	updated := false
	s.bestDiffMu.Lock()
	if s.bestDiffPending == nil {
		s.bestDiffPending = make(map[string]float64)
	}
	if diff > s.bestDiffPending[hash] {
		s.bestDiffPending[hash] = diff
		updated = true
	}
	s.bestDiffMu.Unlock()

	if !updated {
		return false, nil
	}

	if ch := s.bestDiffCh; ch != nil {
		select {
		case ch <- bestDiffUpdate{hash: hash, diff: diff}:
		default:
		}
	}
	return true, nil
}

func (s *workerListStore) List(userID string) ([]SavedWorkerEntry, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, nil
	}
	rows, err := s.db.Query("SELECT worker, worker_hash, notify_enabled, best_difficulty FROM saved_workers WHERE user_id = ? ORDER BY worker COLLATE NOCASE", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var workers []SavedWorkerEntry
	for rows.Next() {
		var entry SavedWorkerEntry
		var notifyEnabledInt int
		var best sql.NullFloat64
		if err := rows.Scan(&entry.Name, &entry.Hash, &notifyEnabledInt, &best); err != nil {
			return nil, err
		}
		entry.NotifyEnabled = notifyEnabledInt != 0
		entry.BestDifficulty = best.Float64
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

	s.bestDiffMu.Lock()
	pending := s.bestDiffPending
	s.bestDiffMu.Unlock()
	if len(pending) > 0 {
		for i := range workers {
			if workers[i].Hash == "" {
				continue
			}
			if v := pending[workers[i].Hash]; v > workers[i].BestDifficulty {
				workers[i].BestDifficulty = v
			}
		}
	}
	return workers, nil
}

func (s *workerListStore) ListAllSavedWorkers() ([]SavedWorkerRecord, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	rows, err := s.db.Query("SELECT user_id, worker, worker_hash, notify_enabled, best_difficulty FROM saved_workers ORDER BY user_id COLLATE NOCASE, worker COLLATE NOCASE")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []SavedWorkerRecord
	for rows.Next() {
		var (
			userID    string
			entry     SavedWorkerEntry
			notifyInt int
			best      sql.NullFloat64
		)
		if err := rows.Scan(&userID, &entry.Name, &entry.Hash, &notifyInt, &best); err != nil {
			return nil, err
		}
		userID = strings.TrimSpace(userID)
		entry.Name = strings.TrimSpace(entry.Name)
		entry.Hash = strings.TrimSpace(entry.Hash)
		entry.NotifyEnabled = notifyInt != 0
		entry.BestDifficulty = best.Float64
		if entry.Hash == "" {
			entry.Hash = workerNameHash(entry.Name)
			if entry.Hash != "" {
				_, _ = s.db.Exec("UPDATE saved_workers SET worker_hash = ? WHERE user_id = ? AND worker = ?", entry.Hash, userID, entry.Name)
			}
		}
		if entry.Hash != "" {
			lower := strings.ToLower(entry.Hash)
			if lower != entry.Hash {
				entry.Hash = lower
				_, _ = s.db.Exec("UPDATE saved_workers SET worker_hash = ? WHERE user_id = ? AND worker = ?", entry.Hash, userID, entry.Name)
			}
		}
		if userID == "" {
			continue
		}
		records = append(records, SavedWorkerRecord{
			UserID:           userID,
			SavedWorkerEntry: entry,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	s.bestDiffMu.Lock()
	pending := s.bestDiffPending
	s.bestDiffMu.Unlock()
	if len(pending) > 0 {
		for i := range records {
			hash := strings.TrimSpace(records[i].Hash)
			if hash == "" {
				continue
			}
			if v := pending[hash]; v > records[i].BestDifficulty {
				records[i].BestDifficulty = v
			}
		}
	}
	return records, nil
}

func (s *workerListStore) RecordClerkUserSeen(userID string, now time.Time) error {
	if s == nil || s.db == nil {
		return nil
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	ts := now.Unix()
	_, err := s.db.Exec(`
		INSERT INTO clerk_users (user_id, first_seen_unix, last_seen_unix, seen_count)
		VALUES (?, ?, ?, 1)
		ON CONFLICT(user_id) DO UPDATE SET
			last_seen_unix = excluded.last_seen_unix,
			seen_count = clerk_users.seen_count + 1
	`, userID, ts, ts)
	return err
}

type ClerkUserRecord struct {
	UserID    string
	FirstSeen time.Time
	LastSeen  time.Time
	SeenCount int
}

func (s *workerListStore) ListAllClerkUsers() ([]ClerkUserRecord, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	rows, err := s.db.Query("SELECT user_id, first_seen_unix, last_seen_unix, seen_count FROM clerk_users ORDER BY last_seen_unix DESC, user_id COLLATE NOCASE")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]ClerkUserRecord, 0, 64)
	for rows.Next() {
		var (
			userID string
			first  int64
			last   int64
			count  int
		)
		if err := rows.Scan(&userID, &first, &last, &count); err != nil {
			return nil, err
		}
		userID = strings.TrimSpace(userID)
		if userID == "" {
			continue
		}
		out = append(out, ClerkUserRecord{
			UserID:    userID,
			FirstSeen: time.Unix(first, 0),
			LastSeen:  time.Unix(last, 0),
			SeenCount: count,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

type bestDiffUpdate struct {
	hash string
	diff float64
}

func (s *workerListStore) startBestDiffWorker(flushInterval time.Duration) {
	if s == nil || s.db == nil {
		return
	}
	if flushInterval <= 0 {
		flushInterval = 10 * time.Second
	}
	if s.bestDiffStop != nil {
		return
	}
	s.bestDiffCh = make(chan bestDiffUpdate, 4096)
	s.bestDiffStop = make(chan struct{})
	s.bestDiffWg.Add(1)
	go func() {
		defer s.bestDiffWg.Done()
		ticker := time.NewTicker(flushInterval)
		defer ticker.Stop()
		for {
			select {
			case <-s.bestDiffStop:
				s.flushBestDiffPending()
				return
			case upd := <-s.bestDiffCh:
				if upd.hash == "" || upd.diff <= 0 {
					continue
				}
				s.bestDiffMu.Lock()
				if s.bestDiffPending == nil {
					s.bestDiffPending = make(map[string]float64)
				}
				if upd.diff > s.bestDiffPending[upd.hash] {
					s.bestDiffPending[upd.hash] = upd.diff
				}
				s.bestDiffMu.Unlock()
			case <-ticker.C:
				s.flushBestDiffPending()
			}
		}
	}()
}

func (s *workerListStore) flushBestDiffPending() {
	if s == nil || s.db == nil {
		return
	}
	s.bestDiffMu.Lock()
	if len(s.bestDiffPending) == 0 {
		s.bestDiffMu.Unlock()
		return
	}
	batch := s.bestDiffPending
	s.bestDiffPending = make(map[string]float64, len(batch))
	s.bestDiffMu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		logger.Warn("saved worker best diff flush begin failed", "error", err)
		return
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare(`
		UPDATE saved_workers
		SET best_difficulty = ?
		WHERE worker_hash = ? AND best_difficulty < ?
	`)
	if err != nil {
		logger.Warn("saved worker best diff flush prepare failed", "error", err)
		return
	}
	defer stmt.Close()

	for hash, diff := range batch {
		if hash == "" || diff <= 0 {
			continue
		}
		if _, err := stmt.Exec(diff, hash, diff); err != nil {
			logger.Warn("saved worker best diff flush update failed", "error", err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		logger.Warn("saved worker best diff flush commit failed", "error", err)
		return
	}
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

func (s *workerListStore) GetDiscordLink(userID string) (discordUserID string, enabled bool, ok bool, err error) {
	if s == nil || s.db == nil {
		return "", false, false, nil
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return "", false, false, nil
	}
	var enabledInt int
	if err := s.db.QueryRow("SELECT discord_user_id, enabled FROM discord_links WHERE user_id = ?", userID).Scan(&discordUserID, &enabledInt); err != nil {
		if err == sql.ErrNoRows {
			return "", false, false, nil
		}
		return "", false, false, err
	}
	return strings.TrimSpace(discordUserID), enabledInt != 0, true, nil
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
		SELECT worker_hash, online, since, seen_online, seen_offline, offline_eligible, offline_notified, recovery_eligible, recovery_notified
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
			offlineEligInt  int
			offlineNotInt   int
			recoveryEligInt int
			recoveryNotInt  int
		)
		if err := rows.Scan(&hash, &onlineInt, &sinceUnix, &seenOnlineInt, &seenOfflineInt, &offlineEligInt, &offlineNotInt, &recoveryEligInt, &recoveryNotInt); err != nil {
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
			OfflineEligible:  offlineEligInt != 0,
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

func (s *workerListStore) SetDiscordLinkEnabled(userID string, enabled bool, now time.Time) (ok bool, err error) {
	if s == nil || s.db == nil {
		return false, nil
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return false, nil
	}

	if _, _, exists, err := s.GetDiscordLink(userID); err != nil {
		return false, err
	} else if !exists {
		return false, nil
	}

	val := 0
	if enabled {
		val = 1
	}
	_, err = s.db.Exec("UPDATE discord_links SET enabled = ?, updated_at = ? WHERE user_id = ?", val, now.Unix(), userID)
	return true, err
}

func (s *workerListStore) ResetDiscordWorkerStateTimers(userID string, now time.Time) error {
	if s == nil || s.db == nil {
		return nil
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil
	}
	ts := now.Unix()
	_, err := s.db.Exec(`
		UPDATE discord_worker_state
		SET
			since = ?,
			offline_eligible = 0,
			offline_notified = 0,
			recovery_eligible = 0,
			recovery_notified = 0,
			updated_at = ?
		WHERE user_id = ?
	`, ts, ts, userID)
	return err
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
				seen_online, seen_offline, offline_eligible,
				offline_notified, recovery_eligible, recovery_notified,
				updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(user_id, worker_hash) DO UPDATE SET
				online = excluded.online,
				since = excluded.since,
				seen_online = excluded.seen_online,
				seen_offline = excluded.seen_offline,
				offline_eligible = excluded.offline_eligible,
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
			offlineElig := 0
			if st.OfflineEligible {
				offlineElig = 1
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
				seenOnline, seenOffline, offlineElig,
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

func (s *workerListStore) RemoveUser(userID string) error {
	if s == nil || s.db == nil {
		return nil
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmts := []string{
		"DELETE FROM saved_workers WHERE user_id = ?",
		"DELETE FROM discord_links WHERE user_id = ?",
		"DELETE FROM discord_worker_state WHERE user_id = ?",
		"DELETE FROM one_time_codes WHERE user_id = ?",
		"DELETE FROM clerk_users WHERE user_id = ?",
	}
	for _, stmt := range stmts {
		if _, execErr := tx.Exec(stmt, userID); execErr != nil {
			_ = tx.Rollback()
			return execErr
		}
	}
	return tx.Commit()
}

func (s *workerListStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	if s.bestDiffStop != nil {
		close(s.bestDiffStop)
		s.bestDiffWg.Wait()
	}
	if s.ownsDB {
		return s.db.Close()
	}
	return nil
}
