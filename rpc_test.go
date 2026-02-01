package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRPCClientHTTPStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	client := &RPCClient{
		url:    srv.URL,
		client: srv.Client(),
		lp:     srv.Client(),
	}

	var out any
	err := client.call("getblockchaininfo", nil, &out)
	if err == nil {
		t.Fatal("expected error from unauthorized response")
	}
	if !strings.Contains(err.Error(), "401 Unauthorized") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRPCClientHTTPStatusWithRPCError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		resp := rpcResponse{Error: &rpcError{Code: -32601, Message: "Method not found"}, ID: 1}
		data, _ := json.Marshal(resp)
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)

	client := &RPCClient{
		url:    srv.URL,
		client: srv.Client(),
		lp:     srv.Client(),
	}

	err := client.call("getaddressinfo", nil, nil)
	if err == nil {
		t.Fatal("expected method not found error")
	}
	rerr, ok := err.(*rpcError)
	if !ok {
		t.Fatalf("expected rpcError, got %T: %v", err, err)
	}
	if rerr.Code != -32601 {
		t.Fatalf("unexpected error code: %d", rerr.Code)
	}
}

func TestRPCClientClearsLastErrorOnSuccess(t *testing.T) {
	client := &RPCClient{}
	client.recordLastError(errors.New("boom"))
	client.recordRPCCallSuccess()
	if err := client.LastError(); err != nil {
		t.Fatalf("expected last error cleared after success, got: %v", err)
	}
}

func TestFetchPayoutScriptMissingAddress(t *testing.T) {
	if _, err := fetchPayoutScript(&RPCClient{}, ""); err == nil {
		t.Fatal("expected payout address error")
	}
}

func TestFetchPayoutScriptValidateAddressFallback(t *testing.T) {
	// Any valid mainnet address is sufficient here; we only care that the
	// helper can derive a non-empty scriptPubKey without RPC.
	script, err := fetchPayoutScript(nil, "1BitcoinEaterAddressDontSendf59kuE")
	if err != nil {
		t.Fatalf("fetchPayoutScript error: %v", err)
	}
	if len(script) == 0 {
		t.Fatalf("expected non-empty script")
	}
}

func TestRPCClientReloadsCookieOnModification(t *testing.T) {
	dir := t.TempDir()
	cookiePath := filepath.Join(dir, ".cookie")
	if err := os.WriteFile(cookiePath, []byte("first:token"), 0o600); err != nil {
		t.Fatalf("write initial cookie: %v", err)
	}
	client := &RPCClient{
		user:       "first",
		pass:       "token",
		cookiePath: cookiePath,
	}
	client.initCookieStat()
	if err := os.WriteFile(cookiePath, []byte("second:secret"), 0o600); err != nil {
		t.Fatalf("rewrite cookie: %v", err)
	}
	client.reloadCookieIfChanged()

	client.authMu.RLock()
	user, pass := client.user, client.pass
	client.authMu.RUnlock()

	if user != "second" || pass != "secret" {
		t.Fatalf("expected credentials reloaded, got %q/%q", user, pass)
	}
}

func TestRPCClientLoadsCookieWhenCredentialsEmpty(t *testing.T) {
	dir := t.TempDir()
	cookiePath := filepath.Join(dir, ".cookie")
	if err := os.WriteFile(cookiePath, []byte("user:pass"), 0o600); err != nil {
		t.Fatalf("write cookie: %v", err)
	}

	client := &RPCClient{
		cookiePath: cookiePath,
	}
	client.initCookieStat()

	client.authMu.RLock()
	user, pass := client.user, client.pass
	client.authMu.RUnlock()

	if user != "user" || pass != "pass" {
		t.Fatalf("expected credentials loaded, got %q/%q", user, pass)
	}
}

