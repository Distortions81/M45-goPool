package main

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Shared display-shortening settings so worker IDs, hashes, and config
// strings are truncated consistently across the UI.
const (
	workerNamePrefix = 8
	workerNameSuffix = 8

	hashPrefix = 0
	hashSuffix = 16

	payoutAddrPrefix = 8
	payoutAddrSuffix = 8

	coinbaseMsgPrefix = 8
	coinbaseMsgSuffix = 8

	workerLookupMaxBytes        = 256
	workerLookupRateLimitMax    = 10
	workerLookupRateLimitWindow = time.Minute
	// workerLookupMaxEntries bounds the number of distinct keys tracked by
	// the in-memory worker lookup rate limiter. Entries are also expired
	// after a quiet period, so memory usage stays bounded over time.
	workerLookupMaxEntries = 4096
)

const hashPerShare = float64(1 << 32)

const overviewRefreshInterval = defaultRefreshInterval
const poolHashrateTTL = 5 * time.Second

// apiVersion is a short, human-readable version identifier included in all
// JSON API responses so power users can detect schema changes.
const apiVersion = "1"

var templateBufferPool = sync.Pool{
	New: func() interface{} {
		return new(bytes.Buffer)
	},
}

type workerLookupRateLimiter struct {
	mu      sync.Mutex
	entries map[string]*workerLookupEntry
	max     int
	window  time.Duration
}

type workerLookupEntry struct {
	count int
	reset time.Time
}

func newWorkerLookupRateLimiter(max int, window time.Duration) *workerLookupRateLimiter {
	return &workerLookupRateLimiter{
		entries: make(map[string]*workerLookupEntry),
		max:     max,
		window:  window,
	}
}

// cleanupLocked drops entries that have been quiet for at least one extra
// window beyond their reset time, and caps the total number of tracked
// entries so the limiter cannot grow without bound when faced with many
// distinct keys. It expects l.mu to be held by the caller.
func (l *workerLookupRateLimiter) cleanupLocked(now time.Time) {
	if len(l.entries) == 0 {
		return
	}
	for k, entry := range l.entries {
		if now.After(entry.reset.Add(l.window)) {
			delete(l.entries, k)
		}
	}
	// As an extra safety net, trim back the map when it grows far beyond
	// the usual working set size. Since this limiter is best-effort and
	// per-key ordering is not important, we simply drop arbitrary entries
	// when above the cap.
	if len(l.entries) <= workerLookupMaxEntries {
		return
	}
	excess := len(l.entries) - workerLookupMaxEntries
	for k := range l.entries {
		delete(l.entries, k)
		excess--
		if excess <= 0 {
			break
		}
	}
}

func (l *workerLookupRateLimiter) allow(key string) bool {
	if l == nil {
		return true
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cleanupLocked(now)
	if key == "" {
		key = "unknown"
	}
	entry, ok := l.entries[key]
	if !ok || now.After(entry.reset) {
		entry = &workerLookupEntry{
			reset: now.Add(l.window),
		}
		l.entries[key] = entry
	}
	if entry.count >= l.max {
		return false
	}
	entry.count++
	return true
}

func remoteHostFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	if fwd := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); fwd != "" {
		parts := strings.Split(fwd, ",")
		if len(parts) > 0 {
			if host := strings.TrimSpace(parts[0]); host != "" {
				return host
			}
		}
	}
	host := r.RemoteAddr
	if host == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

// buildTime can be overridden at build time with:
//
//	go build -ldflags="-X main.buildTime=2025-01-02T15:04:05Z"
var buildTime = ""

var knownGenesis = map[string]string{
	"mainnet": "000000000019d6689c085ae165831e934ff763ae46a2a6c172b3f1b60a8ce26f",
	"regtest": "000000000019d6689c085ae165831e934ff763ae46a2a6c172b3f1b60a8ce26f",
	"testnet": "000000000933ea01ad0ee984209779baaec3ced90fa3f408719526f8d77f4943",
	"signet":  "00000008819873e925422c1ff0f99f7cc9bbb232af63a077a480a3633bee1ef6",
}

type cachedNodeInfo struct {
	network     string
	subversion  string
	blocks      int64
	headers     int64
	ibd         bool
	pruned      bool
	sizeOnDisk  uint64
	conns       int
	connsIn     int
	connsOut    int
	genesisHash string
	bestHash    string
	peerInfos   []cachedPeerInfo
	fetchedAt   time.Time
}

type cachedPeerInfo struct {
	host        string
	display     string
	pingSeconds float64
	connectedAt time.Time
}

