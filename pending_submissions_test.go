package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Test that replayPendingSubmissions successfully resubmits a pending block
// and appends a "submitted" record to the pending_submissions.jsonl log.
func TestReplayPendingSubmissionsMarksSubmitted(t *testing.T) {
	t.Helper()

	tmpDir := t.TempDir()
	cfg := Config{DataDir: tmpDir}
	path := pendingSubmissionsPath(cfg)

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
	appendPendingSubmissionRecord(path, rec)

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
	replayPendingSubmissions(ctx, rpc, path)

	if submitCalls == 0 {
		t.Fatalf("expected submitblock to be called at least once")
	}

	db, err := openStateDB(stateDBPathFromDataDir(tmpDir))
	if err != nil {
		t.Fatalf("openStateDB: %v", err)
	}
	defer db.Close()

	var status string
	if err := db.QueryRow("SELECT status FROM pending_submissions WHERE submission_key = ?", "test-hash").Scan(&status); err != nil {
		t.Fatalf("query status: %v", err)
	}
	if status != "submitted" {
		t.Fatalf("expected status=submitted, got %q", status)
	}
}
