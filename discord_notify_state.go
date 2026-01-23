package main

import "time"

// workerNotifyState tracks per-user, per-worker notification state.
type workerNotifyState struct {
	Online bool
	Since  time.Time

	SeenOnline  bool
	SeenOffline bool

	// OfflineEligible is set when a worker transitions from online -> offline and
	// had been continuously online for long enough to qualify for an offline
	// notification.
	OfflineEligible bool

	OfflineNotified bool

	RecoveryEligible bool
	RecoveryNotified bool
}