type peerDisplayInfo struct {
	host        string
	display     string
	pingSeconds float64
	connectedAt time.Time
	rawAddr     string
}

type peerLookupEntry struct {
	name      string
	expiresAt time.Time
}

func stripPeerPort(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

func formatPeerDisplay(host, resolved string) string {
	resolved = strings.TrimSpace(resolved)
	if resolved != "" && !strings.EqualFold(resolved, host) {
		return resolved
	}
	return host
}

func (s *StatusServer) lookupPeerName(host string) string {
	if host == "" {
		return ""
	}
	now := time.Now()
	s.peerLookupMu.Lock()
	if entry, ok := s.peerLookupCache[host]; ok && now.Before(entry.expiresAt) {
		name := entry.name
		s.peerLookupMu.Unlock()
		return name
	}
	s.peerLookupMu.Unlock()

	var name string
	if ptrs, err := net.LookupAddr(host); err == nil && len(ptrs) > 0 {
		name = strings.TrimSuffix(strings.TrimSpace(ptrs[0]), ".")
	}

	s.peerLookupMu.Lock()
	if s.peerLookupCache == nil {
		s.peerLookupCache = make(map[string]peerLookupEntry)
	}
	s.peerLookupCache[host] = peerLookupEntry{
		name:      name,
		expiresAt: now.Add(peerLookupTTL),
	}
	s.peerLookupMu.Unlock()

	return name
}

func buildNodePeerInfos(peers []cachedPeerInfo) []NodePeerInfo {
	out := make([]NodePeerInfo, 0, len(peers))
	for _, p := range peers {
		out = append(out, NodePeerInfo{
			Display:     p.display,
			PingMs:      p.pingSeconds * 1000,
			ConnectedAt: p.connectedAt.Unix(),
		})
	}
	return out
}

func (s *StatusServer) cleanupHighPingPeers(peers []peerDisplayInfo) map[string]struct{} {
	if !s.Config().PeerCleanupEnabled || s.rpc == nil {
		return nil
	}
	minPeers := s.Config().PeerCleanupMinPeers
	if minPeers <= 0 {
		minPeers = 20
	}
	totalPeers := len(peers)
	if totalPeers <= minPeers {
		return nil
	}
	maxPingMs := s.Config().PeerCleanupMaxPingMs
	if maxPingMs <= 0 {
		return nil
	}
	thresholdSec := maxPingMs / 1000
	candidates := make([]peerDisplayInfo, 0, len(peers))
	for _, p := range peers {
		if p.pingSeconds > thresholdSec {
			candidates = append(candidates, p)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].pingSeconds > candidates[j].pingSeconds
	})
	maxDisconnect := totalPeers - minPeers
	if maxDisconnect <= 0 {
		return nil
	}
	removed := make(map[string]struct{})
	disconnects := 0
	for _, candidate := range candidates {
		if disconnects >= maxDisconnect {
			break
		}
		if candidate.rawAddr == "" {
			continue
		}
		if err := s.disconnectPeer(candidate.rawAddr); err != nil {
			logger.Warn("peer cleanup disconnect failed",
				"peer", candidate.rawAddr,
				"error", err,
			)
			continue
		}
		removed[candidate.rawAddr] = struct{}{}
		logger.Info("peer cleanup disconnected high-ping peer",
			"peer", candidate.rawAddr,
			"ping_ms", candidate.pingSeconds*1000,
			"min_peers", minPeers,
		)
		disconnects++
	}
	if len(removed) == 0 {
		return nil
	}
	return removed
}

func (s *StatusServer) disconnectPeer(addr string) error {
	if s.rpc == nil {
		return fmt.Errorf("rpc client not configured")
	}
	return s.rpcCallCtx("disconnectnode", []interface{}{addr}, nil)
}

type StatusServer struct {
	tmpl                *template.Template
	jobMgr              *JobManager
	metrics             *PoolMetrics
	registry            *MinerRegistry
	workerRegistry      *workerConnectionRegistry
	accounting          *AccountStore
	rpc                 *RPCClient
	cfg                 atomic.Value
	ctx                 context.Context
	clerk               *ClerkVerifier
	start               time.Time
	workerLookupLimiter *workerLookupRateLimiter
	workerLists         *workerListStore
	lastStatsMu         sync.Mutex
	lastAccepted        uint64
	lastRejected        uint64
	cpuMu               sync.Mutex
	lastCPUProc         uint64
	lastCPUTotal        uint64
	lastCPUUsage        float64
	oneTimeCodeMu       sync.Mutex
	oneTimeCodes        map[string]oneTimeCodeEntry

	statusMu        sync.RWMutex
	cachedStatus    StatusData
	lastStatusBuild time.Time

	nodeInfoMu         sync.Mutex
	nodeInfo           cachedNodeInfo
	nodeInfoRefreshing int32
	peerLookupMu       sync.Mutex
	peerLookupCache    map[string]peerLookupEntry

	priceSvc    *PriceService
	jsonCacheMu sync.RWMutex
	jsonCache   map[string]cachedJSONResponse

	workerPageMu    sync.RWMutex
	workerPageCache map[string]cachedWorkerPage
}

