package main

import "time"

func (mc *MinerConn) handleSubmit(req *StratumRequest) {
	// Expect params like:
	// [worker_name, job_id, extranonce2, ntime, nonce]
	now := time.Now()

	task, ok := mc.prepareSubmissionTask(req, now)
	if !ok {
		return
	}
	if mc.cfg.SubmitProcessInline {
		mc.processSubmissionTask(task)
		return
	}
	pool := ensureSubmissionWorkerPool()
	if pool == nil || !pool.submit(task) {
		// Production shutdown joins every miner handler before closing the pool,
		// so this is a defensive fallback for an unexpected lifecycle race. Keep
		// processing on the tracked connection goroutine rather than discarding a
		// submission that may satisfy the network target.
		logger.Warn("submission worker pool unavailable; processing inline", "remote", mc.id)
		mc.processSubmissionTask(task)
	}
}
