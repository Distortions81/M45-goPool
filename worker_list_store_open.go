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
		if err := normalizeSavedWorkersStorage(store.db); err != nil {
			return nil, err
		}
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
	if err := normalizeSavedWorkersStorage(store.db); err != nil {
		_ = db.Close()
		return nil, err
	}
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

func addSavedWorkersDisplayColumn(db *sql.DB) error {
	if db == nil {
		return nil
	}
	_, err := db.Exec("ALTER TABLE saved_workers ADD COLUMN worker_display TEXT")
	if err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return err
	}
	if _, err := db.Exec("UPDATE saved_workers SET worker_display = '' WHERE worker_display IS NULL"); err != nil {
		return err
	}
	return nil
}

func normalizeSavedWorkersStorage(db *sql.DB) error {
	if db == nil {
		return nil
	}
	if err := addSavedWorkersDisplayColumn(db); err != nil {
		return err
	}

	type row struct {
		userID         string
		hash           string
		display        string
		notifyEnabled  int
		bestDifficulty float64
	}
	byKey := make(map[string]row, 64)

	rows, err := db.Query(`
		SELECT
			COALESCE(user_id, ''),
			COALESCE(worker, ''),
			COALESCE(worker_hash, ''),
			COALESCE(worker_display, ''),
			COALESCE(notify_enabled, 1),
			COALESCE(best_difficulty, 0)
		FROM saved_workers
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			userID     string
			worker     string
			workerHash string
			display    string
			notify     int
			best       float64
		)
		if err := rows.Scan(&userID, &worker, &workerHash, &display, &notify, &best); err != nil {
			return err
		}
		userID = strings.TrimSpace(userID)
		if userID == "" {
			continue
		}

		worker = strings.TrimSpace(worker)
		hash, _ := parseSHA256HexStrict(workerHash)
		if hash == "" {
			hash = workerNameHash(worker)
		}
		if hash == "" {
			continue
		}

		display = strings.TrimSpace(display)
		if display == "" {
			display = shortWorkerName(worker, workerNamePrefix, workerNameSuffix)
		}
		if display == "" {
			display = shortDisplayID(hash, workerNamePrefix, workerNameSuffix)
		}

		key := userID + "\x00" + hash
		current, exists := byKey[key]
		if !exists {
			byKey[key] = row{
				userID:         userID,
				hash:           hash,
				display:        display,
				notifyEnabled:  notify,
				bestDifficulty: best,
			}
			continue
		}
		if current.display == "" && display != "" {
			current.display = display
		}
		if notify > current.notifyEnabled {
			current.notifyEnabled = notify
		}
		if best > current.bestDifficulty {
			current.bestDifficulty = best
		}
		byKey[key] = current
	}
	if err := rows.Err(); err != nil {
		return err
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec("DELETE FROM saved_workers"); err != nil {
		return err
	}
	stmt, err := tx.Prepare(`
		INSERT INTO saved_workers (user_id, worker, worker_hash, worker_display, notify_enabled, best_difficulty)
		VALUES (?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, r := range byKey {
		if _, err := stmt.Exec(r.userID, r.hash, r.hash, r.display, r.notifyEnabled, r.bestDifficulty); err != nil {
			return err
		}
	}

	return tx.Commit()
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
