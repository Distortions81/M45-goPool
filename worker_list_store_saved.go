package main

import (
	"database/sql"
	"maps"
	"strings"
	"time"
)

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

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var count int
	if err := tx.QueryRow("SELECT COUNT(*) FROM saved_workers WHERE user_id = ?", userID).Scan(&count); err != nil {
		return err
	}
	if count >= maxSavedWorkersPerUser {
		return nil
	}

	hash := workerNameHash(worker)
	if hash == "" {
		return nil
	}
	display := shortWorkerName(worker, workerNamePrefix, workerNameSuffix)
	if display == "" {
		display = shortDisplayID(hash, workerNamePrefix, workerNameSuffix)
	}
	if _, err := tx.Exec(
		"INSERT OR IGNORE INTO saved_workers (user_id, worker, worker_hash, worker_display, notify_enabled) VALUES (?, ?, ?, ?, 1)",
		userID, hash, hash, display,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(
		"UPDATE saved_workers SET worker = ?, worker_hash = ?, worker_display = ? WHERE user_id = ? AND worker_hash = ?",
		hash, hash, display, userID, hash,
	); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *workerListStore) List(userID string) ([]SavedWorkerEntry, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, nil
	}
	rows, err := s.db.Query(`
		SELECT COALESCE(worker_display, ''), COALESCE(worker_hash, ''), notify_enabled, best_difficulty
		FROM saved_workers
		WHERE user_id = ?
		ORDER BY worker_display COLLATE NOCASE
	`, userID)
	if err != nil {
		return nil, err
	}

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
		entry.Name = strings.TrimSpace(entry.Name)
		entry.Hash = strings.ToLower(strings.TrimSpace(entry.Hash))
		if entry.Hash == "" {
			continue
		}
		if entry.Name == "" {
			entry.Name = shortDisplayID(entry.Hash, workerNamePrefix, workerNameSuffix)
		}
		workers = append(workers, entry)
	}
	iterErr := rows.Err()
	closeErr := rows.Close()
	if iterErr != nil {
		return nil, iterErr
	}
	if closeErr != nil {
		return nil, closeErr
	}

	s.bestDiffMu.Lock()
	var pending map[string]float64
	if len(s.bestDiffPending) > 0 {
		pending = make(map[string]float64, len(s.bestDiffPending))
		maps.Copy(pending, s.bestDiffPending)
	}
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
	rows, err := s.db.Query(`
		SELECT user_id, COALESCE(worker_display, ''), COALESCE(worker_hash, ''), notify_enabled, best_difficulty
		FROM saved_workers
		ORDER BY user_id COLLATE NOCASE, worker_display COLLATE NOCASE
	`)
	if err != nil {
		return nil, err
	}

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
		entry.Hash = strings.ToLower(strings.TrimSpace(entry.Hash))
		entry.NotifyEnabled = notifyInt != 0
		entry.BestDifficulty = best.Float64
		if userID == "" || entry.Hash == "" {
			continue
		}
		if entry.Name == "" {
			entry.Name = shortDisplayID(entry.Hash, workerNamePrefix, workerNameSuffix)
		}
		records = append(records, SavedWorkerRecord{
			UserID:           userID,
			SavedWorkerEntry: entry,
		})
	}
	iterErr := rows.Err()
	closeErr := rows.Close()
	if iterErr != nil {
		return nil, iterErr
	}
	if closeErr != nil {
		return nil, closeErr
	}

	s.bestDiffMu.Lock()
	var pending map[string]float64
	if len(s.bestDiffPending) > 0 {
		pending = make(map[string]float64, len(s.bestDiffPending))
		maps.Copy(pending, s.bestDiffPending)
	}
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

// ListNotifiedUsersForWorker returns saved worker rows (paired with Clerk user
// IDs) for a given worker name, limited to those with notify_enabled=1.
//
// This is used for user-facing notifications (e.g. Discord pings) when a given
// worker triggers an event (like a found block).
func (s *workerListStore) ListNotifiedUsersForWorker(worker string) ([]SavedWorkerRecord, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	worker = strings.TrimSpace(worker)
	if worker == "" {
		return nil, nil
	}
	hash := workerNameHash(worker)

	rows, err := s.db.Query(`
		SELECT user_id, COALESCE(worker_display, ''), COALESCE(worker_hash, '')
		FROM saved_workers
		WHERE notify_enabled = 1 AND worker_hash = ?
	`, strings.ToLower(strings.TrimSpace(hash)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]SavedWorkerRecord, 0, 8)
	for rows.Next() {
		var (
			userID string
			name   string
			h      string
		)
		if err := rows.Scan(&userID, &name, &h); err != nil {
			return nil, err
		}
		userID = strings.TrimSpace(userID)
		name = strings.TrimSpace(name)
		h = strings.TrimSpace(h)
		if userID == "" {
			continue
		}
		h = strings.ToLower(h)
		if h == "" {
			continue
		}
		if name == "" {
			name = shortDisplayID(h, workerNamePrefix, workerNameSuffix)
		}
		out = append(out, SavedWorkerRecord{
			UserID: userID,
			SavedWorkerEntry: SavedWorkerEntry{
				Name:          name,
				Hash:          h,
				NotifyEnabled: true,
			},
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func (s *workerListStore) Remove(userID, workerHash string) error {
	if s == nil || s.db == nil {
		return nil
	}
	userID = strings.TrimSpace(userID)
	workerHash, errMsg := parseSHA256HexStrict(workerHash)
	if userID == "" || workerHash == "" || errMsg != "" {
		return nil
	}
	_, err := s.db.Exec("DELETE FROM saved_workers WHERE user_id = ? AND worker_hash = ?", userID, workerHash)
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
