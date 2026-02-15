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
	ensureSubmissionWorkerPool()
	submissionWorkers.submit(task)
}

func (mc *MinerConn) handleSubmitStringParams(id any, params []string) {
	now := time.Now()
	task, ok := mc.prepareSubmissionTaskStringParams(id, params, now)
	if !ok {
		return
	}
	if mc.cfg.SubmitProcessInline {
		mc.processSubmissionTask(task)
		return
	}
	ensureSubmissionWorkerPool()
	submissionWorkers.submit(task)
}

func (mc *MinerConn) prepareSubmissionTaskStringParams(id any, params []string, now time.Time) (submissionTask, bool) {
	parsed, ok := mc.parseSubmitParamsStrings(id, params, now)
	if !ok {
		return submissionTask{}, false
	}
	if !mc.useStrictSubmitPath() {
		return mc.prepareSubmissionTaskSoloParsed(id, parsed, now)
	}
	return mc.prepareSubmissionTaskStrictParsed(id, parsed, now)
}
