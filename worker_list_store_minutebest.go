package main

import (
	"strings"
	"time"
)

const savedWorkerPeriodBucketMinutes = int(savedWorkerPeriodBucket / time.Minute)

func savedWorkerMinuteBucketAt(now time.Time) time.Time {
	if now.IsZero() {
		return time.Time{}
	}
	return now.UTC().Truncate(savedWorkerPeriodBucket)
}

func savedWorkerUnixMinute(now time.Time) uint32 {
	b := savedWorkerMinuteBucketAt(now)
	if b.IsZero() {
		return 0
	}
	sec := b.Unix()
	if sec <= 0 {
		return 0
	}
	return uint32(sec / 60)
}

func savedWorkerRingIndex(minute uint32) int {
	if savedWorkerPeriodBucketMinutes <= 1 {
		return int(minute % uint32(savedWorkerPeriodSlots))
	}
	return int((minute / uint32(savedWorkerPeriodBucketMinutes)) % uint32(savedWorkerPeriodSlots))
}

func (s *workerListStore) UpdateSavedWorkerMinuteBestDifficulty(hash string, diff float64, now time.Time) {
	if s == nil || s.db == nil {
		return
	}
	hash = strings.ToLower(strings.TrimSpace(hash))
	if hash == "" || diff <= 0 {
		return
	}
	minute := savedWorkerUnixMinute(now)
	if minute == 0 {
		return
	}
	code := encodeBestShareSI16(diff)
	if code == 0 {
		return
	}

	s.minuteBestMu.Lock()
	defer s.minuteBestMu.Unlock()
	if s.minuteBestByID == nil {
		s.minuteBestByID = make(map[string]*savedWorkerMinuteBestRing)
	}
	ring := s.minuteBestByID[hash]
	if ring == nil {
		ring = &savedWorkerMinuteBestRing{}
		s.minuteBestByID[hash] = ring
	}
	idx := savedWorkerRingIndex(minute)
	if ring.minutes[idx] != minute {
		ring.minutes[idx] = minute
		ring.bestQ[idx] = 0
	}
	if code > ring.bestQ[idx] {
		ring.bestQ[idx] = code
	}
	ring.lastMinute = minute
	s.pruneSavedWorkerMinuteBestLocked(minute)
}

func (s *workerListStore) SavedWorkerMinuteBestDifficulty(hash string, bucket time.Time) float64 {
	if s == nil || s.db == nil {
		return 0
	}
	hash = strings.ToLower(strings.TrimSpace(hash))
	if hash == "" {
		return 0
	}
	minute := savedWorkerUnixMinute(bucket)
	if minute == 0 {
		return 0
	}
	s.minuteBestMu.Lock()
	defer s.minuteBestMu.Unlock()
	s.pruneSavedWorkerMinuteBestLocked(minute)
	ring := s.minuteBestByID[hash]
	if ring == nil {
		return 0
	}
	idx := savedWorkerRingIndex(minute)
	if ring.minutes[idx] != minute {
		return 0
	}
	return decodeBestShareSI16(ring.bestQ[idx])
}

// ConsumeSavedWorkerMinuteBestDifficulty returns the finalized best share for a
// bucket and clears that bucket so subsequent reads don't reuse stale values.
func (s *workerListStore) ConsumeSavedWorkerMinuteBestDifficulty(hash string, bucket time.Time) float64 {
	if s == nil || s.db == nil {
		return 0
	}
	hash = strings.ToLower(strings.TrimSpace(hash))
	if hash == "" {
		return 0
	}
	minute := savedWorkerUnixMinute(bucket)
	if minute == 0 {
		return 0
	}
	s.minuteBestMu.Lock()
	defer s.minuteBestMu.Unlock()
	// Prune using "now", not the sampled bucket minute. The sampler consumes the
	// previous completed bucket; if current shares have already advanced
	// ring.lastMinute into the newer bucket, pruning with the older sample minute
	// can underflow the uint32 age math and incorrectly delete active rings.
	pruneMinute := savedWorkerUnixMinute(time.Now())
	if pruneMinute == 0 || pruneMinute < minute {
		pruneMinute = minute
	}
	s.pruneSavedWorkerMinuteBestLocked(pruneMinute)
	ring := s.minuteBestByID[hash]
	if ring == nil {
		return 0
	}
	idx := savedWorkerRingIndex(minute)
	if ring.minutes[idx] != minute {
		return 0
	}
	best := decodeBestShareSI16(ring.bestQ[idx])
	ring.bestQ[idx] = 0
	return best
}

func (s *workerListStore) pruneSavedWorkerMinuteBestLocked(nowMinute uint32) {
	if s == nil || len(s.minuteBestByID) == 0 || nowMinute == 0 {
		return
	}
	maxAge := uint32(savedWorkerPeriodSlots * savedWorkerPeriodBucketMinutes)
	for hash, ring := range s.minuteBestByID {
		if ring == nil {
			delete(s.minuteBestByID, hash)
			continue
		}
		if ring.lastMinute == 0 {
			delete(s.minuteBestByID, hash)
			continue
		}
		// uint32 subtraction intentionally handles wraparound.
		if nowMinute-ring.lastMinute > maxAge {
			delete(s.minuteBestByID, hash)
		}
	}
}
