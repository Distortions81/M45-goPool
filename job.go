package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/bytedance/gopkg/util/logger"
	"github.com/pebbe/zmq4"
)

// GetBlockTemplateResult mirrors BIP22/23 getblocktemplate fields.
// See docs/protocols/bip-0022.mediawiki and docs/protocols/bip-0023.mediawiki.
type GetBlockTemplateResult struct {
	Bits                     string           `json:"bits"`
	CurTime                  int64            `json:"curtime"`
	Height                   int64            `json:"height"`
	Mintime                  int64            `json:"mintime"`
	Target                   string           `json:"target"`
	Version                  int32            `json:"version"`
	Previous                 string           `json:"previousblockhash"`
	CoinbaseValue            int64            `json:"coinbasevalue"`
	DefaultWitnessCommitment string           `json:"default_witness_commitment"`
	LongPollID               string           `json:"longpollid"`
	Transactions             []GBTTransaction `json:"transactions"`
	VbAvailable              map[string]int   `json:"vbavailable"`
	VbRequired               int              `json:"vbrequired"`
	Mutable                  []string         `json:"mutable"`
	Rules                    []string         `json:"rules"`
	CoinbaseAux              struct {
		Flags string `json:"flags"`
	} `json:"coinbaseaux"`
}

type GBTTransaction struct {
	Data string `json:"data"`
	Txid string `json:"txid"`
	Hash string `json:"hash"`
}

type Job struct {
	JobID                   string
	Template                GetBlockTemplateResult
	Target                  *big.Int
	CreatedAt               time.Time
	Clean                   bool
	Extranonce2Size         int
	CoinbaseValue           int64
	WitnessCommitment       string
	CoinbaseMsg             string
	MerkleBranches          []string
	Transactions            []GBTTransaction
	TransactionIDs          [][]byte
	PayoutScript            []byte
	VersionMask             uint32
	PrevHash                string
	prevHashBytes           [32]byte
	bitsBytes               [4]byte
	coinbaseFlagsBytes      []byte
	witnessCommitScript     []byte
	ScriptTime              int64
	TemplateExtraNonce2Size int
}

const (
	minBackoff              = 1 * time.Second
	maxBackoff              = 1 * time.Minute
	zmqRecvTimeout          = 15 * time.Second
	jobSubscriberBuffer     = 4
	coinbaseExtranonce1Size = 4
)

var errStaleTemplate = errors.New("stale template")

// blockBufferPool reuses buffers for block assembly to reduce allocations
// when constructing raw blocks for submitblock or tests.
var blockBufferPool = sync.Pool{
	New: func() interface{} {
		return new(bytes.Buffer)
	},
}

type JobManager struct {
	rpc            *RPCClient
	cfg            Config
	mu             sync.RWMutex
	curJob         *Job
	payoutScript   []byte
	extraID        uint32
	subs           map[chan *Job]struct{}
	subsMu         sync.Mutex
	zmqHealthy     atomic.Bool
	zmqDisconnects uint64
	zmqReconnects  uint64
	lastErrMu      sync.RWMutex
	lastErr        error
	lastErrAt      time.Time
	lastJobSuccess time.Time
	// Refresh coordination to prevent duplicate refreshes from longpoll/ZMQ
	refreshMu          sync.Mutex
	lastRefreshAttempt time.Time
}

func NewJobManager(rpc *RPCClient, cfg Config, payoutScript []byte) *JobManager {
	return &JobManager{
		rpc:          rpc,
		cfg:          cfg,
		payoutScript: payoutScript,
		subs:         make(map[chan *Job]struct{}),
	}
}

type JobFeedStatus struct {
	Ready          bool
	LastSuccess    time.Time
	LastError      error
	LastErrorAt    time.Time
	ZMQHealthy     bool
	ZMQDisconnects uint64
	ZMQReconnects  uint64
}

func (jm *JobManager) recordJobError(err error) {
	if err == nil {
		return
	}
	jm.lastErrMu.Lock()
	jm.lastErr = err
	jm.lastErrAt = time.Now()
	jm.lastJobSuccess = time.Time{}
	jm.lastErrMu.Unlock()
}

func (jm *JobManager) recordJobSuccess(job *Job) {
	jm.lastErrMu.Lock()
	jm.lastErr = nil
	jm.lastErrAt = time.Time{}
	if job != nil && !job.CreatedAt.IsZero() {
		jm.lastJobSuccess = job.CreatedAt
	} else {
		jm.lastJobSuccess = time.Now()
	}
	jm.lastErrMu.Unlock()
}

func (jm *JobManager) FeedStatus() JobFeedStatus {
	jm.lastErrMu.RLock()
	lastErr := jm.lastErr
	lastErrAt := jm.lastErrAt
	lastSuccess := jm.lastJobSuccess
	jm.lastErrMu.RUnlock()

	jm.mu.RLock()
	cur := jm.curJob
	jm.mu.RUnlock()

	if lastSuccess.IsZero() && cur != nil && !cur.CreatedAt.IsZero() {
		lastSuccess = cur.CreatedAt
	}

	return JobFeedStatus{
		Ready:          cur != nil,
		LastSuccess:    lastSuccess,
		LastError:      lastErr,
		LastErrorAt:    lastErrAt,
		ZMQHealthy:     jm.zmqHealthy.Load(),
		ZMQDisconnects: atomic.LoadUint64(&jm.zmqDisconnects),
		ZMQReconnects:  atomic.LoadUint64(&jm.zmqReconnects),
	}
}

func nextBackoff(cur time.Duration) time.Duration {
	if cur <= 0 {
		return minBackoff
	}
	cur *= 2
	if cur > maxBackoff {
		return maxBackoff
	}
	return cur
}

func (jm *JobManager) Start(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := jm.refreshJobCtx(ctx); err != nil {
		logger.Error("initial job refresh error", "error", err)
	}

	go jm.longpollLoop(ctx)
	if jm.cfg.ZMQBlockAddr != "" {
		go jm.zmqBlockLoop(ctx)
	}
}

func (jm *JobManager) refreshJobCtx(ctx context.Context) error {
	// Debounce rapid refresh attempts from multiple sources (ZMQ + longpoll).
	// If a refresh happened in the last 100ms, skip to avoid duplicate work.
	jm.refreshMu.Lock()
	if time.Since(jm.lastRefreshAttempt) < 100*time.Millisecond {
		jm.refreshMu.Unlock()
		return nil // Recent refresh already in progress or completed
	}
	jm.lastRefreshAttempt = time.Now()
	jm.refreshMu.Unlock()

	// Match NOMP daemon.js: request segwit rules and coinbase capabilities so
	// coinbasetxn/append/workid are allowed by bitcoind.
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
	clean := jm.templateChanged(tpl)
	job, err := jm.buildJob(ctx, tpl)
	if err != nil {
		jm.recordJobError(err)
		return err
	}
	job.Clean = clean

	jm.mu.Lock()
	jm.curJob = job
	jm.mu.Unlock()

	jm.recordJobSuccess(job)
	logger.Info("new job", "height", tpl.Height, "job_id", job.JobID, "bits", tpl.Bits, "txs", len(tpl.Transactions))
	jm.broadcastJob(job)
	return nil
}