type cachedJSONResponse struct {
	payload   []byte
	updatedAt time.Time
	expiresAt time.Time
}

type cachedWorkerPage struct {
	payload   []byte
	updatedAt time.Time
	expiresAt time.Time
}

func (s *StatusServer) Config() Config {
	if s == nil {
		return Config{}
	}
	if v := s.cfg.Load(); v != nil {
		if cfg, ok := v.(Config); ok {
			return cfg
		}
	}
	return Config{}
}

func (s *StatusServer) UpdateConfig(cfg Config) {
	s.cfg.Store(cfg)
}

// shortDisplayID returns a sanitized, shortened version of s suitable for
// display in HTML templates. It keeps only [A-Za-z0-9._-] characters and, if
// the cleaned string is longer than prefix+suffix+3, returns
// prefix + "..." + suffix.
func shortDisplayID(s string, prefix, suffix int) string {
	if s == "" {
		return ""
	}
	var cleaned []rune
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			cleaned = append(cleaned, r)
		case r >= 'A' && r <= 'Z':
			cleaned = append(cleaned, r)
		case r >= '0' && r <= '9':
			cleaned = append(cleaned, r)
		case r == '.', r == '-', r == '_':
			cleaned = append(cleaned, r)
		default:
			// drop spaces, newlines, and any other unexpected chars
		}
	}
	if len(cleaned) == 0 {
		return ""
	}
	rs := cleaned
	n := len(rs)
	if n <= prefix+suffix+3 {
		return string(rs)
	}
	if prefix < 0 {
		prefix = 0
	}
	if suffix < 0 {
		suffix = 0
	}
	if prefix+suffix+3 > n {
		return string(rs)
	}
	return string(rs[:prefix]) + "..." + string(rs[n-suffix:])
}

// shortWorkerName shortens a worker name but preserves the suffix beginning
// at the first '.' (if any). This keeps pool-suffixes like ".01" or
// ".hashboard1" fully visible while shortening only the leading address or
// base ID.
func shortWorkerName(s string, prefix, suffix int) string {
	if s == "" {
		return ""
	}
	parts := strings.SplitN(s, ".", 2)
	if len(parts) == 1 {
		return shortDisplayID(s, prefix, suffix)
	}
	head := parts[0]
	tail := parts[1]
	shortHead := shortDisplayID(head, prefix, suffix)
	return shortHead + "." + tail
}

type WorkerStatusData struct {
	StatusData
	QueriedWorker     string
	QueriedWorkerHash string // SHA256 hash used by the UI refresh logic
	Worker            *WorkerView
	Error             string
	// Hex-encoded scriptPubKey for pool payout, donation, and worker wallet so the
	// UI can label coinbase outputs without re-parsing addresses.
	PoolScriptHex     string
	DonationScriptHex string
	WorkerScriptHex   string
	// FiatNote is an optional human-readable summary of approximate
	// fiat values for the worker's pending balance and last coinbase
	// split, computed using the current BTC price when available.
	FiatNote string
	// PrivacyMode controls whether sensitive wallet/hash data is hidden.
	PrivacyMode bool
	// These flags remember whether sensitive values existed before redaction.
	HasWalletAddress    bool
	HasShareHashDetails bool
}

type SignInPageData struct {
	StatusData
	ClerkPublishableKey string
	ClerkJSURL          string
	AfterSignInURL      string
	AfterSignUpURL      string
}

// workerPrivacyModeFromRequest parses the "privacy" query parameter and
// returns whether privacy mode should be enabled for a request. Privacy is
// enabled by default (hiding wallet and hash details) unless explicitly
// disabled via values like "off", "0", "false", or "no".
func workerPrivacyModeFromRequest(r *http.Request) bool {
	value := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("privacy")))
	switch value {
	case "off", "0", "false", "no":
		return false
	case "on", "1", "true", "yes":
		return true
	default:
		return true
	}
}

// ErrorPageData is used for the generic error page which presents HTTP
// errors using the same layout and styling as the rest of the status UI.
type ErrorPageData struct {
	StatusData
	StatusCode int
	Title      string
	Message    string
	Detail     string
	Path       string
}

