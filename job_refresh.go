package main

import (
	"context"
	"sync"
	"time"
)

const (
	jobTemplateRefreshTimeout     = 10 * time.Second
	jobBlockHistoryRefreshTimeout = 3 * time.Second
	jobLongPollCoalesceDelay      = 25 * time.Millisecond
)

func (jm *JobManager) refreshJobCtx(ctx context.Context) error {
	return jm.refreshJobCtxMinIntervalUnlessParent(ctx, 100*time.Millisecond, "")
}

func (jm *JobManager) refreshJobCtxForce(ctx context.Context) error {
	return jm.refreshJobCtxMinIntervalUnlessParent(ctx, 0, "")
}

func (jm *JobManager) refreshJobCtxMinInterval(ctx context.Context, minInterval time.Duration) error {
	return jm.refreshJobCtxMinIntervalUnlessParent(ctx, minInterval, "")
}

func (jm *JobManager) refreshJobCtxForceUnlessParent(ctx context.Context, parent string) error {
	return jm.refreshJobCtxMinIntervalUnlessParent(ctx, 0, parent)
}

func (jm *JobManager) refreshJobCtxMinIntervalUnlessParent(ctx context.Context, minInterval time.Duration, parent string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	jm.refreshMu.Lock()
	defer jm.refreshMu.Unlock()
	if jm.currentTemplateUsesParent(parent) {
		return nil
	}
	if minInterval > 0 && time.Since(jm.lastRefreshAttempt) < minInterval {
		return nil
	}
	jm.lastRefreshAttempt = time.Now()

	params := map[string]any{
		"rules":        []string{"segwit"},
		"capabilities": []string{"coinbasetxn", "workid", "coinbase/append"},
	}
	refreshTimeout := jm.refreshRPCTimeout
	if refreshTimeout <= 0 {
		refreshTimeout = jobTemplateRefreshTimeout
	}
	refreshCtx, cancel := context.WithTimeout(ctx, refreshTimeout)
	defer cancel()

	tpl, err := jm.fetchTemplateCtx(refreshCtx, params, false)
	if err != nil {
		jm.recordJobError(err)
		return err
	}
	// Keep the verification RPC and template application inside the same
	// bounded attempt as GBT. refreshFromTemplate also applies its own bound for
	// long-poll callers, whose parent context intentionally has no deadline.
	return jm.refreshFromTemplate(refreshCtx, tpl)
}

func (jm *JobManager) applyLongPollTemplate(ctx context.Context, tpl GetBlockTemplateResult) error {
	jm.refreshMu.Lock()
	defer jm.refreshMu.Unlock()
	return jm.refreshFromTemplate(ctx, tpl)
}

func (jm *JobManager) fetchTemplateCtx(ctx context.Context, params map[string]any, useLongPoll bool) (GetBlockTemplateResult, error) {
	var tpl GetBlockTemplateResult
	var err error
	if useLongPoll {
		err = jm.rpc.callLongPollCtx(ctx, "getblocktemplate", []any{params}, &tpl)
	} else {
		err = jm.rpc.callCtx(ctx, "getblocktemplate", []any{params}, &tpl)
	}
	return tpl, err
}

