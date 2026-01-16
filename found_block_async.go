package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// foundBlockLogEntry represents a single JSONL line to append to the
// found_blocks.jsonl log. Writes are serialized by a background goroutine
// so the submit hot path never blocks on filesystem I/O.
type foundBlockLogEntry struct {
	Dir  string
	Line []byte
	Done chan struct{}
}

var (
	foundBlockLogCh        = make(chan foundBlockLogEntry, 64)
	foundBlockLogOnce      sync.Once
	foundBlockTestDropOnce sync.Once
)

func init() {
	foundBlockLogOnce.Do(startFoundBlockLogger)
}

func runningUnderGoTest() bool {
	if len(os.Args) == 0 {
		return false
	}
	// `go test` binaries are typically named like `pkg.test`.
	return strings.HasSuffix(filepath.Base(os.Args[0]), ".test")
}

func startFoundBlockLogger() {
	go func() {
		var db *sql.DB
		var curDBPath string
		for entry := range foundBlockLogCh {
			if entry.Done != nil {
				close(entry.Done)
				continue
			}
			dir := entry.Dir
			if dir == "" {
				dir = defaultDataDir
			}
			// Avoid polluting the real `data/` directory when running unit tests.
			// Tests should set `cfg.DataDir = t.TempDir()` when they need persistence.
			if runningUnderGoTest() && dir == defaultDataDir {
				foundBlockTestDropOnce.Do(func() {
					logger.Warn("dropping found-block log writes during go test; set DataDir to a temp dir to persist")
				})
				continue
			}
			dbPath := stateDBPathFromDataDir(dir)
			if dbPath != curDBPath || db == nil {
				if db != nil {
					_ = db.Close()
					db = nil
				}
				ndb, err := openStateDB(dbPath)
				if err != nil {
					logger.Warn("found block sqlite open", "error", err, "path", dbPath)
					continue
				}
				db = ndb
				curDBPath = dbPath
			}
			if db == nil {
				continue
			}

			line := strings.TrimSpace(string(entry.Line))
			if line == "" {
				continue
			}
			if _, err := db.Exec("INSERT INTO found_blocks_log (created_at_unix, json) VALUES (?, ?)", time.Now().Unix(), line); err != nil {
				logger.Warn("found block sqlite insert", "error", err)
			}
		}
		if db != nil {
			_ = db.Close()
		}
	}()
}
