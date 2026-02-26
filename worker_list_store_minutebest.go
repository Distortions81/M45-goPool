package main

import (
	"fmt"
	"strings"
	"time"
)

func savedWorkerMinuteBucketAt(now time.Time) time.Time {
	if now.IsZero() {
		return time.Time{}
	}
	return now.UTC().Truncate(savedWorkerPeriodBucket)
}

func savedWorkerMinuteBestKey(hash string, bucket time.Time) string {
	hash = strings.ToLower(strings.TrimSpace(hash))
	if hash == "" || bucket.IsZero() {
		return ""
	}
	return fmt.Sprintf("%s:%d", hash, bucket.Unix())
}

func (s *workerListStore) UpdateSavedWorkerMinuteBestDifficulty(hash string, diff float64, now time.Time) {
	if s == nil || s.db == nil {
		return
	}
	hash = strings.ToLower(strings.TrimSpace(hash))
	if hash == "" || diff <= 0 {
		return
	}
	bucket := savedWorkerMinuteBucketAt(now)
	if bucket.IsZero() {
		return
	}
	key := savedWorkerMinuteBestKey(hash, bucket)
	if key == "" {
		return
	}

	s.minuteBestMu.Lock()
	defer s.minuteBestMu.Unlock()
	code := encodeBestShareSI16(diff)
	if code == 0 {
		return
	}
	if s.minuteBestByID == nil {
		s.minuteBestByID = make(map[string]uint16)
	}
	if code > s.minuteBestByID[key] {
		s.minuteBestByID[key] = code
	}
	s.pruneSavedWorkerMinuteBestLocked(bucket)
}

func (s *workerListStore) SavedWorkerMinuteBestDifficulty(hash string, bucket time.Time) float64 {
	if s == nil || s.db == nil {
		return 0
	}
	key := savedWorkerMinuteBestKey(hash, bucket.UTC().Truncate(savedWorkerPeriodBucket))
	if key == "" {
		return 0
	}
	s.minuteBestMu.Lock()
	defer s.minuteBestMu.Unlock()
	s.pruneSavedWorkerMinuteBestLocked(bucket)
	return decodeBestShareSI16(s.minuteBestByID[key])
}

func (s *workerListStore) pruneSavedWorkerMinuteBestLocked(now time.Time) {
	if s == nil || len(s.minuteBestByID) == 0 {
		return
	}
	cutoff := now.Add(-savedWorkerPeriodHistoryWindow).Unix()
	for key := range s.minuteBestByID {
		i := strings.LastIndexByte(key, ':')
		if i < 0 || i+1 >= len(key) {
			delete(s.minuteBestByID, key)
			continue
		}
		var ts int64
		for _, ch := range key[i+1:] {
			if ch < '0' || ch > '9' {
				ts = 0
				break
			}
			ts = ts*10 + int64(ch-'0')
		}
		if ts < cutoff {
			delete(s.minuteBestByID, key)
		}
	}
}
