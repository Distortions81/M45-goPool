package main

import (
	"bufio"
	"context"
	"math/big"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

type StratumRequest struct {
	ID     any    `json:"id"`
	Method string `json:"method"`
	Params []any  `json:"params"`
}

type StratumResponse struct {
	ID     any `json:"id"`
	Result any `json:"result"`
	Error  any `json:"error"`
}

func newStratumError(code int, msg string) []any {
	return []any{code, msg, nil}
}

type VarDiffConfig struct {
	MinDiff            float64
	MaxDiff            float64
	TargetSharesPerMin float64
	AdjustmentWindow   time.Duration
	// RetargetDelay is a minimum cooldown between vardiff decisions so miners
	// have time to refill work queues and settle clocks after changes.
	RetargetDelay time.Duration
	Step          float64
	// DampingFactor controls how aggressively vardiff moves toward target.
	// 1.0 = full correction (old behavior), 0.5 = move halfway, etc.
	// Lower values reduce overshoot. Typical range: 0.5-0.85.
	DampingFactor float64
}

type MinerStats struct {
	Worker            string
	WorkerSHA256      string
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

type notifiedCoinbaseParts struct {
	coinb1 string
	coinb2 string
}

var defaultVarDiff = VarDiffConfig{
	MinDiff:            defaultMinDifficulty,
	MaxDiff:            defaultMaxDifficulty,
	TargetSharesPerMin: defaultVarDiffTargetSharesPerMin, // aim for roughly one share every 12s
	AdjustmentWindow:   defaultVarDiffAdjustmentWindow,
	RetargetDelay:      defaultVarDiffRetargetDelay,
	Step:               defaultVarDiffStep,
	DampingFactor:      defaultVarDiffDampingFactor, // move 70% toward target for faster convergence
}

type MinerConn struct {
	id                   string
	ctx                  context.Context
	conn                 net.Conn
	writeMu              sync.Mutex
	writeScratch         []byte
	reader               *bufio.Reader
	jobMgr               *JobManager
	rpc                  rpcCaller
	cfg                  Config
	extranonce1          []byte
	extranonce1Hex       string
	jobCh                chan *Job
	difficulty           atomic.Uint64 // float64 stored as bits
	previousDifficulty   atomic.Uint64 // float64 stored as bits
	shareTarget          atomic.Pointer[big.Int]
	lastDiffChange       atomic.Int64 // Unix nanos
	stateMu              sync.Mutex
	listenerOn           bool
	stats                MinerStats
	statsMu              sync.Mutex
	initWorkMu           sync.Mutex
	statsUpdates         chan statsUpdate // Buffered channel for async stats updates
	statsWg              sync.WaitGroup   // Wait for stats worker to finish
	vardiff              VarDiffConfig
	metrics              *PoolMetrics
	accounting           *AccountStore
	workerRegistry       *workerConnectionRegistry
	savedWorkerStore     *workerListStore
	discordNotifier      *discordNotifier
	savedWorkerTracked   bool
	savedWorkerBestDiff  float64
	registeredWorker     string
	registeredWorkerHash string
	jobMu                sync.Mutex
	activeJobs           map[string]*Job
	jobOrder             []string
	maxRecentJobs        int
	shareCache           map[string]*duplicateShareSet
	evictedShareCache    map[string]*evictedCacheEntry
	lastJob              *Job
	lastJobPrevHash      string
	lastJobHeight        int64
	lastClean            bool
	notifySeq            uint64 // Incremented each job notification to ensure unique coinbase
	jobScriptTime        map[string]int64
	jobNotifyCoinbase    map[string]notifiedCoinbaseParts
	jobNTimeBounds       map[string]jobNTimeBounds
	banUntil             time.Time
	banReason            string
	lastPenalty          time.Time
	invalidSubs          int
	validSubsForBan      int
	lastProtoViolation   time.Time
	protoViolations      int
	versionRoll          bool
	versionMask          uint32
	poolMask             uint32
	minerMask            uint32
	minVerBits           int
	lastShareHash        string
	lastShareAccepted    bool
	lastShareDifficulty  float64
	lastShareDetail      *ShareDetail
	lastRejectReason     string
	walletMu             sync.Mutex
	workerWallets        map[string]workerWalletState
	subscribed           bool
	authorized           bool
	cleanupOnce          sync.Once
	// If true, VarDiff adjustments are disabled for this miner and the
	// current difficulty is treated as fixed (typically from suggest_difficulty).
	lockDifficulty bool
	// vardiffAdjustments counts applied VarDiff difficulty changes for this
	// connection so startup can use larger initial correction steps.
	vardiffAdjustments atomic.Int32
	// vardiffPendingDirection/vardiffPendingCount debounce retarget decisions
	// after bootstrap so random share noise does not cause constant churn.
	// direction: -1 down, +1 up, 0 unset.
	vardiffPendingDirection atomic.Int32
	vardiffPendingCount     atomic.Int32
	// vardiffUpwardCooldownUntil blocks repeat upward retargets for a short
	// cooldown after a large upward jump to avoid stacked overshoots.
	vardiffUpwardCooldownUntil atomic.Int64
	// vardiffWarmupHighLatencyStreak tracks persistent windows where work-start
	// latency p95 is high; used for a small downward difficulty bias.
	vardiffWarmupHighLatencyStreak atomic.Int32
	// bootstrapDone tracks whether we've already performed the initial
	// "bootstrap" vardiff move for this connection.
	bootstrapDone bool
	// restoredRecentDiff is set when we restore a worker's persisted
	// difficulty after a short disconnect so we can skip bootstrap and
	// suggested-difficulty overrides on reconnect.
	restoredRecentDiff   bool
	minerType            string
	minerClientName      string
	minerClientVersion   string
	extranonceSubscribed bool
	// connectedAt is the time this miner connection was established,
	// used as the zero point for per-share timing in detail logs.
	connectedAt time.Time
	// lastActivity tracks when we last saw a RPC message from this miner.
	lastActivity time.Time
	// stratumMsgWindowStart/stratumMsgCount track per-connection Stratum message rate.
	// stratumMsgCount stores weighted half-message units (2 = full message).
	stratumMsgWindowStart time.Time
	stratumMsgCount       int
	// lastHashrateUpdate tracks the last time we updated the per-connection
	// hashrate EMA so we can apply a time-based decay between shares.
	lastHashrateUpdate time.Time
	// hashrateSampleCount counts how many shares have been recorded since the
	// last EMA update so we can ensure the window spans enough work.
	hashrateSampleCount int
	// hashrateAccumulatedDiff accumulates credited difficulties between samples.
	hashrateAccumulatedDiff float64
	// submitRTTSamplesMs keeps a small rolling window of submit processing RTT
	// estimates (server-side receive -> response write complete), in ms.
	submitRTTSamplesMs [64]float64
	submitRTTCount     int
	submitRTTIndex     int
	// notifySentAt / notifyAwaitingFirstShare track notify->first-share latency.
	notifySentAt             time.Time
	notifyAwaitingFirstShare bool
	lastNotifyToFirstShareMs float64
	notifyToFirstSamplesMs   [64]float64
	notifyToFirstCount       int
	notifyToFirstIndex       int
	// recentSubmissionKinds tracks a rolling stale-reject ratio for VarDiff.
	// 0 = accepted/other reject, 1 = stale reject.
	recentSubmissionKinds  [128]uint8
	recentSubmissionCount  int
	recentSubmissionIndex  int
	recentStaleRejectCount int
	pingRTTSamplesMs       [64]float64
	pingRTTCount           int
	pingRTTIndex           int
	// jobDifficulty records the difficulty in effect when each job notify
	// was sent to this miner so we can credit shares with the assigned
	// target even if vardiff changes before the share arrives.
	jobDifficulty map[string]float64
	// rollingHashrateValue holds the current EMA-smoothed hashrate estimate
	// for this connection, derived from accepted work over time.
	rollingHashrateValue float64
	// rollingHashrateControl is a faster EMA used by VarDiff control decisions.
	rollingHashrateControl float64
	// initialEMAWindowDone marks that the first (bootstrap) EMA window has
	// completed; after this, configured tau is used.
	initialEMAWindowDone atomic.Bool
	// windowResetAnchor stores when the current sampling window was reset so
	// the first post-reset share can anchor WindowStart midway between reset
	// time and first-share time.
	windowResetAnchor time.Time
	// vardiffWindow* tracks a short-horizon retarget window used only for
	// difficulty control; status/confidence windows are kept separate.
	vardiffWindowStart       time.Time
	vardiffWindowResetAnchor time.Time
	vardiffWindowAccepted    int
	vardiffWindowSubmissions int
	vardiffWindowDifficulty  float64
	// isTLSConnection tracks whether this miner connected over the TLS listener.
	isTLSConnection bool
	connectionSeq   uint64
	// suggestDiffProcessed tracks whether we've already processed mining.suggest_difficulty
	// during the initialization phase. Subsequent suggests will be ignored to prevent
	// repeated keepalive messages from disrupting vardiff adjustments.
	suggestDiffProcessed bool
	initialWorkScheduled bool
	initialWorkSent      bool
}

type rpcCaller interface {
	callCtx(ctx context.Context, method string, params any, out any) error
}

type jobNTimeBounds struct {
	min int64
	max int64
}
