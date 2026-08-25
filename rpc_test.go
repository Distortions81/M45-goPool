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

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type delayedReadCloser struct {
	reader  *strings.Reader
	delay   time.Duration
	delayed bool
}

func (r *delayedReadCloser) Read(p []byte) (int, error) {
	if !r.delayed {
		r.delayed = true
		time.Sleep(r.delay)
	}
	return r.reader.Read(p)
}

func (r *delayedReadCloser) Close() error { return nil }

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

func TestRPCRetryDelayWithBackoff(t *testing.T) {
	prevJitter := rpcRetryJitterFrac
	prevMax := rpcRetryMaxDelay
	t.Cleanup(func() {
		rpcRetryJitterFrac = prevJitter
		rpcRetryMaxDelay = prevMax
	})
	rpcRetryJitterFrac = 0
	rpcRetryMaxDelay = 250 * time.Millisecond

	base := rpcRetryDelay
	if got := rpcRetryDelayWithBackoff(1); got != base {
		t.Fatalf("attempt 1: expected %v, got %v", base, got)
	}
	if got := rpcRetryDelayWithBackoff(2); got != base*2 {
		t.Fatalf("attempt 2: expected %v, got %v", base*2, got)
	}
	if got := rpcRetryDelayWithBackoff(3); got != rpcRetryMaxDelay {
		t.Fatalf("attempt 3: expected %v, got %v", rpcRetryMaxDelay, got)
	}
	if got := rpcRetryDelayWithBackoff(4); got != rpcRetryMaxDelay {
		t.Fatalf("attempt 4: expected %v, got %v", rpcRetryMaxDelay, got)
	}
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

	if err := client.call("disconnectnode", []any{"180.181.249.116:20630"}, nil); err != nil {
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

	if err := client.call("disconnectnode", []any{"180.181.249.116:20630"}, nil); err != nil {
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

func TestRPCClientMeasuresCompleteGBTPhases(t *testing.T) {
	const bodyDelay = 20 * time.Millisecond
	result, _ := json.Marshal(GetBlockTemplateResult{Height: 1})
	body, _ := json.Marshal(rpcResponse{ID: 1, Result: result})
	httpClient := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body: &delayedReadCloser{
				reader: strings.NewReader(string(body)),
				delay:  bodyDelay,
			},
			Request: req,
		}, nil
	})}

	metrics := NewPoolMetrics()
	client := &RPCClient{
		url:     "http://bitcoin.test",
		client:  httpClient,
		lp:      &http.Client{Transport: httpClient.Transport},
		metrics: metrics,
	}
	var tpl GetBlockTemplateResult
	if err := client.callCtx(context.Background(), "getblocktemplate", []any{map[string]any{}}, &tpl); err != nil {
		t.Fatalf("getblocktemplate: %v", err)
	}
	if tpl.Height != 1 {
		t.Fatalf("template height = %d, want 1", tpl.Height)
	}

	timing := metrics.SnapshotGBTTiming()
	if timing.BodyLast < (bodyDelay - 5*time.Millisecond).Seconds() {
		t.Fatalf("body phase = %.6fs, want at least %.6fs", timing.BodyLast, (bodyDelay - 5*time.Millisecond).Seconds())
	}
	_, _, _, _, total, _, count, _, _, _, _, _ := metrics.SnapshotDiagnostics()
	if count != 1 {
		t.Fatalf("GBT count = %d, want 1", count)
	}
	phaseSum := timing.HeadersLast + timing.BodyLast + timing.DecodeLast
	if total < phaseSum-0.001 || total > phaseSum+0.001 {
		t.Fatalf("total %.6fs does not match phase sum %.6fs", total, phaseSum)
	}
}
