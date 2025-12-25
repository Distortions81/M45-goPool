package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/remeh/sizedwaitgroup"
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
	DonationScript          []byte
	OperatorDonationPercent float64
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
	jobSubscriberBuffer     = 4
	coinbaseExtranonce1Size = 4
	jobRetryDelay           = 100 * time.Millisecond
)

var errStaleTemplate = errors.New("stale template")

// blockBufferPool reuses buffers for raw block assembly.
var blockBufferPool = sync.Pool{
	New: func() interface{} {
		return new(bytes.Buffer)
	},
}

type JobFeedPayloadStatus struct {
	LastRawBlockAt    time.Time
	LastRawBlockBytes int
	LastHashTx        string
	LastHashTxAt      time.Time
	LastRawTxAt       time.Time
	LastRawTxBytes    int
	BlockTip          ZMQBlockTip
	RecentBlockTimes  []time.Time // Last 4 block times
	BlockTimerActive  bool        // Whether block timer should count down (only after first new block)
}

type ZMQBlockTip struct {
	Hash       string
	Height     int64
	Time       time.Time
	Bits       string
	Difficulty float64
}

const jobFeedErrorHistorySize = 3

type JobManager struct {
	rpc               *RPCClient
	cfg               Config
	mu                sync.RWMutex
	curJob            *Job
	payoutScript      []byte
	donationScript    []byte
	extraID           uint32
	subs              map[chan *Job]struct{}
	subsMu            sync.Mutex
	zmqHealthy        atomic.Bool
	zmqDisconnects    uint64
	zmqReconnects     uint64
	lastErrMu         sync.RWMutex
	lastErr           error
	lastErrAt         time.Time
	lastJobSuccess    time.Time
	jobFeedErrHistory []string
	// Refresh coordination to prevent duplicate refreshes from longpoll/ZMQ
	refreshMu          sync.Mutex
	lastRefreshAttempt time.Time
	zmqPayload         JobFeedPayloadStatus
	zmqPayloadMu       sync.RWMutex
	// Async notification queue
	notifyQueue chan *Job
	notifyWg    sizedwaitgroup.SizedWaitGroup
}

func NewJobManager(rpc *RPCClient, cfg Config, payoutScript []byte, donationScript []byte) *JobManager {
	return &JobManager{
		rpc:            rpc,
		cfg:            cfg,
		payoutScript:   payoutScript,
		donationScript: donationScript,
		subs:           make(map[chan *Job]struct{}),
		notifyQueue:    make(chan *Job, 100), // Buffered queue for async notifications
	}
}

type JobFeedStatus struct {
	Ready          bool
	LastSuccess    time.Time
	LastError      error
	LastErrorAt    time.Time
	ErrorHistory   []string
	ZMQHealthy     bool
	ZMQDisconnects uint64
	ZMQReconnects  uint64
	Payload        JobFeedPayloadStatus
}

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
	errorHistory := append([]string(nil), jm.jobFeedErrHistory...)
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
		ErrorHistory:   errorHistory,
		ZMQHealthy:     jm.zmqHealthy.Load(),
		ZMQDisconnects: atomic.LoadUint64(&jm.zmqDisconnects),
		ZMQReconnects:  atomic.LoadUint64(&jm.zmqReconnects),
		Payload:        jm.payloadStatus(),
	}
}

