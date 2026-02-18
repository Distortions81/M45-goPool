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
	"strings"
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

		// Close stats channel and wait for worker to finish processing.
		// Some tests build lightweight MinerConn instances without a stats
		// worker/channel; guard those cases.
		if mc.statsUpdates != nil {
			close(mc.statsUpdates)
			mc.statsWg.Wait()
		}

		mc.statsMu.Lock()
		mc.stats.WindowStart = time.Time{}
		mc.stats.WindowAccepted = 0
		mc.stats.WindowSubmissions = 0
		mc.stats.WindowDifficulty = 0
		mc.vardiffWindowStart = time.Time{}
		mc.vardiffWindowResetAnchor = time.Time{}
		mc.vardiffWindowAccepted = 0
		mc.vardiffWindowSubmissions = 0
		mc.vardiffWindowDifficulty = 0
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
	if cfg.TargetSharesPerMin > 0 {
		vdiff.TargetSharesPerMin = cfg.TargetSharesPerMin
	}
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
	if cfg.ShareCheckDuplicate {
		shareCache = make(map[string]*duplicateShareSet, maxRecentJobs)
		evictedShareCache = make(map[string]*evictedCacheEntry, maxRecentJobs)
	}

	mc := &MinerConn{
		ctx:               ctx,
		id:                c.RemoteAddr().String(),
		conn:              c,
		reader:            bufio.NewReaderSize(c, maxStratumMessageSize),
		writeScratch:      make([]byte, 0, 256),
		jobMgr:            jobMgr,
		rpc:               rpc,
		cfg:               cfg,
		extranonce1:       en1,
		extranonce1Hex:    hex.EncodeToString(en1),
		jobCh:             jobCh,
		vardiff:           vdiff,
		metrics:           metrics,
		accounting:        accounting,
		workerRegistry:    workerRegistry,
		savedWorkerStore:  workerLists,
		discordNotifier:   notifier,
		activeJobs:        make(map[string]*Job, maxRecentJobs), // Pre-allocate for expected job count
		jobOrder:          make([]string, 0, maxRecentJobs),
		connectedAt:       now,
		lastActivity:      now,
		jobDifficulty:     make(map[string]float64, maxRecentJobs), // Pre-allocate for expected job count
		jobScriptTime:     make(map[string]int64, maxRecentJobs),
		jobNotifyCoinbase: make(map[string]notifiedCoinbaseParts, maxRecentJobs),
		jobNTimeBounds:    nil,
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
		workerWallets:     make(map[string]workerWalletState, 4),
	}
	if cfg.ShareCheckNTimeWindow {
		mc.jobNTimeBounds = make(map[string]jobNTimeBounds, maxRecentJobs)
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
		logger.Info("miner connected", "remote", mc.id, "extranonce1", mc.extranonce1Hex)
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

		line, err := mc.reader.ReadSlice('\n')
		now = time.Now()
		if err != nil {
			if errors.Is(err, bufio.ErrBufferFull) {
				logger.Warn("closing miner for oversized message", "remote", mc.id, "limit_bytes", maxStratumMessageSize)
				if banned, count := mc.noteProtocolViolation(now); banned {
					mc.sendClientShowMessage("Banned: " + mc.banReason)
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
		logNetMessage("recv", line)
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		mc.recordActivity(now)
		sniffedMethod, sniffedIDRaw, sniffedOK := sniffStratumMethodIDTagRawID(line)
		if mc.stratumMsgRateLimitExceeded(now, sniffedMethod) {
			banWorker := mc.workerForRateLimitBan(sniffedMethod, line)
			logger.Warn("closing miner for stratum message rate limit",
				"remote", mc.id,
				"worker", banWorker,
				"configured_limit_per_min", mc.cfg.StratumMessagesPerMinute,
				"effective_limit_per_min", mc.cfg.StratumMessagesPerMinute*stratumFloodLimitMultiplier,
			)
			mc.banFor("stratum message rate limit", time.Hour, banWorker)
			return
		}

		if sniffedOK && mc.cfg.StratumFastDecodeEnabled {
			switch sniffedMethod {
			case stratumMethodMiningPing:
				mc.writePongResponseRawID(sniffedIDRaw)
				continue
			case stratumMethodMiningAuthorize:
				// Fast-path: mining.authorize typically uses string params.
				// Avoid full JSON unmarshal on the connection goroutine.
				if params, ok := sniffStratumStringParams(line, 2); ok && len(params) > 0 {
					worker := params[0]
					pass := ""
					if len(params) > 1 {
						pass = params[1]
					}
					idVal, _, ok := parseJSONValue(sniffedIDRaw, 0)
					if ok {
						mc.handleAuthorizeID(idVal, worker, pass)
					}
					continue
				}
			case stratumMethodMiningSubscribe:
				// Fast-path: mining.subscribe only needs the request ID and (optionally)
				// a string client identifier in params[0] and optional session in params[1].
				params, ok := sniffStratumStringParams(line, 2)
				if ok {
					clientID := ""
					haveClientID := false
					sessionID := ""
					haveSessionID := false
					if len(params) > 0 {
						clientID = params[0]
						haveClientID = true
					}
					if len(params) > 1 {
						sessionID = strings.TrimSpace(params[1])
						haveSessionID = sessionID != ""
					}
					mc.handleSubscribeRawID(sniffedIDRaw, clientID, haveClientID, sessionID, haveSessionID)
					continue
				}
			case stratumMethodMiningSubmit:
				// Fast-path: most mining.submit payloads are small and string-only.
				// Avoid full JSON unmarshal on the connection goroutine to reduce
				// allocations and tail latency under load.
				worker, jobID, en2, ntime, nonce, version, haveVersion, ok := sniffStratumSubmitParamsBytes(line)
				if ok {
					idVal, _, ok := parseJSONValue(sniffedIDRaw, 0)
					if ok {
						mc.handleSubmitFastBytes(idVal, worker, jobID, en2, ntime, nonce, version, haveVersion)
					}
					continue
				}
			}
		}
		var req StratumRequest
		if err := fastJSONUnmarshal(line, &req); err != nil {
			logger.Warn("json error from miner", "remote", mc.id, "error", err)
			if banned, count := mc.noteProtocolViolation(now); banned {
				mc.sendClientShowMessage("Banned: " + mc.banReason)
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
		case "mining.set_difficulty":
			// Non-standard (pool->miner) message that some proxies/miners may
			// accidentally send to the pool. Treat it like a difficulty hint.
			mc.suggestDifficulty(&req)
		case "mining.set_target":
			// Non-standard (pool->miner) message that some proxies/miners may
			// accidentally send to the pool. Treat it like a target hint.
			mc.suggestTarget(&req)
		case "client.get_version":
			v := strings.TrimSpace(buildVersion)
			if v == "" || v == "(dev)" {
				v = "dev"
			}
			mc.writeResponse(StratumResponse{
				ID:     req.ID,
				Result: "goPool/" + v,
				Error:  nil,
			})
		case "client.ping":
			// Some software uses client.ping instead of mining.ping.
			mc.writePongResponse(req.ID)
		case "client.show_message":
			// Some software stacks send this method even though it's typically
			// a pool->miner notification. Acknowledge to avoid breaking proxies.
			mc.writeTrueResponse(req.ID)
		case "client.reconnect":
			// Some stacks treat this as a request rather than a notification.
			// Acknowledge and let the miner decide what to do.
			mc.writeTrueResponse(req.ID)
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

func (mc *MinerConn) workerForRateLimitBan(method stratumMethodTag, line []byte) string {
	if mc == nil {
		return ""
	}
	if worker := strings.TrimSpace(mc.currentWorker()); worker != "" {
		return worker
	}
	if method != stratumMethodMiningAuthorize {
		return ""
	}
	params, ok := sniffStratumStringParams(line, 1)
	if !ok || len(params) == 0 {
		return ""
	}
	return strings.TrimSpace(params[0])
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
			mc.setDifficulty(mc.startupPrimedDifficulty(diff))
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
