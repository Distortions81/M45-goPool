package main

import "time"

type poolHashrateHistorySample struct {
	At          time.Time
	Hashrate    float64
	BlockHeight int64
}

type poolHashrateHistoryPoint struct {
	At          string  `json:"at"`
	Hashrate    float64 `json:"hashrate"`
	BlockHeight int64   `json:"block_height,omitempty"`
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

func (s *StatusServer) poolHashrateHistorySnapshot(now time.Time) []poolHashrateHistoryPoint {
	if s == nil {
		return nil
	}
	s.poolHashrateHistoryMu.Lock()
	defer s.poolHashrateHistoryMu.Unlock()

	s.trimPoolHashrateHistoryLocked(now)
	if len(s.poolHashrateHistory) == 0 {
		return nil
	}
	out := make([]poolHashrateHistoryPoint, 0, len(s.poolHashrateHistory))
	for _, sample := range s.poolHashrateHistory {
		if sample.Hashrate <= 0 || sample.At.IsZero() {
			continue
		}
		out = append(out, poolHashrateHistoryPoint{
			At:          sample.At.UTC().Format(time.RFC3339),
			Hashrate:    sample.Hashrate,
			BlockHeight: sample.BlockHeight,
		})
	}
	return out
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