func (jm *JobManager) updateBlockTipFromTemplate(tpl GetBlockTemplateResult) {
	if !jm.shouldUseLongpollFallback() {
		return
	}
	if tpl.Height <= 0 {
		return
	}

	jm.zmqPayloadMu.Lock()
	defer jm.zmqPayloadMu.Unlock()

	tip := jm.zmqPayload.BlockTip
	if tip.Height == 0 || tpl.Height > tip.Height {
		tip.Height = tpl.Height
	}
	if tpl.CurTime > 0 {
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
}

func (jm *JobManager) recordRawBlockPayload(size int) {
	jm.zmqPayloadMu.Lock()
	jm.zmqPayload.LastRawBlockAt = time.Now()
	jm.zmqPayload.LastRawBlockBytes = size
	jm.zmqPayloadMu.Unlock()
}

func (jm *JobManager) recordBlockTip(tip ZMQBlockTip) {
	jm.zmqPayloadMu.Lock()
	defer jm.zmqPayloadMu.Unlock()

	// Check if this is a new block (different from current block tip)
	isNewBlock := jm.zmqPayload.BlockTip.Height == 0 ||
		(tip.Height > jm.zmqPayload.BlockTip.Height) ||
		(tip.Hash != "" && tip.Hash != jm.zmqPayload.BlockTip.Hash)

	jm.zmqPayload.BlockTip = tip

	// Track recent block times (keep last 4)
	if !tip.Time.IsZero() {
		// Only add if this is a new block (different from the last one)
		if len(jm.zmqPayload.RecentBlockTimes) == 0 ||
			!jm.zmqPayload.RecentBlockTimes[len(jm.zmqPayload.RecentBlockTimes)-1].Equal(tip.Time) {
			jm.zmqPayload.RecentBlockTimes = append(jm.zmqPayload.RecentBlockTimes, tip.Time)
			if len(jm.zmqPayload.RecentBlockTimes) > 4 {
				jm.zmqPayload.RecentBlockTimes = jm.zmqPayload.RecentBlockTimes[len(jm.zmqPayload.RecentBlockTimes)-4:]
			}
			// Activate the block timer when we see a new block
			if isNewBlock {
				jm.zmqPayload.BlockTimerActive = true
			}
		}
	}
}

func (jm *JobManager) recordHashTx(hash string) {
	if hash == "" {
		return
	}
	jm.zmqPayloadMu.Lock()
	jm.zmqPayload.LastHashTx = hash
	jm.zmqPayload.LastHashTxAt = time.Now()
	jm.zmqPayloadMu.Unlock()
}

func (jm *JobManager) recordRawTxPayload(size int) {
	jm.zmqPayloadMu.Lock()
	jm.zmqPayload.LastRawTxAt = time.Now()
	jm.zmqPayload.LastRawTxBytes = size
	jm.zmqPayloadMu.Unlock()
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

	// Keep only the last 3 timestamps (current + up to 2 previous)
	if len(recentTimes) > 3 {
		recentTimes = recentTimes[len(recentTimes)-3:]
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
	for i := 0; i < numWorkers; i++ {
		jm.notifyWg.Add()
		go jm.notificationWorker(ctx, i)
	}
	logger.Info("started async notification workers", "count", numWorkers)

	// Fetch initial block info from the blockchain to start the timer immediately
	jm.fetchInitialBlockInfo(ctx)

	if err := jm.refreshJobCtx(ctx); err != nil {
		logger.Error("initial job refresh error", "error", err)
	}

	go jm.longpollLoop(ctx)
	if jm.cfg.ZMQBlockAddr != "" {
		go jm.zmqBlockLoop(ctx)
	}
}

func (jm *JobManager) refreshJobCtx(ctx context.Context) error {
	jm.refreshMu.Lock()
	if time.Since(jm.lastRefreshAttempt) < 100*time.Millisecond {
		jm.refreshMu.Unlock()
		return nil
	}
	jm.lastRefreshAttempt = time.Now()
	jm.refreshMu.Unlock()

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
	jm.updateBlockTipFromTemplate(tpl)
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
		msg, err := buildCoinbaseMsgWithSuffix(coinbaseMsg, jm.cfg.CoinbasePoolTag, jm.cfg.CoinbaseSuffixBytes)
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
		DonationScript:          jm.donationScript,
		OperatorDonationPercent: jm.cfg.OperatorDonationPercent,
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

func buildCoinbaseMsgWithSuffix(base, poolTag string, suffixChars int) (string, error) {
	suffix, err := buildPoolSuffix(poolTag, suffixChars)
	if err != nil {
		return "", fmt.Errorf("coinbase suffix: %w", err)
	}
	if base == "" {
		return suffix, nil
	}
	if suffix == "" {
		return base, nil
	}
	return fmt.Sprintf("%s-%s", base, suffix), nil
}

func buildPoolSuffix(poolTag string, suffixChars int) (string, error) {
	if suffixChars < 0 {
		suffixChars = 0
	}
	randomPart := ""
	if suffixChars > 0 {
		part, err := randomAlnumString(suffixChars)
		if err != nil {
			return "", err
		}
		randomPart = part
	}
	if poolTag == "" {
		return randomPart, nil
	}
	return poolTag + randomPart, nil
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
	// Treat input as 8 big-endian uint32 words, rewrite each as little-endian,
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
	txids := make([][]byte, len(txs)) // Pre-allocate exact size since we know we'll add all txs
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

		txids[i] = reverseBytes(computedRaw)
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
	var buf [4]byte // Use fixed-size array instead of slice allocation
	binary.BigEndian.PutUint32(buf[:], id)
	return buf[:]
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
	// Queue the job for async distribution instead of blocking here
	select {
	case jm.notifyQueue <- job:
		// Successfully queued for async processing
	default:
		// Queue is full, fall back to synchronous broadcast
		logger.Warn("notification queue full, falling back to sync broadcast")
		jm.broadcastJobSync(job)
	}
}

// broadcastJobSync performs synchronous job notification (fallback only)
func (jm *JobManager) broadcastJobSync(job *Job) {
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

// notificationWorker processes job notifications asynchronously
func (jm *JobManager) notificationWorker(ctx context.Context, workerID int) {
	defer jm.notifyWg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case job, ok := <-jm.notifyQueue:
			if !ok {
				return
			}

			// Send directly to all subscribers - no snapshot needed since
			// the send operations are non-blocking (select/default)
			jm.subsMu.Lock()
			blocked := 0
			subscriberCount := len(jm.subs)
			for ch := range jm.subs {
				select {
				case ch <- job:
					// Successfully sent
				default:
					// Channel full, drop the notification
					blocked++
				}
			}
			jm.subsMu.Unlock()

			if blocked > 0 {
				logger.Warn("job broadcast blocked; dropping update", "worker", workerID, "subscribers", subscriberCount, "blocked", blocked)
			}
		}
	}
}
