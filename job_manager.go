package main

import (
	"context"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/remeh/sizedwaitgroup"
)

const jobMgrNodeSyncTimeout = 3 * time.Second

func (jm *JobManager) recordJobError(err error) {
	if err == nil {
		return
	}
	jm.lastErrMu.Lock()
	if jm.lastErr == nil || jm.lastErrAt.IsZero() {
		// Keep the beginning of the continuous failure visible. Repeated
		// heartbeat failures update the detail, but must not restart the
		// stale-work grace window.
		jm.lastErrAt = time.Now()
	}
	jm.lastErr = err
	jm.appendJobFeedError(err.Error())
	jm.lastErrMu.Unlock()
}

func (jm *JobManager) appendJobFeedError(msg string) {
	if msg == "" {
		return
	}
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return
	}
	jm.jobFeedErrHistory = append(jm.jobFeedErrHistory, msg)
	if len(jm.jobFeedErrHistory) > jobFeedErrorHistorySize {
		jm.jobFeedErrHistory = jm.jobFeedErrHistory[len(jm.jobFeedErrHistory)-jobFeedErrorHistorySize:]
	}
}

func (jm *JobManager) sleepRetry(ctx context.Context) error {
	return sleepContext(ctx, jm.nextRetryDelay())
}

func (jm *JobManager) nextRetryDelay() time.Duration {
	jm.retryMu.Lock()
	defer jm.retryMu.Unlock()
	if jm.retryDelay == 0 {
		jm.retryDelay = jobRetryDelayMin
		return jm.retryDelay
	}
	jm.retryDelay *= 2
	if jm.retryDelay > jobRetryDelayMax {
		jm.retryDelay = jobRetryDelayMax
	}
	return jm.retryDelay
}

func (jm *JobManager) resetRetryDelay() {
	jm.retryMu.Lock()
	jm.retryDelay = 0
	jm.retryMu.Unlock()
}

func (jm *JobManager) recordJobSuccess(job *Job) {
	jm.lastErrMu.Lock()
	hadErr := jm.lastErr != nil
	jm.lastErr = nil
	jm.lastErrAt = time.Time{}
	if job != nil && !job.CreatedAt.IsZero() {
		jm.lastJobSuccess = job.CreatedAt
	} else {
		jm.lastJobSuccess = time.Now()
	}
	if hadErr {
		target := "(unknown)"
		if jm.rpc != nil {
			target = jm.rpc.EndpointLabel()
		}
		jm.appendJobFeedError("event: job feed recovered (rpc " + target + ")")
	}
	jm.lastErrMu.Unlock()
	jm.resetRetryDelay()
}

func (jm *JobManager) nodeSyncSnapshot() (ibd bool, blocks int64, headers int64, fetchedAt time.Time) {
	if jm == nil {
		return false, 0, 0, time.Time{}
	}
	jm.nodeSyncMu.RLock()
	ibd = jm.nodeIBD
	blocks = jm.nodeBlocks
	headers = jm.nodeHeaders
	fetchedAt = jm.nodeSyncFetched
	jm.nodeSyncMu.RUnlock()
	return
}

// refreshNodeSyncInfo updates the node sync/indexing state via getblockchaininfo.
// This is best-effort; failures should not poison job-feed health while we
// already have a usable current job template, otherwise transient
// getblockchaininfo hiccups can flap Stratum gating and disconnect miners.
// We only record the error when no job template exists yet.
func (jm *JobManager) refreshNodeSyncInfo(ctx context.Context) {
	if jm == nil || jm.rpc == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	type bcInfo struct {
		Blocks               int64 `json:"blocks"`
		Headers              int64 `json:"headers"`
		InitialBlockDownload bool  `json:"initialblockdownload"`
	}
	var bc bcInfo

	callCtx, cancel := context.WithTimeout(ctx, jobMgrNodeSyncTimeout)
	defer cancel()
	err := jm.rpc.callCtx(callCtx, "getblockchaininfo", nil, &bc)
	if err != nil {
		// Some bitcoind warmup/indexing states can still serve sockets but are not usable.
		// Treat these as degraded signals.
		if ctx.Err() != nil {
			return
		}
		job := jm.CurrentJob()
		if job == nil || job.CreatedAt.IsZero() {
			jm.recordJobError(err)
		}
		return
	}

	jm.nodeSyncMu.Lock()
	jm.nodeIBD = bc.InitialBlockDownload
	jm.nodeBlocks = bc.Blocks
	jm.nodeHeaders = bc.Headers
	jm.nodeSyncFetched = time.Now()
	jm.nodeSyncMu.Unlock()
}