func (jm *JobManager) buildJob(ctx context.Context, tpl GetBlockTemplateResult) (*Job, error) {
	if len(jm.payoutScript) == 0 {
		return nil, fmt.Errorf("payout script not configured")
	}

	if err := jm.ensureTemplateFresh(ctx, tpl); err != nil {
		return nil, err
	}

	target, err := validateBits(tpl.Bits, tpl.Target)
	if err != nil {
		return nil, err
	}

	if err := validateWitnessCommitment(tpl.DefaultWitnessCommitment); err != nil {
		return nil, err
	}

	txids, err := validateTransactions(tpl.Transactions)
	if err != nil {
		return nil, err
	}

	merkleBranches := buildMerkleBranches(txids)

	scriptTime := time.Now().Unix()
	coinbaseMsg := jm.cfg.CoinbaseMsg
	if jm.cfg.CoinbaseSuffixBytes > 0 {
		msg, err := buildCoinbaseMsgWithSuffix(coinbaseMsg, jm.cfg.CoinbaseSuffixBytes)
		if err != nil {
			return nil, err
		}
		coinbaseMsg = msg
	}
	if jm.cfg.CoinbaseScriptSigMaxBytes > 0 {
		trimmed, truncated, err := clampCoinbaseMessage(coinbaseMsg, jm.cfg.CoinbaseScriptSigMaxBytes, tpl.Height, scriptTime, tpl.CoinbaseAux.Flags, jm.cfg.Extranonce2Size, jm.cfg.TemplateExtraNonce2Size)
		if err != nil {
			return nil, fmt.Errorf("coinbase scriptsig limit: %w", err)
		}
		if truncated {
			logger.Debug("clamped coinbase message to meet scriptSig limit", "limit", jm.cfg.CoinbaseScriptSigMaxBytes, "message", trimmed)
		}
		coinbaseMsg = trimmed
	}

	var prevBytes [32]byte
	if len(tpl.Previous) != 64 {
		return nil, fmt.Errorf("previousblockhash hex must be 64 chars")
	}
	if n, err := hex.Decode(prevBytes[:], []byte(tpl.Previous)); err != nil || n != 32 {
		return nil, fmt.Errorf("decode previousblockhash: %w", err)
	}

	var bitsBytes [4]byte
	if len(tpl.Bits) != 8 {
		return nil, fmt.Errorf("bits hex must be 8 chars")
	}
	if n, err := hex.Decode(bitsBytes[:], []byte(tpl.Bits)); err != nil || n != 4 {
		return nil, fmt.Errorf("decode bits: %w", err)
	}

	var flagsBytes []byte
	if tpl.CoinbaseAux.Flags != "" {
		b, err := hex.DecodeString(tpl.CoinbaseAux.Flags)
		if err != nil {
			return nil, fmt.Errorf("decode coinbase flags: %w", err)
		}
		flagsBytes = b
	}

	var commitScript []byte
	if tpl.DefaultWitnessCommitment != "" {
		b, err := hex.DecodeString(tpl.DefaultWitnessCommitment)
		if err != nil {
			return nil, fmt.Errorf("decode witness commitment: %w", err)
		}
		commitScript = b
	}

	job := &Job{
		JobID:                   fmt.Sprintf("%d", time.Now().UnixNano()),
		Template:                tpl,
		Target:                  target,
		CreatedAt:               time.Now(),
		ScriptTime:              scriptTime,
		Extranonce2Size:         jm.cfg.Extranonce2Size,
		CoinbaseValue:           tpl.CoinbaseValue,
		WitnessCommitment:       tpl.DefaultWitnessCommitment,
		CoinbaseMsg:             coinbaseMsg,
		MerkleBranches:          merkleBranches,
		Transactions:            tpl.Transactions,
		TransactionIDs:          txids,
		PayoutScript:            jm.payoutScript,
		VersionMask:             computePoolMask(tpl, jm.cfg),
		PrevHash:                tpl.Previous,
		prevHashBytes:           prevBytes,
		bitsBytes:               bitsBytes,
		coinbaseFlagsBytes:      flagsBytes,
		witnessCommitScript:     commitScript,
		TemplateExtraNonce2Size: jm.cfg.TemplateExtraNonce2Size,
	}

	return job, nil
}

func buildCoinbaseMsgWithSuffix(base string, suffixBytes int) (string, error) {
	buf := make([]byte, suffixBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("coinbase suffix: %w", err)
	}
	suffix := hex.EncodeToString(buf)
	if base == "" {
		return suffix, nil
	}
	return fmt.Sprintf("%s-%s", base, suffix), nil
}

func computePoolMask(tpl GetBlockTemplateResult, cfg Config) uint32 {
	base := defaultVersionMask
	if cfg.VersionMaskConfigured {
		base = cfg.VersionMask
	}
	if base == 0 {
		return 0
	}

	// Keep version rolling available to miners even when the template does not
	// advertise version mutability, since some bitcoind templates omit that
	// flag. Falling back to the configured base mask avoids sending a zero mask
	// (which would disable ASIC rolling on miners like ESP-Miner).
	if !versionMutable(tpl.Mutable) {
		return base
	}

	mask := base
	mask &^= uint32(tpl.VbRequired)

	active := make(map[string]struct{})
	for _, rule := range tpl.Rules {
		active[rule] = struct{}{}
	}
	for name, bit := range tpl.VbAvailable {
		if _, ok := active[name]; !ok {
			continue
		}
		if bit < 0 || bit >= 32 {
			continue
		}
		mask &^= uint32(1) << uint(bit)
	}

	if mask == 0 {
		// Avoid broadcasting a zero mask that would turn off version rolling on
		// miners which assume a non-zero range (e.g., ESP-Miner). Fall back to the
		// configured base mask in that rare case.
		return base
	}

	return mask
}

func parseUint32BEHex(hexStr string) (uint32, error) {
	if len(hexStr) != 8 {
		return 0, fmt.Errorf("expected 8 hex characters, got %d", len(hexStr))
	}
	var v uint32
	for i := 0; i < 8; i++ {
		c := hexStr[i]
		var nibble byte
		switch {
		case c >= '0' && c <= '9':
			nibble = c - '0'
		case c >= 'a' && c <= 'f':
			nibble = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			nibble = c - 'A' + 10
		default:
			return 0, fmt.Errorf("invalid hex digit %q in %q", c, hexStr)
		}
		v = (v << 4) | uint32(nibble)
	}
	return v, nil
}

func uint32ToBEHex(v uint32) string {
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], v)
	return hex.EncodeToString(buf[:])
}

func int32ToBEHex(v int32) string {
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], uint32(v))
	return hex.EncodeToString(buf[:])
}

func hexToLEHex(src string) string {
	b, err := hex.DecodeString(src)
	if err != nil || len(b) == 0 {
		return src
	}
	// Match foundation-stratum/node-stratum-pool utils.reverseByteOrder:
	// treat input as 8 big-endian uint32 words, rewrite each as little-endian,
	// then reverse the full buffer.
	if len(b) != 32 {
		return hex.EncodeToString(reverseBytes(b))
	}
	var buf [32]byte
	copy(buf[:], b)
	for i := 0; i < 8; i++ {
		j := i * 4
		v := uint32(buf[j])<<24 | uint32(buf[j+1])<<16 | uint32(buf[j+2])<<8 | uint32(buf[j+3])
		buf[j] = byte(v)
		buf[j+1] = byte(v >> 8)
		buf[j+2] = byte(v >> 16)
		buf[j+3] = byte(v >> 24)
	}
	return hex.EncodeToString(reverseBytes(buf[:]))
}

func versionMutable(mutable []string) bool {
	for _, m := range mutable {
		if strings.HasPrefix(m, "version/") {
			return true
		}
	}
	return false
}

func (jm *JobManager) ensureTemplateFresh(ctx context.Context, tpl GetBlockTemplateResult) error {
	if tpl.CurTime <= 0 {
		return fmt.Errorf("template curtime invalid: %d", tpl.CurTime)
	}

	var bestHash string
	if err := jm.rpc.callCtx(ctx, "getbestblockhash", nil, &bestHash); err != nil {
		return fmt.Errorf("getbestblockhash: %w", err)
	}

	if tpl.Previous != "" && bestHash != "" && tpl.Previous != bestHash {
		return fmt.Errorf("%w: prev hash %s does not match best %s", errStaleTemplate, tpl.Previous, bestHash)
	}

	jm.mu.RLock()
	cur := jm.curJob
	jm.mu.RUnlock()
	if cur != nil && tpl.Height < cur.Template.Height {
		return fmt.Errorf("%w: template height regressed from %d to %d", errStaleTemplate, cur.Template.Height, tpl.Height)
	}
	if cur != nil && tpl.CurTime < cur.Template.CurTime {
		return fmt.Errorf("%w: template curtime regressed from %d to %d", errStaleTemplate, cur.Template.CurTime, tpl.CurTime)
	}
	return nil
}

func validateWitnessCommitment(commitment string) error {
	if commitment == "" {
		return fmt.Errorf("template missing default witness commitment")
	}
	raw, err := hex.DecodeString(commitment)
	if err != nil {
		return fmt.Errorf("invalid default witness commitment: %w", err)
	}
	if len(raw) == 0 {
		return fmt.Errorf("default witness commitment empty")
	}
	return nil
}

