package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"math/bits"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/gopkg/util/logger"
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

type workerWalletState struct {
	address   string
	script    []byte
	validated bool
}

const (
	maxStratumMessageSize = 64 * 1024
	stratumWriteTimeout   = 5 * time.Minute
	ntimeFutureTolerance  = 2 * time.Hour
	defaultVersionMask    = uint32(0x1fffe000)
	// initialReadTimeout limits how long we keep a connection around
	// before it has proven itself by submitting valid shares. This helps
	// protect against floods of idle connections.
	initialReadTimeout = 10 * time.Second
	// Minimum time between difficulty changes so that shares from the
	// previous target have a chance to arrive and be covered by the
	// previous-difficulty grace logic. This caps vardiff moves for a given
	// miner so we don't thrash difficulty on every small fluctuation.
	minDiffChangeInterval = 60 * time.Second

	// Input validation limits for miner-provided fields
	maxMinerClientIDLen = 256 // mining.subscribe client identifier
	maxWorkerNameLen    = 256 // mining.authorize and submit worker name
	maxJobIDLen         = 128 // submit job_id parameter
	maxVersionHexLen    = 8   // submit version_bits parameter (4-byte hex)
)

var defaultVarDiff = VarDiffConfig{
	MinDiff:            4096,
	MaxDiff:            65536,
	TargetSharesPerMin: 5, // aim for roughly one share every 12s
	AdjustmentWindow:   90 * time.Second,
	Step:               2,
	MaxBurstShares:     60, // throttle spammy submitters
	BurstWindow:        60 * time.Second,
	DampingFactor:      0.5, // move 50% toward target to reduce overshoot
}

// duplicateShareKey is a compact, comparable representation of a share
// submission used for duplicate detection. It stores a bounded prefix of
// the concatenated extranonce2, ntime, nonce, and version fields.
const maxDuplicateShareKeyBytes = 64

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
	listenerOn          bool
	stats               MinerStats
	statsMu             sync.Mutex
	vardiff             VarDiffConfig
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
	subscribeDeadline   time.Time
	authorizeDeadline   time.Time
	authorizeTimeout    time.Duration
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
	// lastHashrateUpdate tracks the last time we updated the per-connection
	// hashrate EMA so we can apply a time-based decay between shares.
	lastHashrateUpdate time.Time
	// jobDifficulty records the difficulty in effect when each job notify
	// was sent to this miner so we can credit shares with the assigned
	// target even if vardiff changes before the share arrives.
	jobDifficulty map[string]float64
	// rollingHashrateValue holds the current EMA-smoothed hashrate estimate
	// for this connection, derived from accepted work over time.
	rollingHashrateValue float64
	// isTLSConnection tracks whether this miner connected over the TLS listener.
	isTLSConnection bool
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

