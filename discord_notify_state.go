package main

import "time"

// workerNotifyState tracks per-user, per-worker notification state.
type workerNotifyState struct {
	Online bool
	Since  time.Time

	SeenOnline  bool
	SeenOffline bool

	OfflineNotified bool

	RecoveryEligible bool
	RecoveryNotified bool
}