func validateTransactions(txs []GBTTransaction) ([][]byte, error) {
	txids := make([][]byte, 0, len(txs))
	for i, tx := range txs {
		if len(tx.Txid) != 64 {
			return nil, fmt.Errorf("tx %d has invalid txid length: %d bytes", i, len(tx.Txid)/2)
		}
		txidBytes, err := hex.DecodeString(tx.Txid)
		if err != nil {
			return nil, fmt.Errorf("decode txid %s: %w", tx.Txid, err)
		}
		if len(txidBytes) != 32 {
			return nil, fmt.Errorf("tx %d txid must be 32 bytes, got %d", i, len(txidBytes))
		}

		raw, err := hex.DecodeString(tx.Data)
		if err != nil {
			return nil, fmt.Errorf("decode tx %d data: %w", i, err)
		}
		if len(raw) == 0 {
			return nil, fmt.Errorf("tx %d data empty", i)
		}

		base, hasWitness, err := stripWitnessData(raw)
		if err != nil {
			return nil, fmt.Errorf("tx %d decode: %w", i, err)
		}

		hashInput := raw
		if hasWitness {
			hashInput = base
		}

		computedRaw := doubleSHA256(hashInput)
		if !bytes.Equal(reverseBytes(computedRaw), txidBytes) && !bytes.Equal(computedRaw, txidBytes) {
			return nil, fmt.Errorf("tx %d txid mismatch with provided data", i)
		}

		if tx.Hash != "" {
			wtxidBytes, err := hex.DecodeString(tx.Hash)
			if err != nil {
				return nil, fmt.Errorf("decode wtxid %s: %w", tx.Hash, err)
			}
			if len(wtxidBytes) != 32 {
				return nil, fmt.Errorf("tx %d wtxid must be 32 bytes, got %d", i, len(wtxidBytes))
			}
			wtxidRaw := doubleSHA256(raw)
			if !bytes.Equal(reverseBytes(wtxidRaw), wtxidBytes) && !bytes.Equal(wtxidRaw, wtxidBytes) {
				return nil, fmt.Errorf("tx %d wtxid mismatch with provided data", i)
			}
		}

		// Store txids in little-endian form for merkle building (match node-stratum-pool).
		txids = append(txids, reverseBytes(computedRaw))
	}
	return txids, nil
}

func validateBits(bitsStr, targetStr string) (*big.Int, error) {
	if len(bitsStr) != 8 {
		return nil, fmt.Errorf("bits must be 8 hex characters, got %d", len(bitsStr))
	}
	target, err := targetFromBits(bitsStr)
	if err != nil {
		return nil, err
	}
	if target.Sign() <= 0 {
		return nil, fmt.Errorf("bits produced non-positive target")
	}
	if targetStr == "" {
		return target, nil
	}

	tplTarget := new(big.Int)
	if _, ok := tplTarget.SetString(targetStr, 16); !ok {
		return nil, fmt.Errorf("invalid template target %s", targetStr)
	}
	if tplTarget.Sign() <= 0 {
		return nil, fmt.Errorf("template target non-positive")
	}
	if tplTarget.Cmp(target) != 0 {
		return nil, fmt.Errorf("bits target %s mismatches template target %s", target.Text(16), tplTarget.Text(16))
	}
	return target, nil
}

func (jm *JobManager) templateChanged(tpl GetBlockTemplateResult) bool {
	jm.mu.RLock()
	cur := jm.curJob
	jm.mu.RUnlock()

	if cur == nil {
		return true
	}
	prev := cur.Template

	// Only treat a template as "new work" when previousblockhash, height or
	// bits change. Changes to curtime or the transaction set alone do not
	// invalidate existing jobs, allowing miners to continue working on
	// slightly stale templates.
	if tpl.Previous != prev.Previous ||
		tpl.Height != prev.Height ||
		tpl.Bits != prev.Bits {
		return true
	}

	if len(tpl.Transactions) != len(prev.Transactions) {
		return true
	}
	for i, tx := range tpl.Transactions {
		if tx.Txid != prev.Transactions[i].Txid {
			return true
		}
	}
	return false
}

func (jm *JobManager) CurrentJob() *Job {
	jm.mu.RLock()
	defer jm.mu.RUnlock()
	return jm.curJob
}

func (jm *JobManager) Ready() bool {
	jm.mu.RLock()
	defer jm.mu.RUnlock()
	return jm.curJob != nil
}

func (jm *JobManager) NextExtranonce1() []byte {
	id := atomic.AddUint32(&jm.extraID, 1)
	buf := make([]byte, 4)
	binary.BigEndian.PutUint32(buf, id)
	return buf
}

func (jm *JobManager) Subscribe() chan *Job {
	ch := make(chan *Job, jobSubscriberBuffer)
	jm.subsMu.Lock()
	jm.subs[ch] = struct{}{}
	jm.subsMu.Unlock()

	return ch
}

func (jm *JobManager) Unsubscribe(ch chan *Job) {
	jm.subsMu.Lock()
	delete(jm.subs, ch)
	close(ch)
	jm.subsMu.Unlock()
}

func (jm *JobManager) ActiveMiners() int {
	jm.subsMu.Lock()
	defer jm.subsMu.Unlock()
	return len(jm.subs)
}

func (jm *JobManager) broadcastJob(job *Job) {
	jm.subsMu.Lock()
	blocked := 0
	subscribers := len(jm.subs)
	for ch := range jm.subs {
		select {
		case ch <- job:
		default:
			blocked++
		}
	}
	jm.subsMu.Unlock()

	if blocked > 0 {
		logger.Warn("job broadcast blocked; dropping update", "subscribers", subscribers, "blocked", blocked)
	}
}

func (jm *JobManager) markZMQHealthy() {
	if jm.cfg.ZMQBlockAddr == "" {
		return
	}
	if !jm.zmqHealthy.Load() {
		logger.Info("zmq watcher healthy", "addr", jm.cfg.ZMQBlockAddr)
	}
	atomic.AddUint64(&jm.zmqReconnects, 1)
	jm.zmqHealthy.Store(true)
}

func (jm *JobManager) markZMQUnhealthy(reason string, err error) {
	if jm.cfg.ZMQBlockAddr == "" {
		return
	}
	atomic.AddUint64(&jm.zmqDisconnects, 1)
	fields := []interface{}{"reason", reason}
	if err != nil {
		fields = append(fields, "error", err)
	}
	if jm.zmqHealthy.Swap(false) {
		args := append([]interface{}{"zmq watcher unhealthy; enabling longpoll fallback"}, fields...)
		logger.Warn(args...)
	} else if err != nil {
		args := append([]interface{}{"zmq watcher error"}, fields...)
		logger.Error(args...)
	}
}

