package main

import (
	"database/sql"
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
	foundBlockLogCh   = make(chan foundBlockLogEntry, 64)
	foundBlockLogOnce sync.Once
)

func init() {
	foundBlockLogOnce.Do(startFoundBlockLogger)
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
