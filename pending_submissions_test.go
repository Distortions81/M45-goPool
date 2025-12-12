package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read pending log: %v", err)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
	if len(lines) == 0 {
		t.Fatalf("pending log is empty after replay")
	}

	var foundSubmitted bool
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var r pendingSubmissionRecord
		if err := fastJSONUnmarshal(line, &r); err != nil {
			t.Fatalf("unmarshal line: %v", err)
		}
		if strings.EqualFold(r.Hash, "test-hash") && strings.EqualFold(r.Status, "submitted") {
			foundSubmitted = true
			break
		}
	}
	if !foundSubmitted {
		t.Fatalf("expected a submitted record for hash test-hash, got none")
	}
}
