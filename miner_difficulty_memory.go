package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type difficultyEntry struct {
	diff      float64
	updatedAt time.Time
}

type difficultyCache struct {
	mu         sync.Mutex
	byWorker   map[string]difficultyEntry
	bySession  map[string]difficultyEntry
	maxEntries int
}

var globalDifficultyCache = &difficultyCache{
	byWorker:   make(map[string]difficultyEntry, 4096),
	bySession:  make(map[string]difficultyEntry, 1024),
	maxEntries: 200_000,
}

type difficultyCacheSnapshot struct {
	Worker  map[string]float64 `json:"worker,omitempty"`
	Session map[string]float64 `json:"session,omitempty"`
}

func difficultyCacheJSONPath(dataDir string) string {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		dataDir = defaultDataDir
	}
	return filepath.Join(dataDir, "state", "difficulty_cache.json")
}

func (mc *MinerConn) rememberDifficulty(diff float64, now time.Time) {
	if mc == nil || diff <= 0 {
		return
	}
	if hash := mc.currentWorkerHash(); hash != "" {
		globalDifficultyCache.setWorker(hash, diff, now)
	}
	if sid := mc.currentSessionID(); sid != "" {
		globalDifficultyCache.setSession(sid, diff, now)
	}
}

func (c *difficultyCache) loadFromJSON(path string, now time.Time) error {
	if c == nil {
		return nil
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var snap difficultyCacheSnapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return err
	}
	if now.IsZero() {
		now = time.Now()
	}

	c.mu.Lock()
	if snap.Worker != nil {
		for k, v := range snap.Worker {
			k = strings.TrimSpace(k)
			if k == "" || v <= 0 {
				continue
			}
			c.byWorker[k] = difficultyEntry{diff: v, updatedAt: now}
		}
	}
	if snap.Session != nil {
		for k, v := range snap.Session {
			k = strings.TrimSpace(k)
			if k == "" || v <= 0 {
				continue
			}
			c.bySession[k] = difficultyEntry{diff: v, updatedAt: now}
		}
	}
	c.maybePruneLocked(now)
	c.mu.Unlock()
	return nil
}

func (c *difficultyCache) saveToJSON(path string) error {
	if c == nil {
		return nil
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	c.mu.Lock()
	snap := difficultyCacheSnapshot{
		Worker:  make(map[string]float64, len(c.byWorker)),
		Session: make(map[string]float64, len(c.bySession)),
	}
	for k, v := range c.byWorker {
		if strings.TrimSpace(k) == "" || v.diff <= 0 {
			continue
		}
		snap.Worker[k] = v.diff
	}
	for k, v := range c.bySession {
		if strings.TrimSpace(k) == "" || v.diff <= 0 {
			continue
		}
		snap.Session[k] = v.diff
	}
	c.mu.Unlock()

	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (c *difficultyCache) getWorker(hash string, maxAge time.Duration) (float64, bool) {
	if c == nil {
		return 0, false
	}
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return 0, false
	}
	if maxAge <= 0 {
		maxAge = time.Hour
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	ent, ok := c.byWorker[hash]
	if !ok || ent.diff <= 0 {
		return 0, false
	}
	if !ent.updatedAt.IsZero() && now.Sub(ent.updatedAt) > maxAge {
		return 0, false
	}
	return ent.diff, true
}

func (c *difficultyCache) getSession(sessionID string, maxAge time.Duration) (float64, bool) {
	if c == nil {
		return 0, false
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return 0, false
	}
	if maxAge <= 0 {
		maxAge = time.Hour
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	ent, ok := c.bySession[sessionID]
	if !ok || ent.diff <= 0 {
		return 0, false
	}
	if !ent.updatedAt.IsZero() && now.Sub(ent.updatedAt) > maxAge {
		return 0, false
	}
	return ent.diff, true
}

func (c *difficultyCache) setWorker(hash string, diff float64, now time.Time) {
	if c == nil {
		return
	}
	hash = strings.TrimSpace(hash)
	if hash == "" || diff <= 0 {
		return
	}
	if now.IsZero() {
		now = time.Now()
	}
	c.mu.Lock()
	c.byWorker[hash] = difficultyEntry{diff: diff, updatedAt: now}
	c.maybePruneLocked(now)
	c.mu.Unlock()
}

func (c *difficultyCache) setSession(sessionID string, diff float64, now time.Time) {
	if c == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || diff <= 0 {
		return
	}
	if now.IsZero() {
		now = time.Now()
	}
	c.mu.Lock()
	c.bySession[sessionID] = difficultyEntry{diff: diff, updatedAt: now}
	c.maybePruneLocked(now)
	c.mu.Unlock()
}

func (c *difficultyCache) maybePruneLocked(now time.Time) {
	if c.maxEntries <= 0 {
		return
	}
	total := len(c.byWorker) + len(c.bySession)
	if total <= c.maxEntries {
		return
	}
	// Drop ~10% oldest across both maps (simple, infrequent GC).
	targetDrop := total/10 + 1
	type item struct {
		key       string
		updatedAt time.Time
		isWorker  bool
	}
	items := make([]item, 0, total)
	for k, v := range c.byWorker {
		items = append(items, item{key: k, updatedAt: v.updatedAt, isWorker: true})
	}
	for k, v := range c.bySession {
		items = append(items, item{key: k, updatedAt: v.updatedAt, isWorker: false})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].updatedAt.Before(items[j].updatedAt)
	})
	if targetDrop > len(items) {
		targetDrop = len(items)
	}
	for i := 0; i < targetDrop; i++ {
		it := items[i]
		if it.isWorker {
			delete(c.byWorker, it.key)
		} else {
			delete(c.bySession, it.key)
		}
	}
}
