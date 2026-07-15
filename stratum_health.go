package main

import (
	"strconv"
	"strings"
	"time"
)

type stratumHealth struct {
	Healthy        bool
	Reason         string
	Detail         string
	UnhealthySince time.Time
}

func (h stratumHealth) unhealthyStart(now time.Time) time.Time {
	if now.IsZero() {
		now = time.Now()
	}
	if h.UnhealthySince.IsZero() || h.UnhealthySince.After(now) {
		return now
	}
	return h.UnhealthySince
}

func stratumNodeSyncSnapshotFresh(now, fetchedAt time.Time) bool {
	if fetchedAt.IsZero() {
		return false
	}
	if now.IsZero() {
		now = time.Now()
	}
	if fetchedAt.After(now) {
		return true
	}
	// Treat getblockchaininfo snapshot as best-effort and require it to be recent
	// before using it to gate Stratum. Otherwise a stale "IBD/indexing" snapshot
	// can poison one pool process even while another process remains healthy.
	const maxNodeSyncSnapshotAge = (2 * stratumHeartbeatInterval) + (5 * time.Second)
	return now.Sub(fetchedAt) <= maxNodeSyncSnapshotAge
}

func stratumHealthStatus(jobMgr *JobManager, now time.Time) stratumHealth {
	if now.IsZero() {
		now = time.Now()
	}
	if jobMgr == nil {
		return stratumHealth{Healthy: false, Reason: "no job manager"}
	}

	job := jobMgr.CurrentJob()
	fs := jobMgr.FeedStatus()

	if job == nil || job.CreatedAt.IsZero() {
		if fs.LastError != nil {
			return stratumHealth{Healthy: false, Reason: "node/job feed error", Detail: strings.TrimSpace(fs.LastError.Error())}
		}
		return stratumHealth{Healthy: false, Reason: "no job template available"}
	}

	if fs.LastError != nil {
		// LastSuccess is advanced by every successful GBT response, including
		// unchanged heartbeat templates. It is therefore the last time the work
		// was proven current, unlike Job.CreatedAt, which can legitimately be old.
		unhealthySince := fs.LastSuccess
		if unhealthySince.IsZero() {
			unhealthySince = job.CreatedAt
		}
		if unhealthySince.After(now) {
			unhealthySince = now
		}
		if now.Sub(unhealthySince) < stratumStaleJobGrace {
			return stratumHealth{Healthy: true}
		}
		return stratumHealth{
			Healthy:        false,
			Reason:         "node/job feed error",
			Detail:         strings.TrimSpace(fs.LastError.Error()),
			UnhealthySince: unhealthySince,
		}
	}

	ibd, blocks, headers, fetchedAt := jobMgr.nodeSyncSnapshot()
	if stratumNodeSyncSnapshotFresh(now, fetchedAt) && (ibd || (headers > 0 && blocks >= 0 && blocks < headers)) {
		detail := "node syncing: ibd=" + strconv.FormatBool(ibd) + " blocks=" + strconv.FormatInt(blocks, 10) + " headers=" + strconv.FormatInt(headers, 10)
		return stratumHealth{Healthy: false, Reason: "node syncing/indexing", Detail: detail}
	}

	return stratumHealth{Healthy: true}
}
