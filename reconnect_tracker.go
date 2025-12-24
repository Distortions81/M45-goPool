package main

import (
	"sync"
	"time"
)

// reconnectTracker tracks per-IP connection attempts over a sliding window
// and temporarily bans addresses that reconnect too aggressively.
type reconnectTracker struct {
	mu          sync.Mutex
	entries     map[string]*reconnectEntry
	threshold   int
	window      time.Duration
	banDuration time.Duration
}

type reconnectEntry struct {
	count       int
	reset       time.Time
	bannedUntil time.Time
}

func newReconnectTracker(threshold int, window, banDuration time.Duration) *reconnectTracker {
	if threshold <= 0 || window <= 0 || banDuration <= 0 {
		return nil
	}
	return &reconnectTracker{
		entries:     make(map[string]*reconnectEntry),
		threshold:   threshold,
		window:      window,
		banDuration: banDuration,
	}
}

func (rt *reconnectTracker) allow(host string, now time.Time) bool {
	if rt == nil || host == "" {
		return true
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()

	entry, ok := rt.entries[host]
	if !ok {
		entry = &reconnectEntry{
			reset: now.Add(rt.window),
		}
		rt.entries[host] = entry
	}

	if !entry.bannedUntil.IsZero() && now.Before(entry.bannedUntil) {
		return false
	}

	if now.After(entry.reset) {
		entry.count = 0
		entry.reset = now.Add(rt.window)
		entry.bannedUntil = time.Time{}
	}

	entry.count++
	if entry.count > rt.threshold {
		entry.bannedUntil = now.Add(rt.banDuration)
		return false
	}

	return true
}