func aggregateCoinbaseSplit(poolScriptHex, donationScriptHex, workerScriptHex string, dbg *ShareDetail) {
	if dbg == nil || len(dbg.CoinbaseOutputs) == 0 {
		return
	}
	var poolVal, donationVal, workerVal int64
	for _, o := range dbg.CoinbaseOutputs {
		if o.ValueSats <= 0 {
			continue
		}
		if workerScriptHex != "" && strings.EqualFold(o.ScriptHex, workerScriptHex) {
			workerVal += o.ValueSats
		}
		if poolScriptHex != "" && strings.EqualFold(o.ScriptHex, poolScriptHex) {
			poolVal += o.ValueSats
		}
		if donationScriptHex != "" && strings.EqualFold(o.ScriptHex, donationScriptHex) {
			donationVal += o.ValueSats
		}
	}
	total := poolVal + donationVal + workerVal
	if total <= 0 {
		return
	}
	dbg.WorkerValueSats = workerVal
	dbg.PoolValueSats = poolVal
	dbg.DonationValueSats = donationVal
	dbg.WorkerPercent = float64(workerVal) * 100 / float64(total)
	dbg.PoolPercent = float64(poolVal) * 100 / float64(total)
	dbg.DonationPercent = float64(donationVal) * 100 / float64(total)
}

func setWorkerStatusView(data *WorkerStatusData, wv WorkerView) {
	data.HasWalletAddress = strings.TrimSpace(wv.WalletAddress) != ""
	data.HasShareHashDetails = strings.TrimSpace(wv.LastShareHash) != "" || strings.TrimSpace(wv.DisplayLastShare) != ""
	if data.QueriedWorkerHash == "" && wv.WorkerSHA256 != "" {
		data.QueriedWorkerHash = wv.WorkerSHA256
	}

	workerScriptHex := ""
	if strings.TrimSpace(wv.WalletScript) != "" {
		workerScriptHex = strings.ToLower(strings.TrimSpace(wv.WalletScript))
	}

	// Always set script hex values for template matching
	data.WorkerScriptHex = workerScriptHex
	aggregateCoinbaseSplit(data.PoolScriptHex, data.DonationScriptHex, workerScriptHex, wv.LastShareDetail)

	// Privacy mode is handled client-side, so always send real data
	data.Worker = &wv
}

type SavedWorkerEntry struct {
	Name          string `json:"name"`
	Hash          string `json:"hash"`
	NotifyEnabled bool   `json:"notify_enabled,omitempty"`
}