func (mc *MinerConn) cleanup() {
	mc.cleanupOnce.Do(func() {
		mc.unregisterRegisteredWorker()
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
	if cfg.StratumReadTimeout <= 0 {
		cfg.StratumReadTimeout = defaultStratumReadTimeout
	}
	jobCh := jobMgr.Subscribe()
	en1 := jobMgr.NextExtranonce1()
	maxRecentJobs := cfg.MaxRecentJobs
	if maxRecentJobs <= 0 {
		maxRecentJobs = defaultRecentJobs
	}

	subDeadline := time.Time{}
	if cfg.SubscribeTimeout > 0 {
		subDeadline = time.Now().Add(cfg.SubscribeTimeout)
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

	// Start miners at difficulty 1 (like foundation pool's default of 8, but more accessible)
	// Vardiff will adjust up/down from here based on share rate
	initialDiff := 1.0
	if cfg.MinDifficulty > initialDiff {
		initialDiff = cfg.MinDifficulty
	}

	return &MinerConn{
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
		difficulty:        initialDiff,
		shareTarget:       targetFromDifficulty(initialDiff),
		vardiff:           vdiff,
		metrics:           metrics,
		accounting:        accounting,
		workerRegistry:    workerRegistry,
		activeJobs:        make(map[string]*Job),
		connectedAt:       now,
		jobDifficulty:     make(map[string]float64),
		shareCache:        make(map[string]*duplicateShareRing),
		maxRecentJobs:     maxRecentJobs,
		lastPenalty:       time.Now(),
		versionRoll:       false,
		versionMask:       0,
		poolMask:          mask,
		minerMask:         0,
		minVerBits:        minBits,
		subscribeDeadline: subDeadline,
		authorizeTimeout:  cfg.AuthorizeTimeout,
		bootstrapDone:     false,
		isTLSConnection:   isTLS,
	}
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
		if expired, reason := mc.handshakeExpired(now); expired {
			logger.Warn("closing miner for handshake timeout", "remote", mc.id, "reason", reason)
			return
		}

		deadline := now.Add(mc.currentReadTimeout(now))
		if hsDeadline, ok := mc.nextHandshakeDeadline(now); ok && hsDeadline.Before(deadline) {
			deadline = hsDeadline
		}
		if err := mc.conn.SetReadDeadline(deadline); err != nil {
			if mc.ctx.Err() != nil {
				return
			}
			logger.Error("set read deadline failed", "remote", mc.id, "error", err)
			return
		}

		line, err := mc.reader.ReadBytes('\n')
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
	base := mc.cfg.StratumReadTimeout
	if base <= 0 {
		base = defaultStratumReadTimeout
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
		mc.sendNotifyFor(job)
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
	mc.statsMu.Lock()
	mc.ensureWindowLocked(now)
	if worker != "" {
		mc.stats.Worker = worker
	}
	mc.stats.WindowSubmissions++
	if accepted {
		mc.stats.Accepted++
		mc.stats.WindowAccepted++
		if creditedDiff < 0 {
			creditedDiff = 0
		}
		mc.stats.TotalDifficulty += creditedDiff
		mc.stats.WindowDifficulty += creditedDiff
		mc.updateHashrateLocked(creditedDiff, now)
	} else {
		mc.stats.Rejected++
	}
	mc.stats.LastShare = now

	mc.lastShareHash = shareHash
	mc.lastShareAccepted = accepted
	mc.lastShareDifficulty = shareDiff
	mc.lastShareDetail = detail
	if !accepted && reason != "" {
		mc.lastRejectReason = reason
	}
	mc.statsMu.Unlock()

	if mc.metrics != nil {
		mc.metrics.RecordShare(accepted, reason)
	}
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

// validateWorkerWallet checks whether the given worker name appears to be a
// valid Bitcoin address for the current network using local address
// validation (btcsuite).
func validateWorkerWallet(_ rpcCaller, _ *AccountStore, worker string) bool {
	base := workerBaseAddress(worker)
	if base == "" {
		return false
	}
	_, err := scriptForAddress(base, ChainParams())
	return err == nil
}

func (mc *MinerConn) handshakeExpired(now time.Time) (bool, string) {
	if !mc.subscribed && !mc.subscribeDeadline.IsZero() && !now.Before(mc.subscribeDeadline) {
		return true, "subscribe timeout"
	}
	if !mc.authorized && !mc.authorizeDeadline.IsZero() && !now.Before(mc.authorizeDeadline) {
		return true, "authorize timeout"
	}
	return false, ""
}

func (mc *MinerConn) nextHandshakeDeadline(now time.Time) (time.Time, bool) {
	var deadline time.Time
	if !mc.subscribed && !mc.subscribeDeadline.IsZero() && mc.subscribeDeadline.After(now) {
		deadline = mc.subscribeDeadline
	}
	if !mc.authorized && !mc.authorizeDeadline.IsZero() && mc.authorizeDeadline.After(now) {
		if deadline.IsZero() || mc.authorizeDeadline.Before(deadline) {
			deadline = mc.authorizeDeadline
		}
	}
	if deadline.IsZero() {
		return time.Time{}, false
	}
	return deadline, true
}

// submitRejectReason classifies categories of invalid submissions. It is used
// for ban decisions while allowing human-readable reason strings to remain
// stable and centralized.
type submitRejectReason int

const (
	rejectUnknown submitRejectReason = iota
	rejectInvalidExtranonce2
	rejectInvalidNTime
	rejectInvalidNonce
	rejectInvalidCoinbase
	rejectInvalidMerkle
	rejectInvalidVersion
	rejectInvalidVersionMask
	rejectInsufficientVersionBits
	rejectStaleJob
	rejectDuplicateShare
	rejectLowDiff
)

func (r submitRejectReason) String() string {
	switch r {
	case rejectInvalidExtranonce2:
		return "invalid extranonce2"
	case rejectInvalidNTime:
		return "invalid ntime"
	case rejectInvalidNonce:
		return "invalid nonce"
	case rejectInvalidCoinbase:
		return "invalid coinbase"
	case rejectInvalidMerkle:
		return "invalid merkle"
	case rejectInvalidVersion:
		return "invalid version"
	case rejectInvalidVersionMask:
		return "invalid version mask"
	case rejectInsufficientVersionBits:
		return "insufficient version bits"
	case rejectStaleJob:
		return "stale job"
	case rejectDuplicateShare:
		return "duplicate share"
	case rejectLowDiff:
		return "lowDiff"
	default:
		return "unknown"
	}
}

func (mc *MinerConn) noteInvalidSubmit(now time.Time, reason submitRejectReason) (bool, int) {
	mc.stateMu.Lock()
	defer mc.stateMu.Unlock()
	// Determine the window for counting invalid submissions. Prefer the
	// explicit ban window from config when set, otherwise fall back to the
	// VarDiff burst window, and finally a 60s default.
	window := mc.cfg.BanInvalidSubmissionsWindow
	if window <= 0 {
		window = mc.vardiff.BurstWindow
		if window <= 0 {
			window = time.Minute
		}
	}
	if now.Sub(mc.lastPenalty) > window {
		mc.invalidSubs = 0
	}
	mc.lastPenalty = now

	// Only treat clearly bogus submissions as ban-eligible. Normal mining
	// behavior like low-difficulty shares, stale jobs, or the occasional
	// duplicate share should never trigger a ban.
	switch reason {
	case rejectInvalidExtranonce2,
		rejectInvalidNTime,
		rejectInvalidNonce,
		rejectInvalidCoinbase,
		rejectInvalidMerkle,
		rejectInvalidVersion,
		rejectInvalidVersionMask,
		rejectInsufficientVersionBits:
		mc.invalidSubs++
	default:
		// Track the last penalty time but don't increment the ban counter.
		return false, mc.invalidSubs
	}

	// Require a burst of bad submissions in a short window before banning.
	threshold := mc.cfg.BanInvalidSubmissionsAfter
	if threshold <= 0 {
		threshold = mc.vardiff.MaxBurstShares
		if threshold <= 0 {
			threshold = 60
		}
	}
	if mc.invalidSubs >= threshold {
		banDuration := mc.cfg.BanInvalidSubmissionsDuration
		if banDuration <= 0 {
			banDuration = 15 * time.Minute
		}
		mc.banUntil = now.Add(banDuration)
		if mc.banReason == "" {
			mc.banReason = fmt.Sprintf("too many invalid submissions (%d in %s)", mc.invalidSubs, window)
		}
		return true, mc.invalidSubs
	}
	return false, mc.invalidSubs
}

// noteProtocolViolation tracks protocol-level misbehavior (invalid JSON,
// oversized messages, unknown methods, etc.) and decides whether to ban the
// worker based on configurable thresholds. When BanProtocolViolationsAfter
// is zero, protocol bans are disabled.
func (mc *MinerConn) noteProtocolViolation(now time.Time) (bool, int) {
	mc.stateMu.Lock()
	defer mc.stateMu.Unlock()

	threshold := mc.cfg.BanInvalidSubmissionsAfter
	if threshold <= 0 {
		// When explicit protocol thresholds are not set, reuse the
		// invalid-submission threshold for simplicity.
		return false, mc.protoViolations
	}

	window := mc.cfg.BanInvalidSubmissionsWindow
	if window <= 0 {
		window = time.Minute
	}
	if now.Sub(mc.lastProtoViolation) > window {
		mc.protoViolations = 0
	}
	mc.lastProtoViolation = now
	mc.protoViolations++

	if mc.protoViolations >= threshold {
		banDuration := mc.cfg.BanInvalidSubmissionsDuration
		if banDuration <= 0 {
			banDuration = 15 * time.Minute
		}
		mc.banUntil = now.Add(banDuration)
		if mc.banReason == "" {
			mc.banReason = fmt.Sprintf("too many protocol violations (%d in %s)", mc.protoViolations, window)
		}
		return true, mc.protoViolations
	}
	return false, mc.protoViolations
}

// rejectShareWithBan records a rejected share, updates invalid-submission
// counters, and either bans the worker or returns a typed Stratum error
// depending on recent behavior. It centralizes the common pattern used for
// clearly invalid submissions (bad extranonce, ntime, nonce, etc.).
func (mc *MinerConn) rejectShareWithBan(req *StratumRequest, workerName string, reason submitRejectReason, errCode int, errMsg string, now time.Time) {
	reasonText := reason.String()
	mc.recordShare(workerName, false, 0, 0, reasonText, "", nil, now)
	if banned, invalids := mc.noteInvalidSubmit(now, reason); banned {
		mc.logBan(reasonText, workerName, invalids)
		mc.writeResponse(StratumResponse{
			ID:     req.ID,
			Result: false,
			Error:  newStratumError(24, "banned"),
		})
		return
	}
	mc.writeResponse(StratumResponse{
		ID:     req.ID,
		Result: false,
		Error:  newStratumError(errCode, errMsg),
	})
}

func (mc *MinerConn) shareRates(now time.Time) (float64, float64) {
	mc.statsMu.Lock()
	defer mc.statsMu.Unlock()
	if mc.stats.WindowStart.IsZero() {
		return 0, 0
	}
	window := now.Sub(mc.stats.WindowStart)
	if window <= 0 {
		return 0, 0
	}
	accRate := float64(mc.stats.WindowAccepted) / window.Minutes()
	subRate := float64(mc.stats.WindowSubmissions) / window.Minutes()
	return accRate, subRate
}

func (mc *MinerConn) currentDifficulty() float64 {
	mc.diffMu.Lock()
	defer mc.diffMu.Unlock()
	return mc.difficulty
}

func (mc *MinerConn) currentShareTarget() *big.Int {
	mc.diffMu.Lock()
	defer mc.diffMu.Unlock()
	if mc.shareTarget == nil || mc.shareTarget.Sign() <= 0 {
		return nil
	}
	return new(big.Int).Set(mc.shareTarget)
}

func (mc *MinerConn) shareTargetOrDefault() *big.Int {
	target := mc.currentShareTarget()
	if target != nil {
		return target
	}
	// Fall back to the pool minimum difficulty.
	fallback := targetFromDifficulty(mc.cfg.MinDifficulty)
	mc.diffMu.Lock()
	if mc.shareTarget == nil || mc.shareTarget.Sign() <= 0 {
		mc.shareTarget = new(big.Int).Set(fallback)
	}
	mc.diffMu.Unlock()
	return fallback
}

func (mc *MinerConn) resetShareWindow(now time.Time) {
	mc.statsMu.Lock()
	mc.stats.WindowStart = now
	mc.stats.WindowAccepted = 0
	mc.stats.WindowSubmissions = 0
	mc.stats.WindowDifficulty = 0
	mc.lastHashrateUpdate = time.Time{}
	mc.rollingHashrateValue = 0
	mc.statsMu.Unlock()
}

// currentRollingHashrate returns the smoothed hashrate value derived from
// recent shares. It is safe to call from other goroutines.
func (mc *MinerConn) currentRollingHashrate() float64 {
	mc.statsMu.Lock()
	defer mc.statsMu.Unlock()
	return mc.rollingHashrateValue
}

// updateHashrateLocked updates the per-connection hashrate using a simple
// exponential moving average (EMA) over time. It expects statsMu to be held
// by the caller.
func (mc *MinerConn) updateHashrateLocked(targetDiff float64, shareTime time.Time) {
	if targetDiff <= 0 || shareTime.IsZero() {
		return
	}

	// Derive an instantaneous hashrate sample from this share. We treat the
	// interval since the last hashrate update as the sample window.
	if mc.lastHashrateUpdate.IsZero() {
		// First share: just record the timestamp so the next share can form
		// a meaningful interval.
		mc.lastHashrateUpdate = shareTime
		return
	}
	elapsed := shareTime.Sub(mc.lastHashrateUpdate).Seconds()
	if elapsed <= 0 {
		return
	}
	sample := (targetDiff * hashPerShare) / elapsed

	// Apply an EMA with a configurable time constant so that hashrate responds
	// quickly to changes but decays smoothly when shares slow down.
	tauSeconds := mc.cfg.HashrateEMATauSeconds
	if tauSeconds <= 0 {
		tauSeconds = defaultHashrateEMATauSeconds
	}
	alpha := 1 - math.Exp(-elapsed/tauSeconds)
	if alpha < 0 {
		alpha = 0
	}
	if alpha > 1 {
		alpha = 1
	}

	if mc.rollingHashrateValue <= 0 {
		mc.rollingHashrateValue = sample
	} else {
		mc.rollingHashrateValue = mc.rollingHashrateValue + alpha*(sample-mc.rollingHashrateValue)
	}
	mc.lastHashrateUpdate = shareTime
}

func (mc *MinerConn) trackJob(job *Job, clean bool) {
	mc.jobMu.Lock()
	defer mc.jobMu.Unlock()
	if clean {
		mc.activeJobs = make(map[string]*Job)
		mc.shareCache = make(map[string]*duplicateShareRing)
		mc.jobOrder = mc.jobOrder[:0]
		mc.jobDifficulty = make(map[string]float64)
	}
	if _, ok := mc.activeJobs[job.JobID]; !ok {
		mc.jobOrder = append(mc.jobOrder, job.JobID)
	}
	mc.activeJobs[job.JobID] = job
	mc.lastJob = job
	mc.lastClean = clean

	for len(mc.jobOrder) > mc.maxRecentJobs {
		oldest := mc.jobOrder[0]
		mc.jobOrder = mc.jobOrder[1:]
		delete(mc.activeJobs, oldest)
		delete(mc.shareCache, oldest)
		delete(mc.jobDifficulty, oldest)
	}
}

func (mc *MinerConn) jobForID(jobID string) (*Job, bool) {
	mc.jobMu.Lock()
	defer mc.jobMu.Unlock()
	job, ok := mc.activeJobs[jobID]
	return job, ok
}

func (mc *MinerConn) setJobDifficulty(jobID string, diff float64) {
	if jobID == "" || diff <= 0 {
		return
	}
	mc.jobMu.Lock()
	if mc.jobDifficulty == nil {
		mc.jobDifficulty = make(map[string]float64)
	}
	mc.jobDifficulty[jobID] = diff
	mc.jobMu.Unlock()
}

// assignedDifficulty returns the difficulty we assigned when the job was
// sent to the miner. Falls back to currentDifficulty if unknown.
func (mc *MinerConn) assignedDifficulty(jobID string) float64 {
	curDiff := mc.currentDifficulty()
	if jobID == "" {
		return curDiff
	}
	mc.jobMu.Lock()
	diff, ok := mc.jobDifficulty[jobID]
	mc.jobMu.Unlock()
	if ok && diff > 0 {
		return diff
	}

	return curDiff
}

func (mc *MinerConn) cleanFlagFor(job *Job) bool {
	mc.jobMu.Lock()
	defer mc.jobMu.Unlock()
	if mc.lastJob == nil {
		return true
	}
	return mc.lastJob.Template.Previous != job.Template.Previous || mc.lastJob.Template.Height != job.Template.Height
}

func (mc *MinerConn) isDuplicateShare(jobID, extranonce2, ntime, nonce, versionHex string) bool {
	mc.jobMu.Lock()
	defer mc.jobMu.Unlock()
	cache := mc.shareCache[jobID]
	if cache == nil {
		cache = &duplicateShareRing{}
		mc.shareCache[jobID] = cache
	}
	var dk duplicateShareKey
	makeDuplicateShareKey(&dk, extranonce2, ntime, nonce, versionHex)
	return cache.seenOrAdd(dk)
}

func (mc *MinerConn) maybeAdjustDifficulty(now time.Time) {
	// If this connection is locked to a static difficulty, skip VarDiff.
	if mc.lockDifficulty {
		return
	}

	mc.statsMu.Lock()
	stats := mc.stats
	mc.statsMu.Unlock()

	windowStart := stats.WindowStart
	windowAccepted := stats.WindowAccepted
	windowSubmissions := stats.WindowSubmissions

	if windowSubmissions == 0 || windowStart.IsZero() {
		return
	}

	// Require a full adjustment window before making a difficulty change.
	// This prevents rapid oscillation and gives stable measurements.
	elapsed := now.Sub(windowStart)
	if elapsed < mc.vardiff.AdjustmentWindow {
		return
	}

	// Avoid making multiple vardiff moves back-to-back; give miners a short
	// grace window so shares calculated at the previous difficulty can arrive
	// and be covered by the previous-difficulty acceptance logic.
	mc.diffMu.Lock()
	lastChange := mc.lastDiffChange
	currentDiff := mc.difficulty
	mc.diffMu.Unlock()
	if !lastChange.IsZero() && now.Sub(lastChange) < minDiffChangeInterval {
		return
	}
	if windowAccepted == 0 {
		// No accepted shares in the window; nothing meaningful to base
		// an adjustment on yet.
		return
	}

	// Traditional vardiff: keep each miner near a target shares/min
	// by scaling difficulty in proportion to the observed share rate derived
	// from the EMA-smoothed hashrate shown in the web panels.
	rollingHashrate := mc.currentRollingHashrate()
	if rollingHashrate <= 0 {
		mc.resetShareWindow(now)
		return
	}
	accRate := (rollingHashrate / hashPerShare) * 60
	target := mc.vardiff.TargetSharesPerMin
	if target <= 0 {
		target = 4
	}
	if accRate <= 0 {
		// Reset the window so we do not accumulate stale time.
		mc.resetShareWindow(now)
		return
	}

	// Scale difficulty proportionally to how far the accepted share rate
	// is from the target, but clamp the change factor to avoid
	// over-correcting in a single step.
	ratio := accRate / target
	// Dead band around the target: no change if we're within ±20%.
	const band = 0.20
	if ratio >= 1-band && ratio <= 1+band {
		mc.resetShareWindow(now)
		return
	}

	// Apply damping to reduce overshoot: instead of moving all the way
	// to the ideal difficulty, move only a fraction of the distance.
	// This prevents oscillation and allows the system to converge smoothly.
	dampingFactor := mc.vardiff.DampingFactor
	if dampingFactor <= 0 || dampingFactor > 1 {
		dampingFactor = 0.75 // default: move 75% toward target
	}

	// Calculate damped adjustment ratio:
	// adjustRatio = 1 + dampingFactor * (ratio - 1)
	// Examples with dampingFactor=0.75:
	//   ratio=2.0 (too fast) → adjustRatio=1.75 (increase diff by 75% instead of 100%)
	//   ratio=0.5 (too slow) → adjustRatio=0.625 (decrease diff by 37.5% instead of 50%)
	adjustRatio := 1.0 + dampingFactor*(ratio-1.0)

	newDiff := currentDiff * adjustRatio
	if newDiff <= 0 || math.IsNaN(newDiff) || math.IsInf(newDiff, 0) {
		return
	}

	// Respect the configured step as the maximum factor of change per adjustment.
	// This limits how much difficulty can change in a single adjustment to prevent
	// overshooting and ensure gradual, stable convergence to the target share rate.
	factor := newDiff / currentDiff
	step := mc.vardiff.Step
	if step <= 1 {
		step = 2
	}
	maxFactor := step
	minFactor := 1 / step
	if factor > maxFactor {
		factor = maxFactor
	}
	if factor < minFactor {
		factor = minFactor
	}
	newDiff = currentDiff * factor

	if newDiff == 0 || math.Abs(newDiff-currentDiff) < 1e-6 {
		return
	}
	if newDiff > mc.vardiff.MaxDiff {
		newDiff = mc.vardiff.MaxDiff
	}
	if newDiff < mc.vardiff.MinDiff {
		newDiff = mc.vardiff.MinDiff
	}
	if mc.cfg.MaxDifficulty > 0 && newDiff > mc.cfg.MaxDifficulty {
		newDiff = mc.cfg.MaxDifficulty
	}

	mc.resetShareWindow(now)
	logger.Info("vardiff adjust",
		"miner", mc.minerName(""),
		"shares_per_min", accRate,
		"old_diff", currentDiff,
		"new_diff", newDiff,
	)
	if mc.metrics != nil {
		dir := "down"
		if newDiff > currentDiff {
			dir = "up"
		}
		mc.metrics.RecordVardiffMove(dir)
	}
	mc.setDifficulty(newDiff)
}

// quantizeDifficultyToPowerOfTwo snaps a difficulty value to a power-of-two
// level within [min, max] (if max > 0). This keeps stratum difficulty levels
// on clean power-of-two boundaries.
func quantizeDifficultyToPowerOfTwo(diff, min, max float64) float64 {
	if diff <= 0 {
		diff = min
	}
	if diff <= 0 {
		return diff
	}

	log2 := math.Log2(diff)
	if math.IsNaN(log2) || math.IsInf(log2, 0) {
		return diff
	}

	exp := math.Round(log2)
	cand := math.Pow(2, exp)

	// Ensure candidate lies within [min, max] by snapping up/down as needed.
	if cand < min && min > 0 {
		exp = math.Ceil(math.Log2(min))
		cand = math.Pow(2, exp)
	}
	if max > 0 && cand > max {
		exp = math.Floor(math.Log2(max))
		cand = math.Pow(2, exp)
	}

	if cand < min {
		cand = min
	}
	if max > 0 && cand > max {
		cand = max
	}
	return cand
}

func (mc *MinerConn) clampDifficulty(diff float64) float64 {
	// Determine the tightest enforceable bounds from both pool config and vardiff.
	min := mc.cfg.MinDifficulty
	if min <= 0 {
		min = mc.vardiff.MinDiff
	} else if mc.vardiff.MinDiff > min {
		min = mc.vardiff.MinDiff
	}

	max := mc.cfg.MaxDifficulty
	if max <= 0 {
		max = mc.vardiff.MaxDiff
	} else if mc.vardiff.MaxDiff < max {
		max = mc.vardiff.MaxDiff
	}

	if max > 0 && max < min {
		max = min
	}

	if diff < min {
		diff = min
	}
	if max > 0 && diff > max {
		diff = max
	}
	// Snap the final difficulty to a power-of-two level within [min, max].
	return quantizeDifficultyToPowerOfTwo(diff, min, max)
}

func (mc *MinerConn) setDifficulty(diff float64) {
	requested := diff
	diff = mc.clampDifficulty(diff)
	mc.diffMu.Lock()
	now := time.Now()
	mc.previousDifficulty = mc.difficulty // Save old difficulty before changing
	mc.difficulty = diff
	mc.shareTarget = targetFromDifficulty(diff)
	mc.lastDiffChange = now
	mc.diffMu.Unlock()

	logger.Info("set difficulty",
		"miner", mc.minerName(""),
		"requested_diff", requested,
		"clamped_diff", diff,
		"share_target", fmt.Sprintf("%064x", mc.shareTarget),
	)

	msg := map[string]interface{}{
		"id":     nil,
		"method": "mining.set_difficulty",
		"params": []interface{}{diff},
	}
	if err := mc.writeJSON(msg); err != nil {
		logger.Error("difficulty write error", "remote", mc.id, "error", err)
	}
}

func (mc *MinerConn) sendVersionMask() {
	msg := map[string]interface{}{
		"id":     nil,
		"method": "mining.set_version_mask",
		"params": []interface{}{fmt.Sprintf("%08x", mc.versionMask)},
	}
	if err := mc.writeJSON(msg); err != nil {
		logger.Error("version mask write error", "remote", mc.id, "error", err)
	}
}

func (mc *MinerConn) updateVersionMask(poolMask uint32) bool {
	changed := false
	if mc.poolMask != poolMask {
		mc.poolMask = poolMask
		changed = true
	}

	if !mc.versionRoll {
		if mc.minerMask != 0 {
			final := poolMask & mc.minerMask
			if final != 0 {
				available := bits.OnesCount32(final)
				if mc.minVerBits <= 0 {
					mc.minVerBits = 1
				}
				if mc.minVerBits > available {
					mc.minVerBits = available
					changed = true
				}
				if mc.versionMask != final {
					changed = true
				}
				mc.versionMask = final
				mc.versionRoll = true
				return changed
			}
		}
		if mc.versionMask != poolMask {
			changed = true
		}
		mc.versionMask = poolMask
		return changed
	}

	finalMask := poolMask & mc.minerMask
	if finalMask == 0 {
		if mc.versionMask != 0 {
			changed = true
		}
		mc.versionMask = 0
		mc.versionRoll = false
		return changed
	}

	available := bits.OnesCount32(finalMask)
	if mc.minVerBits > available {
		mc.minVerBits = available
		changed = true
	}
	if mc.versionMask != finalMask {
		changed = true
	}
	mc.versionMask = finalMask
	return changed
}

func (mc *MinerConn) sendSetExtranonce(ex1 string, en2Size int) {
	msg := map[string]interface{}{
		"id":     nil,
		"method": "mining.set_extranonce",
		"params": []interface{}{ex1, en2Size},
	}
	if err := mc.writeJSON(msg); err != nil {
		logger.Error("set_extranonce write error", "remote", mc.id, "error", err)
	}
}

func (mc *MinerConn) handleExtranonceSubscribe(req *StratumRequest) {
	result := StratumResponse{
		ID:     req.ID,
		Result: true,
		Error:  nil,
	}
	mc.writeResponse(result)

	ex1 := hex.EncodeToString(mc.extranonce1)
	en2Size := mc.cfg.Extranonce2Size
	if en2Size <= 0 {
		en2Size = 4
	}
	mc.sendSetExtranonce(ex1, en2Size)
}

// Handle mining.subscribe request.
// Very minimal: return fake subscription and extranonce1/size per docs/protocols/stratum-v1.mediawiki.
func (mc *MinerConn) handleSubscribe(req *StratumRequest) {
	// Many miners send a client identifier as the first subscribe parameter.
	// Capture it so we can summarize miner types on the status page.
	if len(req.Params) > 0 {
		if id, ok := req.Params[0].(string); ok {
			// Validate client ID length to prevent abuse
			if len(id) > maxMinerClientIDLen {
				logger.Warn("subscribe rejected: client identifier too long", "remote", mc.id, "len", len(id))
				mc.writeResponse(StratumResponse{
					ID:     req.ID,
					Result: nil,
					Error:  newStratumError(20, "client identifier too long"),
				})
				mc.Close("client identifier too long")
				return
			}
			if id != "" {
				mc.minerType = id
				// Best-effort split into name/version for nicer aggregation.
				name, ver := parseMinerID(id)
				if name != "" {
					mc.minerClientName = name
				}
				if ver != "" {
					mc.minerClientVersion = ver
				}
			}
		}
	}

	mc.subscribed = true
	if !mc.authorized && mc.authorizeDeadline.IsZero() && mc.authorizeTimeout > 0 {
		mc.authorizeDeadline = time.Now().Add(mc.authorizeTimeout)
	}

	// Result spec (simplified):
	// [
	//   [ ["mining.set_difficulty", "1"], ["mining.notify", "1"] ],
	//   "extranonce1",
	//   extranonce2_size
	// ]
	ex1 := hex.EncodeToString(mc.extranonce1)
	en2Size := mc.cfg.Extranonce2Size
	if en2Size <= 0 {
		en2Size = 4
	}

	var initialJob *Job
	select {
	case job := <-mc.jobCh:
		initialJob = job
	default:
		initialJob = mc.jobMgr.CurrentJob()
	}

	if !mc.listenerOn {
		mc.listenerOn = true
		go mc.listenJobs()
	}

	mc.writeResponse(StratumResponse{
		ID: req.ID,
		Result: []interface{}{
			[][]interface{}{
				{"mining.set_difficulty", "1"},
				{"mining.notify", "1"},
			},
			ex1,
			en2Size,
		},
		Error: nil,
	})
	if initialJob != nil {
		mc.updateVersionMask(initialJob.VersionMask)
	}
	mc.sendSetExtranonce(ex1, en2Size)
	if initialJob == nil {
		status := mc.jobMgr.FeedStatus()
		fields := []interface{}{"remote", mc.id, "reason", "no job available"}
		if status.LastError != nil {
			fields = append(fields, "job_error", status.LastError.Error())
		}
		if !status.LastSuccess.IsZero() {
			fields = append(fields, "last_job_at", status.LastSuccess)
		}
		args := append([]interface{}{"miner subscribed but no job ready"}, fields...)
		logger.Warn(args...)
	}
}

// Handle mining.authorize.
func (mc *MinerConn) handleAuthorize(req *StratumRequest) {
	worker := ""
	if len(req.Params) > 0 {
		worker, _ = req.Params[0].(string)
	}

	// Validate worker name length to prevent abuse
	if len(worker) == 0 {
		logger.Warn("authorize rejected: empty worker name", "remote", mc.id)
		mc.writeResponse(StratumResponse{
			ID:     req.ID,
			Result: false,
			Error:  newStratumError(20, "worker name required"),
		})
		mc.Close("empty worker name")
		return
	}
	if len(worker) > maxWorkerNameLen {
		logger.Warn("authorize rejected: worker name too long", "remote", mc.id, "len", len(worker))
		mc.writeResponse(StratumResponse{
			ID:     req.ID,
			Result: false,
			Error:  newStratumError(20, "worker name too long"),
		})
		mc.Close("worker name too long")
		return
	}

	workerName := mc.updateWorker(worker)

	// Before allowing hashing, ensure the worker name is a valid wallet-style
	// address so we can construct dual-payout coinbases. Invalid workers are
	// rejected immediately.
	if workerName != "" {
		if _, _, ok := mc.ensureWorkerWallet(workerName); !ok {
			addr := workerBaseAddress(workerName)
			if addr == "" {
				addr = "(invalid)"
			}
			logger.Warn("worker has invalid wallet-style name",
				"worker", workerName,
				"addr", addr,
			)
			resp := StratumResponse{
				ID:     req.ID,
				Result: false,
				Error:  newStratumError(20, "wallet worker validation failed"),
			}
			mc.writeResponse(resp)
			mc.Close("wallet validation failed")
			return
		}
		if prev := mc.registerWorker(workerName); prev != nil && prev != mc {
			logger.Info("closing previous connection for worker", "worker", workerName, "remote", prev.id)
			prev.Close("duplicate worker connection")
		}
	}

	// Force difficulty to the configured min on authorize so new connections
	// always start at the lowest target we allow.

	mc.authorized = true
	mc.authorizeDeadline = time.Time{}

	resp := StratumResponse{
		ID:     req.ID,
		Result: true,
		Error:  nil,
	}
	mc.writeResponse(resp)

	// Now that the worker is authorized and its wallet-style ID is known
	// to be valid, send initial difficulty and a job so hashing can start.
	if job := mc.jobMgr.CurrentJob(); job != nil {
		mc.setDifficulty(mc.vardiff.MinDiff)
		mc.sendNotifyFor(job)
	}
}

func (mc *MinerConn) suggestDifficulty(req *StratumRequest) {
	resp := StratumResponse{ID: req.ID}
	if len(req.Params) == 0 {
		resp.Error = newStratumError(20, "invalid params")
		mc.writeResponse(resp)
		return
	}

	diff, ok := parseSuggestedDifficulty(req.Params[0])
	if !ok || diff <= 0 {
		resp.Error = newStratumError(20, "invalid params")
		mc.writeResponse(resp)
		return
	}

	// Treat suggested difficulty as the miner's preferred starting point,
	// clamped to the pool's min/max difficulty settings. Vardiff will still
	// adjust difficulty up or down from this baseline over time, unless
	// lock_suggested_difficulty is enabled.
	resp.Result = true
	mc.writeResponse(resp)

	// If we just restored a recent difficulty for this worker on a short
	// reconnect, ignore suggested-difficulty overrides and keep the
	// existing difficulty so we don't fight the remembered setting.
	mc.diffMu.Lock()
	restored := mc.restoredRecentDiff
	mc.diffMu.Unlock()
	if restored {
		return
	}

	if mc.cfg.LockSuggestedDifficulty {
		// Lock this miner to the requested difficulty (within min/max).
		mc.lockDifficulty = true
	}
	mc.setDifficulty(diff)
}

func parseSuggestedDifficulty(value interface{}) (float64, bool) {
	switch v := value.(type) {
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return 0, false
		}
		return v, true
	case string:
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, false
		}
		return f, true
	case jsonNumber:
		f, err := v.Float64()
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

func (mc *MinerConn) handleConfigure(req *StratumRequest) {
	if len(req.Params) == 0 {
		mc.writeResponse(StratumResponse{ID: req.ID, Result: nil, Error: newStratumError(20, "invalid params")})
		return
	}

	rawExts, ok := req.Params[0].([]interface{})
	if !ok {
		mc.writeResponse(StratumResponse{ID: req.ID, Result: nil, Error: newStratumError(20, "invalid params")})
		return
	}
	var opts map[string]interface{}
	if len(req.Params) > 1 {
		opts, _ = req.Params[1].(map[string]interface{})
	}

	result := make(map[string]interface{})
	for _, ext := range rawExts {
		name, _ := ext.(string)
		switch name {
		case "version-rolling":
			// BIP310 version-rolling negotiation (docs/protocols/bip-0310.mediawiki).
			if mc.poolMask == 0 {
				result["version-rolling"] = false
				break
			}
			requestMask := mc.poolMask
			if opts != nil {
				if maskStr, ok := opts["version-rolling.mask"].(string); ok {
					if parsed, err := strconv.ParseUint(maskStr, 16, 32); err == nil {
						requestMask = uint32(parsed)
					}
				}
				if minBits, ok := opts["version-rolling.min-bit-count"].(float64); ok && int(minBits) > 0 {
					mc.minVerBits = int(minBits)
				}
			}
			mask := requestMask & mc.poolMask
			if mask == 0 {
				result["version-rolling"] = false
				mc.versionRoll = false
				mc.minerMask = requestMask
				mc.updateVersionMask(mc.poolMask)
				break
			}
			available := bits.OnesCount32(mask)
			if mc.minVerBits <= 0 {
				mc.minVerBits = 1
			}
			if mc.minVerBits > available {
				mc.minVerBits = available
			}
			mc.minerMask = requestMask
			mc.versionRoll = true
			mc.versionMask = mask
			result["version-rolling"] = true
			result["version-rolling.mask"] = fmt.Sprintf("%08x", mask)
			result["version-rolling.min-bit-count"] = mc.minVerBits
			mc.sendVersionMask()
		default:
			// Unknown extension; explicitly deny so miners don't retry forever.
			result[name] = false
		}
	}

	mc.writeResponse(StratumResponse{ID: req.ID, Result: result, Error: nil})
}

func (mc *MinerConn) sendNotifyFor(job *Job) {
	// Adjust difficulty when sending new jobs (new blocks), not on every share.
	// This gives miners stable difficulty for the duration of a job and prevents
	// mid-job difficulty changes that can cause confusion.
	mc.maybeAdjustDifficulty(time.Now())

	maskChanged := mc.updateVersionMask(job.VersionMask)
	if maskChanged && mc.versionRoll {
		mc.sendVersionMask()
	}

	worker := mc.currentWorker()
	var (
		coinb1 string
		coinb2 string
		err    error
	)
	if poolScript, workerScript, totalValue, feePercent, ok := mc.dualPayoutParams(job, worker); ok {
		// Check if donation is enabled and we should use triple payout
		logger.Debug("payout check", "donation_percent", job.OperatorDonationPercent, "donation_script_len", len(job.DonationScript))
		if job.OperatorDonationPercent > 0 && len(job.DonationScript) > 0 {
			logger.Info("using triple payout", "worker", worker, "donation_percent", job.OperatorDonationPercent)
			coinb1, coinb2, err = buildTriplePayoutCoinbaseParts(
				job.Template.Height,
				mc.extranonce1,
				job.Extranonce2Size,
				job.TemplateExtraNonce2Size,
				poolScript,
				job.DonationScript,
				workerScript,
				totalValue,
				feePercent,
				job.OperatorDonationPercent,
				job.WitnessCommitment,
				job.Template.CoinbaseAux.Flags,
				job.CoinbaseMsg,
				job.ScriptTime,
			)
		} else {
			coinb1, coinb2, err = buildDualPayoutCoinbaseParts(
				job.Template.Height,
				mc.extranonce1,
				job.Extranonce2Size,
				job.TemplateExtraNonce2Size,
				poolScript,
				workerScript,
				totalValue,
				feePercent,
				job.WitnessCommitment,
				job.Template.CoinbaseAux.Flags,
				job.CoinbaseMsg,
				job.ScriptTime,
			)
		}
	}
	// Fallback to single-output coinbase if any required dual-payout parameter is missing.
	if coinb1 == "" || coinb2 == "" || err != nil {
		if err != nil {
			logger.Warn("dual-payout coinbase build failed, falling back to single-output coinbase",
				"error", err,
				"worker", worker,
			)
		}
		coinb1, coinb2, err = buildCoinbaseParts(
			job.Template.Height,
			mc.extranonce1,
			job.Extranonce2Size,
			job.TemplateExtraNonce2Size,
			job.PayoutScript,
			job.CoinbaseValue,
			job.WitnessCommitment,
			job.Template.CoinbaseAux.Flags,
			job.CoinbaseMsg,
			job.ScriptTime,
		)
	}
	if err != nil {
		logger.Error("notify coinbase parts", "error", err)
		return
	}

	prevhashLE := hexToLEHex(job.PrevHash)
	shareTarget := mc.shareTargetOrDefault()

	// clean_jobs should only be true when the template actually changed (prevhash/height)
	// so miners don’t drop work on harmless re-notifies.
	cleanJobs := job.Clean && mc.cleanFlagFor(job)
	mc.trackJob(job, cleanJobs)
	mc.setJobDifficulty(job.JobID, mc.currentDifficulty())

	// Stratum notify shape per docs/protocols/stratum-v1.mediawiki:
	// [job_id, prevhash, coinb1, coinb2, merkle_branch[], version, nbits, ntime, clean_jobs].
	// Version, bits and ntime are sent as big-endian hex, matching the usual
	// Stratum pool conventions.
	versionBE := int32ToBEHex(int32(job.Template.Version))
	bitsBE := job.Template.Bits // bits is already a raw hex string, don't reverse it
	ntimeBE := uint32ToBEHex(uint32(job.Template.CurTime))

	params := []interface{}{
		job.JobID,
		prevhashLE,
		coinb1,
		coinb2,
		job.MerkleBranches,
		versionBE,
		bitsBE,
		ntimeBE,
		cleanJobs,
	}

	if debugLogging || verboseLogging {
		merkleRoot := computeMerkleRootBE(coinb1, coinb2, job.MerkleBranches)
		headerHashLE := headerHashFromNotify(prevhashLE, merkleRoot, uint32(job.Template.Version), job.Template.Bits, job.Template.CurTime)
		logger.Info("notify payload",
			"job", job.JobID,
			"prevhash", prevhashLE,
			"coinb1", coinb1,
			"coinb2", coinb2,
			"branches", job.MerkleBranches,
			"version", versionBE,
			"bits", bitsBE,
			"ntime", ntimeBE,
			"clean", cleanJobs,
			"share_target", fmt.Sprintf("%064x", shareTarget),
			"merkle_root_be", hex.EncodeToString(merkleRoot),
			"header_hash_le", hex.EncodeToString(headerHashLE),
		)
	}

	_ = mc.writeJSON(map[string]interface{}{
		"id":     nil,
		"method": "mining.notify",
		"params": params,
	})
}

// computeMerkleRootBE rebuilds the merkle root (big-endian) from coinb1/coinb2 and branches.
func computeMerkleRootBE(coinb1, coinb2 string, branches []string) []byte {
	c1, _ := hex.DecodeString(coinb1)
	c2, _ := hex.DecodeString(coinb2)
	cb := append(c1, c2...)
	txid := doubleSHA256(cb)
	return computeMerkleRootFromBranches(txid, branches)
}

// headerHashFromNotify rebuilds the block header hash (LE) from notify fields.
func headerHashFromNotify(prevhash string, merkleRoot []byte, version uint32, bits string, ntime int64) []byte {
	prev, err := hex.DecodeString(prevhash)
	if err != nil || len(prev) != 32 || len(merkleRoot) != 32 {
		return nil
	}
	bitsVal, err := strconv.ParseUint(bits, 16, 32)
	if err != nil {
		return nil
	}
	var hdr bytes.Buffer
	writeUint32LE(&hdr, version)
	hdr.Write(reverseBytes(prev))
	hdr.Write(reverseBytes(merkleRoot))
	writeUint32LE(&hdr, uint32(ntime))
	writeUint32LE(&hdr, uint32(bitsVal))
	writeUint32LE(&hdr, 0) // dummy nonce for hash preview
	h := doubleSHA256(hdr.Bytes())
	return reverseBytes(h)
}

// Handle mining.submit.
// This is where we’d check PoW and submitblock.
type submitParams struct {
	worker           string
	jobID            string
	extranonce2      string
	ntime            string
	nonce            string
	submittedVersion uint32
}

// parseSubmitParams validates and extracts the core fields from a mining.submit
// request, recording and responding to any parameter errors. It returns params
// and ok=false when a response has already been sent.
func (mc *MinerConn) parseSubmitParams(req *StratumRequest, now time.Time) (submitParams, bool) {
	var out submitParams

	if len(req.Params) < 5 || len(req.Params) > 6 {
		logger.Warn("submit invalid params", "remote", mc.id, "params", req.Params)
		mc.recordShare("", false, 0, 0, "invalid params", "", nil, now)
		mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(20, "invalid params")})
		return out, false
	}

	worker, ok := req.Params[0].(string)
	if !ok {
		mc.recordShare("", false, 0, 0, "invalid worker", "", nil, now)
		mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(20, "invalid worker")})
		return out, false
	}
	// Validate worker name length
	if len(worker) == 0 {
		mc.recordShare("", false, 0, 0, "empty worker", "", nil, now)
		mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(20, "worker name required")})
		return out, false
	}
	if len(worker) > maxWorkerNameLen {
		logger.Warn("submit rejected: worker name too long", "remote", mc.id, "len", len(worker))
		mc.recordShare("", false, 0, 0, "worker name too long", "", nil, now)
		mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(20, "worker name too long")})
		return out, false
	}

	jobID, ok := req.Params[1].(string)
	if !ok {
		mc.recordShare(worker, false, 0, 0, "invalid job id", "", nil, now)
		mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(20, "invalid job id")})
		return out, false
	}
	// Validate job ID length
	if len(jobID) == 0 {
		mc.recordShare(worker, false, 0, 0, "empty job id", "", nil, now)
		mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(20, "job id required")})
		return out, false
	}
	if len(jobID) > maxJobIDLen {
		logger.Warn("submit rejected: job id too long", "remote", mc.id, "len", len(jobID))
		mc.recordShare(worker, false, 0, 0, "job id too long", "", nil, now)
		mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(20, "job id too long")})
		return out, false
	}
	extranonce2, ok := req.Params[2].(string)
	if !ok {
		mc.recordShare(worker, false, 0, 0, "invalid extranonce2", "", nil, now)
		mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(20, "invalid extranonce2")})
		return out, false
	}
	ntime, ok := req.Params[3].(string)
	if !ok {
		mc.recordShare(worker, false, 0, 0, "invalid ntime", "", nil, now)
		mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(20, "invalid ntime")})
		return out, false
	}
	nonce, ok := req.Params[4].(string)
	if !ok {
		mc.recordShare(worker, false, 0, 0, "invalid nonce", "", nil, now)
		mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(20, "invalid nonce")})
		return out, false
	}

	submittedVersion := uint32(0)
	if len(req.Params) == 6 {
		verStr, ok := req.Params[5].(string)
		if !ok {
			mc.recordShare(worker, false, 0, 0, "invalid version", "", nil, now)
			mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(20, "invalid version")})
			return out, false
		}
		// Validate version string length (should be 8-char hex for 4-byte value)
		if len(verStr) == 0 {
			mc.recordShare(worker, false, 0, 0, "empty version", "", nil, now)
			mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(20, "version required")})
			return out, false
		}
		if len(verStr) > maxVersionHexLen {
			logger.Warn("submit rejected: version too long", "remote", mc.id, "len", len(verStr))
			mc.recordShare(worker, false, 0, 0, "version too long", "", nil, now)
			mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(20, "version too long")})
			return out, false
		}
		// Stratum submit version is encoded as big-endian hex.
		verVal, err := parseUint32BEHex(verStr)
		if err != nil {
			mc.recordShare(worker, false, 0, 0, "invalid version", "", nil, now)
			mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(20, "invalid version")})
			return out, false
		}
		submittedVersion = verVal
	}

	out.worker = worker
	out.jobID = jobID
	out.extranonce2 = extranonce2
	out.ntime = ntime
	out.nonce = nonce
	out.submittedVersion = submittedVersion
	return out, true
}

