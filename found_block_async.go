package main

import (
	"strings"
	"sync"
	"time"
)

// foundBlockLogEntry represents a single found-block audit record. Writes are
// serialized by a background goroutine so the submit hot path never blocks on
// SQLite I/O.
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
		for entry := range foundBlockLogCh {
			if entry.Done != nil {
				close(entry.Done)
				continue
			}

			// Use the shared state database connection
			db := getSharedStateDB()
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
	}()
}

// drainFoundBlockLogger waits until every record queued before the barrier has
// completed its SQLite write attempt. Callers must first stop all producers.
func drainFoundBlockLogger() {
	done := make(chan struct{})
	foundBlockLogCh <- foundBlockLogEntry{Done: done}
	<-done
}