func (jm *JobManager) FeedStatus() JobFeedStatus {
	jm.lastErrMu.RLock()
	lastErr := jm.lastErr
	lastErrAt := jm.lastErrAt
	lastSuccess := jm.lastJobSuccess
	errorHistory := append([]string(nil), jm.jobFeedErrHistory...)
	jm.lastErrMu.RUnlock()

	jm.mu.RLock()
	cur := jm.curJob
	jm.mu.RUnlock()

	if lastSuccess.IsZero() && cur != nil && !cur.CreatedAt.IsZero() {
		lastSuccess = cur.CreatedAt
	}

	zmqEnabled := jm.zmqHashBlockAddr != "" || jm.zmqRawBlockAddr != ""
	zmqHealthy := false
	if zmqEnabled {
		zmqHealthy = jm.zmqHashblockHealthy.Load() || jm.zmqRawblockHealthy.Load()
	}

	return JobFeedStatus{
		Ready:          cur != nil,
		LastSuccess:    lastSuccess,
		LastError:      lastErr,
		LastErrorAt:    lastErrAt,
		ErrorHistory:   errorHistory,
		ZMQHealthy:     zmqHealthy,
		ZMQDisconnects: atomic.LoadUint64(&jm.zmqDisconnects),
		ZMQReconnects:  atomic.LoadUint64(&jm.zmqReconnects),
		Payload:        jm.payloadStatus(),
	}
}

func (jm *JobManager) updateBlockTipFromTemplate(tpl GetBlockTemplateResult) {
	// GBT height is the candidate block's height; its Previous hash is the
	// current chain tip one height below it.
	if tpl.Height <= 0 {
		return
	}
	tipHeight := tpl.Height - 1

	jm.zmqPayloadMu.Lock()

	tip := jm.zmqPayload.BlockTip
	oldHeight := tip.Height
	isNewBlock := tip.Height == 0 || tipHeight > tip.Height
	if isNewBlock {
		tip.Height = tipHeight
		if debugLogging {
			logger.Debug("updateBlockTipFromTemplate: height updated", "old", oldHeight, "new", tipHeight)
		}
	}
	// Note: tpl.CurTime is template time (node wall-clock), not a block header
	// timestamp; keep any existing blockchain-derived tip time instead.
	if tip.Time.IsZero() && tpl.CurTime > 0 {
		tip.Time = time.Unix(tpl.CurTime, 0).UTC()
	}
	if bits := strings.TrimSpace(tpl.Bits); bits != "" {
		tip.Bits = bits
		if parsed, err := strconv.ParseUint(bits, 16, 32); err == nil {
			tip.Bits = uint32ToHex8Lower(uint32(parsed))
			tip.Difficulty = difficultyFromBits(uint32(parsed))
		}
	}
	jm.zmqPayload.BlockTip = tip

	jm.zmqPayloadMu.Unlock()

	// Notify status cache of new block (outside lock to avoid holding lock during callback)
	if isNewBlock && jm.onNewBlock != nil {
		jm.onNewBlock()
	}
}

func (jm *JobManager) blockTipHeight() int64 {
	jm.zmqPayloadMu.RLock()
	defer jm.zmqPayloadMu.RUnlock()
	return jm.zmqPayload.BlockTip.Height
}

func (jm *JobManager) refreshBlockHistoryFromRPC(ctx context.Context) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := jm.historyRPCTimeout
	if timeout <= 0 {
		timeout = jobBlockHistoryRefreshTimeout
	}
	historyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	tip, recentTimes, ok := jm.fetchBlockHistoryFromRPC(historyCtx, "")
	if !ok {
		return false
	}
	jm.storeBlockHistory(tip, recentTimes)
	return true
}

type blockHistoryRefreshRequest struct {
	generation  uint64
	prevHash    string
	tipSequence uint64
}

