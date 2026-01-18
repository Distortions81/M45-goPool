package main

import (
	"path/filepath"
	"testing"
	"time"
)

// FuzzDecodeCoinbaseHeight exercises decodeCoinbaseHeight with arbitrary
// script bytes to ensure it never panics and only returns non-negative
// heights within the encoded byte width.
func FuzzDecodeCoinbaseHeight(f *testing.F) {
	f.Add([]byte{0x03, 0x01, 0x00, 0x00}) // height=1
	f.Add([]byte{0x01, 0xff})             // height=255
	f.Add([]byte{})                       // empty

	f.Fuzz(func(t *testing.T, script []byte) {
		h := decodeCoinbaseHeight(script)
		if h < 0 {
			t.Fatalf("decodeCoinbaseHeight returned negative height %d", h)
		}
		if len(script) == 0 {
			if h != 0 {
				t.Fatalf("expected height 0 for empty script, got %d", h)
			}
			return
		}
		op := int(script[0])
		if op < 1 || op > 4 || 1+op > len(script) {
			// Out-of-range or truncated prefixes must produce 0.
			if h != 0 {
				t.Fatalf("expected height 0 for invalid prefix/op, got %d", h)
			}
			return
		}
		// Basic sanity: height must fit within op bytes.
		if h > 0 && h > (1<<(8*op))-1 {
			t.Fatalf("height %d exceeds %d-byte range", h, op)
		}
	})
}

// FuzzBanListRoundTrip feeds random worker names and ban horizons through
// banList.markBan/persist/load to ensure bans remain self-consistent and
// expired entries are dropped as expected.
func FuzzBanListRoundTrip(f *testing.F) {
	f.Add("worker1", int64(60))
	f.Add("worker2", int64(0))    // unban
	f.Add("worker3", int64(3600)) // long ban

	f.Fuzz(func(t *testing.T, worker string, horizonSec int64) {
		if worker == "" {
			return
		}
		dir := t.TempDir()
		dbPath := filepath.Join(dir, "state", "workers.db")
		db, err := openStateDB(dbPath)
		if err != nil {
			t.Fatalf("openStateDB error: %v", err)
		}
		defer db.Close()

		// Set up the shared DB for this test
		cleanup := setSharedStateDBForTest(db)
		defer cleanup()

		bl := &banStore{}

		now := time.Now()
		var until time.Time
		if horizonSec > 0 {
			until = now.Add(time.Duration(horizonSec) * time.Second)
		}
		if err := bl.markBan(worker, until, "fuzz-test", now); err != nil {
			t.Fatalf("markBan error: %v", err)
		}

		// Reload from disk and verify lookup semantics.
		db2, err := openStateDB(dbPath)
		if err != nil {
			t.Fatalf("reload sqlite db error: %v", err)
		}
		defer db2.Close()

		// Update the shared DB for lookup
		cleanup2 := setSharedStateDBForTest(db2)
		defer cleanup2()

		bl2 := &banStore{}
		entry, ok := bl2.lookup(worker, now)

		if horizonSec <= 0 {
			// Zero/negative horizon means unbanned.
			if ok {
				t.Fatalf("expected no ban for %q, got %+v", worker, entry)
			}
			return
		}

		// Non-zero horizon: entry must exist and be unexpired.
		if !ok {
			t.Fatalf("expected ban entry for %q", worker)
		}
		if entry.Worker != worker {
			t.Fatalf("ban entry worker mismatch: got %q, want %q", entry.Worker, worker)
		}
		if !entry.Until.IsZero() && entry.Until.Before(now) {
			t.Fatalf("loaded ban entry already expired: until=%v now=%v", entry.Until, now)
		}
	})
}
