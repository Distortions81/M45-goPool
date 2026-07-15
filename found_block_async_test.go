package main

import (
	"testing"
	"time"
)

func TestDrainFoundBlockLoggerWaitsForQueuedInsert(t *testing.T) {
	db := openPendingSubmissionTestDB(t)
	line := []byte(`{"hash":"shutdown-drain-test"}`)
	foundBlockLogCh <- foundBlockLogEntry{Line: line}

	drainFoundBlockLogger()

	var stored string
	if err := db.QueryRow(`SELECT json FROM found_blocks_log ORDER BY id DESC LIMIT 1`).Scan(&stored); err != nil {
		t.Fatalf("query drained found block: %v", err)
	}
	if stored != string(line) {
		t.Fatalf("stored found block = %q, want %q", stored, line)
	}
}

func TestDrainFoundBlockLoggerRemainsReusable(t *testing.T) {
	_ = openPendingSubmissionTestDB(t)
	drainFoundBlockLogger()

	// A second barrier must remain usable; the logger is drained, not closed.
	done := make(chan struct{})
	go func() {
		drainFoundBlockLogger()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("second found-block drain did not complete")
	}
}
