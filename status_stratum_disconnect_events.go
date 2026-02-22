package main

import (
	"strings"
	"time"
)

const stratumSafeguardDisconnectHistoryLimit = 8

type stratumSafeguardDisconnectEvent struct {
	At           time.Time
	Disconnected int
	Reason       string
	Detail       string
}

func (s *StatusServer) recordStratumSafeguardDisconnectEvent(at time.Time, disconnected int, reason, detail string) uint64 {
	if s == nil || disconnected <= 0 {
		return 0
	}
	if at.IsZero() {
		at = time.Now()
	}

	ev := stratumSafeguardDisconnectEvent{
		At:           at,
		Disconnected: disconnected,
		Reason:       strings.TrimSpace(reason),
		Detail:       strings.TrimSpace(detail),
	}

	s.stratumEventsMu.Lock()
	defer s.stratumEventsMu.Unlock()

	s.stratumSafeguardDisconnectCount++
	s.stratumSafeguardDisconnects = append(s.stratumSafeguardDisconnects, ev)
	if len(s.stratumSafeguardDisconnects) > stratumSafeguardDisconnectHistoryLimit {
		s.stratumSafeguardDisconnects = append([]stratumSafeguardDisconnectEvent(nil), s.stratumSafeguardDisconnects[len(s.stratumSafeguardDisconnects)-stratumSafeguardDisconnectHistoryLimit:]...)
	}
	return s.stratumSafeguardDisconnectCount
}

func (s *StatusServer) stratumSafeguardDisconnectSnapshot() (uint64, []PoolDisconnectEvent) {
	if s == nil {
		return 0, nil
	}

	s.stratumEventsMu.Lock()
	defer s.stratumEventsMu.Unlock()

	if len(s.stratumSafeguardDisconnects) == 0 {
		return s.stratumSafeguardDisconnectCount, nil
	}

	out := make([]PoolDisconnectEvent, 0, len(s.stratumSafeguardDisconnects))
	for _, ev := range s.stratumSafeguardDisconnects {
		out = append(out, PoolDisconnectEvent{
			At:           ev.At.UTC().Format(time.RFC3339),
			Disconnected: ev.Disconnected,
			Reason:       ev.Reason,
			Detail:       ev.Detail,
		})
	}
	return s.stratumSafeguardDisconnectCount, out
}
