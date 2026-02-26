package main

import (
	"strings"
	"time"
)

// savedWorkerPeriodSample stores one minute-bucket sample for a saved worker.
// HashrateQ is the summed live hashrate across active connections sharing the
// same worker hash (SI-quantized uint16). BestDifficultyQ stores the current
// minute-best share difficulty (SI-quantized uint16).
type savedWorkerPeriodSample struct {
	At              time.Time
	HashrateQ       uint16
	BestDifficultyQ uint16
}

func (s *StatusServer) recordSavedOnlineWorkerPeriods(allWorkers []WorkerView, now time.Time) {
	if s == nil || s.workerLists == nil || len(allWorkers) == 0 {
		return
	}
	bucket := now.UTC().Truncate(savedWorkerPeriodBucket)
	if bucket.IsZero() {
		return
	}

	s.savedWorkerPeriodsMu.Lock()
	if !s.savedWorkerPeriodsLastBucket.IsZero() && !bucket.After(s.savedWorkerPeriodsLastBucket) {
		s.savedWorkerPeriodsMu.Unlock()
		return
	}
	s.savedWorkerPeriodsMu.Unlock()

	saved, err := s.workerLists.ListAllSavedWorkers()
	if err != nil {
		logger.Warn("list saved workers for minute sampler", "error", err)
		return
	}
	if len(saved) == 0 {
		s.savedWorkerPeriodsMu.Lock()
		if bucket.After(s.savedWorkerPeriodsLastBucket) {
			s.savedWorkerPeriodsLastBucket = bucket
			s.pruneSavedWorkerPeriodsLocked(bucket)
		}
		s.savedWorkerPeriodsMu.Unlock()
		return
	}

	savedHashes := make(map[string]struct{}, len(saved))
	for _, rec := range saved {
		hash := strings.ToLower(strings.TrimSpace(rec.Hash))
		if hash == "" {
			continue
		}
		savedHashes[hash] = struct{}{}
	}
	if len(savedHashes) == 0 {
		s.savedWorkerPeriodsMu.Lock()
		if bucket.After(s.savedWorkerPeriodsLastBucket) {
			s.savedWorkerPeriodsLastBucket = bucket
			s.pruneSavedWorkerPeriodsLocked(bucket)
		}
		s.savedWorkerPeriodsMu.Unlock()
		return
	}

	hashrateByHash := make(map[string]float64, len(savedHashes))
	onlineSaved := make(map[string]struct{}, len(savedHashes))
	for _, w := range allWorkers {
		hash := strings.ToLower(strings.TrimSpace(w.WorkerSHA256))
		if hash == "" {
			continue
		}
		if _, ok := savedHashes[hash]; !ok {
			continue
		}
		onlineSaved[hash] = struct{}{}
		h := workerHashrateEstimate(w, now)
		if h <= 0 {
			h = w.RollingHashrate
		}
		if h <= 0 {
			continue
		}
		hashrateByHash[hash] += h
	}

	s.savedWorkerPeriodsMu.Lock()
	defer s.savedWorkerPeriodsMu.Unlock()
	if !bucket.After(s.savedWorkerPeriodsLastBucket) {
		return
	}
	s.savedWorkerPeriodsLastBucket = bucket
	if len(onlineSaved) == 0 {
		s.pruneSavedWorkerPeriodsLocked(bucket)
		return
	}
	if s.savedWorkerPeriods == nil {
		s.savedWorkerPeriods = make(map[string][]savedWorkerPeriodSample, len(hashrateByHash))
	}
	for hash := range onlineSaved {
		hashrate := hashrateByHash[hash]
		best := 0.0
		if s.workerLists != nil {
			best = s.workerLists.SavedWorkerMinuteBestDifficulty(hash, bucket)
		}
		hashrateQ := encodeHashrateSI16(hashrate)
		bestQ := encodeBestShareSI16(best)
		samples := s.savedWorkerPeriods[hash]
		if n := len(samples); n > 0 && samples[n-1].At.Equal(bucket) {
			if hashrateQ > 0 {
				samples[n-1].HashrateQ = hashrateQ
			}
			if bestQ > samples[n-1].BestDifficultyQ {
				samples[n-1].BestDifficultyQ = bestQ
			}
			s.savedWorkerPeriods[hash] = samples
			continue
		}
		s.savedWorkerPeriods[hash] = append(samples, savedWorkerPeriodSample{
			At:              bucket,
			HashrateQ:       hashrateQ,
			BestDifficultyQ: bestQ,
		})
	}
	s.pruneSavedWorkerPeriodsLocked(bucket)
}

func (s *StatusServer) savedWorkerPeriodHistory(hash string, now time.Time) []savedWorkerPeriodSample {
	if s == nil {
		return nil
	}
	hash = strings.ToLower(strings.TrimSpace(hash))
	if hash == "" {
		return nil
	}
	s.savedWorkerPeriodsMu.Lock()
	defer s.savedWorkerPeriodsMu.Unlock()
	s.pruneSavedWorkerPeriodsLocked(now.UTC())
	samples := s.savedWorkerPeriods[hash]
	if len(samples) == 0 {
		return nil
	}
	out := make([]savedWorkerPeriodSample, len(samples))
	copy(out, samples)
	return out
}

func (s *StatusServer) pruneSavedWorkerPeriodsLocked(now time.Time) {
	if s == nil || len(s.savedWorkerPeriods) == 0 {
		return
	}
	cutoff := now.Add(-savedWorkerPeriodHistoryWindow)
	for hash, samples := range s.savedWorkerPeriods {
		keepFrom := 0
		for keepFrom < len(samples) {
			at := samples[keepFrom].At
			if at.After(cutoff) || at.Equal(cutoff) {
				break
			}
			keepFrom++
		}
		if keepFrom >= len(samples) {
			delete(s.savedWorkerPeriods, hash)
			continue
		}
		if keepFrom > 0 {
			s.savedWorkerPeriods[hash] = append([]savedWorkerPeriodSample(nil), samples[keepFrom:]...)
		}
	}
	if savedWorkerPeriodMaxWorkers > 0 && len(s.savedWorkerPeriods) > savedWorkerPeriodMaxWorkers {
		for hash := range s.savedWorkerPeriods {
			if len(s.savedWorkerPeriods) <= savedWorkerPeriodMaxWorkers {
				break
			}
			delete(s.savedWorkerPeriods, hash)
		}
	}
}
