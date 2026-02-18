package main

import (
	"strings"
	"time"
	"strconv"
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

	ibd, blocks, headers, fetchedAt := jobMgr.nodeSyncSnapshot()
	if !fetchedAt.IsZero() && (ibd || (headers > 0 && blocks >= 0 && blocks < headers)) {
		detail := "node syncing: ibd=" + strconv.FormatBool(ibd) + " blocks=" + strconv.FormatInt(blocks, 10) + " headers=" + strconv.FormatInt(headers, 10)
		return stratumHealth{Healthy: false, Reason: "node syncing/indexing", Detail: detail}
	}

	return stratumHealth{Healthy: true}
}
