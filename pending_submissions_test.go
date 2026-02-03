package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

// Test that replayPendingSubmissions successfully resubmits a pending block
// and marks it as "submitted" in SQLite.
func TestReplayPendingSubmissionsMarksSubmitted(t *testing.T) {
	t.Helper()

	tmpDir := t.TempDir()

	// Set up the shared DB for this test
	dbPath := filepath.Join(tmpDir, "state", "workers.db")
	db, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("openStateDB: %v", err)
	}
	defer db.Close()
	cleanup := setSharedStateDBForTest(db)
	defer cleanup()

	rec := pendingSubmissionRecord{
		Timestamp:  time.Now().UTC(),
		Height:     100,
		Hash:       "test-hash",
		Worker:     "worker1",
		BlockHex:   "deadbeef",
		RPCError:   "initial error",
		RPCURL:     "http://127.0.0.1:8332",
		PayoutAddr: "bc1qexample",
		Status:     "pending",
	}
	appendPendingSubmissionRecord(rec)

	var submitCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		_ = body
		submitCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":null,"error":null,"id":1}`))
	}))
	defer server.Close()

	rpc := &RPCClient{
		url:     server.URL,
		user:    "",
		pass:    "",
		metrics: nil,
		client:  server.Client(),
		lp:      server.Client(),
		nextID:  1,
	}

	ctx := context.Background()
	replayPendingSubmissions(ctx, rpc)

	if submitCalls == 0 {
		t.Fatalf("expected submitblock to be called at least once")
	}

	// Use the shared DB that was set up earlier
	var status string
	if err := db.QueryRow("SELECT status FROM pending_submissions WHERE submission_key = ?", "test-hash").Scan(&status); err != nil {
		t.Fatalf("query status: %v", err)
	}
	if status != "submitted" {
		t.Fatalf("expected status=submitted, got %q", status)
	}
}

// Test that failed submitblock calls remain pending and are backed off.
func TestReplayPendingSubmissionsFailureBackoff(t *testing.T) {
	t.Helper()

	tmpDir := t.TempDir()

	dbPath := filepath.Join(tmpDir, "state", "workers.db")
	db, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("openStateDB: %v", err)
	}
	defer db.Close()
	cleanup := setSharedStateDBForTest(db)
	defer cleanup()

	prevBase := pendingReplayBaseDelay
	prevMax := pendingReplayMaxDelay
	prevJitter := pendingReplayJitterFrac
	prevBackoff := pendingReplayBackoff
	pendingReplayBaseDelay = 10 * time.Millisecond
	pendingReplayMaxDelay = 50 * time.Millisecond
	pendingReplayJitterFrac = 0
	pendingReplayBackoff = newPendingReplayBackoff()
	defer func() {
		pendingReplayBaseDelay = prevBase
		pendingReplayMaxDelay = prevMax
		pendingReplayJitterFrac = prevJitter
		pendingReplayBackoff = prevBackoff
	}()

	rec := pendingSubmissionRecord{
		Timestamp:  time.Now().UTC(),
		Height:     101,
		Hash:       "fail-hash",
		Worker:     "worker2",
		BlockHex:   "deadbeef",
		RPCError:   "initial error",
		RPCURL:     "http://127.0.0.1:8332",
		PayoutAddr: "bc1qexample",
		Status:     "pending",
	}
	appendPendingSubmissionRecord(rec)

	var submitCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		submitCalls++
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer server.Close()

	rpc := &RPCClient{
		url:     server.URL,
		user:    "",
		pass:    "",
		metrics: nil,
		client:  server.Client(),
		lp:      server.Client(),
		nextID:  1,
	}

	ctx := context.Background()
	replayPendingSubmissions(ctx, rpc)
	if submitCalls != 1 {
		t.Fatalf("expected 1 submit attempt, got %d", submitCalls)
	}

	// Immediate retry should be backed off.
	replayPendingSubmissions(ctx, rpc)
	if submitCalls != 1 {
		t.Fatalf("expected backoff to skip retry, got %d calls", submitCalls)
	}

	time.Sleep(20 * time.Millisecond)
	replayPendingSubmissions(ctx, rpc)
	if submitCalls != 2 {
		t.Fatalf("expected retry after backoff, got %d calls", submitCalls)
	}

	var status string
	if err := db.QueryRow("SELECT status FROM pending_submissions WHERE submission_key = ?", "fail-hash").Scan(&status); err != nil {
		t.Fatalf("query status: %v", err)
	}
	if status != "pending" {
		t.Fatalf("expected status=pending, got %q", status)
	}
}