func (mc *MinerConn) handleSubmit(req *StratumRequest) {
	// Expect params like:
	// [worker_name, job_id, extranonce2, ntime, nonce]
	now := time.Now()

	params, ok := mc.parseSubmitParams(req, now)
	if !ok {
		return
	}

	worker := params.worker
	jobID := params.jobID
	extranonce2 := params.extranonce2
	ntime := params.ntime
	nonce := params.nonce
	submittedVersion := params.submittedVersion

	workerName := mc.updateWorker(worker)
	if mc.isBanned(now) {
		until, reason, _ := mc.banDetails()
		logger.Warn("submit rejected: banned", "miner", mc.minerName(workerName), "ban_until", until, "reason", reason)
		if mc.metrics != nil {
			mc.metrics.RecordSubmitError("banned")
		}
		mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(24, "banned")})
		return
	}

	job, ok := mc.jobForID(jobID)
	if !ok || job == nil {
		logger.Warn("submit rejected: stale job", "remote", mc.id, "job", jobID)
		// Use "job not found" for missing/expired jobs.
		mc.rejectShareWithBan(req, workerName, rejectStaleJob, 21, "job not found", now)
		return
	}

	// Defensive: ensure the job template still matches what we advertised to this
	// connection (prevhash/height). If it changed underneath us, reject as stale.
	mc.jobMu.Lock()
	curLast := mc.lastJob
	mc.jobMu.Unlock()
	if curLast != nil && curLast.Template.Previous != job.Template.Previous {
		logger.Warn("submit rejected: stale job prevhash mismatch", "remote", mc.id, "job", jobID, "expected_prev", job.Template.Previous, "current_prev", curLast.Template.Previous)
		mc.rejectShareWithBan(req, workerName, rejectStaleJob, 21, "job not found", now)
		return
	}

	if len(extranonce2) != job.Extranonce2Size*2 {
		logger.Warn("submit invalid extranonce2 length", "remote", mc.id, "got", len(extranonce2)/2, "expected", job.Extranonce2Size)
		mc.rejectShareWithBan(req, workerName, rejectInvalidExtranonce2, 20, "invalid extranonce2", now)
		return
	}
	en2, err := hex.DecodeString(extranonce2)
	if err != nil {
		logger.Warn("submit bad extranonce2", "remote", mc.id, "error", err)
		mc.rejectShareWithBan(req, workerName, rejectInvalidExtranonce2, 20, "invalid extranonce2", now)
		return
	}

	if len(ntime) != 8 {
		logger.Warn("submit invalid ntime length", "remote", mc.id, "len", len(ntime))
		mc.rejectShareWithBan(req, workerName, rejectInvalidNTime, 20, "invalid ntime", now)
		return
	}
	// Stratum pools send ntime as BIG-ENDIAN hex and parse it back with parseInt(hex, 16).
	ntimeVal, err := parseUint32BEHex(ntime)
	if err != nil {
		logger.Warn("submit bad ntime", "remote", mc.id, "error", err)
		mc.rejectShareWithBan(req, workerName, rejectInvalidNTime, 20, "invalid ntime", now)
		return
	}
	// Tight ntime bounds: require ntime to be >= the template's curtime
	// (or mintime when provided) and allow it to roll forward only a short
	// distance from the template.
	minNTime := job.Template.CurTime
	if job.Template.Mintime > 0 && job.Template.Mintime > minNTime {
		minNTime = job.Template.Mintime
	}
	ntimeForwardSlack := mc.cfg.NTimeForwardSlackSeconds
	if ntimeForwardSlack <= 0 {
		ntimeForwardSlack = defaultNTimeForwardSlackSeconds
	}
	maxNTime := minNTime + int64(ntimeForwardSlack)
	if int64(ntimeVal) < minNTime || int64(ntimeVal) > maxNTime {
		logger.Warn("submit ntime outside window", "remote", mc.id, "ntime", ntimeVal, "min", minNTime, "max", maxNTime)
		mc.rejectShareWithBan(req, workerName, rejectInvalidNTime, 20, "invalid ntime", now)
		return
	}

	if len(nonce) != 8 {
		logger.Warn("submit invalid nonce length", "remote", mc.id, "len", len(nonce))
		mc.rejectShareWithBan(req, workerName, rejectInvalidNonce, 20, "invalid nonce", now)
		return
	}
	// Nonce is sent as BIG-ENDIAN hex in mining.notify.
	if _, err := parseUint32BEHex(nonce); err != nil {
		logger.Warn("submit bad nonce", "remote", mc.id, "error", err)
		mc.rejectShareWithBan(req, workerName, rejectInvalidNonce, 20, "invalid nonce", now)
		return
	}

	// BIP320: reject version rolls outside the negotiated mask (docs/protocols/bip-0320.mediawiki).
	baseVersion := uint32(job.Template.Version)
	useVersion := baseVersion
	versionDiff := uint32(0)
	if submittedVersion != 0 {
		// ESP-Miner sends the delta (rolled_version ^ base_version), while other
		// miners send the full rolled version. Treat values that fit entirely
		// inside the negotiated mask as a delta, otherwise as a full version.
		if submittedVersion&^mc.versionMask == 0 {
			useVersion = baseVersion ^ submittedVersion
			versionDiff = submittedVersion
		} else {
			useVersion = submittedVersion
			versionDiff = useVersion ^ baseVersion
		}
	}

	versionHex := fmt.Sprintf("%08x", useVersion)
	if versionDiff != 0 && !mc.versionRoll {
		logger.Warn("submit rejected: version rolling disabled", "remote", mc.id, "diff", fmt.Sprintf("%08x", versionDiff))
		mc.rejectShareWithBan(req, workerName, rejectInvalidVersion, 20, "version rolling not enabled", now)
		return
	}
	if versionDiff&^mc.versionMask != 0 {
		logger.Warn("submit rejected: version outside mask", "remote", mc.id, "version", fmt.Sprintf("%08x", useVersion), "mask", fmt.Sprintf("%08x", mc.versionMask))
		mc.rejectShareWithBan(req, workerName, rejectInvalidVersionMask, 20, "invalid version mask", now)
		return
	}
	if versionDiff != 0 && mc.minVerBits > 0 && bits.OnesCount32(versionDiff&mc.versionMask) < mc.minVerBits {
		logger.Warn("submit rejected: insufficient version rolling bits", "remote", mc.id, "version", fmt.Sprintf("%08x", useVersion), "required_bits", mc.minVerBits)
		mc.rejectShareWithBan(req, workerName, rejectInsufficientVersionBits, 20, "insufficient version bits", now)
		return
	}

	if mc.isDuplicateShare(job.JobID, extranonce2, ntime, nonce, versionHex) {
		logger.Warn("duplicate share", "remote", mc.id, "job", jobID, "extranonce2", extranonce2, "ntime", ntime, "nonce", nonce, "version", versionHex)
		mc.rejectShareWithBan(req, workerName, rejectDuplicateShare, 22, "duplicate share", now)
		return
	}

	if debugLogging || verboseLogging {
		logger.Info("submit received",
			"remote", mc.id,
			"worker", workerName,
			"job", jobID,
			"extranonce2", extranonce2,
			"ntime", ntime,
			"nonce", nonce,
			"version", versionHex,
		)
	}

	var (
		headerHash       []byte
		header           []byte
		merkleRoot       []byte
		cbTx             []byte
		cbTxid           []byte
		usedDualCoinbase bool
	)

	// Build only the header and merkle root first so we can validate PoW and
	// most rejects without constructing a full block. This avoids per-share
	// hex decode/encode of all transactions when the share is not a block.
	if poolScript, workerScript, totalValue, feePercent, ok := mc.dualPayoutParams(job, workerName); ok {
		// Check if donation is enabled and we should use triple payout
		if job.OperatorDonationPercent > 0 && len(job.DonationScript) > 0 {
			cbTx, cbTxid, err = serializeTripleCoinbaseTxPredecoded(
				job.Template.Height,
				mc.extranonce1,
				en2,
				job.TemplateExtraNonce2Size,
				poolScript,
				job.DonationScript,
				workerScript,
				totalValue,
				feePercent,
				job.OperatorDonationPercent,
				job.witnessCommitScript,
				job.coinbaseFlagsBytes,
				job.CoinbaseMsg,
				job.ScriptTime,
			)
		} else {
			cbTx, cbTxid, err = serializeDualCoinbaseTxPredecoded(
				job.Template.Height,
				mc.extranonce1,
				en2,
				job.TemplateExtraNonce2Size,
				poolScript,
				workerScript,
				totalValue,
				feePercent,
				job.witnessCommitScript,
				job.coinbaseFlagsBytes,
				job.CoinbaseMsg,
				job.ScriptTime,
			)
		}
		if err == nil && len(cbTxid) == 32 {
			merkleRoot = computeMerkleRootFromBranches(cbTxid, job.MerkleBranches)
			header, err = job.buildBlockHeader(merkleRoot, ntime, nonce, int32(useVersion))
			if err == nil {
				usedDualCoinbase = true
			}
		}
	}

	// Fallback to single-output header/coinbase when dual-payout header
	// construction fails.
	if header == nil || merkleRoot == nil || err != nil || len(cbTxid) != 32 {
		if err != nil && usedDualCoinbase {
			logger.Warn("dual-payout header build failed, falling back to single-output header",
				"error", err,
				"worker", workerName,
			)
		}
		cbTx, cbTxid, err = serializeCoinbaseTxPredecoded(
			job.Template.Height,
			mc.extranonce1,
			en2,
			job.TemplateExtraNonce2Size,
			job.PayoutScript,
			job.CoinbaseValue,
			job.witnessCommitScript,
			job.coinbaseFlagsBytes,
			job.CoinbaseMsg,
			job.ScriptTime,
		)
		if err != nil || len(cbTxid) != 32 {
			logger.Warn("submit coinbase rebuild failed", "remote", mc.id, "error", err)
			mc.recordShare(workerName, false, 0, 0, rejectInvalidCoinbase.String(), "", nil, now)
			mc.writeResponse(StratumResponse{
				ID:     req.ID,
				Result: false,
				Error:  newStratumError(20, "invalid coinbase"),
			})
			return
		}
		merkleRoot = computeMerkleRootFromBranches(cbTxid, job.MerkleBranches)
		header, err = job.buildBlockHeader(merkleRoot, ntime, nonce, int32(useVersion))
		if err != nil {
			logger.Error("submit header build error", "remote", mc.id, "error", err)
			mc.recordShare(workerName, false, 0, 0, err.Error(), "", nil, now)
			if banned, invalids := mc.noteInvalidSubmit(now, rejectInvalidCoinbase); banned {
				mc.logBan(rejectInvalidCoinbase.String(), workerName, invalids)
				mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(24, "banned")})
			} else {
				mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(20, err.Error())})
			}
			return
		}
	}

	expectedMerkle := computeMerkleRootFromBranches(cbTxid, job.MerkleBranches)
	if merkleRoot == nil || expectedMerkle == nil || !bytes.Equal(merkleRoot, expectedMerkle) {
		logger.Warn("submit merkle mismatch", "remote", mc.id, "worker", workerName, "job", jobID)
		detail := &ShareDetail{
			Header:         hex.EncodeToString(header),
			ShareHash:      hex.EncodeToString(reverseBytes(headerHash)),
			MerkleBranches: append([]string{}, job.MerkleBranches...),
			MerkleRootBE:   hex.EncodeToString(expectedMerkle),
			MerkleRootLE:   hex.EncodeToString(reverseBytes(expectedMerkle)),
			Coinbase:       hex.EncodeToString(cbTx),
		}
		detail.DecodeCoinbaseFields()
		mc.recordShare(workerName, false, 0, 0, rejectInvalidMerkle.String(), "", detail, now)
		if banned, invalids := mc.noteInvalidSubmit(now, rejectInvalidMerkle); banned {
			mc.logBan(rejectInvalidMerkle.String(), workerName, invalids)
			mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(24, "banned")})
		} else {
			mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(20, "invalid merkle")})
		}
		return
	}

	// Bitcoin PoW: hash bytes from SHA256 are BE, but Bitcoin compares as LE-interpreted number.
	// To emulate JS bignum.fromBuffer(hash, {endian:'little'}), we reverse then SetBytes.
	// Use array-based hash to avoid allocation in hot path
	headerHashArray := doubleSHA256Array(header)
	headerHash = headerHashArray[:]

	// Reverse in-place for little-endian interpretation
	var headerHashLE [32]byte
	copy(headerHashLE[:], headerHashArray[:])
	reverseBytes32(&headerHashLE)

	// Use pooled big.Int to avoid allocation
	hashNum := bigIntPool.Get().(*big.Int)
	hashNum.SetBytes(headerHashLE[:])
	defer bigIntPool.Put(hashNum)

	hashLE := headerHashLE[:] // LE bytes for display
	shareDiff := difficultyFromHash(headerHash)
	hashHex := hex.EncodeToString(hashLE) // log the canonical display form
	assignedDiff := mc.assignedDifficulty(jobID)
	currentDiff := mc.currentDifficulty()
	// For debugging and correct accounting, credit the full target
	// difficulty we assigned for this share (or fall back to current
	// difficulty). Do not cap to the realized share difficulty so we
	// can see when miners return higher/lower-diff shares.
	creditedDiff := assignedDiff
	if creditedDiff <= 0 {
		creditedDiff = currentDiff
	}

	// Treat shares as valid if their computed difficulty is within ~1% of
	// the assigned target difficulty, with low-diff rejects handled via the
	// standard invalid-share path. The block check compares the LE header
	// integer directly to the network target from bits.
	isBlock := hashNum.Cmp(job.Target) <= 0

	lowDiff := false
	// Treat low-diff strictly relative to the difficulty that was in effect
	// when this job was sent (assignedDiff), so vardiff moves after notify
	// do not cause old jobs to be unfairly rejected. Fall back to the current
	// difficulty only when we have no per-job assignment.
	thresholdDiff := assignedDiff
	if thresholdDiff <= 0 {
		thresholdDiff = currentDiff
	}
	if !isBlock && thresholdDiff > 0 {
		ratio := shareDiff / thresholdDiff
		if ratio < 0.98 {
			lowDiff = true
		}
	}

	if lowDiff {
		if debugLogging || verboseLogging {
			logger.Info("share rejected",
				"share_diff", shareDiff,
				"required_diff", thresholdDiff,
				"assigned_diff", assignedDiff,
				"current_diff", currentDiff,
			)
			logger.Warn("submit rejected: lowDiff",
				"miner", mc.minerName(workerName),
				"hash", hashHex,
			)
		}
		detail := mc.buildShareDetail(job, workerName, header, hashLE, nil, extranonce2, merkleRoot)
		acceptedForStats := false
		mc.recordShare(workerName, acceptedForStats, 0, shareDiff, "lowDiff", hashHex, detail, now)

		if banned, invalids := mc.noteInvalidSubmit(now, rejectLowDiff); banned {
			mc.logBan(rejectLowDiff.String(), workerName, invalids)
			mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(24, "banned")})
		} else {
			mc.writeResponse(StratumResponse{
				ID:     req.ID,
				Result: false,
				Error:  []interface{}{23, fmt.Sprintf("low difficulty share of %.8f", shareDiff), nil},
			})
		}
		return
	}

	shareHash := hashHex
	detail := mc.buildShareDetail(job, workerName, header, hashLE, job.Target, extranonce2, merkleRoot)
	mc.recordShare(workerName, true, creditedDiff, shareDiff, "", shareHash, detail, now)
	mc.trackBestShare(workerName, shareHash, shareDiff, now)

	if !isBlock {
		accRate, subRate := mc.shareRates(now)
		stats := mc.snapshotStats()
		logger.Info("share accepted",
			"miner", mc.minerName(workerName),
			"difficulty", shareDiff,
			"hash", hashHex,
			"accepted_total", stats.Accepted,
			"rejected_total", stats.Rejected,
			"worker_difficulty", stats.TotalDifficulty,
			"accept_rate_per_min", accRate,
			"submit_rate_per_min", subRate,
		)
		mc.writeResponse(StratumResponse{ID: req.ID, Result: true, Error: nil})
		return
	}

	mc.handleBlockShare(req, job, workerName, en2, ntime, nonce, useVersion, hashHex, shareDiff, header, merkleRoot, cbTx, cbTxid, now)
}

