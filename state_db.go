package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// sharedStateDB holds the singleton database connection for workers.db.
// All components should use getSharedStateDB() instead of opening their own connections
// to avoid SQLite page cache corruption from concurrent access with modernc.org/sqlite.
var (
	sharedStateDB     *sql.DB
	sharedStateDBOnce sync.Once
	sharedStateDBMu   sync.RWMutex
	sharedStateDBPath string
)

func stateDBPathFromDataDir(dataDir string) string {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		dataDir = defaultDataDir
	}
	return filepath.Join(dataDir, "state", "workers.db")
}

func openStateDB(dbPath string) (*sql.DB, error) {
	if strings.TrimSpace(dbPath) == "" {
		return nil, os.ErrInvalid
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", dbPath+"?_foreign_keys=1&_journal=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}
	// Limit to single connection to prevent modernc.org/sqlite page cache issues
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensureStateTables(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// initSharedStateDB initializes the shared state database connection.
// Must be called once during startup before any component accesses the DB.
func initSharedStateDB(dataDir string) error {
	var initErr error
	sharedStateDBOnce.Do(func() {
		dbPath := stateDBPathFromDataDir(dataDir)
		db, err := openStateDB(dbPath)
		if err != nil {
			initErr = err
			return
		}
		sharedStateDBMu.Lock()
		sharedStateDB = db
		sharedStateDBPath = dbPath
		sharedStateDBMu.Unlock()
	})
	return initErr
}

// getSharedStateDB returns the shared state database connection.
// Returns nil if initSharedStateDB was not called or failed.
func getSharedStateDB() *sql.DB {
	sharedStateDBMu.RLock()
	defer sharedStateDBMu.RUnlock()
	return sharedStateDB
}

// closeSharedStateDB closes the shared state database connection.
// Should be called during graceful shutdown.
func closeSharedStateDB() {
	sharedStateDBMu.Lock()
	defer sharedStateDBMu.Unlock()
	if sharedStateDB != nil {
		_ = sharedStateDB.Close()
		sharedStateDB = nil
	}
}

func ensureStateTables(db *sql.DB) error {
	if db == nil {
		return nil
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS bans (
			worker TEXT PRIMARY KEY,
			worker_hash TEXT NOT NULL,
			until_unix INTEGER NOT NULL,
			reason TEXT,
			updated_at_unix INTEGER NOT NULL
		)
	`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS bans_hash_idx ON bans (worker_hash)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS bans_until_idx ON bans (until_unix)`); err != nil {
		return err
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS best_shares (
			position INTEGER PRIMARY KEY,
			worker TEXT NOT NULL,
			difficulty REAL NOT NULL,
			timestamp_unix INTEGER NOT NULL,
			hash TEXT
		)
	`); err != nil {
		return err
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS saved_workers (
			user_id TEXT NOT NULL,
			worker TEXT NOT NULL,
			worker_hash TEXT,
			notify_enabled INTEGER NOT NULL DEFAULT 1,
			best_difficulty REAL NOT NULL DEFAULT 0,
			PRIMARY KEY(user_id, worker)
		)
	`); err != nil {
		return err
	}
	if err := addSavedWorkersHashColumn(db); err != nil {
		return err
	}
	if err := addSavedWorkersNotifyEnabledColumn(db); err != nil {
		return err
	}
	if err := addSavedWorkersBestDifficultyColumn(db); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS saved_workers_hash_idx ON saved_workers (user_id, worker_hash)`); err != nil {
		return err
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS clerk_users (
			user_id TEXT PRIMARY KEY,
			first_seen_unix INTEGER NOT NULL,
			last_seen_unix INTEGER NOT NULL,
			seen_count INTEGER NOT NULL DEFAULT 0
		)
	`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS clerk_users_last_seen_idx ON clerk_users (last_seen_unix)`); err != nil {
		return err
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
		return err
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS discord_links_discord_user_idx ON discord_links (discord_user_id)`); err != nil {
		return err
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS discord_worker_state (
			user_id TEXT NOT NULL,
			worker_hash TEXT NOT NULL,
			online INTEGER NOT NULL,
			since INTEGER NOT NULL,
			seen_online INTEGER NOT NULL,
			seen_offline INTEGER NOT NULL,
			offline_eligible INTEGER NOT NULL DEFAULT 0,
			offline_notified INTEGER NOT NULL,
			recovery_eligible INTEGER NOT NULL,
			recovery_notified INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY(user_id, worker_hash)
		)
	`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS discord_worker_state_user_idx ON discord_worker_state (user_id)`); err != nil {
		return err
	}
	if err := addDiscordWorkerStateOfflineEligibleColumn(db); err != nil {
		return err
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS one_time_codes (
			user_id TEXT PRIMARY KEY,
			code TEXT NOT NULL,
			created_at_unix INTEGER NOT NULL,
			expires_at_unix INTEGER NOT NULL
		)
	`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS one_time_codes_expires_idx ON one_time_codes (expires_at_unix)`); err != nil {
		return err
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS found_blocks_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			created_at_unix INTEGER NOT NULL,
			json TEXT NOT NULL
		)
	`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS found_blocks_log_created_idx ON found_blocks_log (created_at_unix)`); err != nil {
		return err
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS pending_submissions (
			submission_key TEXT PRIMARY KEY,
			timestamp_unix INTEGER NOT NULL,
			height INTEGER NOT NULL,
			hash TEXT,
			worker TEXT,
			block_hex TEXT NOT NULL,
			rpc_error TEXT,
			rpc_url TEXT,
			payout_addr TEXT,
			status TEXT NOT NULL
		)
	`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS pending_submissions_status_idx ON pending_submissions (status)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS pending_submissions_timestamp_idx ON pending_submissions (timestamp_unix)`); err != nil {
		return err
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS backup_state (
			key TEXT PRIMARY KEY,
			last_backup_unix INTEGER NOT NULL,
			data_version INTEGER NOT NULL,
			updated_at_unix INTEGER NOT NULL
		)
	`); err != nil {
		return err
	}
	if err := ensureWorkerDBChangeTracking(db); err != nil {
		return err
	}

	return nil
}

func ensureWorkerDBChangeTracking(db *sql.DB) error {
	if db == nil {
		return nil
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS db_change_state (
			key TEXT PRIMARY KEY,
			version INTEGER NOT NULL
		)
	`); err != nil {
		return err
	}
	if _, err := db.Exec(`
		INSERT INTO db_change_state (key, version)
		VALUES ('worker_db', 1)
		ON CONFLICT(key) DO NOTHING
	`); err != nil {
		return err
	}

	tables := []string{
		"bans",
		"best_shares",
		"saved_workers",
		"clerk_users",
		"discord_links",
		"discord_worker_state",
		"one_time_codes",
		"found_blocks_log",
		"pending_submissions",
	}
	for _, table := range tables {
		for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
			triggerName := "db_change_" + table + "_" + strings.ToLower(op)
			stmt := `
				CREATE TRIGGER IF NOT EXISTS ` + triggerName + `
				AFTER ` + op + ` ON ` + table + `
				BEGIN
					UPDATE db_change_state SET version = version + 1 WHERE key = 'worker_db';
				END
			`
			if _, err := db.Exec(stmt); err != nil {
				return err
			}
		}
	}
	return nil
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}
