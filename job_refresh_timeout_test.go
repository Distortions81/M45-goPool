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

func TestRefreshJobCtxBoundsRetriesAndReleasesSerialization(t *testing.T) {
	tpl := GetBlockTemplateResult{
		Previous:      "parent",
		Height:        100,
		CurTime:       1_700_000_000,
		Mintime:       1_700_000_000,
		Bits:          "1d00ffff",
		Target:        "target",
		Version:       0x20000000,
		CoinbaseValue: 50 * 1e8,
		LongPollID:    "cursor",
	}

	var unavailable atomic.Bool
	unavailable.Store(true)
	var requests atomic.Int32
	firstRequest := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		select {
		case firstRequest <- struct{}{}:
		default:
		}
		if unavailable.Load() {
			http.Error(w, "node unavailable", http.StatusServiceUnavailable)
			return
		}

		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode RPC request: %v", err)
			return
		}
		var value any
		switch req.Method {
		case "getblocktemplate":
			value = tpl
		case "getbestblockhash":
			value = tpl.Previous
		default:
			t.Errorf("unexpected RPC method %q", req.Method)
			http.Error(w, "unexpected RPC method", http.StatusInternalServerError)
			return
		}
		result, err := json.Marshal(value)
		if err != nil {
			t.Errorf("marshal template: %v", err)
			return
		}
		_ = json.NewEncoder(w).Encode(rpcResponse{Result: result, ID: req.ID})
	}))
	t.Cleanup(server.Close)

	cfg := Config{RPCURL: server.URL}
	jm := NewJobManager(NewRPCClient(cfg, nil), cfg, nil, nil, nil)
	jm.refreshRPCTimeout = 300 * time.Millisecond
	jm.curJob = &Job{
		Template:    tpl,
		CreatedAt:   time.Now(),
		VersionMask: computePoolMask(tpl, cfg),
	}
	jm.recordJobSuccess(nil)
	lastSuccess := jm.FeedStatus().LastSuccess

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- jm.refreshJobCtxForce(context.Background())
	}()

	select {
	case <-firstRequest:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the first GBT request")
	}

	// Queue another forced refresh behind the first one. It must be able to
	// acquire refreshMu after the bounded RPC attempt returns.
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- jm.refreshJobCtxForce(context.Background())
	}()

	select {
	case err := <-firstDone:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("first refresh error = %v, want deadline exceeded", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("retrying GBT refresh did not honor its timeout")
	}

	failed := jm.FeedStatus()
	if failed.LastError == nil || failed.LastErrorAt.IsZero() {
		t.Fatalf("failed refresh did not publish feed health: %+v", failed)
	}
	if !failed.LastSuccess.Equal(lastSuccess) {
		t.Fatalf("last success changed across outage: got %v want %v", failed.LastSuccess, lastSuccess)
	}
	if requests.Load() < 2 {
		t.Fatalf("GBT request count = %d, want retry activity", requests.Load())
	}

	unavailable.Store(false)
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatalf("queued refresh after recovery: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("queued refresh remained blocked after the timed-out owner returned")
	}
	if fs := jm.FeedStatus(); fs.LastError != nil {
		t.Fatalf("successful recovery did not clear feed error: %v", fs.LastError)
	}
}