func (jm *JobManager) fetchBlockHistoryFromRPC(ctx context.Context, expectedHash string) (ZMQBlockTip, []time.Time, bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	if jm.rpc == nil {
		return ZMQBlockTip{}, nil, false
	}

	hash, err := jm.rpc.GetBestBlockHash(ctx)
	if err != nil {
		logger.Warn("failed to fetch best block hash for block history", "error", err)
		return ZMQBlockTip{}, nil, false
	}
	if expectedHash != "" && hash != expectedHash {
		logger.Debug("skipping superseded block history refresh", "expected_tip", expectedHash, "rpc_tip", hash)
		return ZMQBlockTip{}, nil, false
	}

	header, err := jm.rpc.GetBlockHeader(ctx, hash)
	if err != nil {
		logger.Warn("failed to fetch best block header for block history", "error", err)
		return ZMQBlockTip{}, nil, false
	}

	tip := ZMQBlockTip{
		Hash:       header.Hash,
		Height:     header.Height,
		Time:       time.Unix(header.Time, 0).UTC(),
		Bits:       header.Bits,
		Difficulty: header.Difficulty,
	}

	recentTimes := []time.Time{tip.Time}
	prevHash := header.PreviousBlockHash
	for i := 0; i < 3 && prevHash != ""; i++ {
		prevHeader, err := jm.rpc.GetBlockHeader(ctx, prevHash)
		if err != nil {
			logger.Warn("failed to fetch previous block header for block history", "height", header.Height-int64(i+1), "error", err)
			break
		}
		recentTimes = append([]time.Time{time.Unix(prevHeader.Time, 0).UTC()}, recentTimes...)
		prevHash = prevHeader.PreviousBlockHash
	}

	if len(recentTimes) > 4 {
		recentTimes = recentTimes[len(recentTimes)-4:]
	}

	return tip, recentTimes, true
}

func (jm *JobManager) storeBlockHistory(tip ZMQBlockTip, recentTimes []time.Time) {
	jm.zmqPayloadMu.Lock()
	jm.zmqPayload.BlockTip = tip
	jm.zmqPayload.RecentBlockTimes = recentTimes
	jm.zmqPayload.BlockTimerActive = true
	jm.zmqPayloadMu.Unlock()
}

func (jm *JobManager) scheduleBlockHistoryRefresh(job *Job) {
	if jm == nil || jm.rpc == nil || job == nil || job.Generation == 0 || job.Template.Previous == "" {
		return
	}
	jm.zmqPayloadMu.RLock()
	tipSequence := jm.blockTipSequence
	jm.zmqPayloadMu.RUnlock()
	req := blockHistoryRefreshRequest{
		generation:  job.Generation,
		prevHash:    job.Template.Previous,
		tipSequence: tipSequence,
	}

	jm.historyMu.Lock()
	if req.generation < jm.historyLatest {
		jm.historyMu.Unlock()
		return
	}
	jm.historyLatest = req.generation
	if jm.historyRunning {
		// Only the newest request matters. Generation is monotonically assigned
		// when jobs are built, including across lower-height reorgs.
		if !jm.historyPendingSet || req.generation >= jm.historyPending.generation {
			jm.historyPending = req
			jm.historyPendingSet = true
		}
		jm.historyMu.Unlock()
		return
	}
	jm.historyRunning = true
	jm.historyMu.Unlock()

	go jm.runBlockHistoryRefreshes(req)
}

func (jm *JobManager) runBlockHistoryRefreshes(req blockHistoryRefreshRequest) {
	for {
		timeout := jm.historyRPCTimeout
		if timeout <= 0 {
			timeout = jobBlockHistoryRefreshTimeout
		}
		jm.historyMu.Lock()
		baseCtx := jm.historyCtx
		jm.historyMu.Unlock()
		if baseCtx == nil {
			baseCtx = context.Background()
		}
		ctx, cancel := context.WithTimeout(baseCtx, timeout)
		tip, recentTimes, ok := jm.fetchBlockHistoryFromRPC(ctx, req.prevHash)
		cancel()

		if ok {
			jm.commitBlockHistoryIfCurrent(req, tip, recentTimes)
		}

		jm.historyMu.Lock()
		if jm.historyPendingSet {
			req = jm.historyPending
			jm.historyPending = blockHistoryRefreshRequest{}
			jm.historyPendingSet = false
			jm.historyMu.Unlock()
			continue
		}
		jm.historyRunning = false
		jm.historyMu.Unlock()
		return
	}
}

