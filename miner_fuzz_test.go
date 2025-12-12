package main

import (
	"math"
	"testing"
	"time"
)

// FuzzNoteInvalidSubmitThresholds fuzzes noteInvalidSubmit to ensure the
// invalid-submission counters and ban thresholds behave sensibly for a wide
// range of configuration values and event timings.
func FuzzNoteInvalidSubmitThresholds(f *testing.F) {
	now := time.Unix(1700000000, 0)
	f.Add(int32(10), int32(60))  // modest threshold, 60s window
	f.Add(int32(0), int32(0))    // disabled
	f.Add(int32(1000), int32(5)) // large threshold, small window

	f.Fuzz(func(t *testing.T, thresholdRaw, windowSecRaw int32) {
		threshold := int(thresholdRaw)
		if threshold < 0 {
			threshold = -threshold
		}
		windowSec := int(windowSecRaw)
		if windowSec < 0 {
			windowSec = -windowSec
		}

		cfg := Config{
			BanInvalidSubmissionsAfter:    threshold,
			BanInvalidSubmissionsWindow:   time.Duration(windowSec) * time.Second,
			BanInvalidSubmissionsDuration: time.Minute,
		}
		mc := &MinerConn{
			cfg: cfg,
			vardiff: VarDiffConfig{
				MaxBurstShares: 60,
				BurstWindow:    60 * time.Second,
			},
		}

		// Send a burst of invalid submissions spaced evenly across the
		// configured window. Track whether we ever report a ban, and the
		// final invalid count.
		const maxEvents = 200
		events := maxEvents
		if threshold > 0 && threshold < maxEvents {
			events = threshold * 2
		}
		if events <= 0 {
			events = maxEvents
		}

		bannedCount := 0
		var lastInvalids int
		for i := 0; i < events; i++ {
			offsetFrac := 0.0
			if windowSec > 0 {
				offsetFrac = float64(i) / float64(events)
			}
			offset := time.Duration(float64(cfg.BanInvalidSubmissionsWindow) * offsetFrac)
			banned, invalids := mc.noteInvalidSubmit(now.Add(offset), rejectInvalidNonce)
			lastInvalids = invalids
			if banned {
				bannedCount++
				if mc.banUntil.IsZero() {
					t.Fatalf("banned worker has zero banUntil")
				}
				if mc.banUntil.Before(now) {
					t.Fatalf("banUntil %v is before now", mc.banUntil)
				}
				break
			}
		}

		// Compute the effective threshold used by noteInvalidSubmit for
		// this configuration so we can assert bans never occur "too early".
		effectiveThreshold := cfg.BanInvalidSubmissionsAfter
		if effectiveThreshold <= 0 {
			effectiveThreshold = mc.vardiff.MaxBurstShares
			if effectiveThreshold <= 0 {
				effectiveThreshold = 60
			}
		}

		if bannedCount > 0 && lastInvalids < effectiveThreshold {
			t.Fatalf("banned with invalidSubs=%d below effective threshold=%d", lastInvalids, effectiveThreshold)
		}
		// Invalid counter must never go negative or NaN.
		if lastInvalids < 0 || math.IsNaN(float64(lastInvalids)) {
			t.Fatalf("invalidSubs went negative or NaN: %d", lastInvalids)
		}
	})
}
