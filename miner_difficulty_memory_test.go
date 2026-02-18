package main

import (
	"path/filepath"
	"testing"
	"time"
)

func TestDifficultyCacheJSONRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "difficulty_cache.json")

	c1 := &difficultyCache{
		byWorker:   make(map[string]difficultyEntry),
		bySession:  make(map[string]difficultyEntry),
		maxEntries: 100,
	}
	c1.setWorker("workerhash1", 1024, time.Now())
	c1.setSession("session1", 2048, time.Now())

	if err := c1.saveToJSON(path); err != nil {
		t.Fatalf("saveToJSON: %v", err)
	}

	c2 := &difficultyCache{
		byWorker:   make(map[string]difficultyEntry),
		bySession:  make(map[string]difficultyEntry),
		maxEntries: 100,
	}
	now := time.Now().Add(10 * time.Second)
	if err := c2.loadFromJSON(path, now); err != nil {
		t.Fatalf("loadFromJSON: %v", err)
	}

	if diff, ok := c2.getWorker("workerhash1", time.Hour); !ok || diff != 1024 {
		t.Fatalf("getWorker: ok=%v diff=%v", ok, diff)
	}
	if diff, ok := c2.getSession("session1", time.Hour); !ok || diff != 2048 {
		t.Fatalf("getSession: ok=%v diff=%v", ok, diff)
	}
}
