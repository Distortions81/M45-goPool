package main

import (
	"errors"
	"math/big"
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
	targetBE                [32]byte
	CreatedAt               time.Time
	Clean                   bool
	Extranonce2Size         int
	CoinbaseValue           int64
	WitnessCommitment       string
	CoinbaseMsg             string
	MerkleBranches          []string
	merkleBranchesBytes     [][32]byte
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
)

const (
	jobRetryDelayMin = 5 * time.Second
	jobRetryDelayMax = 20 * time.Second
)

var errStaleTemplate = errors.New("stale template")

type JobFeedPayloadStatus struct {
	LastRawBlockAt    time.Time
	LastRawBlockBytes int
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
	rpc                 *RPCClient
	cfg                 Config
	metrics             *PoolMetrics
	mu                  sync.RWMutex
	curJob              *Job
	payoutScript        []byte
	donationScript      []byte
	extraID             uint32
	subs                map[chan *Job]struct{}
	subsMu              sync.Mutex
	zmqHashblockHealthy atomic.Bool
	zmqRawblockHealthy  atomic.Bool
	zmqDisconnects      uint64
	zmqReconnects       uint64
	lastErrMu           sync.RWMutex
	lastErr             error
	lastErrAt           time.Time
	lastJobSuccess      time.Time
	jobFeedErrHistory   []string
	// Refresh/apply coordination to prevent concurrent refreshes and concurrent
	// template application from longpoll/ZMQ.
	refreshMu          sync.Mutex
	lastRefreshAttempt time.Time
	applyMu            sync.Mutex
	zmqPayload         JobFeedPayloadStatus
	zmqPayloadMu       sync.RWMutex
	// Async notification queue
	notifyQueue chan *Job
	notifyWg    sizedwaitgroup.SizedWaitGroup
	// Callback for new block notifications
	onNewBlock func()
	// Retry backoff state for job refresh loops
	retryDelay time.Duration
	retryMu    sync.Mutex
}

func NewJobManager(rpc *RPCClient, cfg Config, metrics *PoolMetrics, payoutScript []byte, donationScript []byte) *JobManager {
	return &JobManager{
		rpc:            rpc,
		cfg:            cfg,
		metrics:        metrics,
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
