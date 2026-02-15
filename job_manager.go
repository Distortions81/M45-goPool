package main

import (
	"context"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/remeh/sizedwaitgroup"
)

func (jm *JobManager) recordJobError(err error) {
	if err == nil {
		return
	}
	jm.lastErrMu.Lock()
	jm.lastErr = err
	jm.lastErrAt = time.Now()
	jm.lastJobSuccess = time.Time{}
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

	zmqEnabled := jm.cfg.ZMQHashBlockAddr != "" || jm.cfg.ZMQRawBlockAddr != ""
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
	if tpl.Height <= 0 {
		return
	}

	jm.zmqPayloadMu.Lock()

	tip := jm.zmqPayload.BlockTip
	oldHeight := tip.Height
	isNewBlock := tip.Height == 0 || tpl.Height > tip.Height
	if isNewBlock {
		tip.Height = tpl.Height
		if debugLogging {
			logger.Debug("updateBlockTipFromTemplate: height updated", "old", oldHeight, "new", tpl.Height)
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
			tip.Bits = fmt.Sprintf("%08x", uint32(parsed))
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
	if jm.rpc == nil {
		return false
	}

	hash, err := jm.rpc.GetBestBlockHash(ctx)
	if err != nil {
		logger.Warn("failed to fetch best block hash for block history", "error", err)
		return false
	}

	header, err := jm.rpc.GetBlockHeader(ctx, hash)
	if err != nil {
		logger.Warn("failed to fetch best block header for block history", "error", err)
		return false
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

	jm.zmqPayloadMu.Lock()
	jm.zmqPayload.BlockTip = tip
	jm.zmqPayload.RecentBlockTimes = recentTimes
	jm.zmqPayload.BlockTimerActive = true
	jm.zmqPayloadMu.Unlock()
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

// fetchInitialBlockInfo queries the node for the current block header and previous 3 blocks
// to initialize the block tip with blockchain timestamp data and historical block times.
func (jm *JobManager) fetchInitialBlockInfo(ctx context.Context) {
	if jm.rpc == nil {
		return
	}

	// Get the current best block hash
	hash, err := jm.rpc.GetBestBlockHash(ctx)
	if err != nil {
		logger.Warn("failed to fetch best block hash on startup", "error", err)
		return
	}

	// Get the block header for the current tip
	header, err := jm.rpc.GetBlockHeader(ctx, hash)
	if err != nil {
		logger.Warn("failed to fetch block header on startup", "error", err)
		return
	}

	// Convert to ZMQBlockTip format
	tip := ZMQBlockTip{
		Hash:       header.Hash,
		Height:     header.Height,
		Time:       time.Unix(header.Time, 0).UTC(),
		Bits:       header.Bits,
		Difficulty: header.Difficulty,
	}

	// Fetch the previous 3 block times for historical data
	recentTimes := []time.Time{tip.Time}
	prevHash := header.PreviousBlockHash
	for i := 0; i < 3 && prevHash != ""; i++ {
		prevHeader, err := jm.rpc.GetBlockHeader(ctx, prevHash)
		if err != nil {
			logger.Warn("failed to fetch previous block header", "height", header.Height-int64(i+1), "error", err)
			break
		}
		recentTimes = append([]time.Time{time.Unix(prevHeader.Time, 0).UTC()}, recentTimes...)
		prevHash = prevHeader.PreviousBlockHash
	}

	// Keep only the last 4 timestamps (current + up to 3 previous)
	if len(recentTimes) > 4 {
		recentTimes = recentTimes[len(recentTimes)-4:]
	}

	// Record this as the initial block tip and activate the timer
	jm.zmqPayloadMu.Lock()
	jm.zmqPayload.BlockTip = tip
	jm.zmqPayload.RecentBlockTimes = recentTimes
	jm.zmqPayload.BlockTimerActive = true
	jm.zmqPayloadMu.Unlock()

	logger.Info("initialized block tip from blockchain", "height", tip.Height, "hash", tip.Hash[:16]+"...", "historical_blocks", len(recentTimes)-1)
}

func (jm *JobManager) Start(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}

	// Start notification workers for async job distribution
	// Use runtime.NumCPU() workers to handle fanout efficiently across available cores
	numWorkers := runtime.NumCPU()
	jm.notifyWg = sizedwaitgroup.New(numWorkers)
	for i := range numWorkers {
		jm.notifyWg.Add()
		go jm.notificationWorker(ctx, i)
	}
	logger.Info("started async notification workers", "count", numWorkers)

	// Fetch initial block info from the blockchain so the UI has a tip/time even
	// if ZMQ isn't providing rawblock updates (or before the first ZMQ message).
	jm.fetchInitialBlockInfo(ctx)

	if err := jm.refreshJobCtx(ctx); err != nil {
		logger.Error("initial job refresh error", "error", err)
	}

	go jm.longpollLoop(ctx)
	jm.startZMQLoops(ctx)
}
