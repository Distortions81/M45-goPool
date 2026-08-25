package main

import (
	"context"
	"testing"
	"time"

	"github.com/remeh/sizedwaitgroup"
)

func TestNotificationWorkerRecordsTemplateToNotifyLatency(t *testing.T) {
	metrics := NewPoolMetrics()
	jm := NewJobManager(nil, Config{}, metrics, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	jm.notifyWg = sizedwaitgroup.New(1)
	jm.notifyWg.Add()
	go jm.notificationWorker(ctx, 0)

	jm.broadcastJob(&Job{templateReceivedAt: time.Now().Add(-10 * time.Millisecond)})
	deadline := time.Now().Add(time.Second)
	for {
		timing := metrics.SnapshotGBTTiming()
		if timing.ApplyNotifyCount == 1 {
			if timing.ApplyNotifyLast < (10 * time.Millisecond).Seconds() {
				t.Fatalf("template-to-notify = %.6fs, want at least 0.010s", timing.ApplyNotifyLast)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("notification timing was not recorded")
		}
		time.Sleep(time.Millisecond)
	}

	cancel()
	jm.notifyWg.Wait()
}
