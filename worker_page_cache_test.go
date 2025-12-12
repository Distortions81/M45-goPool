package main

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// TestWorkerPageCacheHitReturnsCachedPayload verifies that when a cached
// worker page is present and unexpired, handleWorkerStatusBySHA256 returns
// the cached payload without re-rendering the template.
func TestWorkerPageCacheHitReturnsCachedPayload(t *testing.T) {
	s := &StatusServer{
		cfg:             Config{},
		workerPageCache: make(map[string]cachedWorkerPage),
	}

	hash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cacheKey := hash + "|priv" // default privacy mode is on
	now := time.Now()
	payload := []byte("cached-worker-page")

	s.workerPageCache[cacheKey] = cachedWorkerPage{
		payload:   payload,
		updatedAt: now.Add(-time.Minute),
		expiresAt: now.Add(time.Minute),
	}

	req := httptest.NewRequest(http.MethodGet, "/worker/sha256?hash="+hash, nil)
	rec := httptest.NewRecorder()

	s.handleWorkerStatusBySHA256(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if got := rec.Body.String(); got != string(payload) {
		t.Fatalf("expected cached payload %q, got %q", string(payload), got)
	}
}

// TestCacheWorkerPageEvictsExpiredWhenAtCapacity verifies that cacheWorkerPage
// evicts expired entries first when the cache has reached workerPageCacheLimit.
func TestCacheWorkerPageEvictsExpiredWhenAtCapacity(t *testing.T) {
	s := &StatusServer{
		workerPageCache: make(map[string]cachedWorkerPage),
	}
	now := time.Now()

	// Seed the cache with workerPageCacheLimit entries, one of which is expired.
	expiredKey := "expired-worker"
	for i := 0; i < workerPageCacheLimit; i++ {
		key := "worker-" + strconv.Itoa(i)
		expiresAt := now.Add(time.Minute)
		if i == 0 {
			key = expiredKey
			expiresAt = now.Add(-time.Minute)
		}
		s.workerPageCache[key] = cachedWorkerPage{
			payload:   []byte("old-" + key),
			updatedAt: now.Add(-2 * time.Minute),
			expiresAt: expiresAt,
		}
	}

	newKey := "new-worker"
	newPayload := []byte("new-payload")

	s.cacheWorkerPage(newKey, now, newPayload)

	if len(s.workerPageCache) > workerPageCacheLimit {
		t.Fatalf("expected cache size <= %d, got %d", workerPageCacheLimit, len(s.workerPageCache))
	}
	if _, ok := s.workerPageCache[expiredKey]; ok {
		t.Fatalf("expected expired entry %q to be evicted", expiredKey)
	}
	if entry, ok := s.workerPageCache[newKey]; !ok {
		t.Fatalf("expected new entry %q to be present", newKey)
	} else if string(entry.payload) != string(newPayload) {
		t.Fatalf("new entry payload mismatch: got %q want %q", string(entry.payload), string(newPayload))
	}
}
