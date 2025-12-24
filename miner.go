package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"math/big"
	"math/bits"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	bigIntPool = sync.Pool{
		New: func() interface{} {
			return new(big.Int)
		},
	}
)

type StratumRequest struct {
	ID     interface{}   `json:"id"`
	Method string        `json:"method"`
	Params []interface{} `json:"params"`
}

type StratumResponse struct {
	ID     interface{} `json:"id"`
	Result interface{} `json:"result"`
	Error  interface{} `json:"error"`
}

func newStratumError(code int, msg string) []interface{} {
	return []interface{}{code, msg, nil}
}

type VarDiffConfig struct {
	MinDiff            float64
	MaxDiff            float64
	TargetSharesPerMin float64
	AdjustmentWindow   time.Duration
	Step               float64
	MaxBurstShares     int
	BurstWindow        time.Duration
	// DampingFactor controls how aggressively vardiff moves toward target.
	// 1.0 = full correction (old behavior), 0.5 = move halfway, etc.
	// Lower values reduce overshoot. Typical range: 0.5-0.85.
	DampingFactor float64
}

type MinerStats struct {
	Worker            string
	Accepted          int64
	Rejected          int64
	TotalDifficulty   float64
	WindowDifficulty  float64
	LastShare         time.Time
	WindowStart       time.Time
	WindowAccepted    int
	WindowSubmissions int
}

// statsUpdate represents a stats modification to be processed asynchronously
type statsUpdate struct {
	worker       string
	accepted     bool
	creditedDiff float64
	shareDiff    float64
	reason       string
	shareHash    string
	detail       *ShareDetail
	timestamp    time.Time
}

type workerWalletState struct {
	address   string
	script    []byte
	validated bool
}

var defaultVarDiff = VarDiffConfig{
	MinDiff:            defaultMinDifficulty,
	MaxDiff:            defaultMaxDifficulty,
	TargetSharesPerMin: defaultVarDiffTargetSharesPerMin, // aim for roughly one share every 12s
	AdjustmentWindow:   defaultVarDiffAdjustmentWindow,
	Step:               defaultVarDiffStep,
	MaxBurstShares:     defaultVarDiffMaxBurstShares, // throttle spammy submitters
	BurstWindow:        defaultVarDiffBurstWindow,
	DampingFactor:      defaultVarDiffDampingFactor, // move 50% toward target to reduce overshoot
}

var nextConnectionID uint64

// duplicateShareKey is a compact, comparable representation of a share
// submission used for duplicate detection. It stores a bounded prefix of
// the concatenated extranonce2, ntime, nonce, and version fields.
type duplicateShareKey struct {
	n   uint8
	buf [maxDuplicateShareKeyBytes]byte
}

type duplicateShareRing struct {
	keys  [duplicateShareHistory]duplicateShareKey
	count int
	idx   int
}

func makeDuplicateShareKey(dst *duplicateShareKey, extranonce2, ntime, nonce, versionHex string) {
	*dst = duplicateShareKey{}
	write := func(s string) {
		for i := 0; i < len(s) && int(dst.n) < maxDuplicateShareKeyBytes; i++ {
			dst.buf[dst.n] = s[i]
			dst.n++
		}
	}
	if dst.n < maxDuplicateShareKeyBytes {
		write(extranonce2)
	}
	if dst.n < maxDuplicateShareKeyBytes {
		dst.buf[dst.n] = ':'
		dst.n++
	}
	if dst.n < maxDuplicateShareKeyBytes {
		write(ntime)
	}
	if dst.n < maxDuplicateShareKeyBytes {
		dst.buf[dst.n] = ':'
		dst.n++
	}
	if dst.n < maxDuplicateShareKeyBytes {
		write(nonce)
	}
	if dst.n < maxDuplicateShareKeyBytes {
		dst.buf[dst.n] = ':'
		dst.n++
	}
	if dst.n < maxDuplicateShareKeyBytes {
		write(versionHex)
	}
}

// seenOrAdd reports whether key has already been seen in the ring, and
// records it if not. It maintains up to duplicateShareHistory entries and
// overwrites the oldest when full.
func (r *duplicateShareRing) seenOrAdd(key duplicateShareKey) bool {
	for i := 0; i < r.count; i++ {
		if r.keys[i] == key {
			return true
		}
	}
	if r.count < duplicateShareHistory {
		r.keys[r.count] = key
		r.count++
		return false
	}
	r.keys[r.idx] = key
	r.idx++
	if r.idx >= duplicateShareHistory {
		r.idx = 0
	}
	return false
}

