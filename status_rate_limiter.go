package main

import (
	"sync"
	"time"
)

type workerLookupRateLimiter struct {
	mu      sync.Mutex
	entries map[string]*workerLookupEntry
	max     int
	window  time.Duration
}

type workerLookupEntry struct {
	count int
	reset time.Time
}

func newWorkerLookupRateLimiter(max int, window time.Duration) *workerLookupRateLimiter {
	return &workerLookupRateLimiter{
		entries: make(map[string]*workerLookupEntry),
		max:     max,
		window:  window,
	}
}

// cleanupLocked drops entries that have been quiet for at least one extra
// window beyond their reset time, and caps the total number of tracked
// entries so the limiter cannot grow without bound when faced with many
// distinct keys. It expects l.mu to be held by the caller.
func (l *workerLookupRateLimiter) cleanupLocked(now time.Time) {
	if len(l.entries) == 0 {
		return
	}
	for k, entry := range l.entries {
		if now.After(entry.reset.Add(l.window)) {
			delete(l.entries, k)
		}
	}
	// As an extra safety net, trim back the map when it grows far beyond
	// the usual working set size. Since this limiter is best-effort and
	// per-key ordering is not important, we simply drop arbitrary entries
	// when above the cap.
	if len(l.entries) <= workerLookupMaxEntries {
		return
	}
	excess := len(l.entries) - workerLookupMaxEntries
	for k := range l.entries {
		delete(l.entries, k)
		excess--
		if excess <= 0 {
			break
		}
	}
}

func (l *workerLookupRateLimiter) allow(key string) bool {
	if l == nil {
		return true
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cleanupLocked(now)
	if key == "" {
		key = "unknown"
	}
	entry, ok := l.entries[key]
	if !ok || now.After(entry.reset) {
		entry = &workerLookupEntry{
			reset: now.Add(l.window),
		}
		l.entries[key] = entry
	}
	if entry.count >= l.max {
		return false
	}
	entry.count++
	return true
}
