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

func TestStratumHealthStatus_UsesOneGraceFromLastSuccessfulRefresh(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	lastSuccess := now.Add(-(stratumStaleJobGrace - time.Second))
	jm := &JobManager{}
	jm.mu.Lock()
	// Job creation is intentionally much older than the last successful GBT
	// heartbeat; job identity age is not outage age.
	jm.curJob = &Job{CreatedAt: now.Add(-time.Hour)}
	jm.mu.Unlock()
	jm.lastErrMu.Lock()
	jm.lastJobSuccess = lastSuccess
	jm.lastErr = errors.New("gbt unavailable")
	jm.lastErrAt = now.Add(-time.Minute)
	jm.lastErrMu.Unlock()

	h := stratumHealthStatus(jm, now)
	if !h.Healthy {
		t.Fatalf("feed became actionable before one full grace: %+v", h)
	}

	lastSuccess = now.Add(-stratumStaleJobGrace)
	jm.lastErrMu.Lock()
	jm.lastJobSuccess = lastSuccess
	jm.lastErrMu.Unlock()
	h = stratumHealthStatus(jm, now)
	if h.Healthy {
		t.Fatal("expected feed to be actionable after one stale-job grace")
	}
	if !h.UnhealthySince.Equal(lastSuccess) {
		t.Fatalf("unhealthy since = %v, want last success %v", h.UnhealthySince, lastSuccess)
	}
	// Admission and disconnect enforcement both seed their continuous-failure
	// timer through unhealthyStart. Backdating it to LastSuccess prevents them
	// from adding a second five-minute grace.
	if elapsed := now.Sub(h.unhealthyStart(now)); elapsed != stratumStaleJobGrace {
		t.Fatalf("enforcement elapsed = %v, want exactly one grace %v", elapsed, stratumStaleJobGrace)
	}
}

func TestRecordJobErrorPreservesContinuousOutageTimes(t *testing.T) {
	jm := &JobManager{}
	lastSuccess := time.Unix(1_700_000_000, 0)
	jm.lastErrMu.Lock()
	jm.lastJobSuccess = lastSuccess
	jm.lastErrMu.Unlock()

	jm.recordJobError(errors.New("first failure"))
	first := jm.FeedStatus()
	jm.recordJobError(errors.New("second failure"))
	second := jm.FeedStatus()

	if !second.LastSuccess.Equal(lastSuccess) {
		t.Fatalf("last success = %v, want %v", second.LastSuccess, lastSuccess)
	}
	if first.LastErrorAt.IsZero() || !second.LastErrorAt.Equal(first.LastErrorAt) {
		t.Fatalf("continuous outage start moved: first=%v second=%v", first.LastErrorAt, second.LastErrorAt)
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
