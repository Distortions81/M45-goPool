package main

import (
	"sort"
	"strings"
	"sync"
)

const maxSavedWorkersPerUser = 64

type workerListStore struct {
	mu    sync.RWMutex
	lists map[string]map[string]struct{}
}

func newWorkerListStore() *workerListStore {
	return &workerListStore{
		lists: make(map[string]map[string]struct{}),
	}
}

func (s *workerListStore) Add(userID, worker string) {
	if userID == "" {
		return
	}
	worker = strings.TrimSpace(worker)
	if worker == "" || len(worker) > workerLookupMaxBytes {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	set, ok := s.lists[userID]
	if !ok {
		set = make(map[string]struct{})
		s.lists[userID] = set
	}
	if len(set) >= maxSavedWorkersPerUser {
		return
	}
	set[worker] = struct{}{}
}

func (s *workerListStore) List(userID string) []string {
	if userID == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	set, ok := s.lists[userID]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(set))
	for worker := range set {
		out = append(out, worker)
	}
	sort.Strings(out)
	return out
}
