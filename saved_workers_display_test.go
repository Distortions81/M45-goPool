package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func jsCensorSavedWorkerName(s string) string {
	if len(s) <= 16 {
		return s
	}
	if after, ok := strings.CutPrefix(s, ".."); ok {
		s = strings.TrimSpace(after)
	}
	if len(s) <= 16 {
		return s
	}
	return s[:8] + ".." + s[len(s)-8:]
}

func savedWorkersClientDisplayName(name, hash string, hide bool) string {
	raw := name
	if strings.TrimSpace(raw) == "" {
		raw = hash
	}
	if strings.TrimSpace(raw) == "" {
		raw = "Unknown"
	}
	if !hide {
		return raw
	}
	if censored := jsCensorSavedWorkerName(raw); censored != "" {
		return censored
	}
	return "Hidden"
}

func TestSavedWorkersJSON_UsesCensoredDisplayAndClientRenderCompatibility(t *testing.T) {
	store, err := newWorkerListStore(t.TempDir() + "/saved_workers.sqlite")
	if err != nil {
		t.Fatalf("newWorkerListStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const (
		userID = "display-user"
		worker = "bc1qveryverylongaddresscomponent1234567890abcde.worker-01"
	)
	if err := store.Add(userID, worker); err != nil {
		t.Fatalf("store.Add: %v", err)
	}

	s := &StatusServer{workerLists: store}
	req := httptest.NewRequest(http.MethodGet, "/api/saved-workers", nil)
	req = req.WithContext(contextWithClerkUser(req.Context(), &ClerkUser{UserID: userID, SessionID: "s1"}))
	rr := httptest.NewRecorder()
	s.handleSavedWorkersJSON(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}

	var payload struct {
		OfflineWorkers []struct {
			Name string `json:"name"`
			Hash string `json:"hash"`
		} `json:"offline_workers"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.OfflineWorkers) != 1 {
		t.Fatalf("expected 1 offline worker, got %d", len(payload.OfflineWorkers))
	}
	got := payload.OfflineWorkers[0]
	wantName := shortWorkerName(worker, workerNamePrefix, workerNameSuffix)
	wantHash := workerNameHash(worker)
	if got.Name != wantName {
		t.Fatalf("expected API display name %q, got %q", wantName, got.Name)
	}
	if got.Hash != wantHash {
		t.Fatalf("expected API hash %q, got %q", wantHash, got.Hash)
	}

	// Mirror the client-side rendering behavior in saved_workers.tmpl.
	shownNoPrivacy := savedWorkersClientDisplayName(got.Name, got.Hash, false)
	if shownNoPrivacy != wantName {
		t.Fatalf("privacy-off render mismatch: got %q want %q", shownNoPrivacy, wantName)
	}
	shownPrivacy := savedWorkersClientDisplayName(got.Name, got.Hash, true)
	wantPrivacy := jsCensorSavedWorkerName(wantName)
	if shownPrivacy != wantPrivacy {
		t.Fatalf("privacy-on render mismatch: got %q want %q", shownPrivacy, wantPrivacy)
	}
}