func (jm *JobManager) commitBlockHistoryIfCurrent(req blockHistoryRefreshRequest, tip ZMQBlockTip, recentTimes []time.Time) bool {
	// Keep the current-parent check and payload commit in one read-side critical
	// section. Otherwise a reorg job can become current after the check while an
	// older history walk is still storing its result.
	jm.mu.RLock()
	defer jm.mu.RUnlock()
	cur := jm.curJob
	// Transaction/coinbase-only churn creates a newer job generation on the same
	// parent without scheduling another history walk. That history remains valid,
	// so parent identity, rather than exact generation, is authoritative here.
	if cur == nil || cur.Template.Previous != req.prevHash {
		return false
	}

	jm.zmqPayloadMu.Lock()
	defer jm.zmqPayloadMu.Unlock()
	// A raw-block notification may lead the GBT refresh. Do not roll that newer
	// authoritative tip back to this request's parent while the new template is
	// still being fetched. A same-hash raw notification is safe and history can
	// still backfill its preceding timestamps.
	if jm.blockTipSequence != req.tipSequence &&
		jm.zmqPayload.BlockTip.Hash != "" &&
		jm.zmqPayload.BlockTip.Hash != req.prevHash {
		return false
	}
	jm.zmqPayload.BlockTip = tip
	jm.zmqPayload.RecentBlockTimes = recentTimes
	jm.zmqPayload.BlockTimerActive = true
	return true
}

func (jm *JobManager) recordRawBlockPayload(size int) {
	jm.zmqPayloadMu.Lock()
	jm.zmqPayload.LastRawBlockAt = time.Now()
	jm.zmqPayload.LastRawBlockBytes = size
	jm.zmqPayloadMu.Unlock()
}

func (jm *JobManager) recordBlockTip(tip ZMQBlockTip) {
	jm.zmqPayloadMu.Lock()
	jm.blockTipSequence++

	// Check if this is a new block (different from current block tip)
	isNewBlock := jm.zmqPayload.BlockTip.Height == 0 ||
		(tip.Height > jm.zmqPayload.BlockTip.Height) ||
		(tip.Hash != "" && tip.Hash != jm.zmqPayload.BlockTip.Hash)

	jm.zmqPayload.BlockTip = tip

	// Track recent block times (keep last 4)
	if isNewBlock && !tip.Time.IsZero() {
		// Append even if timestamps repeat (multiple blocks can share the same header time).
		jm.zmqPayload.RecentBlockTimes = append(jm.zmqPayload.RecentBlockTimes, tip.Time)
		if len(jm.zmqPayload.RecentBlockTimes) > 4 {
			jm.zmqPayload.RecentBlockTimes = jm.zmqPayload.RecentBlockTimes[len(jm.zmqPayload.RecentBlockTimes)-4:]
		}
		jm.zmqPayload.BlockTimerActive = true
	}

	jm.zmqPayloadMu.Unlock()

	// Notify status cache of new block (outside lock to avoid holding lock during callback)
	if isNewBlock && !tip.Time.IsZero() && jm.onNewBlock != nil {
		jm.onNewBlock()
	}
}

func (jm *JobManager) payloadStatus() JobFeedPayloadStatus {
	jm.zmqPayloadMu.RLock()
	defer jm.zmqPayloadMu.RUnlock()
	return jm.zmqPayload
}

func (jm *JobManager) Start(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	jm.historyMu.Lock()
	jm.historyCtx = ctx
	jm.historyMu.Unlock()

	// A single dispatcher preserves FIFO job order. Parallel consumers can
	// receive consecutive templates in order but acquire the subscriber lock in
	// reverse order, rolling miners back to stale work.
	numWorkers := 1
	jm.notifyWg = sizedwaitgroup.New(numWorkers)
	for i := range numWorkers {
		jm.notifyWg.Add()
		go jm.notificationWorker(ctx, i)
	}
	logger.Info("started async notification workers", "count", numWorkers)

	if err := jm.refreshJobCtx(ctx); err != nil {
		logger.Error("initial job refresh error", "error", err)
	}
	// Best-effort initial sync snapshot so the pool can gate mining while the node
	// is indexing/syncing (IBD).
	jm.refreshNodeSyncInfo(ctx)

	go jm.longpollLoop(ctx)
	go jm.heartbeatLoop(ctx)
	jm.startZMQLoops(ctx)
}

// ApplyRuntimeConfig updates future job-building settings and payout scripts in
// memory so admin Apply can take effect without a process restart.
func (jm *JobManager) ApplyRuntimeConfig(cfg Config, payoutScript, donationScript []byte) {
	if jm == nil {
		return
	}
	jm.applyMu.Lock()
	jm.cfg = cfg
	// Allocate new backing arrays so outstanding jobs can continue using the
	// exact payout policy under which their coinbases were advertised.
	jm.payoutScript = append([]byte(nil), payoutScript...)
	jm.donationScript = append([]byte(nil), donationScript...)
	jm.applyMu.Unlock()
}

func (jm *JobManager) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(stratumHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Prove the node is responsive even without template churn.
			_ = jm.refreshJobCtxMinInterval(ctx, 0)
			jm.refreshNodeSyncInfo(ctx)
		}
	}
}
