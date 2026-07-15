package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
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

// A non-null BIP22 result means submitblock rejected the block even though the
// JSON-RPC request itself succeeded. Keep the record pending and retry it after
// backoff so transient results such as "inconclusive" do not end recovery.
func TestReplayPendingSubmissionsInconclusiveResultRetries(t *testing.T) {
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

	appendPendingSubmissionRecord(pendingSubmissionRecord{
		Timestamp: time.Now().UTC(),
		Height:    102,
		Hash:      "inconclusive-hash",
		Worker:    "worker3",
		BlockHex:  "deadbeef",
		RPCError:  "initial error",
		Status:    "pending",
	})

	var submitCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		submitCalls++
		w.Header().Set("Content-Type", "application/json")
		if submitCalls == 1 {
			_, _ = w.Write([]byte(`{"result":"inconclusive","error":null,"id":1}`))
			return
		}
		_, _ = w.Write([]byte(`{"result":null,"error":null,"id":1}`))
	}))
	defer server.Close()

	rpc := &RPCClient{
		url:    server.URL,
		client: server.Client(),
		lp:     server.Client(),
		nextID: 1,
	}
	ctx := context.Background()

	replayPendingSubmissions(ctx, rpc)
	if submitCalls != 1 {
		t.Fatalf("expected 1 submit attempt, got %d", submitCalls)
	}
	var status string
	if err := db.QueryRow("SELECT status FROM pending_submissions WHERE submission_key = ?", "inconclusive-hash").Scan(&status); err != nil {
		t.Fatalf("query status after inconclusive result: %v", err)
	}
	if status != "pending" {
		t.Fatalf("expected status=pending after inconclusive result, got %q", status)
	}

	// The result rejection participates in the same backoff as RPC failures.
	replayPendingSubmissions(ctx, rpc)
	if submitCalls != 1 {
		t.Fatalf("expected immediate retry to be backed off, got %d calls", submitCalls)
	}

	time.Sleep(20 * time.Millisecond)
	replayPendingSubmissions(ctx, rpc)
	if submitCalls != 2 {
		t.Fatalf("expected retry after backoff, got %d calls", submitCalls)
	}
	if err := db.QueryRow("SELECT status FROM pending_submissions WHERE submission_key = ?", "inconclusive-hash").Scan(&status); err != nil {
		t.Fatalf("query status after successful retry: %v", err)
	}
	if status != "submitted" {
		t.Fatalf("expected status=submitted after successful retry, got %q", status)
	}
}

func TestReplayPendingSubmissionsDuplicateMarksSubmitted(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := openStateDB(filepath.Join(tmpDir, "state", "workers.db"))
	if err != nil {
		t.Fatalf("openStateDB: %v", err)
	}
	defer db.Close()
	cleanup := setSharedStateDBForTest(db)
	defer cleanup()

	rec := pendingSubmissionRecord{
		Timestamp: time.Now().UTC(),
		Height:    103,
		Hash:      "duplicate-side-chain-hash",
		Worker:    "worker4",
		BlockHex:  "deadbeef",
		Status:    pendingSubmissionStatusPending,
	}
	if err := appendPendingSubmissionRecord(rec); err != nil {
		t.Fatalf("append pending submission: %v", err)
	}

	var submitCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		submitCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":"duplicate","error":null,"id":1}`))
	}))
	defer server.Close()
	rpc := &RPCClient{url: server.URL, client: server.Client(), lp: server.Client(), nextID: 1}

	replayPendingSubmissions(context.Background(), rpc)
	if submitCalls != 1 {
		t.Fatalf("submitblock calls = %d, want 1", submitCalls)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM pending_submissions WHERE submission_key = ?`, rec.Hash).Scan(&status); err != nil {
		t.Fatalf("query duplicate status: %v", err)
	}
	if status != pendingSubmissionStatusSubmitted {
		t.Fatalf("duplicate status = %q, want %q", status, pendingSubmissionStatusSubmitted)
	}
}

func TestStartPendingSubmissionReplayerReplaysImmediately(t *testing.T) {
	db := openPendingSubmissionTestDB(t)
	rec := pendingSubmissionRecord{
		Timestamp: time.Now().UTC(),
		Height:    104,
		Hash:      "immediate-replay-hash",
		BlockHex:  "deadbeef",
		Status:    pendingSubmissionStatusPending,
	}
	if err := appendPendingSubmissionRecord(rec); err != nil {
		t.Fatalf("append pending submission: %v", err)
	}

	called := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case called <- struct{}{}:
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":null,"error":null,"id":1}`))
	}))
	defer server.Close()
	rpc := &RPCClient{url: server.URL, client: server.Client(), lp: server.Client(), nextID: 1}
	previousInterval := pendingReplayInterval
	pendingReplayInterval = time.Hour
	defer func() { pendingReplayInterval = previousInterval }()
	ctx, cancel := context.WithCancel(context.Background())

	done, err := startPendingSubmissionReplayer(ctx, rpc)
	if err != nil {
		t.Fatalf("start pending replayer: %v", err)
	}
	defer func() {
		cancel()
		<-done
	}()
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("pending block waited for the periodic replay ticker")
	}
	deadline := time.Now().Add(time.Second)
	for {
		var status string
		if err := db.QueryRow(`SELECT status FROM pending_submissions WHERE submission_key = ?`, rec.Hash).Scan(&status); err == nil && status == pendingSubmissionStatusSubmitted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("immediate replay did not mark block submitted")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestPendingSubmissionPeriodicReplayDoesNotReclaimLiveSubmittingRow(t *testing.T) {
	db := openPendingSubmissionTestDB(t)
	var submitCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		submitCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":null,"error":null,"id":1}`))
	}))
	defer server.Close()
	rpc := &RPCClient{url: server.URL, client: server.Client(), lp: server.Client(), nextID: 1}
	previousInterval := pendingReplayInterval
	pendingReplayInterval = 5 * time.Millisecond
	defer func() { pendingReplayInterval = previousInterval }()
	ctx, cancel := context.WithCancel(context.Background())
	done, err := startPendingSubmissionReplayer(ctx, rpc)
	if err != nil {
		t.Fatalf("start pending replayer: %v", err)
	}
	defer func() {
		cancel()
		<-done
	}()

	rec := pendingSubmissionRecord{
		Timestamp: time.Now().UTC(),
		Height:    105,
		Hash:      "live-submitting-hash",
		BlockHex:  "deadbeef",
		Status:    pendingSubmissionStatusSubmitting,
	}
	if err := appendPendingSubmissionRecord(rec); err != nil {
		t.Fatalf("append live submitting row: %v", err)
	}
	time.Sleep(30 * time.Millisecond)
	if calls := submitCalls.Load(); calls != 0 {
		t.Fatalf("live submitting row replay calls = %d, want 0", calls)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM pending_submissions WHERE submission_key = ?`, rec.Hash).Scan(&status); err != nil {
		t.Fatalf("query live submitting row: %v", err)
	}
	if status != pendingSubmissionStatusSubmitting {
		t.Fatalf("live row status = %q, want submitting", status)
	}
}
