package main

import (
	"database/sql"
	"strings"
	"time"
)

func (s *workerListStore) BestDifficultyForHash(hash string) (float64, bool, error) {
	if s == nil || s.db == nil {
		return 0, false, nil
	}
	hash = strings.ToLower(strings.TrimSpace(hash))
	if hash == "" {
		return 0, false, nil
	}

	pending := 0.0
	s.bestDiffMu.Lock()
	if s.bestDiffPending != nil {
		pending = s.bestDiffPending[hash]
	}
	s.bestDiffMu.Unlock()

	var (
		count int
		best  sql.NullFloat64
	)
	if err := s.db.QueryRow("SELECT COUNT(*), MAX(best_difficulty) FROM saved_workers WHERE worker_hash = ?", hash).Scan(&count, &best); err != nil {
		return 0, false, err
	}

	dbBest := 0.0
	if best.Valid && best.Float64 > 0 {
		dbBest = best.Float64
	}

	out := dbBest
	if pending > out {
		out = pending
	}
	return out, count > 0, nil
}

func (s *workerListStore) UpdateSavedWorkerBestDifficulty(hash string, diff float64) (bool, error) {
	if s == nil || s.db == nil {
		return false, nil
	}
	hash = strings.ToLower(strings.TrimSpace(hash))
	if hash == "" || diff <= 0 {
		return false, nil
	}

	updated := false
	s.bestDiffMu.Lock()
	if s.bestDiffPending == nil {
		s.bestDiffPending = make(map[string]float64)
	}
	if diff > s.bestDiffPending[hash] {
		s.bestDiffPending[hash] = diff
		updated = true
	}
	s.bestDiffMu.Unlock()

	if !updated {
		return false, nil
	}

	if ch := s.bestDiffCh; ch != nil {
		select {
		case ch <- bestDiffUpdate{hash: hash, diff: diff}:
		default:
		}
	}
	return true, nil
}

func (s *workerListStore) startBestDiffWorker(flushInterval time.Duration) {
	if s == nil || s.db == nil {
		return
	}
	if flushInterval <= 0 {
		flushInterval = 10 * time.Second
	}
	if s.bestDiffStop != nil {
		return
	}
	s.bestDiffCh = make(chan bestDiffUpdate, 4096)
	s.bestDiffStop = make(chan struct{})
	s.bestDiffWg.Add(1)
	go func() {
		defer s.bestDiffWg.Done()
		ticker := time.NewTicker(flushInterval)
		defer ticker.Stop()
		for {
			select {
			case <-s.bestDiffStop:
				s.flushBestDiffPending()
				return
			case upd := <-s.bestDiffCh:
				if upd.hash == "" || upd.diff <= 0 {
					continue
				}
				s.bestDiffMu.Lock()
				if s.bestDiffPending == nil {
					s.bestDiffPending = make(map[string]float64)
				}
				if upd.diff > s.bestDiffPending[upd.hash] {
					s.bestDiffPending[upd.hash] = upd.diff
				}
				s.bestDiffMu.Unlock()
			case <-ticker.C:
				s.flushBestDiffPending()
			}
		}
	}()
}

func (s *workerListStore) flushBestDiffPending() {
	if s == nil || s.db == nil {
		return
	}
	s.bestDiffMu.Lock()
	if len(s.bestDiffPending) == 0 {
		s.bestDiffMu.Unlock()
		return
	}
	batch := s.bestDiffPending
	s.bestDiffPending = make(map[string]float64, len(batch))
	s.bestDiffMu.Unlock()

	mergeBack := func() {
		s.bestDiffMu.Lock()
		if s.bestDiffPending == nil {
			s.bestDiffPending = make(map[string]float64, len(batch))
		}
		for hash, diff := range batch {
			if hash == "" || diff <= 0 {
				continue
			}
			if diff > s.bestDiffPending[hash] {
				s.bestDiffPending[hash] = diff
			}
		}
		s.bestDiffMu.Unlock()
	}

	tx, err := s.db.Begin()
	if err != nil {
		logger.Warn("saved worker best diff flush begin failed", "error", err)
		mergeBack()
		return
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare(`
		UPDATE saved_workers
		SET best_difficulty = ?
		WHERE worker_hash = ? AND best_difficulty < ?
	`)
	if err != nil {
		logger.Warn("saved worker best diff flush prepare failed", "error", err)
		mergeBack()
		return
	}
	defer stmt.Close()

	for hash, diff := range batch {
		if hash == "" || diff <= 0 {
			continue
		}
		if _, err := stmt.Exec(diff, hash, diff); err != nil {
			logger.Warn("saved worker best diff flush update failed", "error", err)
			mergeBack()
			return
		}
	}
	if err := tx.Commit(); err != nil {
		logger.Warn("saved worker best diff flush commit failed", "error", err)
		mergeBack()
		return
	}
}