type StatusData struct {
	ListenAddr                     string                `json:"listen_addr"`
	StratumTLSListen               string                `json:"stratum_tls_listen,omitempty"`
	ClerkEnabled                   bool                  `json:"clerk_enabled"`
	ClerkLoginURL                  string                `json:"clerk_login_url,omitempty"`
	ClerkUser                      *ClerkUser            `json:"clerk_user,omitempty"`
	ClerkPublishableKey            string                `json:"-"`
	ClerkJSURL                     string                `json:"-"`
	SavedWorkers                   []SavedWorkerEntry    `json:"saved_workers,omitempty"`
	BrandName                      string                `json:"brand_name"`
	BrandDomain                    string                `json:"brand_domain"`
	Tagline                        string                `json:"tagline,omitempty"`
	ConnectMinerTitleExtra         string                `json:"connect_miner_title_extra,omitempty"`
	ConnectMinerTitleExtraURL      string                `json:"connect_miner_title_extra_url,omitempty"`
	ServerLocation                 string                `json:"server_location,omitempty"`
	FiatCurrency                   string                `json:"fiat_currency,omitempty"`
	BTCPriceFiat                   float64               `json:"btc_price_fiat,omitempty"`
	BTCPriceUpdatedAt              string                `json:"btc_price_updated_at,omitempty"`
	PoolDonationAddress            string                `json:"pool_donation_address,omitempty"`
	DiscordURL                     string                `json:"discord_url,omitempty"`
	DiscordNotificationsEnabled    bool                  `json:"discord_notifications_enabled,omitempty"`
	DiscordNotificationsRegistered bool                  `json:"-"`
	GitHubURL                      string                `json:"github_url,omitempty"`
	NodeNetwork                    string                `json:"node_network,omitempty"`
	NodeSubversion                 string                `json:"node_subversion,omitempty"`
	NodeBlocks                     int64                 `json:"node_blocks"`
	NodeHeaders                    int64                 `json:"node_headers"`
	NodeInitialBlockDownload       bool                  `json:"node_initial_block_download"`
	NodeRPCURL                     string                `json:"node_rpc_url"`
	NodeZMQAddr                    string                `json:"node_zmq_addr,omitempty"`
	PayoutAddress                  string                `json:"payout_address,omitempty"`
	PoolFeePercent                 float64               `json:"pool_fee_percent"`
	OperatorDonationPercent        float64               `json:"operator_donation_percent,omitempty"`
	OperatorDonationAddress        string                `json:"operator_donation_address,omitempty"`
	OperatorDonationName           string                `json:"operator_donation_name,omitempty"`
	OperatorDonationURL            string                `json:"operator_donation_url,omitempty"`
	CoinbaseMessage                string                `json:"coinbase_message,omitempty"`
	DisplayPayoutAddress           string                `json:"display_payout_address,omitempty"`
	DisplayOperatorDonationAddress string                `json:"display_operator_donation_address,omitempty"`
	DisplayCoinbaseMessage         string                `json:"display_coinbase_message,omitempty"`
	NodeConnections                int                   `json:"node_connections"`
	NodeConnectionsIn              int                   `json:"node_connections_in"`
	NodeConnectionsOut             int                   `json:"node_connections_out"`
	NodePeerInfos                  []NodePeerInfo        `json:"node_peer_infos,omitempty"`
	NodePruned                     bool                  `json:"node_pruned"`
	NodeSizeOnDiskBytes            uint64                `json:"node_size_on_disk_bytes"`
	NodePeerCleanupEnabled         bool                  `json:"node_peer_cleanup_enabled"`
	NodePeerCleanupMaxPingMs       float64               `json:"node_peer_cleanup_max_ping_ms"`
	NodePeerCleanupMinPeers        int                   `json:"node_peer_cleanup_min_peers"`
	GenesisHash                    string                `json:"genesis_hash,omitempty"`
	GenesisExpected                string                `json:"genesis_expected,omitempty"`
	GenesisMatch                   bool                  `json:"genesis_match"`
	BestBlockHash                  string                `json:"best_block_hash,omitempty"`
	PoolSoftware                   string                `json:"pool_software"`
	BuildTime                      string                `json:"build_time"`
	RenderDuration                 time.Duration         `json:"render_duration"`
	PageCached                     bool                  `json:"page_cached"`
	ActiveMiners                   int                   `json:"active_miners"`
	ActiveTLSMiners                int                   `json:"active_tls_miners"`
	SharesPerSecond                float64               `json:"shares_per_second"`
	SharesPerMinute                float64               `json:"shares_per_minute,omitempty"`
	Accepted                       uint64                `json:"accepted"`
	Rejected                       uint64                `json:"rejected"`
	StaleShares                    uint64                `json:"stale_shares"`
	LowDiffShares                  uint64                `json:"low_diff_shares"`
	RejectReasons                  map[string]uint64     `json:"reject_reasons,omitempty"`
	CurrentJob                     *Job                  `json:"current_job,omitempty"`
	Uptime                         time.Duration         `json:"uptime"`
	JobCreated                     string                `json:"job_created"`
	TemplateTime                   string                `json:"template_time"`
	Workers                        []WorkerView          `json:"workers"`
	BannedWorkers                  []WorkerView          `json:"banned_workers"`
	WindowAccepted                 uint64                `json:"window_accepted"`
	WindowSubmissions              uint64                `json:"window_submissions"`
	WindowStart                    string                `json:"window_start"`
	RPCError                       string                `json:"rpc_error,omitempty"`
	AccountingError                string                `json:"accounting_error,omitempty"`
	JobFeed                        JobFeedView           `json:"job_feed"`
	BestShares                     []BestShare           `json:"best_shares"`
	FoundBlocks                    []FoundBlockView      `json:"found_blocks,omitempty"`
	MinerTypes                     []MinerTypeView       `json:"miner_types,omitempty"`
	WorkerLookup                   map[string]WorkerView `json:"-"`
	RecentWork                     []RecentWorkView      `json:"-"`
	VardiffUp                      uint64                `json:"vardiff_up"`
	VardiffDown                    uint64                `json:"vardiff_down"`
	PoolHashrate                   float64               `json:"pool_hashrate,omitempty"`
	BlocksAccepted                 uint64                `json:"blocks_accepted"`
	BlocksErrored                  uint64                `json:"blocks_errored"`
	RPCGBTLastSec                  float64               `json:"rpc_gbt_last_sec"`
	RPCGBTMaxSec                   float64               `json:"rpc_gbt_max_sec"`
	RPCGBTCount                    uint64                `json:"rpc_gbt_count"`
	RPCSubmitLastSec               float64               `json:"rpc_submit_last_sec"`
	RPCSubmitMaxSec                float64               `json:"rpc_submit_max_sec"`
	RPCSubmitCount                 uint64                `json:"rpc_submit_count"`
	RPCErrors                      uint64                `json:"rpc_errors"`
	ShareErrors                    uint64                `json:"share_errors"`
	RPCGBTMin1hSec                 float64               `json:"rpc_gbt_min_1h_sec"`
	RPCGBTAvg1hSec                 float64               `json:"rpc_gbt_avg_1h_sec"`
	RPCGBTMax1hSec                 float64               `json:"rpc_gbt_max_1h_sec"`
	ErrorHistory                   []PoolErrorEvent      `json:"error_history,omitempty"`
	// Local process / system diagnostics (server-only).
	ProcessGoroutines   int     `json:"process_goroutines"`
	ProcessCPUPercent   float64 `json:"process_cpu_percent"`
	GoMemAllocBytes     uint64  `json:"go_mem_alloc_bytes"`
	GoMemSysBytes       uint64  `json:"go_mem_sys_bytes"`
	ProcessRSSBytes     uint64  `json:"process_rss_bytes"`
	SystemMemTotalBytes uint64  `json:"system_mem_total_bytes"`
	SystemMemFreeBytes  uint64  `json:"system_mem_free_bytes"`
	SystemMemUsedBytes  uint64  `json:"system_mem_used_bytes"`
	SystemLoad1         float64 `json:"system_load1"`
	SystemLoad5         float64 `json:"system_load5"`
	SystemLoad15        float64 `json:"system_load15"`
	// Safe-to-share pool config summary.
	MaxConns                int     `json:"max_conns"`
	MaxAcceptsPerSecond     int     `json:"max_accepts_per_second"`
	MaxAcceptBurst          int     `json:"max_accept_burst"`
	MinDifficulty           float64 `json:"min_difficulty"`
	MaxDifficulty           float64 `json:"max_difficulty"`
	LockSuggestedDifficulty bool    `json:"lock_suggested_difficulty"`
	HashrateEMATauSeconds   float64 `json:"hashrate_ema_tau_seconds"`
	HashrateEMAMinShares    int     `json:"hashrate_ema_min_shares"`
	NTimeForwardSlackSec    int     `json:"ntime_forward_slack_seconds"`
	// Worker database summary (status page only).
	WorkerDatabase WorkerDatabaseStats `json:"worker_database"`
	Warnings       []string            `json:"warnings,omitempty"`
}

