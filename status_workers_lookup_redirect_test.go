package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHandleWorkerLookupByWallet_RedirectsWalletWorkerToWalletHash(t *testing.T) {
	s := &StatusServer{
		workerLookupLimiter: newWorkerLookupRateLimiter(100, time.Minute),
	}

	req := httptest.NewRequest(http.MethodGet, "/stats/bc1qexample.worker1", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()

	s.handleWorkerLookupByWallet(rec, req, "/stats")

	resp := rec.Result()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected status %d, got %d", http.StatusSeeOther, resp.StatusCode)
	}
	want := "/worker/search?hash=" + workerNameHash("bc1qexample")
	if got := resp.Header.Get("Location"); got != want {
		t.Fatalf("unexpected redirect location:\n got: %s\nwant: %s", got, want)
	}
}

func TestHandleWorkerLookupByWalletHash_PreservesPrehashedWallet(t *testing.T) {
	s := &StatusServer{
		workerLookupLimiter: newWorkerLookupRateLimiter(100, time.Minute),
	}

	hash := "A3D3B95A76F1A4B2C5D8E9F01234567890ABCDEF1234567890ABCDEF12345678"
	req := httptest.NewRequest(http.MethodGet, "/users/"+hash, nil)
	req.RemoteAddr = "127.0.0.1:23456"
	rec := httptest.NewRecorder()

	s.handleWorkerLookupByWalletHash(rec, req, "/users")

	resp := rec.Result()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected status %d, got %d", http.StatusSeeOther, resp.StatusCode)
	}
	want := "/worker/search?hash=" + "a3d3b95a76f1a4b2c5d8e9f01234567890abcdef1234567890abcdef12345678"
	if got := resp.Header.Get("Location"); got != want {
		t.Fatalf("unexpected redirect location:\n got: %s\nwant: %s", got, want)
	}
}
