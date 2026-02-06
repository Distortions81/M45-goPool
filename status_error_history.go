package main

import "time"

func filterRecentPoolErrorEvents(raw []ErrorEvent, now time.Time, maxAge time.Duration) []PoolErrorEvent {
	if len(raw) == 0 {
		return nil
	}
	filtered := make([]PoolErrorEvent, 0, len(raw))
	for _, ev := range raw {
		if ev.At.IsZero() {
			continue
		}
		if maxAge > 0 && now.Sub(ev.At) > maxAge {
			continue
		}
		filtered = append(filtered, PoolErrorEvent{
			At:      ev.At.UTC().Format(time.RFC3339),
			Type:    ev.Type,
			Message: ev.Message,
		})
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}