type ServerPageJobFeed struct {
	LastError         string   `json:"last_error,omitempty"`
	LastErrorAt       string   `json:"last_error_at,omitempty"`
	ErrorHistory      []string `json:"error_history,omitempty"`
	ZMQHealthy        bool     `json:"zmq_healthy"`
	ZMQDisconnects    uint64   `json:"zmq_disconnects"`
	ZMQReconnects     uint64   `json:"zmq_reconnects"`
	LastRawBlockAt    string   `json:"last_raw_block_at,omitempty"`
	LastRawBlockBytes int      `json:"last_raw_block_bytes,omitempty"`
	LastHashTx        string   `json:"last_hash_tx,omitempty"`
	LastHashTxAt      string   `json:"last_hash_tx_at,omitempty"`
	LastRawTxAt       string   `json:"last_raw_tx_at,omitempty"`
	LastRawTxBytes    int      `json:"last_raw_tx_bytes,omitempty"`
	BlockHash         string   `json:"block_hash,omitempty"`
	BlockHeight       int64    `json:"block_height,omitempty"`
	BlockTime         string   `json:"block_time,omitempty"`
	BlockBits         string   `json:"block_bits,omitempty"`
	BlockDifficulty   float64  `json:"block_difficulty,omitempty"`
}

// OverviewPageData contains data for the overview page (minimal payload)
type OverviewPageData struct {
	APIVersion      string           `json:"api_version"`
	ActiveMiners    int              `json:"active_miners"`
	ActiveTLSMiners int              `json:"active_tls_miners"`
	SharesPerMinute float64          `json:"shares_per_minute,omitempty"`
	PoolHashrate    float64          `json:"pool_hashrate,omitempty"`
	BTCPriceUSD     float64          `json:"btc_price_usd,omitempty"`
	BTCPriceUpdated string           `json:"btc_price_updated_at,omitempty"`
	RenderDuration  time.Duration    `json:"render_duration"`
	Workers         []RecentWorkView `json:"workers"`
	BannedWorkers   []WorkerView     `json:"banned_workers"`
	BestShares      []BestShare      `json:"best_shares"`
	FoundBlocks     []FoundBlockView `json:"found_blocks,omitempty"`
	MinerTypes      []MinerTypeView  `json:"miner_types,omitempty"`
}

