package main

import (
	"strconv"
	"time"
)

// humanShortDuration produces a short, human-friendly duration string like
// "just now", "5s", "3m", "2h", "4d".
func humanShortDuration(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	if d < time.Second {
		return "just now"
	}
	if d < time.Minute {
		secs := int(d.Seconds())
		return formatUnit(secs, "s")
	}
	if d < time.Hour {
		mins := int(d.Minutes())
		return formatUnit(mins, "m")
	}
	if d < 24*time.Hour {
		hours := int(d.Hours())
		return formatUnit(hours, "h")
	}
	days := int(d.Hours() / 24)
	return formatUnit(days, "d")
}

func formatUnit(v int, suffix string) string {
	if v <= 0 {
		v = 1
	}
	return strconv.Itoa(v) + suffix
}
