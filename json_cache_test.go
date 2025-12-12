package main

import (
	"bytes"
	"strconv"
	"testing"
	"time"
)

// TestCachedJSONResponseTTL verifies that cachedJSONResponse returns the
// cached payload within the TTL and rebuilds the payload after expiry.
func TestCachedJSONResponseTTL(t *testing.T) {
	s := &StatusServer{
		jsonCache: make(map[string]cachedJSONResponse),
	}

	key := "test-json"
	ttl := 50 * time.Millisecond

	buildCount := 0
	build := func() ([]byte, error) {
		buildCount++
		return []byte("payload-" + strconv.Itoa(buildCount)), nil
	}

	// First call should invoke build.
	p1, updated1, expires1, err := s.cachedJSONResponse(key, ttl, build)
	if err != nil {
		t.Fatalf("first cachedJSONResponse error: %v", err)
	}
	if buildCount != 1 {
		t.Fatalf("expected buildCount=1 after first call, got %d", buildCount)
	}
	if len(p1) == 0 {
		t.Fatalf("expected non-empty payload from first call")
	}
	if !expires1.After(updated1) {
		t.Fatalf("expected expiresAt > updatedAt, got updated=%v expires=%v", updated1, expires1)
	}

	// Second call within TTL should return cached payload and not rebuild.
	p2, updated2, expires2, err := s.cachedJSONResponse(key, ttl, build)
	if err != nil {
		t.Fatalf("second cachedJSONResponse error: %v", err)
	}
	if buildCount != 1 {
		t.Fatalf("expected buildCount to remain 1 within TTL, got %d", buildCount)
	}
	if !bytes.Equal(p1, p2) {
		t.Fatalf("expected cached payload to match original")
	}
	if !updated2.Equal(updated1) || !expires2.Equal(expires1) {
		t.Fatalf("expected updatedAt/expiresAt to remain unchanged within TTL")
	}

	// After TTL has passed, a new call should rebuild.
	time.Sleep(ttl + 20*time.Millisecond)

	p3, updated3, expires3, err := s.cachedJSONResponse(key, ttl, build)
	if err != nil {
		t.Fatalf("third cachedJSONResponse error: %v", err)
	}
	if buildCount != 2 {
		t.Fatalf("expected buildCount=2 after TTL expiry, got %d", buildCount)
	}
	if bytes.Equal(p1, p3) {
		t.Fatalf("expected new payload after TTL expiry")
	}
	if !expires3.After(updated3) {
		t.Fatalf("expected new expiresAt > new updatedAt after rebuild")
	}
}
