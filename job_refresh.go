package main

import (
	"context"
	"time"
)

func (jm *JobManager) refreshJobCtx(ctx context.Context) error {
	return jm.refreshJobCtxMinInterval(ctx, 100*time.Millisecond)
}

func (jm *JobManager) refreshJobCtxForce(ctx context.Context) error {
	return jm.refreshJobCtxMinInterval(ctx, 0)
}

func (jm *JobManager) refreshJobCtxMinInterval(ctx context.Context, minInterval time.Duration) error {
	jm.refreshMu.Lock()
	defer jm.refreshMu.Unlock()
	if minInterval > 0 && time.Since(jm.lastRefreshAttempt) < minInterval {
		return nil
	}
	jm.lastRefreshAttempt = time.Now()

	params := map[string]interface{}{
		"rules":        []string{"segwit"},
		"capabilities": []string{"coinbasetxn", "workid", "coinbase/append"},
	}
	tpl, err := jm.fetchTemplateCtx(ctx, params, false)
	if err != nil {
		jm.recordJobError(err)
		return err
	}
	return jm.refreshFromTemplate(ctx, tpl)
}

func (jm *JobManager) fetchTemplateCtx(ctx context.Context, params map[string]interface{}, useLongPoll bool) (GetBlockTemplateResult, error) {
	var tpl GetBlockTemplateResult
	var err error
	if useLongPoll {
		err = jm.rpc.callLongPollCtx(ctx, "getblocktemplate", []interface{}{params}, &tpl)
	} else {
		err = jm.rpc.callCtx(ctx, "getblocktemplate", []interface{}{params}, &tpl)
	}
	return tpl, err
}

func (jm *JobManager) refreshFromTemplate(ctx context.Context, tpl GetBlockTemplateResult) error {
	jm.applyMu.Lock()
	defer jm.applyMu.Unlock()

	needsNewJob, clean := jm.templateChanged(tpl)

	// If the template hasn't meaningfully changed, skip building and broadcasting a new job.
	// This avoids unnecessary job churn and duplicate JobIDs for the same work.
	if !needsNewJob {
		jm.updateBlockTipFromTemplate(tpl)
		return nil
	}

	job, err := jm.buildJob(ctx, tpl)
	if err != nil {
		jm.recordJobError(err)
		return err
	}
	job.Clean = clean

	jm.mu.Lock()
	jm.curJob = job
	jm.mu.Unlock()

	prevHeight := jm.blockTipHeight()

	jm.recordJobSuccess(job)
	jm.updateBlockTipFromTemplate(tpl)
	if tpl.Height > prevHeight {
		jm.refreshBlockHistoryFromRPC(ctx)
	}
	logger.Info("new job", "height", tpl.Height, "job_id", job.JobID, "bits", tpl.Bits, "txs", len(tpl.Transactions))
	jm.broadcastJob(job)
	return nil
}
