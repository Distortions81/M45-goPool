package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"math/bits"
	"net"
	"sync/atomic"
	"time"
)

var nextConnectionID uint64

func (mc *MinerConn) cleanup() {
	mc.cleanupOnce.Do(func() {
		if mc.metrics != nil {
			if connSeq := atomic.LoadUint64(&mc.connectionSeq); connSeq != 0 {
				mc.metrics.RemoveConnectionHashrate(connSeq)
			}
		}
		mc.unregisterRegisteredWorker()

		// Close stats channel and wait for worker to finish processing
		close(mc.statsUpdates)
		mc.statsWg.Wait()

		mc.statsMu.Lock()
		mc.stats.WindowStart = time.Time{}
		mc.stats.WindowAccepted = 0
		mc.stats.WindowSubmissions = 0
		mc.stats.WindowDifficulty = 0
		mc.lastHashrateUpdate = time.Time{}
		mc.rollingHashrateValue = 0
		mc.statsMu.Unlock()
		if mc.jobMgr != nil && mc.jobCh != nil {
			mc.jobMgr.Unsubscribe(mc.jobCh)
		}
		if mc.conn != nil {
			_ = mc.conn.Close()
		}
	})
}

func (mc *MinerConn) Close(reason string) {
	if reason == "" {
		reason = "shutdown"
	}
	logger.Info("closing miner", "remote", mc.id, "reason", reason)
	mc.cleanup()
}

func (mc *MinerConn) assignConnectionSeq() {
	if atomic.LoadUint64(&mc.connectionSeq) != 0 {
		return
	}
	id := atomic.AddUint64(&nextConnectionID, 1)
	atomic.StoreUint64(&mc.connectionSeq, id)
}

func (mc *MinerConn) connectionIDString() string {
	seq := atomic.LoadUint64(&mc.connectionSeq)
	if seq == 0 {
		return ""
	}
	return encodeBase58Uint64(seq - 1)
}

const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

func encodeBase58Uint64(value uint64) string {
	if value == 0 {
		return string(base58Alphabet[0])
	}
	var buf [16]byte
	i := len(buf)
	for value > 0 {
		i--
		buf[i] = base58Alphabet[value%58]
		value /= 58
	}
	return string(buf[i:])
}

func NewMinerConn(ctx context.Context, c net.Conn, jobMgr *JobManager, rpc rpcCaller, cfg Config, metrics *PoolMetrics, accounting *AccountStore, workerRegistry *workerConnectionRegistry, workerLists *workerListStore, notifier *discordNotifier, isTLS bool) *MinerConn {
	if ctx == nil {
		ctx = context.Background()
	}
	now := time.Now()
	if cfg.ConnectionTimeout <= 0 {
		cfg.ConnectionTimeout = defaultConnectionTimeout
	}
	jobCh := jobMgr.Subscribe()
	en1 := jobMgr.NextExtranonce1()
	maxRecentJobs := cfg.MaxRecentJobs
	if maxRecentJobs <= 0 {
		maxRecentJobs = defaultRecentJobs
	}

	mask := cfg.VersionMask
	if mask == 0 && !cfg.VersionMaskConfigured {
		mask = defaultVersionMask
	}
	minBits := cfg.MinVersionBits
	if mask == 0 {
		minBits = 0
	} else {
		if minBits <= 0 {
			minBits = 1
		}
		if minBits > bits.OnesCount32(mask) {
			minBits = bits.OnesCount32(mask)
		}
	}

	vdiff := defaultVarDiff
	// Difficulty clamps:
	// - cfg.MinDifficulty == 0 disables the minimum clamp (no lower bound).
	// - cfg.MaxDifficulty == 0 disables the maximum clamp (no upper bound).
	// - values > 0 enable a clamp at that value.
	if cfg.MinDifficulty == 0 {
		vdiff.MinDiff = 0
	} else if cfg.MinDifficulty > 0 {
		vdiff.MinDiff = cfg.MinDifficulty
	}
	if cfg.MaxDifficulty == 0 {
		vdiff.MaxDiff = 0
	} else if cfg.MaxDifficulty > 0 {
		// If the defaults ever include a max clamp, use the tighter one.
		if vdiff.MaxDiff <= 0 || cfg.MaxDifficulty < vdiff.MaxDiff {
			vdiff.MaxDiff = cfg.MaxDifficulty
		}
	}
	if vdiff.MaxDiff > 0 && vdiff.MinDiff > vdiff.MaxDiff {
		vdiff.MinDiff = vdiff.MaxDiff
	}

	// Start connections at the configured default difficulty when set; otherwise
	// use the minimum clamp or a conservative fallback and let VarDiff adjust.
	initialDiff := defaultMinDifficulty
	if cfg.DefaultDifficulty > 0 {
		initialDiff = cfg.DefaultDifficulty
	} else if cfg.MinDifficulty > 0 {
		initialDiff = cfg.MinDifficulty
	}
	if initialDiff <= 0 {
		initialDiff = 1.0
	}

	var shareCache map[string]*duplicateShareSet
	var evictedShareCache map[string]*evictedCacheEntry
	if cfg.CheckDuplicateShares {
		shareCache = make(map[string]*duplicateShareSet, maxRecentJobs)
		evictedShareCache = make(map[string]*evictedCacheEntry)
	}

	mc := &MinerConn{
		ctx:               ctx,
		id:                c.RemoteAddr().String(),
		conn:              c,
		writer:            bufio.NewWriter(c),
		reader:            bufio.NewReaderSize(c, maxStratumMessageSize),
		jobMgr:            jobMgr,
		rpc:               rpc,
		cfg:               cfg,
		extranonce1:       en1,
		jobCh:             jobCh,
		vardiff:           vdiff,
		metrics:           metrics,
		accounting:        accounting,
		workerRegistry:    workerRegistry,
		savedWorkerStore:  workerLists,
		discordNotifier:   notifier,
		activeJobs:        make(map[string]*Job, maxRecentJobs), // Pre-allocate for expected job count
		connectedAt:       now,
		lastActivity:      now,
		jobDifficulty:     make(map[string]float64, maxRecentJobs), // Pre-allocate for expected job count
		shareCache:        shareCache,
		evictedShareCache: evictedShareCache,
		maxRecentJobs:     maxRecentJobs,
		lastPenalty:       time.Now(),
		versionRoll:       false,
		versionMask:       0,
		poolMask:          mask,
		minerMask:         0,
		minVerBits:        minBits,
		bootstrapDone:     false,
		isTLSConnection:   isTLS,
		statsUpdates:      make(chan statsUpdate, 1000), // Buffered for up to 1000 pending stats updates
	}

	// Initialize atomic fields
	atomicStoreFloat64(&mc.difficulty, initialDiff)
	mc.shareTarget.Store(targetFromDifficulty(initialDiff))

	// Start stats worker goroutine
	mc.statsWg.Add(1)
	go mc.statsWorker()

	return mc
}

