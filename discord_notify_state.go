package main

import "time"

// workerNotifyState tracks per-user, per-worker notification state.
// It is persisted so we don't alert "offline" unless the worker has previously
// been observed online (and vice versa).
type workerNotifyState struct {
	Online bool
	Since  time.Time

	SeenOnline  bool
	SeenOffline bool

	OfflineNotified bool

	RecoveryEligible bool
	RecoveryNotified bool
}