type MinerConn struct {
	id                  string
	ctx                 context.Context
	conn                net.Conn
	writer              *bufio.Writer
	reader              *bufio.Reader
	jobMgr              *JobManager
	rpc                 rpcCaller
	cfg                 Config
	extranonce1         []byte
	jobCh               chan *Job
	difficulty          float64
	previousDifficulty  float64
	shareTarget         *big.Int
	diffMu              sync.Mutex
	lastDiffChange      time.Time
	stateMu             sync.Mutex
	listenerOn   bool
	stats        MinerStats
	statsMu      sync.Mutex
	statsUpdates chan statsUpdate // Buffered channel for async stats updates
	statsWg      sync.WaitGroup   // Wait for stats worker to finish
	vardiff      VarDiffConfig
	metrics             *PoolMetrics
	accounting          *AccountStore
	workerRegistry      *workerConnectionRegistry
	registeredWorker    string
	jobMu               sync.Mutex
	activeJobs          map[string]*Job
	jobOrder            []string
	maxRecentJobs       int
	shareCache          map[string]*duplicateShareRing
	lastJob             *Job
	lastClean           bool
	banUntil            time.Time
	banReason           string
	lastPenalty         time.Time
	invalidSubs         int
	lastProtoViolation  time.Time
	protoViolations     int
	versionRoll         bool
	versionMask         uint32
	poolMask            uint32
	minerMask           uint32
	minVerBits          int
	lastShareHash       string
	lastShareAccepted   bool
	lastShareDifficulty float64
	lastShareDetail     *ShareDetail
	lastRejectReason    string
	walletMu            sync.Mutex
	workerWallets       map[string]workerWalletState
	subscribed          bool
	authorized          bool
	cleanupOnce         sync.Once
	// If true, VarDiff adjustments are disabled for this miner and the
	// current difficulty is treated as fixed (typically from suggest_difficulty).
	lockDifficulty bool
	// bootstrapDone tracks whether we've already performed the initial
	// "bootstrap" vardiff move for this connection.
	bootstrapDone bool
	// restoredRecentDiff is set when we restore a worker's persisted
	// difficulty after a short disconnect so we can skip bootstrap and
	// suggested-difficulty overrides on reconnect.
	restoredRecentDiff bool
	minerType          string
	minerClientName    string
	minerClientVersion string
	// connectedAt is the time this miner connection was established,
	// used as the zero point for per-share timing in detail logs.
	connectedAt time.Time
	// lastActivity tracks when we last saw a RPC message from this miner.
	lastActivity time.Time
	// lastHashrateUpdate tracks the last time we updated the per-connection
	// hashrate EMA so we can apply a time-based decay between shares.
	lastHashrateUpdate time.Time
	// hashrateSampleCount counts how many shares have been recorded since the
	// last EMA update so we can ensure the window spans enough work.
	hashrateSampleCount int
	// hashrateAccumulatedDiff accumulates credited difficulties between samples.
	hashrateAccumulatedDiff float64
	// jobDifficulty records the difficulty in effect when each job notify
	// was sent to this miner so we can credit shares with the assigned
	// target even if vardiff changes before the share arrives.
	jobDifficulty map[string]float64
	// rollingHashrateValue holds the current EMA-smoothed hashrate estimate
	// for this connection, derived from accepted work over time.
	rollingHashrateValue float64
	// isTLSConnection tracks whether this miner connected over the TLS listener.
	isTLSConnection bool
	connectionSeq   uint64
}

type rpcCaller interface {
	call(method string, params interface{}, out interface{}) error
	callCtx(ctx context.Context, method string, params interface{}, out interface{}) error
}

