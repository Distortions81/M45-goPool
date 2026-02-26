package main

import (
	"strings"
	"time"
)

// savedWorkerPeriodSample is the external/history snapshot shape used by the
// saved-worker history API. Values are stored internally in a ring buffer as
// SI-quantized uint16s and copied out here.
type savedWorkerPeriodSample struct {
	At              time.Time
	HashrateQ       uint16
	BestDifficultyQ uint16
}

type savedWorkerPeriodRing struct {
	minutes         [savedWorkerPeriodSlots]uint32
	hashrateQ       [savedWorkerPeriodSlots]uint16
	bestDifficultyQ [savedWorkerPeriodSlots]uint16
	lastMinute      uint32
}

func (s *StatusServer) recordSavedOnlineWorkerPeriods(allWorkers []WorkerView, now time.Time) {
	if s == nil || s.workerLists == nil || len(allWorkers) == 0 {
		return
	}
	bucket := now.UTC().Truncate(savedWorkerPeriodBucket)
	if bucket.IsZero() {
		return
	}
	currentMinute := savedWorkerUnixMinute(bucket)
	if currentMinute == 0 {
		return
	}
	if savedWorkerPeriodBucketMinutes <= 0 || currentMinute <= uint32(savedWorkerPeriodBucketMinutes) {
		return
	}
	sampleMinute := currentMinute - uint32(savedWorkerPeriodBucketMinutes)
	sampleBucket := bucket.Add(-savedWorkerPeriodBucket)

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
			s.pruneSavedWorkerPeriodsLocked(currentMinute)
		}
		s.savedWorkerPeriodsMu.Unlock()
		return
	}

	savedHashes := make(map[string]struct{}, len(saved))
	savedNames := make(map[string]struct{}, len(saved))
	for _, rec := range saved {
		hash := strings.ToLower(strings.TrimSpace(rec.Hash))
		if hash != "" {
			savedHashes[hash] = struct{}{}
			continue
		}
		name := strings.TrimSpace(rec.Name)
		if name != "" {
			savedNames[name] = struct{}{}
		}
	}
	if len(savedHashes) == 0 && len(savedNames) == 0 {
		s.savedWorkerPeriodsMu.Lock()
		if bucket.After(s.savedWorkerPeriodsLastBucket) {
			s.savedWorkerPeriodsLastBucket = bucket
			s.pruneSavedWorkerPeriodsLocked(currentMinute)
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
			// Backfill history for legacy saved-worker rows that were stored
			// without a hash by matching active worker names.
			if _, nameMatch := savedNames[strings.TrimSpace(w.Name)]; !nameMatch {
				continue
			}
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
		s.pruneSavedWorkerPeriodsLocked(currentMinute)
		return
	}
	if s.savedWorkerPeriods == nil {
		s.savedWorkerPeriods = make(map[string]*savedWorkerPeriodRing, len(hashrateByHash))
	}
	for hash := range onlineSaved {
		hashrateQ := encodeHashrateSI16(hashrateByHash[hash])
		bestQ := uint16(0)
		if s.workerLists != nil {
			bestQ = encodeBestShareSI16(s.workerLists.ConsumeSavedWorkerMinuteBestDifficulty(hash, sampleBucket))
		}
		ring := s.savedWorkerPeriods[hash]
		if ring == nil {
			ring = &savedWorkerPeriodRing{}
			s.savedWorkerPeriods[hash] = ring
		}
		idx := savedWorkerRingIndex(sampleMinute)
		if ring.minutes[idx] != sampleMinute {
			ring.minutes[idx] = sampleMinute
			ring.hashrateQ[idx] = 0
			ring.bestDifficultyQ[idx] = 0
		}
		if hashrateQ > 0 {
			ring.hashrateQ[idx] = hashrateQ
		}
		if bestQ > ring.bestDifficultyQ[idx] {
			ring.bestDifficultyQ[idx] = bestQ
		}
		ring.lastMinute = sampleMinute
	}
	s.pruneSavedWorkerPeriodsLocked(currentMinute)
}

func (s *StatusServer) savedWorkerPeriodHistory(hash string, now time.Time) []savedWorkerPeriodSample {
	if s == nil {
		return nil
	}
	hash = strings.ToLower(strings.TrimSpace(hash))
	if hash == "" {
		return nil
	}
	nowMinute := savedWorkerUnixMinute(now.UTC().Truncate(savedWorkerPeriodBucket))
	if nowMinute == 0 {
		return nil
	}

	s.savedWorkerPeriodsMu.Lock()
	defer s.savedWorkerPeriodsMu.Unlock()
	s.pruneSavedWorkerPeriodsLocked(nowMinute)
	ring := s.savedWorkerPeriods[hash]
	if ring == nil {
		return nil
	}

	out := make([]savedWorkerPeriodSample, 0, savedWorkerPeriodSlots)
	var startMinute uint32
	spanMinutes := uint32((savedWorkerPeriodSlots - 1) * savedWorkerPeriodBucketMinutes)
	if nowMinute >= spanMinutes {
		startMinute = nowMinute - spanMinutes
	}
	step := uint32(savedWorkerPeriodBucketMinutes)
	if step == 0 {
		step = 1
	}
	for m := startMinute; m <= nowMinute; m += step {
		idx := savedWorkerRingIndex(m)
		if ring.minutes[idx] != m {
			if m == nowMinute {
				break
			}
			continue
		}
		out = append(out, savedWorkerPeriodSample{
			At:              time.Unix(int64(m)*60, 0).UTC(),
			HashrateQ:       ring.hashrateQ[idx],
			BestDifficultyQ: ring.bestDifficultyQ[idx],
		})
		if m == nowMinute {
			break
		}
	}
	return out
}

func (s *StatusServer) pruneSavedWorkerPeriodsLocked(nowMinute uint32) {
	if s == nil || len(s.savedWorkerPeriods) == 0 || nowMinute == 0 {
		return
	}
	maxAge := uint32(savedWorkerPeriodSlots * savedWorkerPeriodBucketMinutes)
	for hash, ring := range s.savedWorkerPeriods {
		if ring == nil {
			delete(s.savedWorkerPeriods, hash)
			continue
		}
		if ring.lastMinute == 0 || nowMinute-ring.lastMinute > maxAge {
			delete(s.savedWorkerPeriods, hash)
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