func (mc *MinerConn) handle() {
	defer mc.cleanup()
	if debugLogging || verboseLogging {
		logger.Info("miner connected", "remote", mc.id, "extranonce1", hex.EncodeToString(mc.extranonce1))
	}

	for {
		now := time.Now()
		if mc.ctx.Err() != nil {
			return
		}
		if expired, reason := mc.idleExpired(now); expired {
			logger.Warn("closing miner for idle timeout", "remote", mc.id, "reason", reason)
			return
		}
		deadline := now.Add(mc.currentReadTimeout())
		if err := mc.conn.SetReadDeadline(deadline); err != nil {
			if mc.ctx.Err() != nil {
				return
			}
			logger.Error("set read deadline failed", "remote", mc.id, "error", err)
			return
		}

		line, err := mc.reader.ReadBytes('\n')
		now = time.Now()
		if err != nil {
			if errors.Is(err, bufio.ErrBufferFull) {
				logger.Warn("closing miner for oversized message", "remote", mc.id, "limit_bytes", maxStratumMessageSize)
				if banned, count := mc.noteProtocolViolation(now); banned {
					mc.logBan("oversized stratum message", mc.currentWorker(), count)
				}
				return
			}
			if nErr, ok := err.(net.Error); ok && nErr.Timeout() {
				if expired, reason := mc.idleExpired(now); expired {
					logger.Warn("closing miner for idle timeout", "remote", mc.id, "reason", reason)
					return
				}
				continue
			}
			if err != io.EOF && !errors.Is(err, net.ErrClosed) {
				logger.Error("read error", "remote", mc.id, "error", err)
			}
			return
		}
		if len(line) > maxStratumMessageSize {
			logger.Warn("closing miner for oversized message", "remote", mc.id, "limit_bytes", maxStratumMessageSize)
			if banned, count := mc.noteProtocolViolation(now); banned {
				mc.logBan("oversized stratum message", mc.currentWorker(), count)
			}
			return
		}

		logNetMessage("recv", line)
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		mc.recordActivity(now)
		if mc.stratumMsgRateLimitExceeded(now) {
			logger.Warn("closing miner for stratum message rate limit",
				"remote", mc.id,
				"limit_per_min", mc.cfg.StratumMessagesPerMinute,
			)
			mc.banFor("stratum message rate limit", time.Hour, mc.currentWorker())
			return
		}

		if method, id, ok := sniffStratumMethodID(line); ok {
			switch method {
			case "mining.ping":
				mc.writePongResponse(id)
				continue
			case "mining.get_transactions":
				mc.writeEmptySliceResponse(id)
				continue
			case "mining.capabilities":
				mc.writeTrueResponse(id)
				continue
			case "mining.extranonce.subscribe":
				mc.handleExtranonceSubscribe(&StratumRequest{ID: id})
				continue
			case "mining.subscribe":
				if params, ok := sniffStratumStringParams(line, 1); ok {
					req := buildStringRequest(id, method, params)
					mc.handleSubscribe(&req)
					continue
				}
			case "mining.authorize":
				if params, ok := sniffStratumStringParams(line, 2); ok {
					req := buildStringRequest(id, method, params)
					mc.handleAuthorize(&req)
					continue
				}
			case "mining.submit":
				// Fast-path: most mining.submit payloads are small and string-only.
				// Avoid full JSON unmarshal on the connection goroutine to reduce
				// allocations and tail latency under load.
				if params, ok := sniffStratumStringParams(line, 6); ok && (len(params) == 5 || len(params) == 6) {
					mc.handleSubmitStringParams(id, params)
					continue
				}
			}
		}
		var req StratumRequest
		if err := fastJSONUnmarshal(line, &req); err != nil {
			logger.Warn("json error from miner", "remote", mc.id, "error", err)
			if banned, count := mc.noteProtocolViolation(now); banned {
				mc.logBan("invalid stratum json", mc.currentWorker(), count)
			}
			return
		}

		switch req.Method {
		case "mining.subscribe":
			mc.handleSubscribe(&req)
		case "mining.authorize":
			mc.handleAuthorize(&req)
		case "mining.submit":
			mc.handleSubmit(&req)
		case "mining.configure":
			mc.handleConfigure(&req)
		case "mining.extranonce.subscribe":
			mc.handleExtranonceSubscribe(&req)
		case "mining.suggest_difficulty":
			mc.suggestDifficulty(&req)
		case "mining.suggest_target":
			mc.suggestTarget(&req)
		case "mining.ping":
			// Respond to keepalive ping with pong
			mc.writePongResponse(req.ID)
		case "mining.get_transactions":
			// Client requests transaction hashes for current job. We don't
			// support this but acknowledge to avoid breaking miners that send it.
			mc.writeEmptySliceResponse(req.ID)
		case "mining.capabilities":
			// Draft extension where client advertises its capabilities.
			// Acknowledge receipt but we don't need to act on it.
			mc.writeTrueResponse(req.ID)
		default:
			// Silently ignore unknown methods to avoid confusing miners that
			// send non-standard extensions. Log at debug level for diagnostics.
			if debugLogging {
				logger.Debug("ignoring unknown stratum method", "remote", mc.id, "method", req.Method)
			}
		}

	}
}

