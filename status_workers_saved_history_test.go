package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHandleSavedWorkerHistoryJSON_AllowsPoolHashrateSeries(t *testing.T) {
	store, err := newWorkerListStore(t.TempDir() + "/workers.db")
	if err != nil {
		t.Fatalf("newWorkerListStore: %v", err)
	}
	defer store.Close()

	now := time.Now().UTC().Truncate(savedWorkerPeriodBucket)
	minute := savedWorkerUnixMinute(now)
	if minute <= uint32(savedWorkerPeriodBucketMinutes) {
		t.Fatalf("minute unexpectedly small: %d", minute)
	}
	sampleMinute := minute - uint32(savedWorkerPeriodBucketMinutes)
	idx := savedWorkerRingIndex(sampleMinute)

	ring := &savedWorkerPeriodRing{}
	ring.minutes[idx] = sampleMinute
	ring.hashrateQ[idx] = encodeHashrateSI16(3210)
	ring.lastMinute = sampleMinute

	s := &StatusServer{
		workerLists: store,
		savedWorkerPeriods: map[string]*savedWorkerPeriodRing{
			savedWorkerPeriodPoolKey: ring,
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/saved-workers/history?hash=pool", nil)
	req = req.WithContext(contextWithClerkUser(req.Context(), &ClerkUser{UserID: "u_test"}))
	rr := httptest.NewRecorder()
	s.handleSavedWorkerHistoryJSON(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want %d", rr.Code, http.StatusOK)
	}

	var payload struct {
		Hash string   `json:"hash"`
		Name string   `json:"name"`
		HQ   []uint16 `json:"hq"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Hash != savedWorkerPeriodPoolKey {
		t.Fatalf("hash=%q want %q", payload.Hash, savedWorkerPeriodPoolKey)
	}
	if payload.Name != savedWorkerPeriodPoolKey {
		t.Fatalf("name=%q want %q", payload.Name, savedWorkerPeriodPoolKey)
	}
	if len(payload.HQ) == 0 {
		t.Fatalf("expected non-empty hq")
	}
}
