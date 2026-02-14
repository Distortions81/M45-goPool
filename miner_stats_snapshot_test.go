package main

import (
	"testing"
	"time"
)

func TestSnapshotShareInfo_WorkStartShowsLiveElapsedWhileAwaitingFirstShare(t *testing.T) {
	mc := &MinerConn{}
	mc.statsMu.Lock()
	mc.notifySentAt = time.Now().Add(-7 * time.Second)
	mc.notifyAwaitingFirstShare = true
	mc.statsMu.Unlock()

	snap := mc.snapshotShareInfo()
	if snap.NotifyToFirstShareMS < 6500 || snap.NotifyToFirstShareMS > 9000 {
		t.Fatalf("got %.2fms want live elapsed around 7000ms", snap.NotifyToFirstShareMS)
	}
}

func TestEnsureWindowLocked_DoesNotResetByVardiffCadence(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	mc := &MinerConn{
		vardiff: VarDiffConfig{
			AdjustmentWindow: 10 * time.Second,
		},
	}
	mc.stats.WindowStart = now.Add(-3 * time.Minute)
	mc.stats.WindowAccepted = 12
	mc.stats.WindowSubmissions = 15
	mc.stats.WindowDifficulty = 42
	mc.stats.LastShare = now.Add(-5 * time.Second)

	mc.statsMu.Lock()
	mc.ensureWindowLocked(now)
	got := mc.stats
	mc.statsMu.Unlock()

	if got.WindowStart != now.Add(-3*time.Minute) || got.WindowAccepted != 12 || got.WindowSubmissions != 15 || got.WindowDifficulty != 42 {
		t.Fatalf("status window should not reset on vardiff cadence: start=%v accepted=%d submissions=%d difficulty=%v",
			got.WindowStart, got.WindowAccepted, got.WindowSubmissions, got.WindowDifficulty)
	}
}

func TestEnsureWindowLocked_ResetsAfterLongIdle(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	mc := &MinerConn{}
	mc.stats.WindowStart = now.Add(-2 * time.Hour)
	mc.stats.WindowAccepted = 12
	mc.stats.WindowSubmissions = 15
	mc.stats.WindowDifficulty = 42
	mc.stats.LastShare = now.Add(-statusWindowIdleReset - time.Minute)

	mc.statsMu.Lock()
	mc.ensureWindowLocked(now)
	got := mc.stats
	mc.statsMu.Unlock()

	if !got.WindowStart.Equal(now) || got.WindowAccepted != 0 || got.WindowSubmissions != 0 || got.WindowDifficulty != 0 {
		t.Fatalf("status window should reset after long idle: start=%v accepted=%d submissions=%d difficulty=%v",
			got.WindowStart, got.WindowAccepted, got.WindowSubmissions, got.WindowDifficulty)
	}
}
