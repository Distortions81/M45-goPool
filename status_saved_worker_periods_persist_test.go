package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestSavedWorkerPeriodsSnapshotRoundTrip(t *testing.T) {
	base := time.Now().UTC().Truncate(savedWorkerPeriodBucket)
	if base.IsZero() {
		t.Fatal("base time is zero")
	}
	minuteNow := savedWorkerUnixMinute(base)
	if minuteNow == 0 {
		t.Fatal("minuteNow is zero")
	}

	dir := t.TempDir()
	src := &StatusServer{
		savedWorkerPeriods: make(map[string]*savedWorkerPeriodRing),
	}
	src.UpdateConfig(Config{DataDir: dir})

	src.savedWorkerPeriodsMu.Lock()
	src.savedWorkerPeriodsLastBucket = base
	ringA := &savedWorkerPeriodRing{}
	for i, m := range []uint32{minuteNow - 3, minuteNow - 2, minuteNow - 1} {
		idx := savedWorkerRingIndex(m)
		ringA.minutes[idx] = m
		ringA.hashrateQ[idx] = uint16(100 + i)
		ringA.bestDifficultyQ[idx] = uint16(200 + i)
		ringA.lastMinute = m
	}
	ringB := &savedWorkerPeriodRing{}
	for i, m := range []uint32{minuteNow - 5, minuteNow - 4} {
		idx := savedWorkerRingIndex(m)
		ringB.minutes[idx] = m
		ringB.hashrateQ[idx] = uint16(300 + i)
		ringB.bestDifficultyQ[idx] = uint16(400 + i)
		ringB.lastMinute = m
	}
	src.savedWorkerPeriods["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"] = ringA
	src.savedWorkerPeriods["bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"] = ringB
	src.savedWorkerPeriodsMu.Unlock()

	n, err := src.persistSavedWorkerPeriodsSnapshot()
	if err != nil {
		t.Fatalf("persistSavedWorkerPeriodsSnapshot: %v", err)
	}
	if n != 2 {
		t.Fatalf("persist count = %d, want 2", n)
	}
	path := src.savedWorkerPeriodsSnapshotPath()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat snapshot file: %v", err)
	}
	if info.Size() <= 0 {
		t.Fatalf("snapshot file size = %d, want > 0", info.Size())
	}

	dst := &StatusServer{}
	dst.UpdateConfig(Config{DataDir: dir})
	n, err = dst.loadSavedWorkerPeriodsSnapshot()
	if err != nil {
		t.Fatalf("loadSavedWorkerPeriodsSnapshot: %v", err)
	}
	if n != 2 {
		t.Fatalf("load count = %d, want 2", n)
	}

	dst.savedWorkerPeriodsMu.Lock()
	defer dst.savedWorkerPeriodsMu.Unlock()
	if !dst.savedWorkerPeriodsLastBucket.Equal(base) {
		t.Fatalf("last bucket = %v, want %v", dst.savedWorkerPeriodsLastBucket, base)
	}
	if len(dst.savedWorkerPeriods) != len(src.savedWorkerPeriods) {
		t.Fatalf("worker count = %d, want %d", len(dst.savedWorkerPeriods), len(src.savedWorkerPeriods))
	}
	for hash, want := range src.savedWorkerPeriods {
		got := dst.savedWorkerPeriods[hash]
		if got == nil {
			t.Fatalf("missing ring for %s", hash)
		}
		if *got != *want {
			t.Fatalf("ring mismatch for %s", hash)
		}
	}
}

func TestSavedWorkerPeriodsSnapshotRejectsMissingEndMagic(t *testing.T) {
	base := time.Now().UTC().Truncate(savedWorkerPeriodBucket)
	minuteNow := savedWorkerUnixMinute(base)
	if minuteNow == 0 {
		t.Fatal("minuteNow is zero")
	}

	dir := t.TempDir()
	src := &StatusServer{
		savedWorkerPeriods: make(map[string]*savedWorkerPeriodRing),
	}
	src.UpdateConfig(Config{DataDir: dir})

	hash := strings.Repeat("a", 64)
	ring := &savedWorkerPeriodRing{}
	m := minuteNow - 1
	idx := savedWorkerRingIndex(m)
	ring.minutes[idx] = m
	ring.hashrateQ[idx] = 123
	ring.bestDifficultyQ[idx] = 456
	ring.lastMinute = m

	src.savedWorkerPeriodsMu.Lock()
	src.savedWorkerPeriodsLastBucket = base
	src.savedWorkerPeriods[hash] = ring
	src.savedWorkerPeriodsMu.Unlock()

	if _, err := src.persistSavedWorkerPeriodsSnapshot(); err != nil {
		t.Fatalf("persistSavedWorkerPeriodsSnapshot: %v", err)
	}
	path := src.savedWorkerPeriodsSnapshotPath()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if len(data) <= len(savedWorkerPeriodsSnapshotEndMagic) {
		t.Fatalf("snapshot too small: %d", len(data))
	}
	if err := os.WriteFile(path, data[:len(data)-len(savedWorkerPeriodsSnapshotEndMagic)], 0o644); err != nil {
		t.Fatalf("truncate snapshot footer: %v", err)
	}

	dst := &StatusServer{}
	dst.UpdateConfig(Config{DataDir: dir})
	if _, err := dst.loadSavedWorkerPeriodsSnapshot(); err == nil {
		t.Fatal("expected error for missing snapshot end magic")
	}
}
