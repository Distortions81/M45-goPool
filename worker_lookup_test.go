package main

import (
	"strconv"
	"testing"
	"time"
)

// TestWorkerLookupRateLimiterDropsExpired verifies that allow() prunes
// entries whose reset time is sufficiently in the past.
func TestWorkerLookupRateLimiterDropsExpired(t *testing.T) {
	l := newWorkerLookupRateLimiter(10, time.Second)
	now := time.Now()

	l.mu.Lock()
	l.entries["expired"] = &workerLookupEntry{
		count: 1,
		// Reset well in the past so cleanupLocked should drop it.
		reset: now.Add(-2 * l.window),
	}
	l.entries["active"] = &workerLookupEntry{
		count: 1,
		// Reset in the future so it should be retained.
		reset: now.Add(2 * l.window),
	}
	l.mu.Unlock()

	// Trigger cleanup via allow; key choice doesn't matter here.
	if !l.allow("someone") {
		t.Fatalf("expected allow to succeed")
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if _, ok := l.entries["expired"]; ok {
		t.Fatalf("expected expired entry to be pruned from entries")
	}
	if _, ok := l.entries["active"]; !ok {
		t.Fatalf("expected active entry to remain in entries after cleanup")
	}
}

// TestWorkerLookupRateLimiterCapsEntries verifies that cleanupLocked enforces
// workerLookupMaxEntries as an upper bound on the number of tracked keys.
func TestWorkerLookupRateLimiterCapsEntries(t *testing.T) {
	l := newWorkerLookupRateLimiter(10, time.Minute)
	now := time.Now()

	// Seed the limiter with more entries than the configured cap, all with
	// reset times in the future so they are not removed as expired.
	l.mu.Lock()
	for i := 0; i < workerLookupMaxEntries+16; i++ {
		key := "key-" + strconv.Itoa(i)
		l.entries[key] = &workerLookupEntry{
			reset: now.Add(2 * l.window),
		}
	}
	l.mu.Unlock()

	// Call allow with an existing key so we don't introduce a new entry
	// after cleanup. This will run cleanupLocked under the limiter's mutex.
	_ = l.allow("key-0")

	l.mu.Lock()
	defer l.mu.Unlock()

	if len(l.entries) > workerLookupMaxEntries {
		t.Fatalf("expected entries to be capped at %d, got %d", workerLookupMaxEntries, len(l.entries))
	}
}
