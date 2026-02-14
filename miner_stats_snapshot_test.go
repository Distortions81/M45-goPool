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
