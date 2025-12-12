package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
