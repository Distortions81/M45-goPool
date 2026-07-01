package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestACMEChallengeHandlerServesRuntimeWebroot(t *testing.T) {
	root := t.TempDir()
	challengeDir := filepath.Join(root, ".well-known", "acme-challenge")
	if err := os.MkdirAll(challengeDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(challengeDir, "token"), []byte("challenge-response"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	handler := newACMEChallengeHandler(root)
	req := httptest.NewRequest(http.MethodGet, "/.well-known/acme-challenge/token", nil)
	rec := httptest.NewRecorder()

	if !handler.ServeChallenge(rec, req) {
		t.Fatalf("ServeChallenge returned false")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "challenge-response" {
		t.Fatalf("body=%q, want challenge-response", got)
	}
}

func TestACMEChallengeHandlerRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "secret"), []byte("secret"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	handler := newACMEChallengeHandler(root)
	req := httptest.NewRequest(http.MethodGet, "/.well-known/acme-challenge/../secret", nil)
	rec := httptest.NewRecorder()

	if !handler.ServeChallenge(rec, req) {
		t.Fatalf("ServeChallenge did not handle ACME route")
	}
	if rec.Code == http.StatusOK {
		t.Fatalf("traversal status=%d body=%q", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("served secret body: %q", rec.Body.String())
	}
}

func TestServeACMEOrFallbackBypassesRedirectForChallenge(t *testing.T) {
	root := t.TempDir()
	challengeDir := filepath.Join(root, ".well-known", "acme-challenge")
	if err := os.MkdirAll(challengeDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(challengeDir, "token"), []byte("challenge-response"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	redirect := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.test"+r.URL.RequestURI(), http.StatusTemporaryRedirect)
	})
	handler := serveACMEOrFallback(newACMEChallengeHandler(root), redirect)

	challengeReq := httptest.NewRequest(http.MethodGet, "/.well-known/acme-challenge/token", nil)
	challengeRec := httptest.NewRecorder()
	handler.ServeHTTP(challengeRec, challengeReq)
	if challengeRec.Code != http.StatusOK {
		t.Fatalf("challenge status=%d body=%q", challengeRec.Code, challengeRec.Body.String())
	}
	if got := strings.TrimSpace(challengeRec.Body.String()); got != "challenge-response" {
		t.Fatalf("challenge body=%q, want challenge-response", got)
	}

	otherReq := httptest.NewRequest(http.MethodGet, "/pool", nil)
	otherRec := httptest.NewRecorder()
	handler.ServeHTTP(otherRec, otherReq)
	if otherRec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("non-challenge status=%d, want %d", otherRec.Code, http.StatusTemporaryRedirect)
	}
}