func (jm *JobManager) refreshFromTemplate(ctx context.Context, tpl GetBlockTemplateResult) error {
	templateReceivedAt := time.Now()
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := jm.refreshRPCTimeout
	if timeout <= 0 {
		timeout = jobTemplateRefreshTimeout
	}
	refreshCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := lockMutexContext(refreshCtx, &jm.applyMu); err != nil {
		jm.recordJobError(err)
		return err
	}

	jm.mu.RLock()
	previousJob := jm.curJob
	jm.mu.RUnlock()
	needsNewJob, clean := jm.templateChangedLocked(tpl)

	// If the template hasn't meaningfully changed, skip building and broadcasting a new job.
	// This avoids unnecessary job churn and duplicate JobIDs for the same work.
	if !needsNewJob {
		// An unchanged GBT payload is not proof that its parent is still the
		// active chain tip. In particular, a ZMQ-triggered refresh can race Core's
		// template update after a block or reorg. Verify before treating this as a
		// healthy heartbeat or advancing the opaque long-poll cursor.
		if err := jm.ensureTemplateFresh(refreshCtx, tpl); err != nil {
			jm.recordJobError(err)
			jm.applyMu.Unlock()
			return err
		}
		jm.mu.Lock()
		jm.longPollID = tpl.LongPollID
		jm.mu.Unlock()
		// Heartbeat: the node responded successfully, even if the template was unchanged.
		jm.recordJobSuccess(nil)
		jm.updateBlockTipFromTemplate(tpl)
		jm.applyMu.Unlock()
		jm.signalTemplateUpdate()
		return nil
	}

	job, err := jm.buildJobLocked(refreshCtx, tpl)
	if err != nil {
		jm.recordJobError(err)
		jm.applyMu.Unlock()
		return err
	}
	job.Clean = clean
	job.templateReceivedAt = templateReceivedAt

	jm.mu.Lock()
	jm.curJob = job
	jm.longPollID = tpl.LongPollID
	jm.mu.Unlock()

	prevHeight := jm.blockTipHeight()
	jm.updateBlockTipFromTemplate(tpl)
	tipChanged := previousJob == nil ||
		previousJob.Template.Previous != tpl.Previous ||
		previousJob.Template.Height != tpl.Height
	logger.Info("new job", "height", tpl.Height, "job_id", job.JobID, "bits", tpl.Bits, "txs", len(tpl.Transactions))
	// Publish work before declaring the feed recovered. The queue is
	// non-blocking, so no auxiliary status RPC can delay miners receiving the
	// new parent/template.
	jm.broadcastJob(job)
	jm.recordJobSuccess(job)
	jm.applyMu.Unlock()
	jm.signalTemplateUpdate()

	// Header history feeds only status/timing data. Run it after the complete
	// mining update is published and after applyMu is released. The worker is
	// independently bounded and coalesces churn to the newest job.
	if tipChanged || (tpl.Height > 0 && tpl.Height-1 > prevHeight) {
		jm.scheduleBlockHistoryRefresh(job)
	}
	return nil
}

func (jm *JobManager) signalTemplateUpdate() {
	jm.templateUpdateMu.Lock()
	if jm.templateUpdateCh != nil {
		close(jm.templateUpdateCh)
	}
	jm.templateUpdateCh = make(chan struct{})
	jm.templateUpdateMu.Unlock()
}

func (jm *JobManager) templateUpdateWaiter() <-chan struct{} {
	jm.templateUpdateMu.Lock()
	if jm.templateUpdateCh == nil {
		jm.templateUpdateCh = make(chan struct{})
	}
	ch := jm.templateUpdateCh
	jm.templateUpdateMu.Unlock()
	return ch
}

func (jm *JobManager) currentTemplateUsesParent(parent string) bool {
	if parent == "" {
		return false
	}
	job := jm.CurrentJob()
	return job != nil && job.Template.Previous == parent
}

func (jm *JobManager) waitForLongPollParent(ctx context.Context, parent string) bool {
	if jm.currentTemplateUsesParent(parent) {
		return true
	}
	if !jm.longPollActive.Load() {
		return false
	}
	delay := jm.longPollCoalesceDelay
	if delay <= 0 {
		delay = jobLongPollCoalesceDelay
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		updated := jm.templateUpdateWaiter()
		// Close can race with obtaining the waiter, so recheck the published
		// parent before blocking.
		if jm.currentTemplateUsesParent(parent) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return jm.currentTemplateUsesParent(parent)
		case <-updated:
			if jm.currentTemplateUsesParent(parent) {
				return true
			}
		}
	}
}

func lockMutexContext(ctx context.Context, mu *sync.Mutex) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if mu.TryLock() {
		return nil
	}

	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if !mu.TryLock() {
				continue
			}
			if err := ctx.Err(); err != nil {
				mu.Unlock()
				return err
			}
			return nil
		}
	}
}
