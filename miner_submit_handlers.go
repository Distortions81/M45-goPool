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
	if mc.cfg.DirectSubmitProcessing {
		mc.processSubmissionTask(task)
		return
	}
	ensureSubmissionWorkerPool()
	submissionWorkers.submit(task)
}

func (mc *MinerConn) handleSubmitStringParams(id interface{}, params []string) {
	now := time.Now()
	task, ok := mc.prepareSubmissionTaskStringParams(id, params, now)
	if !ok {
		return
	}
	if mc.cfg.DirectSubmitProcessing {
		mc.processSubmissionTask(task)
		return
	}
	ensureSubmissionWorkerPool()
	submissionWorkers.submit(task)
}

func (mc *MinerConn) prepareSubmissionTaskStringParams(id interface{}, params []string, now time.Time) (submissionTask, bool) {
	parsed, ok := mc.parseSubmitParamsStrings(id, params, now)
	if !ok {
		return submissionTask{}, false
	}
	if mc.cfg.RelaxedSubmitValidation {
		return mc.prepareSubmissionTaskSoloParsed(id, parsed, now)
	}
	return mc.prepareSubmissionTaskStrictParsed(id, parsed, now)
}