func TestRPCClientCookieWatcherLoadsWhenCookieAppears(t *testing.T) {
	oldInterval := rpcCookieWatchInterval
	rpcCookieWatchInterval = 10 * time.Millisecond
	t.Cleanup(func() { rpcCookieWatchInterval = oldInterval })

	dir := t.TempDir()
	cookiePath := filepath.Join(dir, ".cookie")

	client := &RPCClient{
		cookiePath: cookiePath,
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	client.StartCookieWatcher(ctx)

	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(cookiePath, []byte("alice:secret"), 0o600); err != nil {
		t.Fatalf("write cookie: %v", err)
	}

	deadline := time.After(500 * time.Millisecond)
	for {
		client.authMu.RLock()
		user, pass := client.user, client.pass
		client.authMu.RUnlock()
		if user == "alice" && pass == "secret" {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("expected watcher to load cookie credentials, got %q/%q", user, pass)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestRPCErrorPropagatesAndLabelsMetrics(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		_ = json.NewEncoder(w).Encode(rpcResponse{
			Error: &rpcError{Code: -1, Message: "boom"},
			ID:    req.ID,
		})
	}))
	t.Cleanup(srv.Close)

	metrics := NewPoolMetrics()
	client := &RPCClient{
		url:    srv.URL,
		client: srv.Client(),
		lp: &http.Client{
			Transport: srv.Client().Transport,
		},
		metrics: metrics,
	}

	if err := client.call("getblock", nil, nil); err == nil {
		t.Fatal("expected rpc error from call")
	} else if _, ok := err.(*rpcError); !ok {
		t.Fatalf("expected rpcError, got %T", err)
	}
	if err := client.callLongPoll("getblocktemplate", nil, nil); err == nil {
		t.Fatal("expected rpc error from callLongPoll")
	}

	// With Prometheus removed, we only assert that the calls still
	// propagate rpcError and do not panic when recording metrics.
}

func TestRPCClientIgnoresDisconnectNodeNotFoundHTTP500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(rpcResponse{
			Error: &rpcError{Code: -29, Message: "Node not found in connected nodes"},
			ID:    req.ID,
		})
	}))
	t.Cleanup(srv.Close)

	metrics := NewPoolMetrics()
	client := &RPCClient{
		url:     srv.URL,
		client:  srv.Client(),
		lp:      srv.Client(),
		metrics: metrics,
	}

	if err := client.call("disconnectnode", []interface{}{"180.181.249.116:20630"}, nil); err != nil {
		t.Fatalf("expected disconnectnode -29 to be ignored, got: %v", err)
	}

	_, _, _, _, _, _, _, _, _, _, rpcErrors, _ := metrics.SnapshotDiagnostics()
	if rpcErrors != 0 {
		t.Fatalf("expected rpcErrors=0, got %d", rpcErrors)
	}
}

func TestRPCClientIgnoresDisconnectNodeNotFoundHTTP200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		_ = json.NewEncoder(w).Encode(rpcResponse{
			Error: &rpcError{Code: -29, Message: "Node not found in connected nodes"},
			ID:    req.ID,
		})
	}))
	t.Cleanup(srv.Close)

	metrics := NewPoolMetrics()
	client := &RPCClient{
		url:     srv.URL,
		client:  srv.Client(),
		lp:      srv.Client(),
		metrics: metrics,
	}

	if err := client.call("disconnectnode", []interface{}{"180.181.249.116:20630"}, nil); err != nil {
		t.Fatalf("expected disconnectnode -29 to be ignored, got: %v", err)
	}

	_, _, _, _, _, _, _, _, _, _, rpcErrors, _ := metrics.SnapshotDiagnostics()
	if rpcErrors != 0 {
		t.Fatalf("expected rpcErrors=0, got %d", rpcErrors)
	}
}

func TestRPCClientTracksDisconnectsAndReconnects(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(rpcResponse{
			Result: json.RawMessage("null"),
			ID:     req.ID,
		})
	}))
	t.Cleanup(srv.Close)

	client := &RPCClient{
		url:    srv.URL,
		client: srv.Client(),
		lp:     srv.Client(),
	}

	if err := client.call("getblockchaininfo", nil, nil); err != nil {
		t.Fatalf("expected call to succeed after retry, got: %v", err)
	}
	if got := client.Disconnects(); got != 1 {
		t.Fatalf("expected disconnects=1, got %d", got)
	}
	if got := client.Reconnects(); got != 1 {
		t.Fatalf("expected reconnects=1, got %d", got)
	}
	if !client.Healthy() {
		t.Fatalf("expected client to be healthy after retry")
	}
}
