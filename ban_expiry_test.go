package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestBanListPersistPrunesExpired verifies that persistLocked drops expired
// bans from the in-memory map and from the on-disk file, while preserving
// permanent and still-active entries.
func TestBanListPersistPrunesExpired(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bans.bin")

	now := time.Now()
	expiredTime := now.Add(-time.Hour)
	futureTime := now.Add(time.Hour)

	bl := &banList{
		entries: map[string]banEntry{
			"expired": {
				Worker: "expired",
				Until:  expiredTime,
				Reason: "too many invalid shares",
			},
			"permanent": {
				Worker: "permanent",
				// Until zero => permanent ban
				Until:  time.Time{},
				Reason: "manual ban",
			},
			"active": {
				Worker: "active",
				Until:  futureTime,
				Reason: "temporary ban",
			},
		},
		path: path,
	}

	if err := bl.persistLocked(); err != nil {
		t.Fatalf("persistLocked error: %v", err)
	}

	// Expired entry should be removed from the in-memory map.
	if _, ok := bl.entries["expired"]; ok {
		t.Fatalf("expected expired ban to be pruned from entries")
	}
	// Permanent and active entries should remain.
	if _, ok := bl.entries["permanent"]; !ok {
		t.Fatalf("expected permanent ban to remain in entries")
	}
	if _, ok := bl.entries["active"]; !ok {
		t.Fatalf("expected active ban to remain in entries")
	}

	// File should exist and contain only the non-expired entries.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read ban file error: %v", err)
	}
	bans, err := decodeBanEntries(data)
	if err != nil {
		t.Fatalf("decodeBanEntries error: %v", err)
	}

	if len(bans) != 2 {
		t.Fatalf("expected 2 bans on disk, got %d", len(bans))
	}

	seen := make(map[string]banEntry)
	for _, b := range bans {
		seen[b.Worker] = b
	}
	if _, ok := seen["expired"]; ok {
		t.Fatalf("expired ban should not be persisted on disk")
	}
	if p, ok := seen["permanent"]; !ok {
		t.Fatalf("permanent ban missing on disk")
	} else if !p.Until.IsZero() {
		t.Fatalf("permanent ban should have zero Until, got %v", p.Until)
	}
	if a, ok := seen["active"]; !ok {
		t.Fatalf("active ban missing on disk")
	} else if !a.Until.After(now) {
		t.Fatalf("active ban Until should be in the future relative to persist time, got %v (now %v)", a.Until, now)
	}
}