type PoolErrorEvent struct {
	At      string `json:"at,omitempty"`
	Type    string `json:"type"`
	Message string `json:"message"`
}

// PoolPageData contains data for the pool info page
type PoolPageData struct {
	APIVersion       string           `json:"api_version"`
	BlocksAccepted   uint64           `json:"blocks_accepted"`
	BlocksErrored    uint64           `json:"blocks_errored"`
	RPCGBTLastSec    float64          `json:"rpc_gbt_last_sec"`
	RPCGBTMaxSec     float64          `json:"rpc_gbt_max_sec"`
	RPCGBTCount      uint64           `json:"rpc_gbt_count"`
	RPCSubmitLastSec float64          `json:"rpc_submit_last_sec"`
	RPCSubmitMaxSec  float64          `json:"rpc_submit_max_sec"`
	RPCSubmitCount   uint64           `json:"rpc_submit_count"`
	RPCErrors        uint64           `json:"rpc_errors"`
	ShareErrors      uint64           `json:"share_errors"`
	RPCGBTMin1hSec   float64          `json:"rpc_gbt_min_1h_sec"`
	RPCGBTAvg1hSec   float64          `json:"rpc_gbt_avg_1h_sec"`
	RPCGBTMax1hSec   float64          `json:"rpc_gbt_max_1h_sec"`
	ErrorHistory     []PoolErrorEvent `json:"error_history,omitempty"`
}

const poolErrorHistoryDisplayWindow = time.Hour

