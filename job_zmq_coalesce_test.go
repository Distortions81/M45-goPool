package main

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestHashBlockUsesBitcoinCoreDisplayByteOrder(t *testing.T) {
	payload := make([]byte, 32)
	for i := range payload {
		payload[i] = byte(i)
	}
	parent := hex.EncodeToString(payload)

	jm := NewJobManager(nil, Config{ZMQHashBlockAddr: "tcp://127.0.0.1:28334"}, nil, nil, nil)
	jm.curJob = &Job{Template: GetBlockTemplateResult{Previous: parent}}

	if err := jm.handleZMQNotification(context.Background(), "hashblock", payload); err != nil {
		t.Fatalf("handle hashblock notification: %v", err)
	}
	if got := jm.recentZMQParentProof(parent); got != parent {
		t.Fatalf("hashblock parent proof = %q, want %q", got, parent)
	}
}

func TestHashBlockDefersRefreshToHealthyRawBlock(t *testing.T) {
	var rpcCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rpcCalls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	rpc := &RPCClient{url: srv.URL, client: srv.Client(), lp: srv.Client()}
	jm := NewJobManager(rpc, Config{
		ZMQHashBlockAddr: "tcp://127.0.0.1:28334",
		ZMQRawBlockAddr:  "tcp://127.0.0.1:28332",
	}, nil, nil, nil)
	jm.curJob = &Job{Template: GetBlockTemplateResult{Previous: "old-parent"}}
	jm.zmqRawblockHealthy.Store(true)

	payload := make([]byte, 32)
	payload[31] = 1
	if err := jm.handleZMQNotification(context.Background(), "hashblock", payload); err != nil {
		t.Fatalf("handle hashblock notification: %v", err)
	}
	if got := rpcCalls.Load(); got != 0 {
		t.Fatalf("hashblock made %d GBT calls while rawblock was healthy, want 0", got)
	}
	parent := hex.EncodeToString(payload)
	if got := jm.recentZMQParentProof(parent); got != parent {
		t.Fatalf("hashblock parent proof = %q, want %q", got, parent)
	}
}

func TestHashBlockRefreshesWhenRawBlockIsUnhealthy(t *testing.T) {
	var rpcCalls atomic.Int32
	payload := make([]byte, 32)
	payload[31] = 2
	parent := hex.EncodeToString(payload)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rpcCalls.Add(1)
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		result, _ := json.Marshal(GetBlockTemplateResult{Previous: parent})
		_ = json.NewEncoder(w).Encode(rpcResponse{ID: req.ID, Result: result})
	}))
	t.Cleanup(srv.Close)

	rpc := &RPCClient{url: srv.URL, client: srv.Client(), lp: srv.Client()}
	jm := NewJobManager(rpc, Config{
		ZMQHashBlockAddr: "tcp://127.0.0.1:28334",
		ZMQRawBlockAddr:  "tcp://127.0.0.1:28332",
	}, nil, nil, nil)
	jm.curJob = &Job{Template: GetBlockTemplateResult{Previous: parent}}
	jm.zmqRawblockHealthy.Store(false)

	if err := jm.handleZMQNotification(context.Background(), "hashblock", payload); err != nil {
		t.Fatalf("handle hashblock notification: %v", err)
	}
	if got := rpcCalls.Load(); got != 0 {
		t.Fatalf("current-parent hashblock made %d GBT calls, want 0", got)
	}

	jm.mu.Lock()
	jm.curJob = &Job{Template: GetBlockTemplateResult{Previous: "old-parent"}}
	jm.mu.Unlock()
	if err := jm.handleZMQNotification(context.Background(), "hashblock", payload); err == nil {
		t.Fatal("expected deliberately incomplete fallback template to fail")
	}
	if got := rpcCalls.Load(); got != 1 {
		t.Fatalf("hashblock made %d GBT calls while rawblock was unhealthy, want 1", got)
	}
}

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

func TestZMQRefreshRacesActiveLongPollAndDiscardsLosingFetch(t *testing.T) {
	var rpcCalls atomic.Int32
	fallbackStarted := make(chan struct{})
	releaseFallback := make(chan struct{})
	defer closeTestChannel(releaseFallback)
	payload := rawBlockNotificationPayload(uint32(time.Now().Unix()), 2)
	tip, err := parseRawBlockTip(payload)
	if err != nil {
		t.Fatalf("parse raw block tip: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rpcCalls.Add(1)
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		close(fallbackStarted)
		<-releaseFallback
		result, _ := json.Marshal(GetBlockTemplateResult{Previous: tip.Hash})
		_ = json.NewEncoder(w).Encode(rpcResponse{ID: req.ID, Result: result})
	}))
	t.Cleanup(srv.Close)

	rpc := &RPCClient{url: srv.URL, client: srv.Client(), lp: srv.Client()}
	jm := NewJobManager(rpc, Config{}, nil, []byte{0x51}, nil)
	jm.curJob = &Job{Template: GetBlockTemplateResult{Previous: "old-parent"}}

	done := make(chan error, 1)
	go func() {
		done <- jm.handleZMQNotification(context.Background(), "rawblock", payload)
	}()

	select {
	case <-fallbackStarted:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("ZMQ fallback did not start immediately while long poll was active")
	}
	jm.mu.Lock()
	jm.curJob = &Job{Template: GetBlockTemplateResult{Previous: tip.Hash}}
	jm.mu.Unlock()
	close(releaseFallback)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("handle ZMQ notification: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ZMQ handler did not discard its fetch after long poll won")
	}
	if got := rpcCalls.Load(); got != 1 {
		t.Fatalf("raced fallback RPC calls = %d, want 1", got)
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

func TestRacedGBTNeverRollsBackANewerParent(t *testing.T) {
	const (
		baseParent  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		racedParent = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		newerParent = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	)
	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	defer closeTestChannel(releaseRequest)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		close(requestStarted)
		<-releaseRequest
		result, _ := json.Marshal(GetBlockTemplateResult{Previous: racedParent})
		_ = json.NewEncoder(w).Encode(rpcResponse{ID: req.ID, Result: result})
	}))
	t.Cleanup(srv.Close)

	rpc := &RPCClient{url: srv.URL, client: srv.Client(), lp: srv.Client()}
	jm := NewJobManager(rpc, Config{}, nil, []byte{0x51}, nil)
	base := &Job{Generation: 1, Template: GetBlockTemplateResult{Previous: baseParent}}
	jm.curJob = base

	done := make(chan error, 1)
	go func() {
		done <- jm.refreshJobCtxRaceParent(context.Background(), racedParent)
	}()
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("raced GBT did not start")
	}
	newer := &Job{Generation: 2, Template: GetBlockTemplateResult{Previous: newerParent}}
	jm.mu.Lock()
	jm.curJob = newer
	jm.mu.Unlock()
	close(releaseRequest)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("superseded raced GBT: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("superseded raced GBT did not finish")
	}
	if got := jm.CurrentJob(); got != newer {
		t.Fatalf("slower raced GBT rolled back current job: got=%p want=%p", got, newer)
	}
}
