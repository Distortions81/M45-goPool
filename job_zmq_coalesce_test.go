package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func rawBlockNotificationPayload(tipTime uint32, height byte) []byte {
	payload := make([]byte, 0, 140)
	header := make([]byte, 80)
	binary.LittleEndian.PutUint32(header[68:72], tipTime)
	binary.LittleEndian.PutUint32(header[72:76], 0x1d00ffff)
	payload = append(payload, header...)
	payload = append(payload, 0x01)
	payload = append(payload, 0x01, 0x00, 0x00, 0x00)
	payload = append(payload, 0x01)
	payload = append(payload, make([]byte, 36)...)
	payload = append(payload, 0x02, 0x01, height)
	return payload
}

func TestZMQRefreshJoinsActiveLongPollTemplate(t *testing.T) {
	var rpcCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rpcCalls.Add(1)
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		_ = json.NewEncoder(w).Encode(rpcResponse{
			ID:    req.ID,
			Error: &rpcError{Code: -1, Message: "unexpected fallback RPC"},
		})
	}))
	t.Cleanup(srv.Close)

	payload := rawBlockNotificationPayload(uint32(time.Now().Unix()), 2)
	tip, err := parseRawBlockTip(payload)
	if err != nil {
		t.Fatalf("parse raw block tip: %v", err)
	}

	rpc := &RPCClient{url: srv.URL, client: srv.Client(), lp: srv.Client()}
	jm := NewJobManager(rpc, Config{}, nil, []byte{0x51}, nil)
	jm.longPollCoalesceDelay = 250 * time.Millisecond
	jm.curJob = &Job{Template: GetBlockTemplateResult{Previous: "old-parent"}}
	jm.longPollActive.Store(true)

	done := make(chan error, 1)
	go func() {
		done <- jm.handleZMQNotification(context.Background(), "rawblock", payload)
	}()

	time.Sleep(10 * time.Millisecond)
	jm.mu.Lock()
	jm.curJob = &Job{Template: GetBlockTemplateResult{Previous: tip.Hash}}
	jm.mu.Unlock()
	jm.signalTemplateUpdate()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("handle ZMQ notification: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ZMQ handler did not join the long-poll update")
	}
	if got := rpcCalls.Load(); got != 0 {
		t.Fatalf("fallback RPC calls = %d, want 0", got)
	}
}

func TestZMQRefreshFallsBackWithoutActiveLongPoll(t *testing.T) {
	payload := rawBlockNotificationPayload(uint32(time.Now().Unix()), 2)
	var gbtCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		resp := rpcResponse{ID: req.ID}
		switch req.Method {
		case "getblocktemplate":
			gbtCalls.Add(1)
			data, _ := json.Marshal(GetBlockTemplateResult{})
			resp.Result = data
		default:
			resp.Error = &rpcError{Code: -1, Message: "unexpected method"}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)

	rpc := &RPCClient{url: srv.URL, client: srv.Client(), lp: srv.Client()}
	jm := NewJobManager(rpc, Config{}, nil, nil, nil)
	jm.longPollCoalesceDelay = 500 * time.Millisecond
	start := time.Now()
	if err := jm.handleZMQNotification(context.Background(), "rawblock", payload); err == nil {
		t.Fatal("expected invalid fallback template to fail")
	}
	if elapsed := time.Since(start); elapsed >= 250*time.Millisecond {
		t.Fatalf("fallback waited without an active long poll: %v", elapsed)
	}
	if got := gbtCalls.Load(); got != 1 {
		t.Fatalf("GBT calls = %d, want 1", got)
	}
}

func TestZMQFallbackRechecksParentAfterRefreshSerialization(t *testing.T) {
	var rpcCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rpcCalls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	rpc := &RPCClient{url: srv.URL, client: srv.Client(), lp: srv.Client()}
	jm := NewJobManager(rpc, Config{}, nil, nil, nil)
	const parent = "new-parent"

	jm.refreshMu.Lock()
	done := make(chan error, 1)
	go func() {
		done <- jm.refreshJobCtxForceUnlessParent(context.Background(), parent)
	}()
	jm.mu.Lock()
	jm.curJob = &Job{Template: GetBlockTemplateResult{Previous: parent}}
	jm.mu.Unlock()
	jm.refreshMu.Unlock()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serialized fallback: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("serialized fallback did not finish")
	}
	if got := rpcCalls.Load(); got != 0 {
		t.Fatalf("RPC calls = %d, want 0 after parent recheck", got)
	}
}
