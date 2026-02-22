package main

import (
	"errors"
	"testing"
	"time"
)

func TestStratumHealthStatus_AllowsRecentCurrentJobDuringFeedErrors(t *testing.T) {
	now := time.Now()
	jm := &JobManager{}
	jm.mu.Lock()
	jm.curJob = &Job{CreatedAt: now.Add(-(stratumStaleJobGrace - time.Minute))}
	jm.mu.Unlock()
	jm.recordJobError(errors.New("gbt timeout"))

	h := stratumHealthStatus(jm, now)
	if !h.Healthy {
		t.Fatalf("expected healthy during stale job grace, got unhealthy: %+v", h)
	}
}

func TestStratumHealthStatus_MarksUnhealthyAfterStaleJobGrace(t *testing.T) {
	now := time.Now()
	jm := &JobManager{}
	jm.mu.Lock()
	jm.curJob = &Job{CreatedAt: now.Add(-(stratumStaleJobGrace + time.Minute))}
	jm.mu.Unlock()
	jm.recordJobError(errors.New("gbt timeout"))

	h := stratumHealthStatus(jm, now)
	if h.Healthy {
		t.Fatalf("expected unhealthy after stale job grace, got healthy")
	}
	if h.Reason == "" {
		t.Fatalf("expected reason on unhealthy status")
	}
}

func TestStratumHealthStatus_IgnoresStaleNodeSyncSnapshot(t *testing.T) {
	now := time.Now()
	jm := &JobManager{}
	jm.mu.Lock()
	jm.curJob = &Job{CreatedAt: now.Add(-time.Minute)}
	jm.mu.Unlock()

	jm.nodeSyncMu.Lock()
	jm.nodeIBD = true
	jm.nodeBlocks = 100
	jm.nodeHeaders = 200
	jm.nodeSyncFetched = now.Add(-((3 * stratumHeartbeatInterval) + time.Second))
	jm.nodeSyncMu.Unlock()

	h := stratumHealthStatus(jm, now)
	if !h.Healthy {
		t.Fatalf("expected healthy with stale node sync snapshot, got unhealthy: %+v", h)
	}
}