// submitBlockWithFastRetry aggressively retries submitblock without backoff
// to maximize the chance of winning the propagation race. It retries every
// 100ms until either submitblock succeeds, a newer job height is observed,
// or a safety window elapses.
func (mc *MinerConn) submitBlockWithFastRetry(job *Job, workerName, hashHex, blockHex string, submitRes *interface{}) error {
	const (
		retryInterval = 100 * time.Millisecond
		// maxRetryWindow is a final safety cap; in practice we expect to
		// stop much sooner when a new block is seen. Using a full block
		// interval keeps us racing hard for rare finds.
		maxRetryWindow = 10 * time.Minute
	)

	start := time.Now()
	attempt := 0
	var lastErr error

	for {
		attempt++
		err := mc.rpc.call("submitblock", []interface{}{blockHex}, submitRes)
		if err == nil {
			if attempt > 1 {
				logger.Info("submitblock succeeded after retries",
					"attempts", attempt,
					"worker", mc.minerName(workerName),
					"hash", hashHex,
				)
			}
			return nil
		}
		lastErr = err

		// Log the first failure loudly; subsequent failures are summarized
		// when we eventually give up.
		if attempt == 1 {
			logger.Error("submitblock error; retrying aggressively",
				"error", err,
				"worker", mc.minerName(workerName),
				"hash", hashHex,
			)
		}

		// If we've already seen a newer template height, there's no point
		// continuing to spam submitblock for this block.
		if mc.jobMgr != nil && job != nil {
			if cur := mc.jobMgr.CurrentJob(); cur != nil && cur.Template.Height > job.Template.Height {
				logger.Warn("submitblock giving up after new block seen",
					"original_height", job.Template.Height,
					"current_height", cur.Template.Height,
					"attempts", attempt,
					"error", err,
				)
				return err
			}
		}

		// Safety stop: avoid spinning forever if the node is persistently
		// unreachable or rejects the block.
		if time.Since(start) >= maxRetryWindow {
			logger.Error("submitblock giving up after retry window",
				"attempts", attempt,
				"duration", time.Since(start),
				"error", lastErr,
			)
			return lastErr
		}

		time.Sleep(retryInterval)
	}
}

// statsWorker processes stats updates asynchronously from a buffered channel.
// This eliminates lock contention on the hot path (share submission).
func (mc *MinerConn) statsWorker() {
	defer mc.statsWg.Done()

	for update := range mc.statsUpdates {
		mc.statsMu.Lock()
		mc.ensureWindowLocked(update.timestamp)

		if update.worker != "" {
			mc.stats.Worker = update.worker
		}

		mc.stats.WindowSubmissions++
		if update.accepted {
			mc.stats.Accepted++
			mc.stats.WindowAccepted++
			if update.creditedDiff >= 0 {
				mc.stats.TotalDifficulty += update.creditedDiff
				mc.stats.WindowDifficulty += update.creditedDiff
				mc.updateHashrateLocked(update.creditedDiff, update.timestamp)
			}
		} else {
			mc.stats.Rejected++
		}
		mc.stats.LastShare = update.timestamp

		mc.lastShareHash = update.shareHash
		mc.lastShareAccepted = update.accepted
		mc.lastShareDifficulty = update.shareDiff
		mc.lastShareDetail = update.detail
		if !update.accepted && update.reason != "" {
			mc.lastRejectReason = update.reason
		}
		mc.statsMu.Unlock()
	}
}