func filterRecentPoolErrorEvents(raw []ErrorEvent, now time.Time, maxAge time.Duration) []PoolErrorEvent {
	if len(raw) == 0 {
		return nil
	}
	filtered := make([]PoolErrorEvent, 0, len(raw))
	for _, ev := range raw {
		if ev.At.IsZero() {
			continue
		}
		if maxAge > 0 && now.Sub(ev.At) > maxAge {
			continue
		}
		filtered = append(filtered, PoolErrorEvent{
			At:      ev.At.UTC().Format(time.RFC3339),
			Type:    ev.Type,
			Message: ev.Message,
		})
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

// ServerPageData contains data for the server diagnostics page
type ServerPageData struct {
	APIVersion          string            `json:"api_version"`
	Uptime              time.Duration     `json:"uptime"`
	RPCError            string            `json:"rpc_error,omitempty"`
	AccountingError     string            `json:"accounting_error,omitempty"`
	JobFeed             ServerPageJobFeed `json:"job_feed"`
	ProcessGoroutines   int               `json:"process_goroutines"`
	ProcessCPUPercent   float64           `json:"process_cpu_percent"`
	GoMemAllocBytes     uint64            `json:"go_mem_alloc_bytes"`
	GoMemSysBytes       uint64            `json:"go_mem_sys_bytes"`
	ProcessRSSBytes     uint64            `json:"process_rss_bytes"`
	SystemMemTotalBytes uint64            `json:"system_mem_total_bytes"`
	SystemMemFreeBytes  uint64            `json:"system_mem_free_bytes"`
	SystemMemUsedBytes  uint64            `json:"system_mem_used_bytes"`
	SystemLoad1         float64           `json:"system_load1"`
	SystemLoad5         float64           `json:"system_load5"`
	SystemLoad15        float64           `json:"system_load15"`
}

// invalidateStatusCache clears the status cache, forcing the next request to rebuild.
func (s *StatusServer) invalidateStatusCache() {
	s.statusMu.Lock()
	s.lastStatusBuild = time.Time{}
	s.statusMu.Unlock()
}

// statusDataView returns a read-only snapshot of the cached status data.
//
// Unlike statusData(), it does not deep-clone slices/maps. Callers must treat
// the returned value as immutable (do not mutate slices, maps, or nested
// structs). This is safe for building endpoint-specific responses where we
// copy/censor only the fields we actually serialize.
func (s *StatusServer) statusDataView() StatusData {
	now := time.Now()
	s.statusMu.RLock()
	if !s.lastStatusBuild.IsZero() && now.Sub(s.lastStatusBuild) < overviewRefreshInterval {
		data := s.cachedStatus
		s.statusMu.RUnlock()
		return data
	}
	s.statusMu.RUnlock()

	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	if !s.lastStatusBuild.IsZero() && now.Sub(s.lastStatusBuild) < overviewRefreshInterval {
		return s.cachedStatus
	}
	data := s.buildStatusData()
	s.cachedStatus = data
	s.lastStatusBuild = now
	return data
}

func cloneStatusData(in StatusData) StatusData {
	in.Workers = cloneWorkerViews(in.Workers)
	in.BannedWorkers = cloneWorkerViews(in.BannedWorkers)
	in.BestShares = cloneBestShares(in.BestShares)
	in.FoundBlocks = cloneFoundBlocks(in.FoundBlocks)
	in.MinerTypes = cloneMinerTypes(in.MinerTypes)
	in.RecentWork = cloneRecentWorkViews(in.RecentWork)
	in.RejectReasons = cloneRejectReasons(in.RejectReasons)
	in.Warnings = cloneStringSlice(in.Warnings)
	in.SavedWorkers = cloneSavedWorkers(in.SavedWorkers)
	// WorkerLookup is an internal cache; never expose it through cloned views.
	in.WorkerLookup = nil
	return in
}

func cloneWorkerViews(src []WorkerView) []WorkerView {
	if len(src) == 0 {
		return nil
	}
	dst := make([]WorkerView, len(src))
	copy(dst, src)
	return dst
}

func cloneBestShares(src []BestShare) []BestShare {
	if len(src) == 0 {
		return nil
	}
	dst := make([]BestShare, len(src))
	copy(dst, src)
	return dst
}

func cloneFoundBlocks(src []FoundBlockView) []FoundBlockView {
	if len(src) == 0 {
		return nil
	}
	dst := make([]FoundBlockView, len(src))
	copy(dst, src)
	return dst
}

func cloneMinerTypes(src []MinerTypeView) []MinerTypeView {
	if len(src) == 0 {
		return nil
	}
	dst := make([]MinerTypeView, len(src))
	copy(dst, src)
	return dst
}

func cloneRecentWorkViews(src []RecentWorkView) []RecentWorkView {
	if len(src) == 0 {
		return nil
	}
	dst := make([]RecentWorkView, len(src))
	copy(dst, src)
	return dst
}

func cloneRejectReasons(src map[string]uint64) map[string]uint64 {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]uint64, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func cloneStringSlice(src []string) []string {
	if len(src) == 0 {
		return nil
	}
	dst := make([]string, len(src))
	copy(dst, src)
	return dst
}

func cloneSavedWorkers(src []SavedWorkerEntry) []SavedWorkerEntry {
	if len(src) == 0 {
		return nil
	}
	dst := make([]SavedWorkerEntry, len(src))
	copy(dst, src)
	return dst
}

type JobFeedView struct {
	Ready             bool     `json:"ready"`
	LastSuccess       string   `json:"last_success"`
	LastError         string   `json:"last_error,omitempty"`
	LastErrorAt       string   `json:"last_error_at,omitempty"`
	ErrorHistory      []string `json:"error_history,omitempty"`
	ZMQHealthy        bool     `json:"zmq_healthy"`
	ZMQDisconnects    uint64   `json:"zmq_disconnects"`
	ZMQReconnects     uint64   `json:"zmq_reconnects"`
	LastRawBlockAt    string   `json:"last_raw_block_at,omitempty"`
	LastRawBlockBytes int      `json:"last_raw_block_bytes,omitempty"`
	LastHashTx        string   `json:"last_hash_tx,omitempty"`
	LastHashTxAt      string   `json:"last_hash_tx_at,omitempty"`
	LastRawTxAt       string   `json:"last_raw_tx_at,omitempty"`
	LastRawTxBytes    int      `json:"last_raw_tx_bytes,omitempty"`
	BlockHash         string   `json:"block_hash,omitempty"`
	BlockHeight       int64    `json:"block_height,omitempty"`
	BlockTime         string   `json:"block_time,omitempty"`
	BlockBits         string   `json:"block_bits,omitempty"`
	BlockDifficulty   float64  `json:"block_difficulty,omitempty"`
}

type MinerTypeView struct {
	Name     string                 `json:"name"`
	Total    int                    `json:"total_workers"`
	Versions []MinerTypeVersionView `json:"versions"`
}

type MinerTypeVersionView struct {
	Version string `json:"version,omitempty"`
	Workers int    `json:"workers"`
}

type FoundBlockView struct {
	Height           int64     `json:"height"`
	Hash             string    `json:"hash"`
	DisplayHash      string    `json:"display_hash"`
	Worker           string    `json:"worker"`
	DisplayWorker    string    `json:"display_worker"`
	Timestamp        time.Time `json:"timestamp"`
	ShareDiff        float64   `json:"share_diff"`
	PoolFeeSats      int64     `json:"pool_fee_sats,omitempty"`
	WorkerPayoutSats int64     `json:"worker_payout_sats,omitempty"`
}
