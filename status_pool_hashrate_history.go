package main

import "time"

type poolHashrateHistorySample struct {
	At          time.Time
	Hashrate    float64
	BlockHeight int64
}

func (s *StatusServer) appendPoolHashrateHistory(hashrate float64, blockHeight int64, now time.Time) {
	if s == nil || hashrate <= 0 {
		return
	}
	s.poolHashrateHistoryMu.Lock()
	defer s.poolHashrateHistoryMu.Unlock()

	s.poolHashrateHistory = append(s.poolHashrateHistory, poolHashrateHistorySample{
		At:          now,
		Hashrate:    hashrate,
		BlockHeight: blockHeight,
	})
	s.trimPoolHashrateHistoryLocked(now)
}

func (s *StatusServer) poolHashrateHistorySnapshot(now time.Time) []uint16 {
	if s == nil {
		return nil
	}
	s.poolHashrateHistoryMu.Lock()
	defer s.poolHashrateHistoryMu.Unlock()

	s.trimPoolHashrateHistoryLocked(now)
	intervalSeconds := int(poolHashrateTTL / time.Second)
	if intervalSeconds <= 0 {
		intervalSeconds = 1
	}
	n := int(poolHashrateHistoryWindow / poolHashrateTTL)
	if n <= 0 {
		n = 1
	}
	endSec := now.UTC().Unix()
	endSec -= endSec % int64(intervalSeconds)
	startSec := endSec - int64((n-1)*intervalSeconds)
	values := make([]float64, n)
	present := make([]bool, n)
	lastAtUnix := make([]int64, n)

	for _, sample := range s.poolHashrateHistory {
		if sample.Hashrate <= 0 || sample.At.IsZero() {
			continue
		}
		atSec := sample.At.UTC().Unix()
		if atSec < startSec || atSec > endSec {
			continue
		}
		idx := int((atSec - startSec) / int64(intervalSeconds))
		if idx < 0 || idx >= n {
			continue
		}
		if present[idx] && atSec < lastAtUnix[idx] {
			continue
		}
		present[idx] = true
		values[idx] = sample.Hashrate
		lastAtUnix[idx] = atSec
	}
	out := make([]uint16, 0, n)
	for i := 0; i < n; i++ {
		if !present[i] {
			continue
		}
		out = append(out, uint16(encodeHashrateSI8(values[i])))
	}
	return out
}

func (s *StatusServer) latestPoolHashrateHistorySince(now time.Time, maxAge time.Duration) (float64, int64, bool) {
	if s == nil || maxAge <= 0 {
		return 0, 0, false
	}
	s.poolHashrateHistoryMu.Lock()
	defer s.poolHashrateHistoryMu.Unlock()

	s.trimPoolHashrateHistoryLocked(now)
	if len(s.poolHashrateHistory) == 0 {
		return 0, 0, false
	}
	cutoff := now.Add(-maxAge)
	for i := len(s.poolHashrateHistory) - 1; i >= 0; i-- {
		sample := s.poolHashrateHistory[i]
		if sample.Hashrate <= 0 || sample.At.IsZero() {
			continue
		}
		if sample.At.Before(cutoff) {
			break
		}
		return sample.Hashrate, sample.BlockHeight, true
	}
	return 0, 0, false
}

func (s *StatusServer) trimPoolHashrateHistoryLocked(now time.Time) {
	cutoff := now.Add(-poolHashrateHistoryWindow)
	keepFrom := 0
	for keepFrom < len(s.poolHashrateHistory) {
		if s.poolHashrateHistory[keepFrom].At.After(cutoff) || s.poolHashrateHistory[keepFrom].At.Equal(cutoff) {
			break
		}
		keepFrom++
	}
	if keepFrom > 0 {
		s.poolHashrateHistory = append([]poolHashrateHistorySample(nil), s.poolHashrateHistory[keepFrom:]...)
	}
}