func (mc *MinerConn) cleanup() {
	mc.cleanupOnce.Do(func() {
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

// workerPayoutScript returns the cached payout script for a worker, if any.
// This is populated during wallet validation and will be used in a future
// dual-payout coinbase layout.
func (mc *MinerConn) workerPayoutScript(worker string) []byte {
	if worker == "" {
		return nil
	}
	_, script, ok := mc.workerWalletData(worker)
	if !ok {
		return nil
	}
	return script
}

func workerBaseAddress(worker string) string {
	raw := strings.TrimSpace(worker)
	if raw == "" {
		return ""
	}
	if parts := strings.SplitN(raw, ".", 2); len(parts) > 1 {
		raw = parts[0]
	}
	return sanitizePayoutAddress(raw)
}

func cloneBytes(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	c := make([]byte, len(b))
	copy(c, b)
	return c
}

func (mc *MinerConn) workerWalletData(worker string) (string, []byte, bool) {
	if worker == "" {
		return "", nil, false
	}
	mc.walletMu.Lock()
	defer mc.walletMu.Unlock()
	info, ok := mc.workerWallets[worker]
	if !ok || !info.validated {
		return "", nil, false
	}
	return info.address, cloneBytes(info.script), true
}

func (mc *MinerConn) setWorkerWallet(worker, addr string, script []byte) {
	if worker == "" || addr == "" || len(script) == 0 {
		return
	}
	mc.walletMu.Lock()
	if mc.workerWallets == nil {
		mc.workerWallets = make(map[string]workerWalletState)
	}
	mc.workerWallets[worker] = workerWalletState{
		address:   addr,
		script:    cloneBytes(script),
		validated: true,
	}
	mc.walletMu.Unlock()
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

func (mc *MinerConn) ensureWorkerWallet(worker string) (string, []byte, bool) {
	if worker == "" {
		return "", nil, false
	}
	if addr, script, ok := mc.workerWalletData(worker); ok {
		return addr, script, true
	}
	base := workerBaseAddress(worker)
	if base == "" {
		return "", nil, false
	}
	script, err := scriptForAddress(base, ChainParams())
	if err != nil {
		return "", nil, false
	}
	mc.setWorkerWallet(worker, base, script)
	return base, cloneBytes(script), true
}

func (mc *MinerConn) registerWorker(worker string) *MinerConn {
	if worker == "" || mc.workerRegistry == nil {
		return nil
	}
	if mc.registeredWorker == worker {
		return nil
	}
	if mc.registeredWorker != "" {
		mc.workerRegistry.unregister(mc.registeredWorker, mc)
	}
	prev := mc.workerRegistry.register(worker, mc)
	mc.registeredWorker = worker
	return prev
}

func (mc *MinerConn) unregisterRegisteredWorker() {
	if mc.workerRegistry == nil || mc.registeredWorker == "" {
		return
	}
	mc.workerRegistry.unregister(mc.registeredWorker, mc)
	mc.registeredWorker = ""
}

// dualPayoutParams returns the pool and worker payout scripts and fee
// parameters for a job, if all required pieces are available. It does not
// mutate the Job; callers use the returned values with
// buildDualPayoutCoinbaseParts when constructing coinbase data in the
// dual-payout path.
// Returns false (single-payout) when:
// - Pool fee is 0% (entire reward goes to worker)
// - Worker wallet matches pool wallet (same beneficiary)
// - Worker has no valid payout script
func (mc *MinerConn) dualPayoutParams(job *Job, worker string) (poolScript []byte, workerScript []byte, totalValue int64, feePercent float64, ok bool) {
	if job == nil || job.CoinbaseValue <= 0 {
		return nil, nil, 0, 0, false
	}
	if len(job.PayoutScript) == 0 {
		return nil, nil, 0, 0, false
	}
	// If the pool fee is 0%, there's no need for dual-payout since the entire
	// block reward goes to the worker. Use single-output coinbase.
	if mc.cfg.PoolFeePercent <= 0 {
		return nil, nil, 0, 0, false
	}
	// If the worker's wallet address is the same as the pool payout address,
	// there is no benefit to building a dual-payout coinbase; treat it as a
	// single-output payout to that address.
	rawAddr := strings.TrimSpace(worker)
	if rawAddr != "" {
		if parts := strings.SplitN(rawAddr, ".", 2); len(parts) > 1 {
			rawAddr = parts[0]
		}
	}
	baseAddr := sanitizePayoutAddress(rawAddr)
	if baseAddr != "" && strings.EqualFold(baseAddr, mc.cfg.PayoutAddress) {
		return nil, nil, 0, 0, false
	}

	ws := mc.workerPayoutScript(worker)
	if len(ws) == 0 {
		return nil, nil, 0, 0, false
	}
	return job.PayoutScript, ws, job.CoinbaseValue, mc.cfg.PoolFeePercent, true
}

func NewMinerConn(ctx context.Context, c net.Conn, jobMgr *JobManager, rpc rpcCaller, cfg Config, metrics *PoolMetrics, accounting *AccountStore, workerRegistry *workerConnectionRegistry, isTLS bool) *MinerConn {
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
	if cfg.MinDifficulty > 0 {
		vdiff.MinDiff = cfg.MinDifficulty
	}
	if cfg.MaxDifficulty > 0 && cfg.MaxDifficulty < vdiff.MaxDiff {
		vdiff.MaxDiff = cfg.MaxDifficulty
	}
	if vdiff.MinDiff > vdiff.MaxDiff {
		vdiff.MinDiff = vdiff.MaxDiff
	}

	initialDiff := 1.0
	if cfg.MinDifficulty > initialDiff {
		initialDiff = cfg.MinDifficulty
	}

	mc := &MinerConn{
		ctx:             ctx,
		id:              c.RemoteAddr().String(),
		conn:            c,
		writer:          bufio.NewWriter(c),
		reader:          bufio.NewReaderSize(c, maxStratumMessageSize),
		jobMgr:          jobMgr,
		rpc:             rpc,
		cfg:             cfg,
		extranonce1:     en1,
		jobCh:           jobCh,
		difficulty:      initialDiff,
		shareTarget:     targetFromDifficulty(initialDiff),
		vardiff:         vdiff,
		metrics:         metrics,
		accounting:      accounting,
		workerRegistry:  workerRegistry,
		activeJobs:      make(map[string]*Job),
		connectedAt:     now,
		lastActivity:    now,
		jobDifficulty:   make(map[string]float64),
		shareCache:      make(map[string]*duplicateShareRing),
		maxRecentJobs:   maxRecentJobs,
		lastPenalty:     time.Now(),
		versionRoll:     false,
		versionMask:     0,
		poolMask:        mask,
		minerMask:       0,
		minVerBits:      minBits,
		bootstrapDone:   false,
		isTLSConnection: isTLS,
		statsUpdates:    make(chan statsUpdate, 1000), // Buffered for up to 1000 pending stats updates
	}

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
		deadline := now.Add(mc.currentReadTimeout(now))
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
				logger.Warn("closing miner for read timeout", "remote", mc.id)
				return
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
		default:
			logger.Warn("unknown stratum method", "remote", mc.id, "method", req.Method)
			if banned, count := mc.noteProtocolViolation(now); banned {
				mc.logBan("unknown stratum method", mc.currentWorker(), count)
			}
			mc.writeResponse(StratumResponse{
				ID:     req.ID,
				Result: nil,
				Error:  newStratumError(20, "Not supported."),
			})
			return
		}

	}
}

func (mc *MinerConn) writeJSON(v interface{}) error {
	if err := mc.conn.SetWriteDeadline(time.Now().Add(stratumWriteTimeout)); err != nil {
		return err
	}
	b, err := fastJSONMarshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	logNetMessage("send", b)
	if _, err := mc.writer.Write(b); err != nil {
		return err
	}
	return mc.writer.Flush()
}

// currentReadTimeout returns a dynamic read timeout based on whether the
// miner has proven itself by submitting accepted shares. New/idle
// connections get a short timeout to protect against floods; once a miner
// has submitted a few valid shares we switch to the configured, longer
// timeout.
func (mc *MinerConn) currentReadTimeout(now time.Time) time.Duration {
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

func (mc *MinerConn) writeResponse(resp StratumResponse) {
	if err := mc.writeJSON(resp); err != nil {
		logger.Error("write error", "remote", mc.id, "error", err)
	}
}

func (mc *MinerConn) listenJobs() {
	for job := range mc.jobCh {
		mc.sendNotifyFor(job, false)
	}
}

func (mc *MinerConn) minerName(fallback string) string {
	mc.statsMu.Lock()
	worker := mc.stats.Worker
	mc.statsMu.Unlock()
	if worker != "" {
		return worker
	}
	if fallback != "" {
		return fallback
	}
	return mc.id
}

func (mc *MinerConn) currentWorker() string {
	mc.statsMu.Lock()
	defer mc.statsMu.Unlock()
	return mc.stats.Worker
}

func (mc *MinerConn) updateWorker(worker string) string {
	if worker == "" {
		return mc.minerName("")
	}
	mc.statsMu.Lock()
	if mc.stats.Worker != worker {
		mc.stats.Worker = worker
	}
	mc.statsMu.Unlock()
	return worker
}

func (mc *MinerConn) ensureWindowLocked(now time.Time) {
	if mc.stats.WindowStart.IsZero() {
		mc.stats.WindowStart = now
		mc.stats.WindowDifficulty = 0
		return
	}
	if now.Sub(mc.stats.WindowStart) > mc.vardiff.AdjustmentWindow*2 {
		mc.stats.WindowStart = now
		mc.stats.WindowAccepted = 0
		mc.stats.WindowSubmissions = 0
		mc.stats.WindowDifficulty = 0
	}
}

// recordShare updates accounting for a submitted share. creditedDiff is the
// target difficulty we assigned for this share (used for hashrate), while
// shareDiff is the difficulty implied by the submitted hash (used for
// display/detail). They may differ when vardiff changed between notify and
// submit; we always want hashrate to use the assigned target.
func (mc *MinerConn) recordShare(worker string, accepted bool, creditedDiff float64, shareDiff float64, reason string, shareHash string, detail *ShareDetail, now time.Time) {
	// Send update to async stats worker instead of blocking on mutex
	update := statsUpdate{
		worker:       worker,
		accepted:     accepted,
		creditedDiff: creditedDiff,
		shareDiff:    shareDiff,
		reason:       reason,
		shareHash:    shareHash,
		detail:       detail,
		timestamp:    now,
	}

	select {
	case mc.statsUpdates <- update:
		// Successfully queued for async processing
	default:
		// Channel full, process synchronously as fallback
		mc.recordShareSync(update)
	}

	if mc.metrics != nil {
		mc.metrics.RecordShare(accepted, reason)
	}
}

// recordShareSync is the fallback synchronous stats update (only when channel is full)
func (mc *MinerConn) recordShareSync(update statsUpdate) {
	mc.statsMu.Lock()
	mc.ensureWindowLocked(update.timestamp)
	if update.worker != "" {
		mc.stats.Worker = update.worker
	}
	mc.stats.WindowSubmissions++
	if update.accepted {
		mc.stats.Accepted++
		mc.stats.WindowAccepted++
		if update.creditedDiff >= 0 {
			mc.stats.TotalDifficulty += update.creditedDiff
			mc.stats.WindowDifficulty += update.creditedDiff
			mc.updateHashrateLocked(update.creditedDiff, update.timestamp)
		}
	} else {
		mc.stats.Rejected++
	}
	mc.stats.LastShare = update.timestamp

	mc.lastShareHash = update.shareHash
	mc.lastShareAccepted = update.accepted
	mc.lastShareDifficulty = update.shareDiff
	mc.lastShareDetail = update.detail
	if !update.accepted && update.reason != "" {
		mc.lastRejectReason = update.reason
	}
	mc.statsMu.Unlock()
}

func (mc *MinerConn) trackBestShare(worker, hash string, difficulty float64, now time.Time) {
	if mc.metrics == nil {
		return
	}
	mc.metrics.TrackBestShare(worker, hash, difficulty, now)
}

func (mc *MinerConn) snapshotStats() MinerStats {
	mc.statsMu.Lock()
	defer mc.statsMu.Unlock()
	return mc.stats
}

type minerShareSnapshot struct {
	Stats               MinerStats
	RollingHashrate     float64
	LastShareHash       string
	LastShareAccepted   bool
	LastShareDifficulty float64
	LastShareDetail     *ShareDetail
	LastReject          string
}

func (mc *MinerConn) snapshotShareInfo() minerShareSnapshot {
	mc.statsMu.Lock()
	defer mc.statsMu.Unlock()
	return minerShareSnapshot{
		Stats:               mc.stats,
		RollingHashrate:     mc.rollingHashrateValue,
		LastShareHash:       mc.lastShareHash,
		LastShareAccepted:   mc.lastShareAccepted,
		LastShareDifficulty: mc.lastShareDifficulty,
		LastShareDetail:     mc.lastShareDetail,
		LastReject:          mc.lastRejectReason,
	}
}

func (mc *MinerConn) isBanned(now time.Time) bool {
	mc.stateMu.Lock()
	defer mc.stateMu.Unlock()
	return now.Before(mc.banUntil)
}

func (mc *MinerConn) banDetails() (time.Time, string, int) {
	mc.stateMu.Lock()
	defer mc.stateMu.Unlock()
	return mc.banUntil, mc.banReason, mc.invalidSubs
}

func (mc *MinerConn) logBan(reason, worker string, invalidSubs int) {
	until, banReason, _ := mc.banDetails()
	if banReason == "" {
		banReason = reason
	}
	if mc.accounting != nil && worker != "" {
		mc.accounting.MarkBan(worker, until, banReason)
	}
	logger.Warn("miner banned",
		"miner", mc.minerName(worker),
		"remote", mc.id,
		"reason", banReason,
		"ban_until", until,
		"invalid_submissions", invalidSubs,
	)
}
