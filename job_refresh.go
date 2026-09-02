package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	jobTemplateRefreshTimeout     = 10 * time.Second
	jobBlockHistoryRefreshTimeout = 3 * time.Second
	jobRawBlockCoalesceDelay      = 25 * time.Millisecond
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

// refreshJobCtxRaceParent starts a normal GBT request without taking refreshMu
// so it can genuinely race the already-open long poll. Template application is
// still serialized by applyMu, and a long-poll winner makes this result a no-op.
func (jm *JobManager) refreshJobCtxRaceParent(ctx context.Context, parent string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if parent == "" || jm.currentTemplateUsesParent(parent) {
		return nil
	}
	baseJob := jm.CurrentJob()
	baseParent := ""
	baseHadJob := baseJob != nil
	if baseJob != nil {
		baseParent = baseJob.Template.Previous
	}
	timeout := jm.refreshRPCTimeout
	if timeout <= 0 {
		timeout = jobTemplateRefreshTimeout
	}
	refreshCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	params := map[string]any{
		"rules":        []string{"segwit"},
		"capabilities": []string{"coinbasetxn", "workid", "coinbase/append"},
	}
	tpl, err := jm.fetchTemplateCtx(refreshCtx, params, false)
	if err != nil {
		jm.recordJobError(err)
		return err
	}
	if jm.currentTemplateUsesParent(parent) {
		jm.recordJobSuccess(nil)
		return nil
	}
	if tpl.Previous != parent {
		err := fmt.Errorf("%w: raced template parent %s does not match announced %s", errStaleTemplate, tpl.Previous, parent)
		jm.recordJobError(err)
		return err
	}
	return jm.refreshFromTemplateExpected(refreshCtx, tpl, parent, true, baseParent, baseHadJob)
}

func (jm *JobManager) applyLongPollTemplate(ctx context.Context, tpl GetBlockTemplateResult) error {
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
	return jm.refreshFromTemplateExpected(ctx, tpl, "", false, "", false)
}

func (jm *JobManager) refreshFromTemplateExpected(ctx context.Context, tpl GetBlockTemplateResult, expectedParent string, discardIfParentCurrent bool, raceBaseParent string, raceBaseHadJob bool) error {
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
	if expectedParent != "" && tpl.Previous != expectedParent {
		err := fmt.Errorf("%w: prev hash %s does not match announced %s", errStaleTemplate, tpl.Previous, expectedParent)
		jm.recordJobError(err)
		jm.applyMu.Unlock()
		return err
	}
	if discardIfParentCurrent && previousJob != nil && !previousJob.FastEmpty && previousJob.Template.Previous == expectedParent {
		jm.recordJobSuccess(nil)
		jm.applyMu.Unlock()
		return nil
	}
	if discardIfParentCurrent && ((!raceBaseHadJob && previousJob != nil) ||
		(raceBaseHadJob && (previousJob == nil || previousJob.Template.Previous != raceBaseParent))) {
		// A different parent won while this request was in flight. Never let the
		// slower result roll work back, even though its own ZMQ proof is valid.
		jm.applyMu.Unlock()
		return nil
	}
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
		return nil
	}

	// Only the GBT request started for this exact ZMQ notification may bypass
	// getbestblockhash. A time-based global announcement is not a freshness
	// proof: an unrelated long-poll response can arrive after a newer parent has
	// already won and otherwise roll miners back to stale work.
	job, err := jm.buildJobLockedWithParent(refreshCtx, tpl, expectedParent)
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
		previousJob.FastEmpty ||
		previousJob.Template.Previous != tpl.Previous ||
		previousJob.Template.Height != tpl.Height
	logger.Info("new job", "height", tpl.Height, "job_id", job.JobID, "bits", tpl.Bits, "txs", len(tpl.Transactions))
	// Publish work before declaring the feed recovered. The queue is
	// non-blocking, so no auxiliary status RPC can delay miners receiving the
	// new parent/template.
	jm.broadcastJob(job)
	jm.recordJobSuccess(job)
	jm.applyMu.Unlock()

	// Header history feeds only status/timing data. Run it after the complete
	// mining update is published and after applyMu is released. The worker is
	// independently bounded and coalesces churn to the newest job.
	if tipChanged || (tpl.Height > 0 && tpl.Height-1 > prevHeight) {
		jm.scheduleBlockHistoryRefresh(job)
	}
	return nil
}

// deferHashblockRefresh gives the matching rawblock notification a short
// opportunity to publish a safe empty job first. The timer is deliberately
// asynchronous so hashblock and rawblock may share one SUB socket without the
// receive loop blocking rawblock behind the delay.
func (jm *JobManager) deferHashblockRefresh(ctx context.Context, parent string) bool {
	if jm == nil || parent == "" {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	delay := jm.rawBlockCoalesceDelay
	if delay <= 0 {
		delay = jobRawBlockCoalesceDelay
	}

	jm.hashblockFallbackMu.Lock()
	if jm.hashblockFallbackTimer != nil && jm.hashblockFallbackParent == parent {
		jm.hashblockFallbackMu.Unlock()
		return true
	}
	if jm.hashblockFallbackTimer != nil {
		jm.hashblockFallbackTimer.Stop()
	}
	jm.hashblockFallbackGeneration++
	generation := jm.hashblockFallbackGeneration
	jm.hashblockFallbackParent = parent
	jm.hashblockFallbackTimer = time.AfterFunc(delay, func() {
		jm.runDeferredHashblockRefresh(ctx, parent, generation)
	})
	jm.hashblockFallbackMu.Unlock()
	return true
}

func (jm *JobManager) runDeferredHashblockRefresh(ctx context.Context, parent string, generation uint64) {
	jm.hashblockFallbackMu.Lock()
	if jm.hashblockFallbackGeneration != generation || jm.hashblockFallbackParent != parent {
		jm.hashblockFallbackMu.Unlock()
		return
	}
	jm.hashblockFallbackTimer = nil
	jm.hashblockFallbackParent = ""
	jm.hashblockFallbackMu.Unlock()

	if ctx.Err() != nil {
		return
	}
	if err := jm.refreshJobCtxRaceParent(ctx, parent); err != nil {
		if errors.Is(err, errStaleTemplate) {
			if debugLogging {
				logger.Debug("deferred hashblock refresh superseded", "component", "zmq", "kind", "coalesce", "block_hash", parent, "error", err)
			}
			return
		}
		logger.Error("deferred hashblock refresh error", "component", "zmq", "kind", "coalesce", "block_hash", parent, "error", err)
	}
}

// cancelDeferredHashblockRefresh transfers responsibility for the full GBT to
// the matching rawblock handler. Advancing the generation also makes a timer
// callback that was queued concurrently become a no-op.
func (jm *JobManager) cancelDeferredHashblockRefresh(parent string) bool {
	if jm == nil || parent == "" {
		return false
	}
	jm.hashblockFallbackMu.Lock()
	defer jm.hashblockFallbackMu.Unlock()
	if jm.hashblockFallbackTimer == nil || jm.hashblockFallbackParent != parent {
		return false
	}
	jm.hashblockFallbackGeneration++
	jm.hashblockFallbackTimer.Stop()
	jm.hashblockFallbackTimer = nil
	jm.hashblockFallbackParent = ""
	return true
}

func (jm *JobManager) currentTemplateUsesParent(parent string) bool {
	if parent == "" {
		return false
	}
	job := jm.CurrentJob()
	// A fast empty job starts useful work but does not satisfy the pending full
	// template refresh for that parent.
	return job != nil && !job.FastEmpty && job.Template.Previous == parent
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