// handleBlockShare processes a share that satisfies the network target. It
// builds the full block (reusing any dual-payout header/coinbase when
// available), submits it via RPC, logs the reward split and found-block
// record, and sends the final Stratum response.
func (mc *MinerConn) handleBlockShare(req *StratumRequest, job *Job, workerName string, en2 []byte, ntime string, nonce string, useVersion uint32, hashHex string, shareDiff float64, header []byte, merkleRoot []byte, cbTx []byte, cbTxid []byte, now time.Time) {
	var (
		blockHex  string
		submitRes interface{}
		err       error
	)
	// Only construct the full block (including all non-coinbase transactions)
	// when the share actually satisfies the network target.
	if poolScript, workerScript, totalValue, feePercent, ok := mc.dualPayoutParams(job, workerName); ok {
		var cbTx, cbTxid []byte
		var err error
		// Check if donation is enabled and we should use triple payout
		if job.OperatorDonationPercent > 0 && len(job.DonationScript) > 0 {
			cbTx, cbTxid, err = serializeTripleCoinbaseTxPredecoded(
				job.Template.Height,
				mc.extranonce1,
				en2,
				job.TemplateExtraNonce2Size,
				poolScript,
				job.DonationScript,
				workerScript,
				totalValue,
				feePercent,
				job.OperatorDonationPercent,
				job.witnessCommitScript,
				job.coinbaseFlagsBytes,
				job.CoinbaseMsg,
				job.ScriptTime,
			)
		} else {
			cbTx, cbTxid, err = serializeDualCoinbaseTxPredecoded(
				job.Template.Height,
				mc.extranonce1,
				en2,
				job.TemplateExtraNonce2Size,
				poolScript,
				workerScript,
				totalValue,
				feePercent,
				job.witnessCommitScript,
				job.coinbaseFlagsBytes,
				job.CoinbaseMsg,
				job.ScriptTime,
			)
		}
		if err == nil && len(cbTxid) == 32 {
			merkleRoot := computeMerkleRootFromBranches(cbTxid, job.MerkleBranches)
			header, err := job.buildBlockHeader(merkleRoot, ntime, nonce, int32(useVersion))
			if err == nil {
				buf := blockBufferPool.Get().(*bytes.Buffer)
				buf.Reset()
				defer blockBufferPool.Put(buf)

				buf.Write(header)
				writeVarInt(buf, uint64(1+len(job.Transactions)))
				buf.Write(cbTx)
				for _, tx := range job.Transactions {
					raw, derr := hex.DecodeString(tx.Data)
					if derr != nil {
						err = fmt.Errorf("decode tx data: %w", derr)
						break
					}
					buf.Write(raw)
				}
				if err == nil {
					blockHex = hex.EncodeToString(buf.Bytes())
				}
			}
		}
	}
	if blockHex == "" {
		// Fallback to single-output block build if dual-payout params are
		// unavailable or any step fails. This reuses the existing helper that
		// constructs a canonical block for submission.
		blockHex, _, _, _, err = buildBlock(job, mc.extranonce1, en2, ntime, nonce, int32(useVersion))
		if err != nil {
			if mc.metrics != nil {
				mc.metrics.RecordBlockSubmission("error")
			}
			logger.Error("submitblock build error", "remote", mc.id, "error", err)
			mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(20, err.Error())})
			return
		}
	}

	// Submit the block via RPC using an aggressive, no-backoff retry loop
	// so we race the rest of the network as hard as possible. This path is
	// intentionally not tied to the miner or process context so shutdown
	// signals do not cancel in-flight submissions.
	err = mc.submitBlockWithFastRetry(job, workerName, hashHex, blockHex, &submitRes)
	if err != nil {
		if mc.metrics != nil {
			mc.metrics.RecordBlockSubmission("error")
		}
		logger.Error("submitblock error", "error", err)
		// Best-effort: record this block for manual or future retry when the
		// node RPC is unavailable or submitblock fails. This does not imply
		// that the block was accepted; it only preserves the data needed for
		// a later submitblock attempt.
		mc.logPendingSubmission(job, workerName, hashHex, blockHex, err)
		mc.writeResponse(StratumResponse{ID: req.ID, Result: false, Error: newStratumError(20, err.Error())})
		return
	}
	if mc.metrics != nil {
		mc.metrics.RecordBlockSubmission("accepted")
	}

	// For solo mining, treat the worker that submitted the block as the
	// beneficiary of the block reward. We always split the reward between
	// the pool fee and worker payout for logging purposes.
	if workerName != "" && job != nil && job.CoinbaseValue > 0 {
		total := job.CoinbaseValue
		feePct := mc.cfg.PoolFeePercent
		if feePct < 0 {
			feePct = 0
		}
		if feePct > 99.99 {
			feePct = 99.99
		}
		poolFee := int64(math.Round(float64(total) * feePct / 100.0))
		if poolFee < 0 {
			poolFee = 0
		}
		if poolFee > total {
			poolFee = total
		}
		minerAmt := total - poolFee
		if minerAmt > 0 {
			logger.Info("block reward split",
				"miner", mc.minerName(workerName),
				"worker_address", workerName,
				"height", job.Template.Height,
				"block_value_sats", total,
				"pool_fee_sats", poolFee,
				"worker_payout_sats", minerAmt,
				"fee_percent", feePct,
			)
		}
	}

	stats := mc.snapshotStats()
	mc.logFoundBlock(job, workerName, hashHex, shareDiff)
	logger.Info("block found",
		"miner", mc.minerName(workerName),
		"height", job.Template.Height,
		"hash", hashHex,
		"accepted_total", stats.Accepted,
		"rejected_total", stats.Rejected,
		"worker_difficulty", stats.TotalDifficulty,
	)
	mc.writeResponse(StratumResponse{ID: req.ID, Result: true, Error: nil})
}