func (jm *JobManager) longpollLoop(ctx context.Context) {
	backoff := minBackoff
	for {
		if ctx.Err() != nil {
			return
		}
		if jm.cfg.ZMQBlockAddr != "" && jm.zmqHealthy.Load() {
			if err := sleepContext(ctx, time.Second); err != nil {
				return
			}
			continue
		}

		job := jm.CurrentJob()
		if job == nil {
			if err := jm.refreshJobCtx(ctx); err != nil {
				logger.Error("longpoll refresh (no job) error", "error", err)
				if err := sleepContext(ctx, withJitter(backoff)); err != nil {
					return
				}
				backoff = nextBackoff(backoff)
				continue
			}
			backoff = minBackoff
			continue
		}

		if job.Template.LongPollID == "" {
			logger.Warn("longpollid missing; refreshing job normally")
			if err := jm.refreshJobCtx(ctx); err != nil {
				logger.Error("job refresh error", "error", err)
			}
			if err := sleepContext(ctx, withJitter(backoff)); err != nil {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		params := map[string]interface{}{
			"rules":      []string{"segwit"},
			"longpollid": job.Template.LongPollID,
		}
		tpl, err := jm.fetchTemplateCtx(ctx, params, true)
		if err != nil {
			jm.recordJobError(err)
			logger.Error("longpoll gbt error", "error", err)
			if err := sleepContext(ctx, withJitter(backoff)); err != nil {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		if err := jm.refreshFromTemplate(ctx, tpl); err != nil {
			logger.Error("longpoll refresh error", "error", err)
			if errors.Is(err, errStaleTemplate) {
				if err := jm.refreshJobCtx(ctx); err != nil {
					logger.Error("fallback refresh after stale template", "error", err)
				}
			}
			if err := sleepContext(ctx, withJitter(backoff)); err != nil {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		backoff = minBackoff
	}
}

// Prefer block notifications when bitcoind is configured with -zmqpubhashblock (docs/protocols/zmq.md).
func (jm *JobManager) zmqBlockLoop(ctx context.Context) {
	backoff := minBackoff
	for {
		if ctx.Err() != nil {
			return
		}
		if jm.CurrentJob() == nil {
			if err := jm.refreshJobCtx(ctx); err != nil {
				logger.Error("zmq loop refresh (no job) error", "error", err)
				if err := sleepContext(ctx, withJitter(backoff)); err != nil {
					return
				}
				backoff = nextBackoff(backoff)
				continue
			}
		}

		sub, err := zmq4.NewSocket(zmq4.SUB)
		if err != nil {
			jm.markZMQUnhealthy("socket", err)
			if err := sleepContext(ctx, withJitter(backoff)); err != nil {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		if err := sub.SetSubscribe("hashblock"); err != nil {
			jm.markZMQUnhealthy("subscribe", err)
			sub.Close()
			if err := sleepContext(ctx, withJitter(backoff)); err != nil {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		if err := sub.SetRcvtimeo(zmqRecvTimeout); err != nil {
			jm.markZMQUnhealthy("set_rcvtimeo", err)
			sub.Close()
			if err := sleepContext(ctx, withJitter(backoff)); err != nil {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		if err := sub.Connect(jm.cfg.ZMQBlockAddr); err != nil {
			jm.markZMQUnhealthy("connect", err)
			sub.Close()
			if err := sleepContext(ctx, withJitter(backoff)); err != nil {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		jm.markZMQHealthy()
		logger.Info("watching ZMQ block notifications", "addr", jm.cfg.ZMQBlockAddr)
		backoff = minBackoff

		for {
			if ctx.Err() != nil {
				sub.Close()
				return
			}
			frames, err := sub.RecvMessageBytes(0)
			if err != nil {
				eno := zmq4.AsErrno(err)
				if eno == zmq4.Errno(syscall.EAGAIN) || eno == zmq4.ETIMEDOUT {
					continue
				}
				jm.markZMQUnhealthy("receive", err)
				sub.Close()
				if err := sleepContext(ctx, withJitter(backoff)); err != nil {
					return
				}
				backoff = nextBackoff(backoff)
				break
			}
			if len(frames) < 2 {
				logger.Warn("zmq notification malformed", "frames", len(frames))
				continue
			}

			blockHash := hex.EncodeToString(frames[1])
			logger.Info("zmq block notification", "block_hash", blockHash)
			jm.markZMQHealthy()
			if err := jm.refreshJobCtx(ctx); err != nil {
				logger.Error("refresh after zmq block error", "error", err)
				if err := sleepContext(ctx, withJitter(backoff)); err != nil {
					return
				}
				backoff = nextBackoff(backoff)
				continue
			}
			backoff = minBackoff
		}
	}
}

func targetFromBits(bits string) (*big.Int, error) {
	b, err := hex.DecodeString(bits)
	if err != nil {
		return nil, fmt.Errorf("decode bits: %w", err)
	}
	if len(b) != 4 {
		return nil, fmt.Errorf("invalid bits length %d", len(b))
	}
	exp := b[0]
	mantissa := new(big.Int).SetBytes(b[1:])
	target := new(big.Int).Lsh(mantissa, 8*uint(exp-3))
	return target, nil
}

var diff1Target = func() *big.Int {
	n, _ := new(big.Int).SetString("00000000FFFF0000000000000000000000000000000000000000000000000000", 16)
	return n
}()

// maxUint256 is the maximum value representable in 256 bits.
var maxUint256 = func() *big.Int {
	n := new(big.Int).Lsh(big.NewInt(1), 256)
	return n.Sub(n, big.NewInt(1))
}()

func targetFromDifficulty(diff float64) *big.Int {
	if diff <= 0 {
		// Lowest difficulty means the largest possible target.
		return new(big.Int).Set(maxUint256)
	}
	// Compute target = diff1Target / diff using rational math to match node-stratum-pool.
	diffStr := strconv.FormatFloat(diff, 'g', -1, 64)
	r, ok := new(big.Rat).SetString(diffStr)
	if !ok || r.Sign() <= 0 {
		return new(big.Int).Set(maxUint256)
	}
	target := new(big.Rat).SetInt(diff1Target)
	target.Quo(target, r)
	tgt := new(big.Int).Quo(target.Num(), target.Denom())
	if tgt.Sign() == 0 {
		tgt = big.NewInt(1)
	}
	if tgt.Cmp(maxUint256) > 0 {
		tgt = new(big.Int).Set(maxUint256)
	}
	return tgt
}

// difficultyFromHash converts the block hash to a difficulty value relative to diff=1.
// The hash parameter should be big-endian bytes from SHA256.
func difficultyFromHash(hash []byte) float64 {
	// Bitcoin PoW uses little-endian interpretation
	n := new(big.Int).SetBytes(reverseBytes(hash))
	if n.Sign() == 0 {
		return defaultVarDiff.MaxDiff
	}

	f := new(big.Float).SetPrec(256).SetInt(diff1Target)
	f.Quo(f, new(big.Float).SetInt(n))
	val, _ := f.Float64()
	return val
}

func doubleSHA256(b []byte) []byte {
	first := sha256Sum(b)
	second := sha256Sum(first[:])
	return second[:]
}

// doubleSHA256Array returns the double SHA256 hash as a fixed-size array,
// avoiding slice allocation for hot paths.
func doubleSHA256Array(b []byte) [32]byte {
	first := sha256Sum(b)
	return sha256Sum(first[:])
}

func reverseBytes(in []byte) []byte {
	out := make([]byte, len(in))
	for i := range in {
		out[i] = in[len(in)-1-i]
	}
	return out
}

// reverseBytes32 reverses a 32-byte array in-place, avoiding allocation.
// Use this for hot paths when you know the size is 32 bytes (hashes).
func reverseBytes32(a *[32]byte) {
	for i := 0; i < 16; i++ {
		a[i], a[31-i] = a[31-i], a[i]
	}
}

func readVarInt(raw []byte) (uint64, int, error) {
	if len(raw) == 0 {
		return 0, 0, fmt.Errorf("varint empty")
	}
	switch raw[0] {
	case 0xff:
		if len(raw) < 9 {
			return 0, 0, fmt.Errorf("varint 0xff missing bytes")
		}
		val := binary.LittleEndian.Uint64(raw[1:9])
		return val, 9, nil
	case 0xfe:
		if len(raw) < 5 {
			return 0, 0, fmt.Errorf("varint 0xfe missing bytes")
		}
		val := binary.LittleEndian.Uint32(raw[1:5])
		return uint64(val), 5, nil
	case 0xfd:
		if len(raw) < 3 {
			return 0, 0, fmt.Errorf("varint 0xfd missing bytes")
		}
		val := binary.LittleEndian.Uint16(raw[1:3])
		return uint64(val), 3, nil
	default:
		return uint64(raw[0]), 1, nil
	}
}

// parseMinerID makes a best-effort attempt to split a miner client
// identifier into a name and version. Common formats include:
//
//	"cgminer/4.11.0"     -> ("cgminer", "4.11.0")
//	"BraiinsOS-stratum"  -> ("BraiinsOS-stratum", "")
//	"Some Miner 1.2.3"   -> ("Some Miner", "1.2.3")
func parseMinerID(id string) (string, string) {
	s := strings.TrimSpace(id)
	if s == "" {
		return "", ""
	}
	if idx := strings.Index(s, "/"); idx > 0 && idx < len(s)-1 {
		return s[:idx], s[idx+1:]
	}
	parts := strings.Fields(s)
	if len(parts) >= 2 {
		last := parts[len(parts)-1]
		hasDigit := false
		hasDot := false
		for i := 0; i < len(last); i++ {
			if last[i] >= '0' && last[i] <= '9' {
				hasDigit = true
			}
			if last[i] == '.' {
				hasDot = true
			}
		}
		if hasDigit && hasDot {
			return strings.Join(parts[:len(parts)-1], " "), last
		}
	}
	return s, ""
}

func stripWitnessData(raw []byte) ([]byte, bool, error) {
	if len(raw) < 6 {
		return nil, false, fmt.Errorf("tx too short: %d bytes", len(raw))
	}

	idx := 4 // skip version
	hasWitness := len(raw) > idx+1 && raw[idx] == 0x00 && raw[idx+1] != 0x00
	if hasWitness {
		idx += 2
	}

	inputsStart := idx

	vinCount, consumed, err := readVarInt(raw[idx:])
	if err != nil {
		return nil, false, fmt.Errorf("inputs count: %w", err)
	}
	idx += consumed

	for inIdx := uint64(0); inIdx < vinCount; inIdx++ {
		if idx+36 > len(raw) {
			return nil, false, fmt.Errorf("input %d truncated", inIdx)
		}
		idx += 36 // prevout hash + index

		scriptLen, used, err := readVarInt(raw[idx:])
		if err != nil {
			return nil, false, fmt.Errorf("input %d script len: %w", inIdx, err)
		}
		idx += used

		if idx+int(scriptLen)+4 > len(raw) {
			return nil, false, fmt.Errorf("input %d script truncated", inIdx)
		}
		idx += int(scriptLen) + 4 // script + sequence
	}

	voutCount, consumed, err := readVarInt(raw[idx:])
	if err != nil {
		return nil, false, fmt.Errorf("outputs count: %w", err)
	}
	idx += consumed

	for outIdx := uint64(0); outIdx < voutCount; outIdx++ {
		if idx+8 > len(raw) {
			return nil, false, fmt.Errorf("output %d truncated", outIdx)
		}
		idx += 8 // value

		pkLen, used, err := readVarInt(raw[idx:])
		if err != nil {
			return nil, false, fmt.Errorf("output %d script len: %w", outIdx, err)
		}
		idx += used

		if idx+int(pkLen) > len(raw) {
			return nil, false, fmt.Errorf("output %d script truncated", outIdx)
		}
		idx += int(pkLen)
	}

	witnessStart := idx

	if hasWitness {
		for inIdx := uint64(0); inIdx < vinCount; inIdx++ {
			itemCount, used, err := readVarInt(raw[idx:])
			if err != nil {
				return nil, false, fmt.Errorf("input %d witness count: %w", inIdx, err)
			}
			idx += used

			for itemIdx := uint64(0); itemIdx < itemCount; itemIdx++ {
				itemLen, n, err := readVarInt(raw[idx:])
				if err != nil {
					return nil, false, fmt.Errorf("input %d witness %d len: %w", inIdx, itemIdx, err)
				}
				idx += n

				if idx+int(itemLen) > len(raw) {
					return nil, false, fmt.Errorf("input %d witness %d truncated", inIdx, itemIdx)
				}
				idx += int(itemLen)
			}
		}
	}

	if idx+4 > len(raw) {
		return nil, false, fmt.Errorf("locktime truncated")
	}
	locktimeStart := idx
	idx += 4

	if idx != len(raw) {
		return nil, false, fmt.Errorf("unexpected trailing data: %d bytes", len(raw)-idx)
	}

	if !hasWitness {
		return raw, false, nil
	}

	// Rebuild transaction without marker/flag and witness data to compute legacy txid.
	stripped := make([]byte, 0, 4+(witnessStart-inputsStart)+4)
	stripped = append(stripped, raw[:4]...)
	stripped = append(stripped, raw[inputsStart:witnessStart]...)
	stripped = append(stripped, raw[locktimeStart:locktimeStart+4]...)

	return stripped, true, nil
}

// putVarInt encodes v into dst and returns the number of bytes written.
// Using the caller-provided buffer avoids per-call allocations.
func putVarInt(dst *[9]byte, v uint64) int {
	switch {
	case v < 0xfd:
		dst[0] = byte(v)
		return 1
	case v <= 0xffff:
		dst[0] = 0xfd
		dst[1] = byte(v)
		dst[2] = byte(v >> 8)
		return 3
	case v <= 0xffffffff:
		dst[0] = 0xfe
		dst[1] = byte(v)
		dst[2] = byte(v >> 8)
		dst[3] = byte(v >> 16)
		dst[4] = byte(v >> 24)
		return 5
	default:
		dst[0] = 0xff
		dst[1] = byte(v)
		dst[2] = byte(v >> 8)
		dst[3] = byte(v >> 16)
		dst[4] = byte(v >> 24)
		dst[5] = byte(v >> 32)
		dst[6] = byte(v >> 40)
		dst[7] = byte(v >> 48)
		dst[8] = byte(v >> 56)
		return 9
	}
}

// writeVarInt writes v to buf without heap allocation.
func writeVarInt(buf *bytes.Buffer, v uint64) {
	var tmp [9]byte
	n := putVarInt(&tmp, v)
	buf.Write(tmp[:n])
}

// appendVarInt appends the varint encoding of v to dst without allocating.
func appendVarInt(dst []byte, v uint64) []byte {
	var tmp [9]byte
	n := putVarInt(&tmp, v)
	return append(dst, tmp[:n]...)
}

func writeUint32LE(buf *bytes.Buffer, v uint32) {
	var tmp [4]byte
	tmp[0] = byte(v)
	tmp[1] = byte(v >> 8)
	tmp[2] = byte(v >> 16)
	tmp[3] = byte(v >> 24)
	buf.Write(tmp[:])
}

func writeUint64LE(buf *bytes.Buffer, v uint64) {
	var tmp [8]byte
	tmp[0] = byte(v)
	tmp[1] = byte(v >> 8)
	tmp[2] = byte(v >> 16)
	tmp[3] = byte(v >> 24)
	tmp[4] = byte(v >> 32)
	tmp[5] = byte(v >> 40)
	tmp[6] = byte(v >> 48)
	tmp[7] = byte(v >> 56)
	buf.Write(tmp[:])
}

// serializeCoinbaseTx builds a coinbase with segwit commitments per
// docs/protocols/bip-0141.mediawiki and docs/protocols/bip-0143.mediawiki.
// The trailing string in the scriptSig is the pool's coinbase message; if
// empty, a legacy "/nodeStratum/" tag is used for compatibility.
func serializeCoinbaseTx(height int64, extranonce1, extranonce2 []byte, templateExtraNonce2Size int, payoutScript []byte, coinbaseValue int64, witnessCommitment string, coinbaseFlags string, coinbaseMsg string, scriptTime int64) ([]byte, []byte, error) {
	var flagsBytes []byte
	if coinbaseFlags != "" {
		b, err := hex.DecodeString(coinbaseFlags)
		if err != nil {
			return nil, nil, fmt.Errorf("decode coinbase flags: %w", err)
		}
		flagsBytes = b
	}
	var commitmentScript []byte
	if witnessCommitment != "" {
		b, err := hex.DecodeString(witnessCommitment)
		if err != nil {
			return nil, nil, fmt.Errorf("decode witness commitment: %w", err)
		}
		commitmentScript = b
	}
	return serializeCoinbaseTxPredecoded(height, extranonce1, extranonce2, templateExtraNonce2Size, payoutScript, coinbaseValue, commitmentScript, flagsBytes, coinbaseMsg, scriptTime)
}

// serializeCoinbaseTxPredecoded is the hot-path variant that reuses
// pre-decoded flags/commitment bytes.
func serializeCoinbaseTxPredecoded(height int64, extranonce1, extranonce2 []byte, templateExtraNonce2Size int, payoutScript []byte, coinbaseValue int64, commitmentScript []byte, flagsBytes []byte, coinbaseMsg string, scriptTime int64) ([]byte, []byte, error) {
	if len(payoutScript) == 0 {
		return nil, nil, fmt.Errorf("payout script required")
	}

	padLen := templateExtraNonce2Size - len(extranonce2)
	if padLen < 0 {
		padLen = 0
	}
	placeholderLen := len(extranonce1) + len(extranonce2) + padLen
	extraNoncePlaceholder := bytes.Repeat([]byte{0x00}, placeholderLen)

	scriptSigPart1 := bytes.Join([][]byte{
		serializeNumberScript(height),
		flagsBytes,                        // coinbaseaux.flags from bitcoind
		serializeNumberScript(scriptTime), // stable per job
		{byte(len(extraNoncePlaceholder))},
	}, nil)
	msg := normalizeCoinbaseMessage(coinbaseMsg)
	scriptSigPart2 := serializeStringScript(msg)
	scriptSigLen := len(scriptSigPart1) + padLen + len(extranonce1) + len(extranonce2) + len(scriptSigPart2)

	var vin bytes.Buffer
	writeVarInt(&vin, 1)
	vin.Write(bytes.Repeat([]byte{0x00}, 32))
	writeUint32LE(&vin, 0xffffffff) // NOMP prevout index (-1)
	writeVarInt(&vin, uint64(scriptSigLen))
	vin.Write(scriptSigPart1)
	if padLen > 0 {
		vin.Write(bytes.Repeat([]byte{0x00}, padLen))
	}
	vin.Write(extranonce1)
	vin.Write(extranonce2)
	vin.Write(scriptSigPart2)
	writeUint32LE(&vin, 0) // sequence

	var outputs bytes.Buffer
	outputCount := uint64(1)
	if len(commitmentScript) > 0 {
		outputCount++
	}
	writeVarInt(&outputs, outputCount)
	if len(commitmentScript) > 0 {
		// NOMP writes the witness commitment output first.
		writeUint64LE(&outputs, 0)
		writeVarInt(&outputs, uint64(len(commitmentScript)))
		outputs.Write(commitmentScript)
	}
	writeUint64LE(&outputs, uint64(coinbaseValue))
	writeVarInt(&outputs, uint64(len(payoutScript)))
	outputs.Write(payoutScript)

	var tx bytes.Buffer
	writeUint32LE(&tx, 1) // version
	tx.Write(vin.Bytes())
	tx.Write(outputs.Bytes())
	writeUint32LE(&tx, 0) // locktime

	txid := doubleSHA256(tx.Bytes())
	return tx.Bytes(), txid, nil
}

// serializeDualCoinbaseTx builds a coinbase transaction that splits the block
// reward between a pool-fee output and a worker output. It mirrors
// serializeCoinbaseTx but takes separate scripts and a fee percentage.
func serializeDualCoinbaseTx(height int64, extranonce1, extranonce2 []byte, templateExtraNonce2Size int, poolScript []byte, workerScript []byte, totalValue int64, feePercent float64, witnessCommitment string, coinbaseFlags string, coinbaseMsg string, scriptTime int64) ([]byte, []byte, error) {
	var flagsBytes []byte
	if coinbaseFlags != "" {
		b, err := hex.DecodeString(coinbaseFlags)
		if err != nil {
			return nil, nil, fmt.Errorf("decode coinbase flags: %w", err)
		}
		flagsBytes = b
	}
	var commitmentScript []byte
	if witnessCommitment != "" {
		b, err := hex.DecodeString(witnessCommitment)
		if err != nil {
			return nil, nil, fmt.Errorf("decode witness commitment: %w", err)
		}
		commitmentScript = b
	}
	return serializeDualCoinbaseTxPredecoded(height, extranonce1, extranonce2, templateExtraNonce2Size, poolScript, workerScript, totalValue, feePercent, commitmentScript, flagsBytes, coinbaseMsg, scriptTime)
}

// serializeDualCoinbaseTxPredecoded is the hot-path variant that reuses
// pre-decoded flags/commitment bytes.
func serializeDualCoinbaseTxPredecoded(height int64, extranonce1, extranonce2 []byte, templateExtraNonce2Size int, poolScript []byte, workerScript []byte, totalValue int64, feePercent float64, commitmentScript []byte, flagsBytes []byte, coinbaseMsg string, scriptTime int64) ([]byte, []byte, error) {
	if len(poolScript) == 0 || len(workerScript) == 0 {
		return nil, nil, fmt.Errorf("both pool and worker payout scripts are required")
	}
	if totalValue <= 0 {
		return nil, nil, fmt.Errorf("total coinbase value must be positive")
	}

	padLen := templateExtraNonce2Size - len(extranonce2)
	if padLen < 0 {
		padLen = 0
	}
	placeholderLen := len(extranonce1) + len(extranonce2) + padLen
	extraNoncePlaceholder := bytes.Repeat([]byte{0x00}, placeholderLen)

	scriptSigPart1 := bytes.Join([][]byte{
		serializeNumberScript(height),
		flagsBytes,
		serializeNumberScript(scriptTime),
		{byte(len(extraNoncePlaceholder))},
	}, nil)
	msg := normalizeCoinbaseMessage(coinbaseMsg)
	scriptSigPart2 := serializeStringScript(msg)
	scriptSigLen := len(scriptSigPart1) + padLen + len(extranonce1) + len(extranonce2) + len(scriptSigPart2)

	var vin bytes.Buffer
	writeVarInt(&vin, 1)
	vin.Write(bytes.Repeat([]byte{0x00}, 32))
	writeUint32LE(&vin, 0xffffffff)
	writeVarInt(&vin, uint64(scriptSigLen))
	vin.Write(scriptSigPart1)
	if padLen > 0 {
		vin.Write(bytes.Repeat([]byte{0x00}, padLen))
	}
	vin.Write(extranonce1)
	vin.Write(extranonce2)
	vin.Write(scriptSigPart2)
	writeUint32LE(&vin, 0)

	// Split total value into pool fee and worker payout.
	if feePercent < 0 {
		feePercent = 0
	}
	if feePercent > 99.99 {
		feePercent = 99.99
	}
	poolFee := int64(math.Round(float64(totalValue) * feePercent / 100.0))
	if poolFee < 0 {
		poolFee = 0
	}
	if poolFee > totalValue {
		poolFee = totalValue
	}
	workerValue := totalValue - poolFee
	if workerValue <= 0 {
		return nil, nil, fmt.Errorf("worker payout must be positive after applying pool fee")
	}

	var outputs bytes.Buffer
	outputCount := uint64(2) // pool + worker
	if len(commitmentScript) > 0 {
		outputCount++
	}
	writeVarInt(&outputs, outputCount)
	if len(commitmentScript) > 0 {
		writeUint64LE(&outputs, 0)
		writeVarInt(&outputs, uint64(len(commitmentScript)))
		outputs.Write(commitmentScript)
	}
	// Pool fee output.
	writeUint64LE(&outputs, uint64(poolFee))
	writeVarInt(&outputs, uint64(len(poolScript)))
	outputs.Write(poolScript)
	// Worker payout output.
	writeUint64LE(&outputs, uint64(workerValue))
	writeVarInt(&outputs, uint64(len(workerScript)))
	outputs.Write(workerScript)

	var tx bytes.Buffer
	writeUint32LE(&tx, 1)
	tx.Write(vin.Bytes())
	tx.Write(outputs.Bytes())
	writeUint32LE(&tx, 0)

	txid := doubleSHA256(tx.Bytes())
	return tx.Bytes(), txid, nil
}

// serializeNumberScript matches node-stratum-pool util.serializeNumber.
func serializeNumberScript(n int64) []byte {
	if n >= 1 && n <= 16 {
		return []byte{byte(0x50 + n)}
	}
	l := 1
	buf := make([]byte, 9)
	for n > 0x7f {
		buf[l] = byte(n & 0xff)
		l++
		n >>= 8
	}
	buf[0] = byte(l)
	buf[l] = byte(n)
	return buf[:l+1]
}

// normalizeCoinbaseMessage trims spaces and ensures the message has '/' prefix and suffix.
// If the message is empty after trimming, returns the default "/nodeStratum/" tag.
func normalizeCoinbaseMessage(msg string) string {
	// Trim spaces
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return "/nodeStratum/"
	}
	// Remove existing '/' prefix and suffix
	msg = strings.TrimPrefix(msg, "/")
	msg = strings.TrimSuffix(msg, "/")
	// Add '/' prefix and suffix
	return "/" + msg + "/"
}

// serializeStringScript matches node-stratum-pool util.serializeString.
func serializeStringScript(s string) []byte {
	b := []byte(s)
	if len(b) < 253 {
		return append([]byte{byte(len(b))}, b...)
	}
	if len(b) < 0x10000 {
		out := []byte{253, byte(len(b)), byte(len(b) >> 8)}
		return append(out, b...)
	}
	if len(b) < 0x100000000 {
		out := []byte{254, byte(len(b)), byte(len(b) >> 8), byte(len(b) >> 16), byte(len(b) >> 24)}
		return append(out, b...)
	}
	out := []byte{255}
	out = appendVarInt(out, uint64(len(b)))
	return append(out, b...)
}

func coinbaseScriptSigFixedLen(height int64, scriptTime int64, coinbaseFlags string, extranonce2Size int, templateExtraNonce2Size int) (int, error) {
	flagsBytes := []byte{}
	if coinbaseFlags != "" {
		var err error
		flagsBytes, err = hex.DecodeString(coinbaseFlags)
		if err != nil {
			return 0, fmt.Errorf("decode coinbase flags: %w", err)
		}
	}
	if templateExtraNonce2Size < extranonce2Size {
		templateExtraNonce2Size = extranonce2Size
	}
	padLen := templateExtraNonce2Size - extranonce2Size
	if padLen < 0 {
		padLen = 0
	}
	partLen := len(serializeNumberScript(height)) + len(flagsBytes) + len(serializeNumberScript(scriptTime)) + 1
	return partLen + padLen + coinbaseExtranonce1Size + extranonce2Size, nil
}

func clampCoinbaseMessage(message string, limit int, height int64, scriptTime int64, coinbaseFlags string, extranonce2Size int, templateExtraNonce2Size int) (string, bool, error) {
	if limit <= 0 {
		return message, false, nil
	}
	fixedLen, err := coinbaseScriptSigFixedLen(height, scriptTime, coinbaseFlags, extranonce2Size, templateExtraNonce2Size)
	if err != nil {
		return "", false, err
	}
	allowed := limit - fixedLen
	if allowed <= 0 {
		return "", true, nil
	}

	normalized := normalizeCoinbaseMessage(message)
	body := ""
	if len(normalized) > 2 {
		body = normalized[1 : len(normalized)-1]
	}
	if len(serializeStringScript(normalized)) <= allowed {
		return body, false, nil
	}
	for len(body) > 0 {
		body = body[:len(body)-1]
		candidate := "/" + body + "/"
		if len(serializeStringScript(candidate)) <= allowed {
			return body, true, nil
		}
	}
	// Fallback to the default tag if we trimmed everything.
	defaultNormalized := normalizeCoinbaseMessage("")
	if len(serializeStringScript(defaultNormalized)) <= allowed {
		return "", true, nil
	}
	return "", true, nil
}

// buildCoinbaseParts constructs coinb1/coinb2 using the node-stratum-pool layout.
// The trailing string in the scriptSig is the pool's coinbase message; if
// empty, a legacy "/nodeStratum/" tag is used for compatibility.
func buildCoinbaseParts(height int64, extranonce1 []byte, extranonce2Size int, templateExtraNonce2Size int, payoutScript []byte, coinbaseValue int64, witnessCommitment string, coinbaseFlags string, coinbaseMsg string, scriptTime int64) (string, string, error) {
	if extranonce2Size <= 0 {
		extranonce2Size = 4
	}
	if templateExtraNonce2Size < extranonce2Size {
		templateExtraNonce2Size = extranonce2Size
	}
	templatePlaceholderLen := len(extranonce1) + templateExtraNonce2Size
	extraNoncePlaceholder := bytes.Repeat([]byte{0x00}, templatePlaceholderLen)
	padLen := templateExtraNonce2Size - extranonce2Size

	// Decode coinbase aux flags from bitcoind (per BIP34 and node-stratum-pool).
	var flagsBytes []byte
	if coinbaseFlags != "" {
		var err error
		flagsBytes, err = hex.DecodeString(coinbaseFlags)
		if err != nil {
			return "", "", fmt.Errorf("decode coinbase flags: %w", err)
		}
	}

	scriptSigPart1 := bytes.Join([][]byte{
		serializeNumberScript(height),
		flagsBytes, // coinbaseaux.flags from bitcoind
		serializeNumberScript(scriptTime),
		{byte(len(extraNoncePlaceholder))},
	}, nil)
	msg := normalizeCoinbaseMessage(coinbaseMsg)
	scriptSigPart2 := serializeStringScript(msg)

	// p1: version || input count || prevout || scriptsig length || scriptsig_part1
	var p1 bytes.Buffer
	writeUint32LE(&p1, 1) // tx version
	writeVarInt(&p1, 1)
	p1.Write(bytes.Repeat([]byte{0x00}, 32)) // prev hash
	writeUint32LE(&p1, 0xffffffff)           // prev index
	writeVarInt(&p1, uint64(len(scriptSigPart1)+len(extraNoncePlaceholder)+len(scriptSigPart2)))
	p1.Write(scriptSigPart1)

	// Outputs
	var outputs bytes.Buffer
	outputCount := uint64(1)
	if witnessCommitment != "" {
		outputCount++
	}
	writeVarInt(&outputs, outputCount)
	if witnessCommitment != "" {
		commitmentScript, err := hex.DecodeString(witnessCommitment)
		if err != nil {
			return "", "", fmt.Errorf("decode witness commitment: %w", err)
		}
		writeUint64LE(&outputs, 0)
		writeVarInt(&outputs, uint64(len(commitmentScript)))
		outputs.Write(commitmentScript)
	}
	writeUint64LE(&outputs, uint64(coinbaseValue))
	writeVarInt(&outputs, uint64(len(payoutScript)))
	outputs.Write(payoutScript)

	// p2: scriptSig_part2 || sequence || outputs || locktime
	var p2 bytes.Buffer
	p2.Write(scriptSigPart2)
	writeUint32LE(&p2, 0) // sequence
	p2.Write(outputs.Bytes())
	writeUint32LE(&p2, 0) // locktime

	coinb1 := hex.EncodeToString(p1.Bytes())
	if padLen > 0 {
		coinb1 += strings.Repeat("00", padLen)
	}
	coinb2 := hex.EncodeToString(p2.Bytes())
	return coinb1, coinb2, nil
}

// buildDualPayoutCoinbaseParts constructs coinbase parts for a dual-payout
// layout where the block reward is split between a pool-fee output and a
// worker output. It mirrors buildCoinbaseParts but takes separate scripts for
// the pool and worker, along with a fee percentage, and is used by
// MinerConn.sendNotifyFor when dual-payout parameters are available.
func buildDualPayoutCoinbaseParts(height int64, extranonce1 []byte, extranonce2Size int, templateExtraNonce2Size int, poolScript []byte, workerScript []byte, totalValue int64, feePercent float64, witnessCommitment string, coinbaseFlags string, coinbaseMsg string, scriptTime int64) (string, string, error) {
	if len(poolScript) == 0 || len(workerScript) == 0 {
		return "", "", fmt.Errorf("both pool and worker payout scripts are required")
	}
	if extranonce2Size <= 0 {
		extranonce2Size = 4
	}
	if templateExtraNonce2Size < extranonce2Size {
		templateExtraNonce2Size = extranonce2Size
	}
	templatePlaceholderLen := len(extranonce1) + templateExtraNonce2Size
	extraNoncePlaceholder := bytes.Repeat([]byte{0x00}, templatePlaceholderLen)
	padLen := templateExtraNonce2Size - extranonce2Size

	// Decode coinbase aux flags from bitcoind (per BIP34 and node-stratum-pool).
	var flagsBytes []byte
	if coinbaseFlags != "" {
		var err error
		flagsBytes, err = hex.DecodeString(coinbaseFlags)
		if err != nil {
			return "", "", fmt.Errorf("decode coinbase flags: %w", err)
		}
	}

	scriptSigPart1 := bytes.Join([][]byte{
		serializeNumberScript(height),
		flagsBytes, // coinbaseaux.flags from bitcoind
		serializeNumberScript(scriptTime),
		{byte(len(extraNoncePlaceholder))},
	}, nil)
	msg := normalizeCoinbaseMessage(coinbaseMsg)
	scriptSigPart2 := serializeStringScript(msg)

	// p1: version || input count || prevout || scriptsig length || scriptsig_part1
	var p1 bytes.Buffer
	writeUint32LE(&p1, 1) // tx version
	writeVarInt(&p1, 1)
	p1.Write(bytes.Repeat([]byte{0x00}, 32)) // prev hash
	writeUint32LE(&p1, 0xffffffff)           // prev index
	writeVarInt(&p1, uint64(len(scriptSigPart1)+len(extraNoncePlaceholder)+len(scriptSigPart2)))
	p1.Write(scriptSigPart1)

	// Split total value into pool fee and worker payout.
	if totalValue <= 0 {
		return "", "", fmt.Errorf("total coinbase value must be positive")
	}
	if feePercent < 0 {
		feePercent = 0
	}
	if feePercent > 99.99 {
		feePercent = 99.99
	}
	poolFee := int64(math.Round(float64(totalValue) * feePercent / 100.0))
	if poolFee < 0 {
		poolFee = 0
	}
	if poolFee > totalValue {
		poolFee = totalValue
	}
	workerValue := totalValue - poolFee
	if workerValue <= 0 {
		return "", "", fmt.Errorf("worker payout must be positive after applying pool fee")
	}

	// Outputs: optional witness commitment, then pool-fee output, then worker output.
	var outputs bytes.Buffer
	outputCount := uint64(2) // pool + worker
	if witnessCommitment != "" {
		outputCount++
	}
	writeVarInt(&outputs, outputCount)
	if witnessCommitment != "" {
		commitmentScript, err := hex.DecodeString(witnessCommitment)
		if err != nil {
			return "", "", fmt.Errorf("decode witness commitment: %w", err)
		}
		writeUint64LE(&outputs, 0)
		writeVarInt(&outputs, uint64(len(commitmentScript)))
		outputs.Write(commitmentScript)
	}
	// Pool fee output.
	writeUint64LE(&outputs, uint64(poolFee))
	writeVarInt(&outputs, uint64(len(poolScript)))
	outputs.Write(poolScript)
	// Worker payout output.
	writeUint64LE(&outputs, uint64(workerValue))
	writeVarInt(&outputs, uint64(len(workerScript)))
	outputs.Write(workerScript)

	// p2: scriptSig_part2 || sequence || outputs || locktime
	var p2 bytes.Buffer
	p2.Write(scriptSigPart2)
	writeUint32LE(&p2, 0) // sequence
	p2.Write(outputs.Bytes())
	writeUint32LE(&p2, 0) // locktime

	coinb1 := hex.EncodeToString(p1.Bytes())
	if padLen > 0 {
		coinb1 += strings.Repeat("00", padLen)
	}
	coinb2 := hex.EncodeToString(p2.Bytes())
	return coinb1, coinb2, nil
}

func buildMerkleBranches(txids [][]byte) []string {
	if len(txids) == 0 {
		return []string{}
	}
	// Emulate NOMP merkleTree.calculateSteps: data = [nil] + txids (LE).
	layer := make([][]byte, 0, len(txids)+1)
	layer = append(layer, nil)
	layer = append(layer, txids...)

	var steps []string
	L := len(layer)
	for L > 1 {
		steps = append(steps, hex.EncodeToString(layer[1]))
		if L%2 == 1 {
			layer = append(layer, layer[L-1])
			L++
		}
		next := make([][]byte, 0, L/2)
		for i := 1; i+1 < L; i += 2 {
			joined := append(append([]byte{}, layer[i]...), layer[i+1]...)
			next = append(next, doubleSHA256(joined))
		}
		layer = append([][]byte{nil}, next...)
		L = len(layer)
	}
	return steps
}

func appendMust(dst []byte, more ...[]byte) []byte {
	for _, m := range more {
		dst = append(dst, m...)
	}
	return dst
}

// computeMerkleRootFromBranches mirrors NOMP merkleTree.withFirst: start with the
// coinbase txid (BE) and apply each branch (LE) in order, returning a BE root.
func computeMerkleRootFromBranches(coinbaseHash []byte, branches []string) []byte {
	root := coinbaseHash
	var hashBuf [32]byte
	var concatBuf [64]byte
	for _, b := range branches {
		if len(b) != 64 {
			return nil
		}
		n, err := hex.Decode(hashBuf[:], []byte(b))
		if err != nil || n != 32 {
			return nil
		}
		copy(concatBuf[:32], root)
		copy(concatBuf[32:], hashBuf[:])
		root = doubleSHA256(concatBuf[:])
	}
	return root
}

// buildBlockHeaderFromHex constructs the block header bytes for SHA256d jobs
// using the same layout as foundation-stratum's Template.serializeHeader
// default branch. This differs from the canonical Bitcoin header layout but
// matches what miners expect from the reference pool:
//
//	header[0:4]   = nonce (BE hex from miner)
//	header[4:8]   = bits  (BE hex from template)
//	header[8:12]  = ntime (BE hex from miner)
//	header[12:44] = merkleRoot (LE bytes)
//	header[44:76] = previousblockhash (BE bytes from template)
//	header[76:80] = version (big-endian uint32)
//
// The entire 80-byte header is then reversed before hashing.
//
// This helper is intended for test and block-construction paths where the
// previous block hash and bits fields are available only as hex strings. For
// hot share-validation paths that already have a Job, prefer Job.buildBlockHeader,
// which reuses pre-decoded header fields.
func buildBlockHeaderFromHex(version int32, prevhash string, merkleRootBE []byte, ntimeHex string, bitsHex string, nonceHex string) ([]byte, error) {
	if len(merkleRootBE) != 32 {
		return nil, fmt.Errorf("merkle root must be 32 bytes")
	}

	// Use static arrays to avoid allocations
	var prev [32]byte
	var ntimeBytes [4]byte
	var bitsBytes [4]byte
	var nonceBytes [4]byte
	var hdr [80]byte
	var merkleReversed [32]byte

	// Decode prevhash
	if len(prevhash) != 64 {
		return nil, fmt.Errorf("prevhash hex must be 64 chars")
	}
	n, err := hex.Decode(prev[:], []byte(prevhash))
	if err != nil || n != 32 {
		return nil, fmt.Errorf("decode prevhash: %w", err)
	}

	// Decode ntime
	if len(ntimeHex) != 8 {
		return nil, fmt.Errorf("ntime hex must be 8 chars")
	}
	n, err = hex.Decode(ntimeBytes[:], []byte(ntimeHex))
	if err != nil || n != 4 {
		return nil, fmt.Errorf("decode ntime: %w", err)
	}

	// Decode bits
	if len(bitsHex) != 8 {
		return nil, fmt.Errorf("bits hex must be 8 chars")
	}
	n, err = hex.Decode(bitsBytes[:], []byte(bitsHex))
	if err != nil || n != 4 {
		return nil, fmt.Errorf("decode bits: %w", err)
	}

	// Decode nonce
	if len(nonceHex) != 8 {
		return nil, fmt.Errorf("nonce hex must be 8 chars")
	}
	n, err = hex.Decode(nonceBytes[:], []byte(nonceHex))
	if err != nil || n != 4 {
		return nil, fmt.Errorf("decode nonce: %w", err)
	}

	// Reverse merkle root in place
	for i := 0; i < 32; i++ {
		merkleReversed[i] = merkleRootBE[31-i]
	}

	// Build header
	copy(hdr[0:4], nonceBytes[:])
	copy(hdr[4:8], bitsBytes[:])
	copy(hdr[8:12], ntimeBytes[:])
	copy(hdr[12:44], merkleReversed[:])
	copy(hdr[44:76], prev[:])
	uver := uint32(version)
	hdr[76] = byte(uver >> 24)
	hdr[77] = byte(uver >> 16)
	hdr[78] = byte(uver >> 8)
	hdr[79] = byte(uver)

	// Foundation/template.serializeHeader reverses the entire header buffer
	// before hashing; mirror that here. Reverse in place.
	for i := 0; i < 40; i++ {
		hdr[i], hdr[79-i] = hdr[79-i], hdr[i]
	}

	return hdr[:], nil
}

// buildBlockHeader constructs the block header bytes using precomputed per-job
// header pieces (previous block hash bytes and bits bytes) stored on Job. It
// avoids redundant hex decoding on every share submission and is used on
// performance-sensitive paths such as share validation and submitblock rebuilds.
func (job *Job) buildBlockHeader(merkleRootBE []byte, ntimeHex string, nonceHex string, version int32) ([]byte, error) {
	if len(merkleRootBE) != 32 {
		return nil, fmt.Errorf("merkle root must be 32 bytes")
	}

	var ntimeBytes [4]byte
	var nonceBytes [4]byte
	var hdr [80]byte
	var merkleReversed [32]byte

	// Decode ntime
	if len(ntimeHex) != 8 {
		return nil, fmt.Errorf("ntime hex must be 8 chars")
	}
	n, err := hex.Decode(ntimeBytes[:], []byte(ntimeHex))
	if err != nil || n != 4 {
		return nil, fmt.Errorf("decode ntime: %w", err)
	}

	// Decode nonce
	if len(nonceHex) != 8 {
		return nil, fmt.Errorf("nonce hex must be 8 chars")
	}
	n, err = hex.Decode(nonceBytes[:], []byte(nonceHex))
	if err != nil || n != 4 {
		return nil, fmt.Errorf("decode nonce: %w", err)
	}

	// Reverse merkle root in place
	for i := 0; i < 32; i++ {
		merkleReversed[i] = merkleRootBE[31-i]
	}

	// Build header using precomputed prevHashBytes and bitsBytes.
	copy(hdr[0:4], nonceBytes[:])
	copy(hdr[4:8], job.bitsBytes[:])
	copy(hdr[8:12], ntimeBytes[:])
	copy(hdr[12:44], merkleReversed[:])
	copy(hdr[44:76], job.prevHashBytes[:])
	uver := uint32(version)
	hdr[76] = byte(uver >> 24)
	hdr[77] = byte(uver >> 16)
	hdr[78] = byte(uver >> 8)
	hdr[79] = byte(uver)

	for i := 0; i < 40; i++ {
		hdr[i], hdr[79-i] = hdr[79-i], hdr[i]
	}

	return hdr[:], nil
}

func buildBlock(job *Job, extranonce1 []byte, extranonce2 []byte, ntimeHex string, nonceHex string, version int32) (string, []byte, []byte, []byte, error) {
	if len(extranonce2) != job.Extranonce2Size {
		return "", nil, nil, nil, fmt.Errorf("extranonce2 must be %d bytes", job.Extranonce2Size)
	}

	coinbaseTx, coinbaseTxid, err := serializeCoinbaseTx(job.Template.Height, extranonce1, extranonce2, job.TemplateExtraNonce2Size, job.PayoutScript, job.CoinbaseValue, job.WitnessCommitment, job.Template.CoinbaseAux.Flags, job.CoinbaseMsg, job.ScriptTime)
	if err != nil {
		return "", nil, nil, nil, fmt.Errorf("coinbase build: %w", err)
	}

	// Build the raw merkle root from the coinbase txid plus the tx list.
	// coinbaseTxid is canonical (big-endian); txids are little-endian.
	merkleRootBE := computeMerkleRootFromBranches(coinbaseTxid, job.MerkleBranches)

	header, err := buildBlockHeaderFromHex(version, job.Template.Previous, merkleRootBE, ntimeHex, job.Template.Bits, nonceHex)
	if err != nil {
		return "", nil, nil, nil, err
	}

	buf := blockBufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer blockBufferPool.Put(buf)

	buf.Write(header)
	writeVarInt(buf, uint64(1+len(job.Transactions)))
	buf.Write(coinbaseTx)

	for _, tx := range job.Transactions {
		raw, err := hex.DecodeString(tx.Data)
		if err != nil {
			return "", nil, nil, nil, fmt.Errorf("decode tx data: %w", err)
		}
		buf.Write(raw)
	}

	blockHex := hex.EncodeToString(buf.Bytes())
	headerHash := doubleSHA256(header)
	return blockHex, headerHash, header, merkleRootBE, nil
}
