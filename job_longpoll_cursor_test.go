package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestLongPollCursorAdvancesWithoutJobChurn(t *testing.T) {
	cfg := Config{}
	currentTemplate := GetBlockTemplateResult{
		Previous:      "prev",
		Height:        100,
		CurTime:       1_700_000_000,
		Mintime:       1_700_000_000,
		Bits:          "1d00ffff",
		Target:        "target",
		Version:       0x20000000,
		CoinbaseValue: 50 * 1e8,
		LongPollID:    "cursor-1",
	}
	current := &Job{
		JobID:       "current-job",
		Generation:  7,
		Template:    currentTemplate,
		VersionMask: computePoolMask(currentTemplate, cfg),
	}
	verifyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode verification request: %v", err)
			return
		}
		if req.Method != "getbestblockhash" {
			t.Errorf("verification RPC method = %q, want getbestblockhash", req.Method)
			return
		}
		result, _ := json.Marshal(currentTemplate.Previous)
		_ = json.NewEncoder(w).Encode(rpcResponse{Result: result, ID: req.ID})
	}))
	t.Cleanup(verifyServer.Close)
	verifyCfg := cfg
	verifyCfg.RPCURL = verifyServer.URL
	jm := NewJobManager(NewRPCClient(verifyCfg, nil), cfg, nil, nil, nil)
	jm.curJob = current
	jm.longPollID = currentTemplate.LongPollID
	atomic.StoreUint64(&jm.jobGeneration, current.Generation)

	next := currentTemplate
	next.CurTime++
	next.LongPollID = "cursor-2"
	if err := jm.refreshFromTemplate(context.Background(), next); err != nil {
		t.Fatalf("refresh unchanged template: %v", err)
	}

	job, cursor := jm.currentJobAndLongPollID()
	if job != current {
		t.Fatal("cursor-only refresh replaced the current job")
	}
	if cursor != next.LongPollID {
		t.Fatalf("long-poll cursor = %q, want %q", cursor, next.LongPollID)
	}
	if current.Template.LongPollID != currentTemplate.LongPollID {
		t.Fatalf("published job cursor mutated to %q", current.Template.LongPollID)
	}
	if got := atomic.LoadUint64(&jm.jobGeneration); got != current.Generation {
		t.Fatalf("job generation = %d, want unchanged %d", got, current.Generation)
	}
	if got := len(jm.notifyQueue); got != 0 {
		t.Fatalf("cursor-only refresh queued %d job notifications", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	requestedCursor := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode long-poll request: %v", err)
			cancel()
			return
		}
		switch req.Method {
		case "getblocktemplate":
			params, _ := req.Params.([]any)
			options, _ := params[0].(map[string]any)
			longPollID, _ := options["longpollid"].(string)
			requestedCursor <- longPollID
			result, _ := json.Marshal(next)
			_ = json.NewEncoder(w).Encode(rpcResponse{Result: result, ID: req.ID})
		case "getbestblockhash":
			result, _ := json.Marshal(currentTemplate.Previous)
			_ = json.NewEncoder(w).Encode(rpcResponse{Result: result, ID: req.ID})
			cancel()
		default:
			t.Errorf("unexpected RPC method %q", req.Method)
			cancel()
		}
	}))
	t.Cleanup(server.Close)
	jm.rpc = NewRPCClient(Config{RPCURL: server.URL}, nil)

	done := make(chan struct{})
	go func() {
		jm.longpollLoop(ctx)
		close(done)
	}()

	select {
	case got := <-requestedCursor:
		if got != next.LongPollID {
			t.Fatalf("next long-poll request used cursor %q, want %q", got, next.LongPollID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for long-poll request")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("long-poll loop did not stop after cancellation")
	}
}

func TestLongPollCursorDoesNotAdvanceWhenJobBuildFails(t *testing.T) {
	cfg := Config{}
	currentTemplate := GetBlockTemplateResult{
		Previous:   "parent-a",
		Height:     100,
		CurTime:    1_700_000_000,
		Mintime:    1_700_000_000,
		Bits:       "1d00ffff",
		Target:     "target",
		Version:    0x20000000,
		LongPollID: "cursor-1",
	}
	current := &Job{
		JobID:       "current-job",
		Generation:  7,
		Template:    currentTemplate,
		VersionMask: computePoolMask(currentTemplate, cfg),
	}
	// An empty payout script makes buildJob fail before any RPC call.
	jm := NewJobManager(nil, cfg, nil, nil, nil)
	jm.curJob = current
	jm.longPollID = currentTemplate.LongPollID

	next := currentTemplate
	next.Previous = "parent-b"
	next.LongPollID = "cursor-2"
	if err := jm.refreshFromTemplate(context.Background(), next); err == nil {
		t.Fatal("expected changed template build to fail")
	}
	job, cursor := jm.currentJobAndLongPollID()
	if job != current {
		t.Fatal("failed build replaced the current job")
	}
	if cursor != currentTemplate.LongPollID {
		t.Fatalf("failed build advanced cursor to %q, want %q", cursor, currentTemplate.LongPollID)
	}
}

func TestLongPollCursorDoesNotAdvanceForStaleTemplate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode RPC request: %v", err)
			return
		}
		if req.Method != "getbestblockhash" {
			t.Errorf("RPC method = %q, want getbestblockhash", req.Method)
			return
		}
		result, _ := json.Marshal("best-parent")
		_ = json.NewEncoder(w).Encode(rpcResponse{Result: result, ID: req.ID})
	}))
	t.Cleanup(server.Close)

	cfg := Config{}
	currentTemplate := GetBlockTemplateResult{
		Previous:   "parent-a",
		Height:     100,
		CurTime:    1_700_000_000,
		Mintime:    1_700_000_000,
		Bits:       "1d00ffff",
		Target:     "target",
		Version:    0x20000000,
		LongPollID: "cursor-1",
	}
	current := &Job{
		JobID:       "current-job",
		Generation:  7,
		Template:    currentTemplate,
		VersionMask: computePoolMask(currentTemplate, cfg),
	}
	rpc := NewRPCClient(Config{RPCURL: server.URL}, nil)
	jm := NewJobManager(rpc, cfg, nil, []byte{0x51}, nil)
	jm.curJob = current
	jm.longPollID = currentTemplate.LongPollID

	next := currentTemplate
	next.Previous = "displaced-parent"
	next.LongPollID = "cursor-2"
	err := jm.refreshFromTemplate(context.Background(), next)
	if !errors.Is(err, errStaleTemplate) {
		t.Fatalf("refresh error = %v, want errStaleTemplate", err)
	}
	job, cursor := jm.currentJobAndLongPollID()
	if job != current {
		t.Fatal("stale template replaced the current job")
	}
	if cursor != currentTemplate.LongPollID {
		t.Fatalf("stale template advanced cursor to %q, want %q", cursor, currentTemplate.LongPollID)
	}
}
