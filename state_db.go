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

const (
	stateMigrationBansJSON                = "bans_json"
	stateMigrationBestSharesJSON          = "best_shares_json"
	stateMigrationOneTimeCodesJSON        = "one_time_codes_json"
	stateMigrationFoundBlocksJSONL        = "found_blocks_jsonl"
	stateMigrationPendingSubmissionsJSONL = "pending_submissions_jsonl"
	stateMigrationBackupStampFiles        = "backup_stamp_files"
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

// setSharedStateDBForTest allows tests to set a custom shared DB.
// Returns a cleanup function that restores the previous state.
// This is only for testing purposes.
func setSharedStateDBForTest(db *sql.DB) func() {
	sharedStateDBMu.Lock()
	oldDB := sharedStateDB
	sharedStateDB = db
	sharedStateDBMu.Unlock()
	return func() {
		sharedStateDBMu.Lock()
		sharedStateDB = oldDB
		sharedStateDBMu.Unlock()
	}
}

func ensureStateTables(db *sql.DB) error {
	if db == nil {
		return nil
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS state_migrations (
			key TEXT PRIMARY KEY,
			migrated_at_unix INTEGER NOT NULL
		)
	`); err != nil {
		return err
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

	return nil
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func hasStateMigration(db *sql.DB, key string) (bool, error) {
	if db == nil {
		return false, nil
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return false, nil
	}
	var v int
	if err := db.QueryRow("SELECT 1 FROM state_migrations WHERE key = ? LIMIT 1", key).Scan(&v); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func recordStateMigration(db *sql.DB, key string, now time.Time) error {
	if db == nil {
		return nil
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}
	_, err := db.Exec("INSERT OR REPLACE INTO state_migrations (key, migrated_at_unix) VALUES (?, ?)", key, now.Unix())
	return err
}

func renameLegacyFileToOld(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	old := path + ".old"
	if _, err := os.Stat(old); err == nil {
		_ = os.Remove(old)
	}
	if err := os.Rename(path, old); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return nil
}