// logFoundBlock appends a JSON line describing a found block to a log file in
// the data directory. This is purely for operator audit/debugging and is best
// effort; failures are logged but do not affect pool operation.
func (mc *MinerConn) logFoundBlock(job *Job, worker, hashHex string, shareDiff float64) {
	dir := mc.cfg.DataDir
	if dir == "" {
		dir = defaultDataDir
	}
	// Compute a simple view of the payout split used for this block. In
	// dual-payout mode with a validated worker script, the coinbase uses a
	// pool-fee + worker output; otherwise the entire reward is logically
	// treated as a worker payout in single mode, or sent to the pool in
	// dual-payout fallback cases.
	total := job.Template.CoinbaseValue
	feePct := mc.cfg.PoolFeePercent
	if feePct < 0 {
		feePct = 0
	}
	if feePct > 99.99 {
		feePct = 99.99
	}
	poolFee := int64(math.Round(float64(total) * feePct / 100.0))
	if poolFee < 0 {
		poolFee = 0
	}
	if poolFee > total {
		poolFee = total
	}
	workerAmt := total - poolFee
	// If dual payout is disabled, treat the full reward as a worker payout
	// ("Single" mode = miner only). When dual payout is enabled but the
	// worker has no cached script or the worker wallet equals the pool
	// payout address, treat this block as pool-only and record the full
	// amount as pool_fee_sats with dual_payout_fallback=true.
	dualFallback := false
	workerAddr := ""
	if worker != "" {
		raw := strings.TrimSpace(worker)
		if parts := strings.SplitN(raw, ".", 2); len(parts) > 1 {
			raw = parts[0]
		}
		workerAddr = sanitizePayoutAddress(raw)
	}
	// Check if we fell back to single-output coinbase (worker wallet matches pool wallet)
	if len(mc.workerPayoutScript(worker)) == 0 || (workerAddr != "" && strings.EqualFold(workerAddr, mc.cfg.PayoutAddress)) {
		poolFee = total
		workerAmt = 0
		dualFallback = true
	}

	rec := map[string]interface{}{
		"timestamp":            time.Now().UTC(),
		"height":               job.Template.Height,
		"hash":                 hashHex,
		"worker":               mc.minerName(worker),
		"share_diff":           shareDiff,
		"job_id":               job.JobID,
		"payout_address":       mc.cfg.PayoutAddress,
		"coinbase_value_sats":  total,
		"pool_fee_sats":        poolFee,
		"worker_payout_sats":   workerAmt,
		"dual_payout_fallback": dualFallback,
	}
	data, err := fastJSONMarshal(rec)
	if err != nil {
		logger.Warn("found block log marshal", "error", err)
		return
	}
	line := append(data, '\n')
	select {
	case foundBlockLogCh <- foundBlockLogEntry{Dir: dir, Line: line}:
	default:
		// If the queue is full, drop the log entry rather than blocking
		// the submit path; this log is best-effort operator metadata.
		logger.Warn("found block log queue full; dropping entry")
	}
}

// logPendingSubmission appends a JSON line describing a block that failed
// submitblock to a log file in the data directory. This allows operators to
// manually retry submission with bitcoin-cli or future tooling when the node
// RPC is down or returns an error. It is best effort only.
func (mc *MinerConn) logPendingSubmission(job *Job, worker, hashHex, blockHex string, submitErr error) {
	if job == nil || blockHex == "" {
		return
	}
	rec := pendingSubmissionRecord{
		Timestamp:  time.Now().UTC(),
		Height:     job.Template.Height,
		Hash:       hashHex,
		Worker:     mc.minerName(worker),
		BlockHex:   blockHex,
		RPCError:   submitErr.Error(),
		RPCURL:     mc.cfg.RPCURL,
		PayoutAddr: mc.cfg.PayoutAddress,
		Status:     "pending",
	}
	appendPendingSubmissionRecord(pendingSubmissionsPath(mc.cfg), rec)
}
