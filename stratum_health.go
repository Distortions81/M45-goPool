package main

import (
	"strings"
	"time"
)

type stratumHealth struct {
	Healthy bool
	Reason  string
	Detail  string
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
		return stratumHealth{Healthy: false, Reason: "node/job feed error", Detail: strings.TrimSpace(fs.LastError.Error())}
	}

	if fs.LastSuccess.IsZero() {
		return stratumHealth{Healthy: false, Reason: "no successful job refresh yet"}
	}

	successAge := now.Sub(fs.LastSuccess)
	if successAge > stratumMaxFeedLag {
		return stratumHealth{Healthy: false, Reason: "node/job updates stalled", Detail: "last success " + humanShortDuration(successAge) + " ago"}
	}

	return stratumHealth{Healthy: true}
}
