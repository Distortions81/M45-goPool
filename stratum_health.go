package main

import (
	"strconv"
	"strings"
	"time"
)

type stratumHealth struct {
	Healthy bool
	Reason  string
	Detail  string
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
		if now.Sub(job.CreatedAt) < stratumStaleJobGrace {
			return stratumHealth{Healthy: true}
		}
		return stratumHealth{Healthy: false, Reason: "node/job feed error", Detail: strings.TrimSpace(fs.LastError.Error())}
	}

	ibd, blocks, headers, fetchedAt := jobMgr.nodeSyncSnapshot()
	if stratumNodeSyncSnapshotFresh(now, fetchedAt) && (ibd || (headers > 0 && blocks >= 0 && blocks < headers)) {
		detail := "node syncing: ibd=" + strconv.FormatBool(ibd) + " blocks=" + strconv.FormatInt(blocks, 10) + " headers=" + strconv.FormatInt(headers, 10)
		return stratumHealth{Healthy: false, Reason: "node syncing/indexing", Detail: detail}
	}

	return stratumHealth{Healthy: true}
}
