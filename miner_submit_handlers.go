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

	// Resolve and hash every plausible header on the connection handler before
	// touching the ordinary-share FIFO. Raw submissions cannot be prioritized
	// safely because a winning block is indistinguishable until this work is
	// complete.
	mc.logSubmissionTask(task)
	prepared, ok := mc.prepareSubmissionForProcessing(task)
	if !ok {
		mc.recordSubmissionTaskRTT(task)
		return
	}
	if prepared.ctx.isBlock {
		// Network-target work is cryptographically scarce and must never wait
		// behind public share traffic. The connection handler is tracked by
		// connWg, so shutdown still waits for its detached RPC retry window.
		mc.processPreparedSubmissionTask(prepared)
		return
	}

	pool := ensureSubmissionWorkerPool()
	switch pool.trySubmit(prepared) {
	case submissionAdmissionAccepted:
		return
	case submissionAdmissionClosed:
		// Production shutdown joins every miner handler before closing the pool,
		// so this is a defensive fallback for an unexpected lifecycle race. Keep
		// processing the already-proven non-block on the tracked handler.
		logger.Warn("submission worker pool unavailable; processing inline", "remote", mc.id)
		mc.processPreparedSubmissionTask(prepared)
	case submissionAdmissionFull:
		// This task and every alternate/rescue interpretation are proven
		// non-blocks. Shed it without an invalid-submit strike so this connection
		// remains able to deliver a later winning nonce immediately.
		defer mc.recordSubmissionTaskRTT(task)
		if mc.metrics != nil {
			mc.metrics.RecordSubmitError("server_busy")
		}
		logger.Debug("submission worker queue full; rejecting non-block share", "remote", mc.id, "job", task.jobID)
		mc.writeResponse(StratumResponse{
			ID:     task.reqID,
			Result: false,
			Error:  newStratumError(stratumErrCodeInvalidRequest, "server busy"),
		})
	}
}