func (mc *MinerConn) scheduleInitialWork() {
	mc.initWorkMu.Lock()
	if mc.initialWorkScheduled || mc.initialWorkSent {
		mc.initWorkMu.Unlock()
		return
	}
	mc.initialWorkScheduled = true
	mc.initWorkMu.Unlock()

	go func() {
		time.Sleep(defaultInitialDifficultyDelay)
		mc.sendInitialWork()
	}()
}

func (mc *MinerConn) maybeSendInitialWork() {
	mc.initWorkMu.Lock()
	alreadySent := mc.initialWorkSent
	mc.initWorkMu.Unlock()
	if alreadySent {
		return
	}
	mc.sendInitialWork()
}

func (mc *MinerConn) sendInitialWork() {
	mc.initWorkMu.Lock()
	if mc.initialWorkSent {
		mc.initWorkMu.Unlock()
		return
	}
	mc.initialWorkSent = true
	mc.initWorkMu.Unlock()

	if !mc.authorized || !mc.listenerOn {
		return
	}

	// Respect suggested difficulty if already processed. Otherwise, fall back
	// to a sane default/minimum so miners have a starting target.
	if !mc.suggestDiffProcessed && !mc.restoredRecentDiff {
		diff := mc.cfg.DefaultDifficulty
		if diff <= 0 {
			// Default difficulty of 0 means "unset": treat it as the minimum
			// difficulty (config min when set; otherwise the compiled-in minimum).
			diff = mc.cfg.MinDifficulty
			if diff <= 0 {
				diff = defaultMinDifficulty
			}
		}
		if diff > 0 {
			mc.setDifficulty(diff)
		}
	}

	// First job always has clean_jobs=true so the miner starts fresh.
	if job := mc.jobMgr.CurrentJob(); job != nil {
		mc.sendNotifyFor(job, true)
	}
}

// currentReadTimeout returns a dynamic read timeout based on whether the
// miner has proven itself by submitting accepted shares. New/idle
// connections get a short timeout to protect against floods; once a miner
// has submitted a few valid shares we switch to the configured, longer
// timeout.
func (mc *MinerConn) currentReadTimeout() time.Duration {
	base := mc.cfg.ConnectionTimeout
	if base <= 0 {
		base = defaultConnectionTimeout
	}

	mc.statsMu.Lock()
	accepted := mc.stats.Accepted
	mc.statsMu.Unlock()

	if accepted < 3 {
		return initialReadTimeout
	}
	return base
}

func (mc *MinerConn) listenJobs() {
	defer func() {
		if r := recover(); r != nil {
			logger.Error("listenJobs panic recovered", "remote", mc.id, "panic", r)
			// Restart the listener after a brief delay to avoid tight panic loops
			time.Sleep(100 * time.Millisecond)
			go mc.listenJobs()
		}
	}()

	for job := range mc.jobCh {
		mc.sendNotifyFor(job, false)
	}
}
