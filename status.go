package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	stdjson "encoding/json"
	"fmt"
	"html/template"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bytedance/gopkg/util/logger"
	"github.com/bytedance/sonic"
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
	network       string
	subversion    string
	blocks        int64
	headers       int64
	ibd           bool
	pruned        bool
	sizeOnDisk    uint64
	conns         int
	connsIn       int
	connsOut      int
	genesisHash   string
	bestHash      string
	peerAddresses []string
	fetchedAt     time.Time
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

type StatusServer struct {
	tmpl                *template.Template
	jobMgr              *JobManager
	metrics             *PoolMetrics
	registry            *MinerRegistry
	accounting          *AccountStore
	rpc                 *RPCClient
	cfg                 Config
	ctx                 context.Context
	start               time.Time
	workerLookupLimiter *workerLookupRateLimiter
	lastStatsMu         sync.Mutex
	lastAccepted        uint64
	lastRejected        uint64
	cpuMu               sync.Mutex
	lastCPUProc         uint64
	lastCPUTotal        uint64
	lastCPUUsage        float64

	statusMu        sync.RWMutex
	cachedStatus    StatusData
	lastStatusBuild time.Time

	nodeInfoMu         sync.Mutex
	nodeInfo           cachedNodeInfo
	nodeInfoRefreshing int32

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

// privacyDisplay returns a censored form with ".." between prefix/suffix.
func privacyDisplay(s string, prefix, suffix int) string {
	short := shortDisplayID(s, prefix, suffix)
	return strings.Replace(short, "...", "..", 1)
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

func redactWorkerViewForPrivacy(w WorkerView) WorkerView {
	w.WalletAddress = privacyDisplay(w.WalletAddress, 8, 8)
	w.WalletScript = ""
	w.LastShareHash = privacyDisplay(w.LastShareHash, 8, 8)
	w.DisplayLastShare = privacyDisplay(w.DisplayLastShare, 8, 8)
	w.MinerName = privacyDisplay(w.MinerName, 8, 8)
	w.Name = privacyDisplay(w.Name, 8, 8)
	w.DisplayName = privacyDisplay(w.DisplayName, 8, 8)
	if w.LastShareDetail != nil {
		c := *w.LastShareDetail
		c.Header = privacyDisplay(c.Header, 8, 8)
		c.ShareHash = privacyDisplay(c.ShareHash, 8, 8)
		c.Target = privacyDisplay(c.Target, 8, 8)
		c.MerkleRootBE = privacyDisplay(c.MerkleRootBE, 8, 8)
		c.MerkleRootLE = privacyDisplay(c.MerkleRootLE, 8, 8)
		c.Coinbase = privacyDisplay(c.Coinbase, 8, 8)
		if len(c.CoinbaseOutputs) > 0 {
			outs := make([]CoinbaseOutputDebug, len(c.CoinbaseOutputs))
			copy(outs, c.CoinbaseOutputs)
			for i := range outs {
				outs[i].ScriptHex = privacyDisplay(outs[i].ScriptHex, 8, 8)
			}
			c.CoinbaseOutputs = outs
		}
		w.LastShareDetail = &c
	}
	return w
}

func setWorkerStatusView(data *WorkerStatusData, wv WorkerView, privacyMode bool) {
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

type StatusData struct {
	ListenAddr                     string            `json:"listen_addr"`
	StratumTLSListen               string            `json:"stratum_tls_listen,omitempty"`
	BrandName                      string            `json:"brand_name"`
	BrandDomain                    string            `json:"brand_domain"`
	Tagline                        string            `json:"tagline,omitempty"`
	ServerLocation                 string            `json:"server_location,omitempty"`
	FiatCurrency                   string            `json:"fiat_currency,omitempty"`
	BTCPriceFiat                   float64           `json:"btc_price_fiat,omitempty"`
	BTCPriceUpdatedAt              string            `json:"btc_price_updated_at,omitempty"`
	PoolDonationAddress            string            `json:"pool_donation_address,omitempty"`
	DiscordURL                     string            `json:"discord_url,omitempty"`
	NodeNetwork                    string            `json:"node_network,omitempty"`
	NodeSubversion                 string            `json:"node_subversion,omitempty"`
	NodeBlocks                     int64             `json:"node_blocks"`
	NodeHeaders                    int64             `json:"node_headers"`
	NodeInitialBlockDownload       bool              `json:"node_initial_block_download"`
	NodeRPCURL                     string            `json:"node_rpc_url"`
	NodeZMQAddr                    string            `json:"node_zmq_addr,omitempty"`
	PayoutAddress                  string            `json:"payout_address,omitempty"`
	PoolFeePercent                 float64           `json:"pool_fee_percent"`
	OperatorDonationPercent        float64           `json:"operator_donation_percent,omitempty"`
	OperatorDonationAddress        string            `json:"operator_donation_address,omitempty"`
	OperatorDonationName           string            `json:"operator_donation_name,omitempty"`
	OperatorDonationURL            string            `json:"operator_donation_url,omitempty"`
	CoinbaseMessage                string            `json:"coinbase_message,omitempty"`
	DisplayPayoutAddress           string            `json:"display_payout_address,omitempty"`
	DisplayOperatorDonationAddress string            `json:"display_operator_donation_address,omitempty"`
	DisplayCoinbaseMessage         string            `json:"display_coinbase_message,omitempty"`
	NodeConnections                int               `json:"node_connections"`
	NodeConnectionsIn              int               `json:"node_connections_in"`
	NodeConnectionsOut             int               `json:"node_connections_out"`
	NodePeerAddresses              []string          `json:"node_peer_addresses,omitempty"`
	NodePruned                     bool              `json:"node_pruned"`
	NodeSizeOnDiskBytes            uint64            `json:"node_size_on_disk_bytes"`
	GenesisHash                    string            `json:"genesis_hash,omitempty"`
	GenesisExpected                string            `json:"genesis_expected,omitempty"`
	GenesisMatch                   bool              `json:"genesis_match"`
	BestBlockHash                  string            `json:"best_block_hash,omitempty"`
	PoolSoftware                   string            `json:"pool_software"`
	BuildTime                      string            `json:"build_time"`
	RenderDuration                 time.Duration     `json:"render_duration"`
	PageCached                     bool              `json:"page_cached"`
	ActiveMiners                   int               `json:"active_miners"`
	ActiveTLSMiners                int               `json:"active_tls_miners"`
	SharesPerSecond                float64           `json:"shares_per_second"`
	SharesPerMinute                float64           `json:"shares_per_minute,omitempty"`
	Accepted                       uint64            `json:"accepted"`
	Rejected                       uint64            `json:"rejected"`
	StaleShares                    uint64            `json:"stale_shares"`
	LowDiffShares                  uint64            `json:"low_diff_shares"`
	RejectReasons                  map[string]uint64 `json:"reject_reasons,omitempty"`
	CurrentJob                     *Job              `json:"current_job,omitempty"`
	Uptime                         time.Duration     `json:"uptime"`
	JobCreated                     string            `json:"job_created"`
	TemplateTime                   string            `json:"template_time"`
	Workers                        []WorkerView      `json:"workers"`
	BannedWorkers                  []WorkerView      `json:"banned_workers"`
	WindowAccepted                 uint64            `json:"window_accepted"`
	WindowSubmissions              uint64            `json:"window_submissions"`
	WindowStart                    string            `json:"window_start"`
	RPCError                       string            `json:"rpc_error,omitempty"`
	AccountingError                string            `json:"accounting_error,omitempty"`
	JobFeed                        JobFeedView       `json:"job_feed"`
	BestShares                     []BestShare       `json:"best_shares"`
	FoundBlocks                    []FoundBlockView  `json:"found_blocks,omitempty"`
	MinerTypes                     []MinerTypeView   `json:"miner_types,omitempty"`
	VardiffUp                      uint64            `json:"vardiff_up"`
	VardiffDown                    uint64            `json:"vardiff_down"`
	PoolHashrate                   float64           `json:"pool_hashrate,omitempty"`
	BlocksAccepted                 uint64            `json:"blocks_accepted"`
	BlocksErrored                  uint64            `json:"blocks_errored"`
	RPCGBTLastSec                  float64           `json:"rpc_gbt_last_sec"`
	RPCGBTMaxSec                   float64           `json:"rpc_gbt_max_sec"`
	RPCGBTCount                    uint64            `json:"rpc_gbt_count"`
	RPCSubmitLastSec               float64           `json:"rpc_submit_last_sec"`
	RPCSubmitMaxSec                float64           `json:"rpc_submit_max_sec"`
	RPCSubmitCount                 uint64            `json:"rpc_submit_count"`
	RPCErrors                      uint64            `json:"rpc_errors"`
	ShareErrors                    uint64            `json:"share_errors"`
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
	RenderDuration  time.Duration    `json:"render_duration"`
	Workers         []RecentWorkView `json:"workers"`
	BannedWorkers   []WorkerView     `json:"banned_workers"`
	BestShares      []BestShare      `json:"best_shares"`
	FoundBlocks     []FoundBlockView `json:"found_blocks,omitempty"`
	MinerTypes      []MinerTypeView  `json:"miner_types,omitempty"`
}

// PoolPageData contains data for the pool info page
type PoolPageData struct {
	APIVersion       string  `json:"api_version"`
	BlocksAccepted   uint64  `json:"blocks_accepted"`
	BlocksErrored    uint64  `json:"blocks_errored"`
	RPCGBTLastSec    float64 `json:"rpc_gbt_last_sec"`
	RPCGBTMaxSec     float64 `json:"rpc_gbt_max_sec"`
	RPCGBTCount      uint64  `json:"rpc_gbt_count"`
	RPCSubmitLastSec float64 `json:"rpc_submit_last_sec"`
	RPCSubmitMaxSec  float64 `json:"rpc_submit_max_sec"`
	RPCSubmitCount   uint64  `json:"rpc_submit_count"`
	RPCErrors        uint64  `json:"rpc_errors"`
	ShareErrors      uint64  `json:"share_errors"`
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

func (s *StatusServer) statusData() StatusData {
	now := time.Now()
	s.statusMu.RLock()
	if !s.lastStatusBuild.IsZero() && now.Sub(s.lastStatusBuild) < overviewRefreshInterval {
		data := s.cachedStatus
		s.statusMu.RUnlock()
		return cloneStatusData(data)
	}
	s.statusMu.RUnlock()

	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	if !s.lastStatusBuild.IsZero() && now.Sub(s.lastStatusBuild) < overviewRefreshInterval {
		return cloneStatusData(s.cachedStatus)
	}
	data := s.buildStatusData()
	s.cachedStatus = data
	s.lastStatusBuild = now
	return cloneStatusData(data)
}

func cloneStatusData(in StatusData) StatusData {
	in.Workers = cloneWorkerViews(in.Workers)
	in.BannedWorkers = cloneWorkerViews(in.BannedWorkers)
	in.BestShares = cloneBestShares(in.BestShares)
	in.FoundBlocks = cloneFoundBlocks(in.FoundBlocks)
	in.MinerTypes = cloneMinerTypes(in.MinerTypes)
	in.RejectReasons = cloneRejectReasons(in.RejectReasons)
	in.Warnings = cloneStringSlice(in.Warnings)
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

func shareRatePerMinute(stats MinerStats, now time.Time) float64 {
	if stats.WindowStart.IsZero() {
		return 0
	}
	window := now.Sub(stats.WindowStart)
	if window <= 0 {
		return 0
	}
	return float64(stats.WindowAccepted) / window.Minutes()
}

// computeWindowHashrate converts the accepted work within the rolling window into hashes per second.
func computeWindowHashrate(stats MinerStats, now time.Time) float64 {
	if stats.WindowStart.IsZero() {
		return 0
	}
	window := now.Sub(stats.WindowStart)
	if window <= 0 || window < 5*time.Second {
		return 0
	}
	if stats.WindowDifficulty <= 0 || stats.WindowAccepted <= 0 {
		return 0
	}
	return (stats.WindowDifficulty * hashPerShare) / window.Seconds()
}

func computeWindowShareRate(stats MinerStats, now time.Time) float64 {
	hashrate := computeWindowHashrate(stats, now)
	if hashrate <= 0 {
		return 0
	}
	return (hashrate / hashPerShare) * 60
}

func workerViewFromConn(mc *MinerConn, now time.Time) WorkerView {
	snap := mc.snapshotShareInfo()
	stats := snap.Stats
	name := stats.Worker
	if name == "" {
		name = mc.id
	}
	displayName := shortWorkerName(name, workerNamePrefix, workerNameSuffix)
	workerHash := ""
	if stats.Worker != "" {
		sum := sha256.Sum256([]byte(stats.Worker))
		workerHash = fmt.Sprintf("%x", sum[:])
	}
	accRate := shareRatePerMinute(stats, now)
	diff := mc.currentDifficulty()
	hashRate := snap.RollingHashrate
	addr, script, valid := mc.workerWalletData(stats.Worker)
	scriptHex := ""
	if len(script) > 0 {
		scriptHex = strings.ToLower(hex.EncodeToString(script))
	}
	lastShareHash := snap.LastShareHash
	displayHash := ""
	if lastShareHash != "" {
		displayHash = shortDisplayID(lastShareHash, hashPrefix, hashSuffix)
	}
	banned := mc.isBanned(now)
	until, reason, _ := mc.banDetails()
	return WorkerView{
		Name:                name,
		DisplayName:         displayName,
		WorkerSHA256:        workerHash,
		Accepted:            uint64(stats.Accepted),
		Rejected:            uint64(stats.Rejected),
		BalanceSats:         0,
		WalletAddress:       addr,
		WalletScript:        scriptHex,
		MinerType:           mc.minerType,
		MinerName:           mc.minerClientName,
		MinerVersion:        mc.minerClientVersion,
		LastShare:           stats.LastShare,
		LastShareHash:       lastShareHash,
		DisplayLastShare:    displayHash,
		LastShareAccepted:   snap.LastShareAccepted,
		LastShareDifficulty: snap.LastShareDifficulty,
		LastShareDetail:     snap.LastShareDetail,
		Difficulty:          diff,
		RollingHashrate:     hashRate,
		LastReject:          snap.LastReject,
		Banned:              banned,
		BannedUntil:         until,
		BanReason:           reason,
		WindowStart:         stats.WindowStart,
		WindowAccepted:      stats.WindowAccepted,
		WindowSubmissions:   stats.WindowSubmissions,
		ShareRate:           accRate,
		WalletValidated:     valid,
	}
}

func (s *StatusServer) snapshotWorkerViews(now time.Time) []WorkerView {
	if s.registry == nil {
		return nil
	}
	conns := s.registry.Snapshot()
	views := make([]WorkerView, 0, len(conns))
	for _, mc := range conns {
		views = append(views, workerViewFromConn(mc, now))
	}
	sort.Slice(views, func(i, j int) bool {
		return views[i].LastShare.After(views[j].LastShare)
	})
	return views
}

func (s *StatusServer) computePoolHashrate() float64 {
	if s.registry == nil {
		return 0
	}
	now := time.Now()
	var total float64
	for _, mc := range s.registry.Snapshot() {
		snap := mc.snapshotShareInfo()
		total += computeWindowHashrate(snap.Stats, now)
	}
	return total
}

func (s *StatusServer) findWorkerViewByName(name string, now time.Time) (WorkerView, bool) {
	if name == "" {
		return WorkerView{}, false
	}
	for _, w := range s.snapshotWorkerViews(now) {
		if w.Name == name {
			return w, true
		}
	}
	return WorkerView{}, false
}

func (s *StatusServer) findWorkerViewByHash(hash string, now time.Time) (WorkerView, bool) {
	if hash == "" {
		return WorkerView{}, false
	}
	for _, w := range s.snapshotWorkerViews(now) {
		if w.WorkerSHA256 == hash {
			return w, true
		}
	}
	return WorkerView{}, false
}

// buildTemplateFuncs returns the template.FuncMap used for all HTML templates.
func buildTemplateFuncs() template.FuncMap {
	return template.FuncMap{
		"humanDuration": func(d time.Duration) string {
			if d < 0 {
				return "0s"
			}
			return d.Round(time.Second).String()
		},
		"shortID": func(s string) string {
			// Shorten IDs / hashes to a stable, display-safe form.
			return shortDisplayID(s, hashPrefix, hashSuffix)
		},
		"formatHashrate": func(h float64) string {
			units := []string{"H/s", "KH/s", "MH/s", "GH/s", "TH/s", "PH/s"}
			unit := units[0]
			val := h
			for i := 0; i < len(units)-1 && val >= 1000; i++ {
				val /= 1000
				unit = units[i+1]
			}
			return fmt.Sprintf("%.3f %s", val, unit)
		},
		"formatDiff": func(d float64) string {
			if d <= 0 {
				return "0"
			}
			// Show difficulty in millions with a single decimal, e.g.
			//  512         -> "512"
			//  4096        -> "4096"
			// 21_546_700   -> "21.5M"
			if d < 1_000_000 {
				return fmt.Sprintf("%.0f", math.Round(d))
			}
			val := d / 1_000_000.0
			return fmt.Sprintf("%.1fM", val)
		},
		"formatTime": func(t time.Time) string {
			if t.IsZero() {
				return "—"
			}
			s := humanShortDuration(time.Since(t))
			if s == "just now" {
				return s
			}
			return s + " ago"
		},
		"formatTimeUTC": func(t time.Time) string {
			if t.IsZero() {
				return "—"
			}
			return t.UTC().Format("2006-01-02 15:04:05 UTC")
		},
		"addrPort": func(addr string) string {
			if addr == "" {
				return "—"
			}
			_, port, err := net.SplitHostPort(addr)
			if err != nil {
				return addr
			}
			return port
		},
		"formatBytes": func(b uint64) string {
			const unit = 1024.0
			if b == 0 {
				return "0 B"
			}
			val := float64(b)
			units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
			u := units[0]
			for i := 0; i < len(units)-1 && val >= unit; i++ {
				val /= unit
				u = units[i+1]
			}
			return fmt.Sprintf("%.2f %s", val, u)
		},
		"formatShareRate": func(r float64) string {
			if r < 0 {
				r = 0
			}
			units := []string{"", "K", "M", "G"}
			val := r
			unit := units[0]
			for i := 0; i < len(units)-1 && val >= 1000; i++ {
				val /= 1000
				unit = units[i+1]
			}
			if unit == "" {
				return fmt.Sprintf("%.2f", val)
			}
			return fmt.Sprintf("%.2f %s", val, unit)
		},
		"formatBTC": func(sats int64) string {
			if sats == 0 {
				return "0 BTC"
			}
			btc := float64(sats) / 1e8
			return fmt.Sprintf("%.8f BTC", btc)
		},
		"formatBTCShort": func(sats int64) string {
			btc := float64(sats) / 1e8
			return fmt.Sprintf("%.8f BTC", btc)
		},
		"formatFiat": func(sats int64, price float64, currency string) string {
			if sats == 0 || price <= 0 {
				return ""
			}
			btc := float64(sats) / 1e8
			amt := btc * price
			cur := strings.ToUpper(strings.TrimSpace(currency))
			if cur == "" {
				cur = "USD"
			}
			return fmt.Sprintf("≈ %.2f %s", amt, cur)
		},
		"formatRenderDuration": func(d time.Duration) string {
			if d <= 0 {
				return "0s"
			}
			if d < time.Millisecond {
				return "<1ms"
			}
			ms := float64(d) / float64(time.Millisecond)
			return fmt.Sprintf("%.0fms", ms)
		},
	}
}

// loadTemplates loads and parses all HTML templates from the specified data directory.
// It returns a fully configured template or an error if any template fails to load or parse.
func loadTemplates(dataDir string) (*template.Template, error) {
	funcs := buildTemplateFuncs()

	// Build template paths
	layoutPath := filepath.Join(dataDir, "templates", "layout.tmpl")
	statusPath := filepath.Join(dataDir, "templates", "overview.tmpl")
	serverInfoPath := filepath.Join(dataDir, "templates", "server.tmpl")
	workerLoginPath := filepath.Join(dataDir, "templates", "worker_login.tmpl")
	workerStatusPath := filepath.Join(dataDir, "templates", "worker_status.tmpl")
	nodeInfoPath := filepath.Join(dataDir, "templates", "node.tmpl")
	poolInfoPath := filepath.Join(dataDir, "templates", "pool.tmpl")
	aboutPath := filepath.Join(dataDir, "templates", "about.tmpl")
	errorPath := filepath.Join(dataDir, "templates", "error.tmpl")

	// Load template files
	layoutHTML, err := os.ReadFile(layoutPath)
	if err != nil {
		return nil, fmt.Errorf("load layout template: %w", err)
	}
	statusHTML, err := os.ReadFile(statusPath)
	if err != nil {
		return nil, fmt.Errorf("load status template: %w", err)
	}
	serverInfoHTML, err := os.ReadFile(serverInfoPath)
	if err != nil {
		return nil, fmt.Errorf("load server info template: %w", err)
	}
	workerLoginHTML, err := os.ReadFile(workerLoginPath)
	if err != nil {
		return nil, fmt.Errorf("load worker login template: %w", err)
	}
	workerStatusHTML, err := os.ReadFile(workerStatusPath)
	if err != nil {
		return nil, fmt.Errorf("load worker status template: %w", err)
	}
	nodeInfoHTML, err := os.ReadFile(nodeInfoPath)
	if err != nil {
		return nil, fmt.Errorf("load node info template: %w", err)
	}
	poolInfoHTML, err := os.ReadFile(poolInfoPath)
	if err != nil {
		return nil, fmt.Errorf("load pool info template: %w", err)
	}
	aboutHTML, err := os.ReadFile(aboutPath)
	if err != nil {
		return nil, fmt.Errorf("load about template: %w", err)
	}
	errorHTML, err := os.ReadFile(errorPath)
	if err != nil {
		return nil, fmt.Errorf("load error template: %w", err)
	}

	// Parse templates
	tmpl := template.New("overview").Funcs(funcs)
	template.Must(tmpl.Parse(string(layoutHTML)))
	tmpl = template.Must(tmpl.New("overview").Parse(string(statusHTML)))
	template.Must(tmpl.New("server").Parse(string(serverInfoHTML)))
	template.Must(tmpl.New("worker_login").Parse(string(workerLoginHTML)))
	template.Must(tmpl.New("worker_status").Parse(string(workerStatusHTML)))
	template.Must(tmpl.New("node").Parse(string(nodeInfoHTML)))
	template.Must(tmpl.New("pool").Parse(string(poolInfoHTML)))
	template.Must(tmpl.New("about").Parse(string(aboutHTML)))
	template.Must(tmpl.New("error").Parse(string(errorHTML)))

	return tmpl, nil
}

func NewStatusServer(ctx context.Context, jobMgr *JobManager, metrics *PoolMetrics, registry *MinerRegistry, accounting *AccountStore, rpc *RPCClient, cfg Config, start time.Time) *StatusServer {
	// Load HTML templates from data_dir/templates so operators can customize the
	// UI without recompiling. These are treated as required assets.
	tmpl, err := loadTemplates(cfg.DataDir)
	if err != nil {
		fatal("load templates", err)
	}

	if ctx == nil {
		ctx = context.Background()
	}

	server := &StatusServer{
		tmpl:                tmpl,
		jobMgr:              jobMgr,
		metrics:             metrics,
		registry:            registry,
		accounting:          accounting,
		rpc:                 rpc,
		cfg:                 cfg,
		ctx:                 ctx,
		start:               start,
		workerLookupLimiter: newWorkerLookupRateLimiter(workerLookupRateLimitMax, workerLookupRateLimitWindow),
		priceSvc:            NewPriceService(),
		jsonCache:           make(map[string]cachedJSONResponse),
	}
	server.scheduleNodeInfoRefresh()
	return server
}

// ReloadTemplates reloads all HTML templates from disk. This allows operators
// to update templates without restarting the pool server. It's designed to be
// called in response to SIGUSR1 or other reload triggers.
func (s *StatusServer) ReloadTemplates() error {
	if s == nil {
		return fmt.Errorf("status server is nil")
	}

	tmpl, err := loadTemplates(s.cfg.DataDir)
	if err != nil {
		return err
	}

	// Atomically replace the template
	s.tmpl = tmpl
	logger.Info("templates reloaded successfully")
	return nil
}

// handleRPCResult is registered as an RPCClient result hook to opportunistically
// warm cached node info based on normal RPC traffic. It never changes how
// callers use the RPC client; it only updates StatusServer's own cache.
func (s *StatusServer) handleRPCResult(method string, params interface{}, raw stdjson.RawMessage) {
	if s == nil {
		return
	}

	switch method {
	case "getblockchaininfo":
		var bc struct {
			Chain                string  `json:"chain"`
			Blocks               int64   `json:"blocks"`
			Headers              int64   `json:"headers"`
			InitialBlockDownload bool    `json:"initialblockdownload"`
			Pruned               bool    `json:"pruned"`
			SizeOnDisk           float64 `json:"size_on_disk"`
		}
		if err := sonic.Unmarshal(raw, &bc); err != nil {
			return
		}
		s.nodeInfoMu.Lock()
		defer s.nodeInfoMu.Unlock()
		now := time.Now()
		if s.nodeInfo.fetchedAt.IsZero() || now.Sub(s.nodeInfo.fetchedAt) >= nodeInfoTTL {
			var info cachedNodeInfo = s.nodeInfo
			chain := strings.ToLower(strings.TrimSpace(bc.Chain))
			switch chain {
			case "main", "mainnet", "":
				info.network = "mainnet"
			case "test", "testnet", "testnet3", "testnet4":
				info.network = "testnet"
			case "signet":
				info.network = "signet"
			case "regtest":
				info.network = "regtest"
			default:
				info.network = bc.Chain
			}
			info.blocks = bc.Blocks
			info.headers = bc.Headers
			info.ibd = bc.InitialBlockDownload
			info.pruned = bc.Pruned
			if bc.SizeOnDisk > 0 {
				info.sizeOnDisk = uint64(bc.SizeOnDisk)
			}
			info.fetchedAt = now
			s.nodeInfo = info
		}
	case "getnetworkinfo":
		var netInfo struct {
			Subversion     string `json:"subversion"`
			Connections    int    `json:"connections"`
			ConnectionsIn  int    `json:"connections_in"`
			ConnectionsOut int    `json:"connections_out"`
		}
		if err := sonic.Unmarshal(raw, &netInfo); err != nil {
			return
		}
		s.nodeInfoMu.Lock()
		defer s.nodeInfoMu.Unlock()
		now := time.Now()
		if s.nodeInfo.fetchedAt.IsZero() || now.Sub(s.nodeInfo.fetchedAt) >= nodeInfoTTL {
			var info cachedNodeInfo = s.nodeInfo
			info.subversion = strings.TrimSpace(netInfo.Subversion)
			info.conns = netInfo.Connections
			info.connsIn = netInfo.ConnectionsIn
			info.connsOut = netInfo.ConnectionsOut
			info.fetchedAt = now
			s.nodeInfo = info
		}
	case "getblockhash":
		// Only care about genesis hash (height 0) to avoid polluting cache
		// with unrelated getblockhash calls.
		args, ok := params.([]interface{})
		if !ok || len(args) != 1 {
			return
		}
		h, ok := args[0].(float64)
		if !ok || int64(h) != 0 {
			return
		}
		var genesis string
		if err := sonic.Unmarshal(raw, &genesis); err != nil {
			return
		}
		genesis = strings.TrimSpace(genesis)
		if genesis == "" {
			return
		}
		s.nodeInfoMu.Lock()
		if s.nodeInfo.genesisHash == "" {
			s.nodeInfo.genesisHash = genesis
		}
		s.nodeInfoMu.Unlock()
	case "getbestblockhash":
		var best string
		if err := sonic.Unmarshal(raw, &best); err != nil {
			return
		}
		best = strings.TrimSpace(best)
		if best == "" {
			return
		}
		s.nodeInfoMu.Lock()
		s.nodeInfo.bestHash = best
		s.nodeInfoMu.Unlock()
	}
}

// SetJobManager attaches a JobManager after the status server has started.
func (s *StatusServer) SetJobManager(jm *JobManager) {
	s.jobMgr = jm
}

func (s *StatusServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/" || r.URL.Path == "":
		start := time.Now()
		data := s.baseTemplateData(start)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := s.tmpl.ExecuteTemplate(w, "overview", data); err != nil {
			logger.Error("status template error", "error", err)
			s.renderErrorPage(w, r, http.StatusInternalServerError,
				"Status page error",
				"We couldn't render the pool status page.",
				"Template error while rendering the main status view.")
		}
	default:
		s.renderErrorPage(w, r, http.StatusNotFound,
			"Page not found",
			"The page you requested could not be found.",
			"Check the URL or use the navigation links above.")
	}
}

// PoolStatsData contains essential pool statistics without worker details
type PoolStatsData struct {
	APIVersion              string              `json:"api_version"`
	BrandName               string              `json:"brand_name"`
	BrandDomain             string              `json:"brand_domain"`
	ListenAddr              string              `json:"listen_addr"`
	StratumTLSListen        string              `json:"stratum_tls_listen,omitempty"`
	PoolSoftware            string              `json:"pool_software"`
	BuildTime               string              `json:"build_time"`
	Uptime                  time.Duration       `json:"uptime"`
	ActiveMiners            int                 `json:"active_miners"`
	PoolHashrate            float64             `json:"pool_hashrate"`
	SharesPerSecond         float64             `json:"shares_per_second"`
	Accepted                uint64              `json:"accepted"`
	Rejected                uint64              `json:"rejected"`
	StaleShares             uint64              `json:"stale_shares"`
	LowDiffShares           uint64              `json:"low_diff_shares"`
	RejectReasons           map[string]uint64   `json:"reject_reasons,omitempty"`
	WindowAccepted          uint64              `json:"window_accepted"`
	WindowSubmissions       uint64              `json:"window_submissions"`
	WindowStart             string              `json:"window_start"`
	VardiffUp               uint64              `json:"vardiff_up"`
	VardiffDown             uint64              `json:"vardiff_down"`
	BlocksAccepted          uint64              `json:"blocks_accepted"`
	BlocksErrored           uint64              `json:"blocks_errored"`
	MinDifficulty           float64             `json:"min_difficulty"`
	MaxDifficulty           float64             `json:"max_difficulty"`
	PoolFeePercent          float64             `json:"pool_fee_percent"`
	OperatorDonationPercent float64             `json:"operator_donation_percent,omitempty"`
	OperatorDonationName    string              `json:"operator_donation_name,omitempty"`
	OperatorDonationURL     string              `json:"operator_donation_url,omitempty"`
	CurrentJob              *Job                `json:"current_job,omitempty"`
	JobCreated              string              `json:"job_created"`
	TemplateTime            string              `json:"template_time"`
	JobFeed                 JobFeedView         `json:"job_feed"`
	BTCPriceFiat            float64             `json:"btc_price_fiat,omitempty"`
	BTCPriceUpdatedAt       string              `json:"btc_price_updated_at,omitempty"`
	FiatCurrency            string              `json:"fiat_currency,omitempty"`
	WorkerDatabase          WorkerDatabaseStats `json:"worker_database"`
	Warnings                []string            `json:"warnings,omitempty"`
}

// NodePageData contains Bitcoin node information for the node page
type NodePageData struct {
	APIVersion               string   `json:"api_version"`
	NodeNetwork              string   `json:"node_network,omitempty"`
	NodeSubversion           string   `json:"node_subversion,omitempty"`
	NodeBlocks               int64    `json:"node_blocks"`
	NodeHeaders              int64    `json:"node_headers"`
	NodeInitialBlockDownload bool     `json:"node_initial_block_download"`
	NodeConnections          int      `json:"node_connections"`
	NodeConnectionsIn        int      `json:"node_connections_in"`
	NodeConnectionsOut       int      `json:"node_connections_out"`
	NodePeers                []string `json:"node_peers,omitempty"`
	NodePruned               bool     `json:"node_pruned"`
	NodeSizeOnDiskBytes      uint64   `json:"node_size_on_disk_bytes"`
	GenesisHash              string   `json:"genesis_hash,omitempty"`
	GenesisExpected          string   `json:"genesis_expected,omitempty"`
	GenesisMatch             bool     `json:"genesis_match"`
	BestBlockHash            string   `json:"best_block_hash,omitempty"`
}

// WorkersListData contains paginated worker information
type WorkersListData struct {
	APIVersion    string          `json:"api_version"`
	Workers       []WorkerView    `json:"workers"`
	BannedWorkers []WorkerView    `json:"banned_workers"`
	BestShares    []BestShare     `json:"best_shares"`
	MinerTypes    []MinerTypeView `json:"miner_types,omitempty"`
	Page          int             `json:"page,omitempty"`
	Pages         int             `json:"pages,omitempty"`
	Total         int             `json:"total,omitempty"`
	PerPage       int             `json:"per_page,omitempty"`
}

// DiagnosticsData contains system diagnostics (server-only)
type DiagnosticsData struct {
	APIVersion          string  `json:"api_version"`
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
	ShareErrors         uint64  `json:"share_errors"`
	AccountingError     string  `json:"accounting_error,omitempty"`
}

func (s *StatusServer) cachedJSONResponse(key string, ttl time.Duration, build func() ([]byte, error)) ([]byte, time.Time, time.Time, error) {
	now := time.Now()
	s.jsonCacheMu.RLock()
	entry, ok := s.jsonCache[key]
	if ok && now.Before(entry.expiresAt) && len(entry.payload) > 0 {
		payload := entry.payload
		s.jsonCacheMu.RUnlock()
		return payload, entry.updatedAt, entry.expiresAt, nil
	}
	s.jsonCacheMu.RUnlock()

	payload, err := build()
	if err != nil {
		return nil, time.Time{}, time.Time{}, err
	}

	updatedAt := time.Now()
	s.jsonCacheMu.Lock()
	s.jsonCache[key] = cachedJSONResponse{
		payload:   payload,
		updatedAt: updatedAt,
		expiresAt: updatedAt.Add(ttl),
	}
	s.jsonCacheMu.Unlock()
	return payload, updatedAt, updatedAt.Add(ttl), nil
}

func (s *StatusServer) serveCachedJSON(w http.ResponseWriter, key string, ttl time.Duration, build func() ([]byte, error)) {
	payload, updatedAt, expiresAt, err := s.cachedJSONResponse(key, ttl, build)
	if err != nil {
		logger.Error("cached json response error", "key", key, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("X-JSON-Updated-At", updatedAt.UTC().Format(time.RFC3339))
	w.Header().Set("X-JSON-Next-Update-At", expiresAt.UTC().Format(time.RFC3339))
	if _, err := w.Write(payload); err != nil {
		logger.Error("write cached json response", "key", key, "error", err)
	}
}

// handlePoolStatsJSON returns essential pool statistics
func (s *StatusServer) handlePoolStatsJSON(w http.ResponseWriter, r *http.Request) {
	key := "pool_stats"
	s.serveCachedJSON(w, key, overviewRefreshInterval, func() ([]byte, error) {
		full := s.buildCensoredStatusData()
		data := PoolStatsData{
			APIVersion:              apiVersion,
			BrandName:               full.BrandName,
			BrandDomain:             full.BrandDomain,
			ListenAddr:              full.ListenAddr,
			StratumTLSListen:        full.StratumTLSListen,
			PoolSoftware:            full.PoolSoftware,
			BuildTime:               full.BuildTime,
			Uptime:                  full.Uptime,
			ActiveMiners:            full.ActiveMiners,
			PoolHashrate:            full.PoolHashrate,
			SharesPerSecond:         full.SharesPerSecond,
			Accepted:                full.Accepted,
			Rejected:                full.Rejected,
			StaleShares:             full.StaleShares,
			LowDiffShares:           full.LowDiffShares,
			RejectReasons:           full.RejectReasons,
			WindowAccepted:          full.WindowAccepted,
			WindowSubmissions:       full.WindowSubmissions,
			WindowStart:             full.WindowStart,
			VardiffUp:               full.VardiffUp,
			VardiffDown:             full.VardiffDown,
			BlocksAccepted:          full.BlocksAccepted,
			BlocksErrored:           full.BlocksErrored,
			MinDifficulty:           full.MinDifficulty,
			MaxDifficulty:           full.MaxDifficulty,
			PoolFeePercent:          full.PoolFeePercent,
			OperatorDonationPercent: full.OperatorDonationPercent,
			OperatorDonationName:    full.OperatorDonationName,
			OperatorDonationURL:     full.OperatorDonationURL,
			CurrentJob:              nil, // Excluded for security
			JobCreated:              full.JobCreated,
			TemplateTime:            full.TemplateTime,
			JobFeed:                 full.JobFeed,
			BTCPriceFiat:            full.BTCPriceFiat,
			BTCPriceUpdatedAt:       full.BTCPriceUpdatedAt,
			FiatCurrency:            full.FiatCurrency,
			WorkerDatabase:          full.WorkerDatabase,
			Warnings:                full.Warnings,
		}
		return sonic.Marshal(data)
	})
}

// handleNodeStatsJSON returns Bitcoin node information
func (s *StatusServer) handleNodePageJSON(w http.ResponseWriter, r *http.Request) {
	key := "node_page"
	s.serveCachedJSON(w, key, overviewRefreshInterval, func() ([]byte, error) {
		full := s.buildCensoredStatusData()
		data := NodePageData{
			APIVersion:               apiVersion,
			NodeNetwork:              full.NodeNetwork,
			NodeSubversion:           full.NodeSubversion,
			NodeBlocks:               full.NodeBlocks,
			NodeHeaders:              full.NodeHeaders,
			NodeInitialBlockDownload: full.NodeInitialBlockDownload,
			NodeConnections:          full.NodeConnections,
			NodeConnectionsIn:        full.NodeConnectionsIn,
			NodeConnectionsOut:       full.NodeConnectionsOut,
			NodePeers:                full.NodePeerAddresses,
			NodePruned:               full.NodePruned,
			NodeSizeOnDiskBytes:      full.NodeSizeOnDiskBytes,
			GenesisHash:              full.GenesisHash,
			GenesisExpected:          full.GenesisExpected,
			GenesisMatch:             full.GenesisMatch,
			BestBlockHash:            full.BestBlockHash,
		}
		return sonic.Marshal(data)
	})
}

// censorWorkerView censors sensitive data in a WorkerView for public API endpoints
func censorWorkerView(w WorkerView) WorkerView {
	// Censor worker name - many workers use their wallet address as the name
	if w.Name != "" {
		w.Name = shortWorkerName(w.Name, workerNamePrefix, workerNameSuffix)
	}
	// Display name should also be censored
	if w.DisplayName != "" {
		w.DisplayName = shortWorkerName(w.DisplayName, workerNamePrefix, workerNameSuffix)
	}
	// Censor full wallet address - keep first 8 and last 8 chars
	if w.WalletAddress != "" {
		w.WalletAddress = shortDisplayID(w.WalletAddress, 8, 8)
	}
	// Remove the raw wallet script entirely from public endpoints
	w.WalletScript = ""
	// Censor last share hash
	if w.LastShareHash != "" {
		w.LastShareHash = shortDisplayID(w.LastShareHash, hashPrefix, hashSuffix)
	}
	// Censor display last share hash if present
	if w.DisplayLastShare != "" && len(w.DisplayLastShare) > 20 {
		w.DisplayLastShare = shortDisplayID(w.DisplayLastShare, hashPrefix, hashSuffix)
	}
	// Remove detailed share debug info from public endpoints
	w.LastShareDetail = nil
	return w
}

// censorBestShare censors sensitive data in a BestShare for public API endpoints
func censorBestShare(b BestShare) BestShare {
	if b.Hash != "" {
		b.Hash = shortDisplayID(b.Hash, hashPrefix, hashSuffix)
	}
	if b.Worker != "" {
		b.Worker = shortWorkerName(b.Worker, workerNamePrefix, workerNameSuffix)
	}
	return b
}

// handleWorkersListJSON returns paginated worker list
func (s *StatusServer) handleWorkersListJSON(w http.ResponseWriter, r *http.Request) {
	page := 1
	perPage := 50

	if p := strings.TrimSpace(r.URL.Query().Get("page")); p != "" {
		if n, err := strconv.Atoi(p); err == nil && n > 0 {
			page = n
		}
	}
	if pp := strings.TrimSpace(r.URL.Query().Get("per_page")); pp != "" {
		if n, err := strconv.Atoi(pp); err == nil && n > 0 && n <= 200 {
			perPage = n
		}
	}

	key := fmt.Sprintf("workers_%d_%d", page, perPage)
	s.serveCachedJSON(w, key, overviewRefreshInterval, func() ([]byte, error) {
		// Get censored data from source
		full := s.buildCensoredStatusData()

		var workers []WorkerView
		var bannedWorkers []WorkerView

		workers = append(workers, full.Workers...)
		bannedWorkers = append(bannedWorkers, full.BannedWorkers...)

		bestShares := full.BestShares

		total := len(workers)
		pages := 1
		if total > 0 {
			pages = (total + perPage - 1) / perPage
		}
		if page > pages {
			page = pages
		}
		if page <= 0 {
			page = 1
		}

		startIdx := (page - 1) * perPage
		if startIdx > total {
			startIdx = total
		}
		endIdx := startIdx + perPage
		if endIdx > total {
			endIdx = total
		}

		typeCounts := make(map[string]map[string]int)
		for _, w := range workers {
			name := strings.TrimSpace(w.MinerName)
			version := strings.TrimSpace(w.MinerVersion)
			if name == "" {
				t := strings.TrimSpace(w.MinerType)
				if t != "" {
					name, version = parseMinerID(t)
				}
			}
			if name == "" {
				name = "(unknown)"
			}
			if typeCounts[name] == nil {
				typeCounts[name] = make(map[string]int)
			}
			typeCounts[name][version]++
		}

		var minerTypes []MinerTypeView
		for name, versions := range typeCounts {
			mt := MinerTypeView{Name: name}
			for v, count := range versions {
				mt.Versions = append(mt.Versions, MinerTypeVersionView{
					Version: v,
					Workers: count,
				})
				mt.Total += count
			}
			sort.Slice(mt.Versions, func(i, j int) bool {
				if mt.Versions[i].Workers == mt.Versions[j].Workers {
					return mt.Versions[i].Version < mt.Versions[j].Version
				}
				return mt.Versions[i].Workers > mt.Versions[j].Workers
			})
			minerTypes = append(minerTypes, mt)
		}
		sort.Slice(minerTypes, func(i, j int) bool {
			if minerTypes[i].Total == minerTypes[j].Total {
				return minerTypes[i].Name < minerTypes[j].Name
			}
			return minerTypes[i].Total > minerTypes[j].Total
		})

		data := WorkersListData{
			APIVersion:    apiVersion,
			Workers:       workers[startIdx:endIdx],
			BannedWorkers: bannedWorkers,
			BestShares:    bestShares,
			MinerTypes:    minerTypes,
			Page:          page,
			Pages:         pages,
			Total:         total,
			PerPage:       perPage,
		}
		return sonic.Marshal(data)
	})
}

// handleBlocksListJSON returns found blocks
func (s *StatusServer) handleBlocksListJSON(w http.ResponseWriter, r *http.Request) {
	limit := 10
	if l := strings.TrimSpace(r.URL.Query().Get("limit")); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}

	key := fmt.Sprintf("blocks_%d", limit)
	s.serveCachedJSON(w, key, overviewRefreshInterval, func() ([]byte, error) {
		full := s.buildCensoredStatusData()
		blocks := full.FoundBlocks
		if len(blocks) > limit {
			blocks = blocks[:limit]
		}
		return sonic.Marshal(blocks)
	})
}

// handleOverviewPageJSON returns minimal data for the overview page
func (s *StatusServer) handleOverviewPageJSON(w http.ResponseWriter, r *http.Request) {
	key := "overview_page"
	s.serveCachedJSON(w, key, overviewRefreshInterval, func() ([]byte, error) {
		full := s.buildCensoredStatusData()

		// Convert full WorkerView to minimal RecentWorkView for the overview page
		recentWork := make([]RecentWorkView, len(full.Workers))
		for i, w := range full.Workers {
			recentWork[i] = RecentWorkView{
				Name:            w.Name,
				DisplayName:     w.DisplayName,
				RollingHashrate: w.RollingHashrate,
				Difficulty:      w.Difficulty,
				ShareRate:       w.ShareRate,
				Accepted:        w.Accepted,
			}
		}

		data := OverviewPageData{
			APIVersion:      apiVersion,
			ActiveMiners:    full.ActiveMiners,
			ActiveTLSMiners: full.ActiveTLSMiners,
			SharesPerMinute: full.SharesPerMinute,
			PoolHashrate:    full.PoolHashrate,
			RenderDuration:  full.RenderDuration,
			Workers:         recentWork,
			BannedWorkers:   full.BannedWorkers,
			BestShares:      full.BestShares,
			FoundBlocks:     full.FoundBlocks,
			MinerTypes:      full.MinerTypes,
		}
		return sonic.Marshal(data)
	})
}

// handlePoolPageJSON returns pool configuration data for the pool info page
func (s *StatusServer) handlePoolPageJSON(w http.ResponseWriter, r *http.Request) {
	key := "pool_page"
	s.serveCachedJSON(w, key, overviewRefreshInterval, func() ([]byte, error) {
		full := s.buildCensoredStatusData()
		data := PoolPageData{
			APIVersion:       apiVersion,
			BlocksAccepted:   full.BlocksAccepted,
			BlocksErrored:    full.BlocksErrored,
			RPCGBTLastSec:    full.RPCGBTLastSec,
			RPCGBTMaxSec:     full.RPCGBTMaxSec,
			RPCGBTCount:      full.RPCGBTCount,
			RPCSubmitLastSec: full.RPCSubmitLastSec,
			RPCSubmitMaxSec:  full.RPCSubmitMaxSec,
			RPCSubmitCount:   full.RPCSubmitCount,
			RPCErrors:        full.RPCErrors,
			ShareErrors:      full.ShareErrors,
		}
		return sonic.Marshal(data)
	})
}

// handleServerPageJSON returns combined status and diagnostics for the server page
func (s *StatusServer) handleServerPageJSON(w http.ResponseWriter, r *http.Request) {
	key := "server_page"
	s.serveCachedJSON(w, key, overviewRefreshInterval, func() ([]byte, error) {
		full := s.buildCensoredStatusData()
		data := ServerPageData{
			APIVersion:      apiVersion,
			Uptime:          full.Uptime,
			RPCError:        full.RPCError,
			AccountingError: full.AccountingError,
			JobFeed: ServerPageJobFeed{
				LastError:         full.JobFeed.LastError,
				LastErrorAt:       full.JobFeed.LastErrorAt,
				ErrorHistory:      full.JobFeed.ErrorHistory,
				ZMQHealthy:        full.JobFeed.ZMQHealthy,
				ZMQDisconnects:    full.JobFeed.ZMQDisconnects,
				ZMQReconnects:     full.JobFeed.ZMQReconnects,
				LastRawBlockAt:    full.JobFeed.LastRawBlockAt,
				LastRawBlockBytes: full.JobFeed.LastRawBlockBytes,
				LastHashTx:        full.JobFeed.LastHashTx,
				LastHashTxAt:      full.JobFeed.LastHashTxAt,
				LastRawTxAt:       full.JobFeed.LastRawTxAt,
				LastRawTxBytes:    full.JobFeed.LastRawTxBytes,
				BlockHash:         full.JobFeed.BlockHash,
				BlockHeight:       full.JobFeed.BlockHeight,
				BlockTime:         full.JobFeed.BlockTime,
				BlockBits:         full.JobFeed.BlockBits,
				BlockDifficulty:   full.JobFeed.BlockDifficulty,
			},
			ProcessGoroutines:   full.ProcessGoroutines,
			ProcessCPUPercent:   full.ProcessCPUPercent,
			GoMemAllocBytes:     full.GoMemAllocBytes,
			GoMemSysBytes:       full.GoMemSysBytes,
			ProcessRSSBytes:     full.ProcessRSSBytes,
			SystemMemTotalBytes: full.SystemMemTotalBytes,
			SystemMemFreeBytes:  full.SystemMemFreeBytes,
			SystemMemUsedBytes:  full.SystemMemUsedBytes,
			SystemLoad1:         full.SystemLoad1,
			SystemLoad5:         full.SystemLoad5,
			SystemLoad15:        full.SystemLoad15,
		}
		return sonic.Marshal(data)
	})
}

// handleDiagnosticsJSON returns system diagnostics
func (s *StatusServer) handleDiagnosticsJSON(w http.ResponseWriter, r *http.Request) {
	key := "diagnostics"
	s.serveCachedJSON(w, key, overviewRefreshInterval, func() ([]byte, error) {
		full := s.statusData()
		data := DiagnosticsData{
			APIVersion:          apiVersion,
			ProcessGoroutines:   full.ProcessGoroutines,
			ProcessCPUPercent:   full.ProcessCPUPercent,
			GoMemAllocBytes:     full.GoMemAllocBytes,
			GoMemSysBytes:       full.GoMemSysBytes,
			ProcessRSSBytes:     full.ProcessRSSBytes,
			SystemMemTotalBytes: full.SystemMemTotalBytes,
			SystemMemFreeBytes:  full.SystemMemFreeBytes,
			SystemMemUsedBytes:  full.SystemMemUsedBytes,
			SystemLoad1:         full.SystemLoad1,
			SystemLoad5:         full.SystemLoad5,
			SystemLoad15:        full.SystemLoad15,
			ShareErrors:         full.ShareErrors,
			AccountingError:     full.AccountingError,
		}
		return sonic.Marshal(data)
	})
}

func (s *StatusServer) handlePoolHashrateJSON(w http.ResponseWriter, r *http.Request) {
	key := "pool_hashrate"
	s.serveCachedJSON(w, key, poolHashrateTTL, func() ([]byte, error) {
		data := struct {
			APIVersion   string  `json:"api_version"`
			PoolHashrate float64 `json:"pool_hashrate"`
			UpdatedAt    string  `json:"updated_at"`
		}{
			APIVersion:   apiVersion,
			PoolHashrate: s.computePoolHashrate(),
			UpdatedAt:    time.Now().UTC().Format(time.RFC3339),
		}
		return sonic.Marshal(data)
	})
}

// handleWorkerStatus renders the worker login page.
func (s *StatusServer) handleWorkerStatus(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	base := s.baseTemplateData(start)

	data := WorkerStatusData{
		StatusData: base,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "worker_login", data); err != nil {
		logger.Error("worker login template error", "error", err)
		s.renderErrorPage(w, r, http.StatusInternalServerError,
			"Worker login page error",
			"We couldn't render the worker login page.",
			"Template error while rendering worker login.")
	}
}

// handleWorkerStatusBySHA256 handles worker lookups using pre-computed SHA256 hashes.
// This endpoint expects the SHA256 hash of the worker name and performs direct lookup.
func (s *StatusServer) handleWorkerStatusBySHA256(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	base := s.baseTemplateData(start)

	// Best-effort derivation of the pool payout script so the UI can
	// label coinbase outputs by destination. Errors are treated as
	// "unknown" and do not affect normal worker stats rendering.
	var poolScriptHex string
	addr := strings.TrimSpace(s.cfg.PayoutAddress)
	if addr != "" {
		if script, err := scriptForAddress(addr, ChainParams()); err == nil && len(script) > 0 {
			poolScriptHex = strings.ToLower(hex.EncodeToString(script))
		}
	}

	// Best-effort derivation of the donation script for 3-way payout display.
	var donationScriptHex string
	donationAddr := strings.TrimSpace(s.cfg.OperatorDonationAddress)
	if donationAddr != "" && s.cfg.OperatorDonationPercent > 0 {
		if script, err := scriptForAddress(donationAddr, ChainParams()); err == nil && len(script) > 0 {
			donationScriptHex = strings.ToLower(hex.EncodeToString(script))
		}
	}

	// Best-effort BTC price lookup for fiat hints on the worker page.
	var btcPrice float64
	var btcPriceUpdated string
	if s.priceSvc != nil {
		if price, err := s.priceSvc.BTCPrice(s.cfg.FiatCurrency); err == nil && price > 0 {
			btcPrice = price
			if ts := s.priceSvc.LastUpdate(); !ts.IsZero() {
				btcPriceUpdated = ts.UTC().Format("2006-01-02 15:04:05 MST")
			}
		}
	}

	privacyMode := workerPrivacyModeFromRequest(r)
	data := WorkerStatusData{
		StatusData:        base,
		PoolScriptHex:     poolScriptHex,
		DonationScriptHex: donationScriptHex,
		PrivacyMode:       privacyMode,
	}
	data.BTCPriceFiat = btcPrice
	data.BTCPriceUpdatedAt = btcPriceUpdated

	var workerHash string
	switch r.Method {
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			data.Error = "invalid form submission"
			break
		}
		workerHash = strings.TrimSpace(r.FormValue("hash"))
	case http.MethodGet:
		workerHash = strings.TrimSpace(r.URL.Query().Get("hash"))
	}

	now := time.Now()

	if workerHash != "" {
		// Validate hash format (64 hex characters for SHA256)
		if len(workerHash) != 64 {
			data.Error = "invalid hash format (expected 64 hex characters)"
		} else {
			data.QueriedWorker = workerHash
			data.QueriedWorkerHash = workerHash

			// Cache HTML for this worker/privacymode keyed by SHA so repeated
			// refreshes avoid rebuilding the page. This is a best-effort cache:
			// failures simply fall back to on-demand rendering.
			cacheKey := workerHash
			if privacyMode {
				cacheKey = cacheKey + "|priv"
			}
			s.workerPageMu.RLock()
			if entry, ok := s.workerPageCache[cacheKey]; ok && now.Before(entry.expiresAt) {
				payload := entry.payload
				s.workerPageMu.RUnlock()
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = w.Write(payload)
				return
			}
			s.workerPageMu.RUnlock()

			if wv, ok := s.findWorkerViewByHash(workerHash, now); ok {
				setWorkerStatusView(&data, wv, privacyMode)
				if data.BTCPriceFiat > 0 {
					cur := strings.ToUpper(strings.TrimSpace(data.FiatCurrency))
					if cur == "" {
						cur = "USD"
					}
					base := fmt.Sprintf("Approximate fiat values (%s) use 1 BTC ≈ %.2f", cur, data.BTCPriceFiat)
					if data.BTCPriceUpdatedAt != "" {
						base += " (price updated " + data.BTCPriceUpdatedAt + ")"
					}
					data.FiatNote = base + "; payouts are always made in BTC."
				}
			} else if s.accounting != nil {
				if wv, ok := s.accounting.WorkerViewBySHA256(workerHash); ok {
					setWorkerStatusView(&data, wv, privacyMode)
					if data.BTCPriceFiat > 0 {
						cur := strings.ToUpper(strings.TrimSpace(data.FiatCurrency))
						if cur == "" {
							cur = "USD"
						}
						base := fmt.Sprintf("Approximate fiat values (%s) use 1 BTC ≈ %.2f", cur, data.BTCPriceFiat)
						if data.BTCPriceUpdatedAt != "" {
							base += " (price updated " + data.BTCPriceUpdatedAt + ")"
						}
						data.FiatNote = base + "; payouts are always made in BTC."
					}
				} else {
					data.Error = "worker not found"
				}
			} else {
				data.Error = "worker not found"
			}
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Render into a buffer so we can cache the HTML on success without
	// risking partial writes on template errors.
	buf := templateBufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer templateBufferPool.Put(buf)
	if err := s.tmpl.ExecuteTemplate(buf, "worker_status", data); err != nil {
		logger.Error("worker status template error", "error", err)
		s.renderErrorPage(w, r, http.StatusInternalServerError,
			"Worker page error",
			"We couldn't render the worker info page.",
			"Template error while rendering worker details.")
		return
	}
	// Cache successful renders keyed by worker hash and privacy mode.
	if workerHash != "" && len(workerHash) == 64 && data.Error == "" {
		cacheKey := workerHash
		if privacyMode {
			cacheKey = cacheKey + "|priv"
		}
		s.cacheWorkerPage(cacheKey, now, buf.Bytes())
	}
	_, _ = w.Write(buf.Bytes())
}

// cacheWorkerPage stores a rendered worker-status HTML payload in the
// workerPageCache, enforcing the workerPageCacheLimit and pruning expired
// entries when necessary.
func (s *StatusServer) cacheWorkerPage(key string, now time.Time, payload []byte) {
	entry := cachedWorkerPage{
		payload:   append([]byte(nil), payload...),
		updatedAt: now,
		expiresAt: now.Add(overviewRefreshInterval),
	}
	s.workerPageMu.Lock()
	defer s.workerPageMu.Unlock()
	if s.workerPageCache == nil {
		s.workerPageCache = make(map[string]cachedWorkerPage)
	}
	// Enforce a simple size bound: evict expired entries first, then
	// fall back to removing an arbitrary entry if still at capacity.
	if workerPageCacheLimit > 0 && len(s.workerPageCache) >= workerPageCacheLimit {
		for k, v := range s.workerPageCache {
			if now.After(v.expiresAt) {
				delete(s.workerPageCache, k)
				break
			}
		}
		if len(s.workerPageCache) >= workerPageCacheLimit {
			for k := range s.workerPageCache {
				delete(s.workerPageCache, k)
				break
			}
		}
	}
	s.workerPageCache[key] = entry
}

// handleWorkerLookup is a generic handler that extracts a worker identifier from the URL path
// and performs a direct worker lookup. It supports multiple URL patterns for miner software:
// - /user/{worker}
// - /users/{worker}
// - /stats/{worker}
// - /app/{worker}
// The worker identifier is used directly for lookup (plaintext worker name).
func (s *StatusServer) handleWorkerLookup(w http.ResponseWriter, r *http.Request, prefix string) {
	host := remoteHostFromRequest(r)
	if !s.workerLookupLimiter.allow(host) {
		http.Error(w, "Worker lookup rate limit exceeded; try again later.", http.StatusTooManyRequests)
		return
	}

	start := time.Now()
	base := s.buildStatusData()
	base.RenderDuration = time.Since(start)

	// Best-effort derivation of the pool payout script so the UI can
	// label coinbase outputs by destination. Errors are treated as
	// "unknown" and do not affect normal worker stats rendering.
	var poolScriptHex string
	addr := strings.TrimSpace(s.cfg.PayoutAddress)
	if addr != "" {
		if script, err := scriptForAddress(addr, ChainParams()); err == nil && len(script) > 0 {
			poolScriptHex = strings.ToLower(hex.EncodeToString(script))
		}
	}

	// Best-effort derivation of the donation script for 3-way payout display.
	var donationScriptHex string
	donationAddr := strings.TrimSpace(s.cfg.OperatorDonationAddress)
	if donationAddr != "" && s.cfg.OperatorDonationPercent > 0 {
		if script, err := scriptForAddress(donationAddr, ChainParams()); err == nil && len(script) > 0 {
			donationScriptHex = strings.ToLower(hex.EncodeToString(script))
		}
	}

	privacyMode := workerPrivacyModeFromRequest(r)
	data := WorkerStatusData{
		StatusData:        base,
		PoolScriptHex:     poolScriptHex,
		DonationScriptHex: donationScriptHex,
		PrivacyMode:       privacyMode,
	}

	// Extract worker ID from path after the prefix
	path := strings.TrimPrefix(r.URL.Path, prefix)
	path = strings.TrimPrefix(path, "/")
	workerID := strings.TrimSpace(path)

	if len(workerID) > workerLookupMaxBytes {
		s.renderErrorPage(w, r, http.StatusBadRequest,
			"Worker ID too long",
			fmt.Sprintf("Worker identifiers are limited to %d bytes.", workerLookupMaxBytes),
			"Please shorten the worker name and try again.")
		return
	}

	if workerID == "" {
		s.renderErrorPage(w, r, http.StatusBadRequest,
			"Missing worker ID",
			"Please provide a worker identifier in the URL.",
			"Example: "+prefix+"/worker_name")
		return
	}

	hashSum := sha256.Sum256([]byte(workerID))
	data.QueriedWorkerHash = fmt.Sprintf("%x", hashSum[:])

	data.QueriedWorker = workerID
	now := time.Now()
	found := false
	if wv, ok := s.findWorkerViewByName(workerID, now); ok {
		setWorkerStatusView(&data, wv, privacyMode)
		found = true
	} else if s.accounting != nil {
		if wv, ok := s.accounting.WorkerViewByName(workerID); ok {
			setWorkerStatusView(&data, wv, privacyMode)
			found = true
		}
	}
	if found {
		if data.BTCPriceFiat > 0 {
			cur := strings.ToUpper(strings.TrimSpace(data.FiatCurrency))
			if cur == "" {
				cur = "USD"
			}
			base := fmt.Sprintf("Approximate fiat values (%s) use 1 BTC ≈ %.2f", cur, data.BTCPriceFiat)
			if data.BTCPriceUpdatedAt != "" {
				base += " (price updated " + data.BTCPriceUpdatedAt + ")"
			}
			data.FiatNote = base + "; payouts are always made in BTC."
		}
	} else {
		data.Error = "worker not found"
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "worker_status", data); err != nil {
		logger.Error("worker status template error", "error", err)
		s.renderErrorPage(w, r, http.StatusInternalServerError,
			"Worker page error",
			"We couldn't render the worker info page.",
			"Template error while rendering worker details.")
	}
}

// handleServerInfoPage renders the public server information page with backend status and diagnostics.
func (s *StatusServer) handleServerInfoPage(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	data := s.baseTemplateData(start)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "server", data); err != nil {
		logger.Error("server info template error", "error", err)
		s.renderErrorPage(w, r, http.StatusInternalServerError,
			"Server info page error",
			"We couldn't render the server information page.",
			"Template error while rendering the server info view.")
	}
}

// handlePoolInfo renders a pool configuration/limits summary page. It exposes
// only non-secret settings and aggregate stats that are safe to share.
func (s *StatusServer) handlePoolInfo(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	data := s.baseTemplateData(start)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "pool", data); err != nil {
		logger.Error("pool info template error", "error", err)
		s.renderErrorPage(w, r, http.StatusInternalServerError,
			"Pool info error",
			"We couldn't render the pool configuration page.",
			"Template error while rendering the pool info view.")
	}
}

func (s *StatusServer) handleAboutPage(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	data := s.baseTemplateData(start)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "about", data); err != nil {
		logger.Error("about page template error", "error", err)
		s.renderErrorPage(w, r, http.StatusInternalServerError,
			"About page error",
			"We couldn't render the about page.",
			"Template error while rendering the about page view.")
	}
}

func (s *StatusServer) buildStatusData() StatusData {
	var currentJob *Job
	if s.jobMgr != nil {
		currentJob = s.jobMgr.CurrentJob()
	}
	var accepted, rejected uint64
	var reasons map[string]uint64
	var vardiffUp, vardiffDown, blocksAccepted, blocksErrored uint64
	var rpcGBTLast, rpcGBTMax float64
	var rpcGBTCount uint64
	var rpcSubmitLast, rpcSubmitMax float64
	var rpcSubmitCount uint64
	var rpcErrors, shareErrors uint64
	if s.metrics != nil {
		accepted, rejected, reasons = s.metrics.Snapshot()
		s.logShareTotals(accepted, rejected)
		vardiffUp, vardiffDown, blocksAccepted, blocksErrored,
			rpcGBTLast, rpcGBTMax, rpcGBTCount,
			rpcSubmitLast, rpcSubmitMax, rpcSubmitCount,
			rpcErrors, shareErrors = s.metrics.SnapshotDiagnostics()
	}
	// Process / system diagnostics (best-effort only; failures are treated as
	// zero values).
	procGoroutines := runtime.NumGoroutine()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	procRSS := readProcessRSS()
	cpuPercent := s.sampleCPUPercent()
	sysMemTotal, sysMemFree := readSystemMemory()
	var sysMemUsed uint64
	if sysMemTotal > sysMemFree {
		sysMemUsed = sysMemTotal - sysMemFree
	}
	load1, load5, load15 := readLoadAverages()
	// Separate out stale shares so the main rejected count reflects only
	// non-stale rejects (to avoid suggesting pool issues when most rejects
	// are from template changes).
	var stale, lowDiff uint64
	for reason, count := range reasons {
		low := strings.ToLower(reason)
		if strings.HasPrefix(low, "stale") {
			stale += count
			continue
		}
		if low == "low difficulty share" || low == "lowdiff" {
			lowDiff += count
		}
	}
	nonStaleRejected := rejected
	if stale > nonStaleRejected {
		nonStaleRejected = 0
	} else {
		nonStaleRejected -= stale
	}
	nonStaleNonLowRejected := nonStaleRejected
	if lowDiff > nonStaleNonLowRejected {
		nonStaleNonLowRejected = 0
	} else {
		nonStaleNonLowRejected -= lowDiff
	}

	// Filter low-difficulty shares out of the generic reject reasons list so
	// they can be displayed separately on the status page.
	filteredReasons := make(map[string]uint64, len(reasons))
	for reason, count := range reasons {
		lower := strings.ToLower(reason)
		if lower == "low difficulty share" || lower == "lowdiff" {
			continue
		}
		if lower == "low difficulty share" {
			reason = "lowDiff"
		}
		filteredReasons[reason] = count
	}
	var jobCreated, templateTime string
	if currentJob != nil {
		since := time.Since(currentJob.CreatedAt)
		jobCreated = humanShortDuration(since)
		if jobCreated != "just now" {
			jobCreated += " ago"
		}
		templateSince := time.Since(time.Unix(currentJob.Template.CurTime, 0))
		templateTime = humanShortDuration(templateSince)
		if templateTime != "just now" {
			templateTime += " ago"
		}
	}

	now := time.Now()
	var workers []WorkerView
	var bannedWorkers []WorkerView
	var bestShares []BestShare
	if s.metrics != nil {
		bestShares = s.metrics.SnapshotBestShares()
	}
	var allWorkers []WorkerView
	var workerDBStats WorkerDatabaseStats
	allWorkers = s.snapshotWorkerViews(now)
	workers = make([]WorkerView, 0, len(allWorkers))
	bannedWorkers = make([]WorkerView, 0, len(allWorkers))
	seen := make(map[string]struct{}, len(allWorkers))
	for _, w := range allWorkers {
		if w.Banned {
			bannedWorkers = append(bannedWorkers, w)
		} else {
			workers = append(workers, w)
		}
		if w.Name != "" {
			seen[w.Name] = struct{}{}
		}
	}
	if s.accounting != nil {
		workerDBStats = s.accounting.WorkerDatabaseStats()
		for _, wv := range s.accounting.WorkersSnapshot() {
			if wv.Name == "" {
				continue
			}
			if _, ok := seen[wv.Name]; ok {
				continue
			}
			bannedWorkers = append(bannedWorkers, wv)
		}
	}

	foundBlocks := loadFoundBlocks(s.cfg.DataDir, 10)

	// Aggregate workers by miner type for a "miner types" summary near the
	// bottom of the status page, grouping versions per miner type.
	typeCounts := make(map[string]map[string]int) // name -> version -> count
	for _, w := range workers {
		name := strings.TrimSpace(w.MinerName)
		version := strings.TrimSpace(w.MinerVersion)
		if name == "" {
			// Fall back to splitting the raw miner type string if present.
			t := strings.TrimSpace(w.MinerType)
			if t != "" {
				name, version = parseMinerID(t)
			}
		}
		if name == "" {
			name = "(unknown)"
		}
		if typeCounts[name] == nil {
			typeCounts[name] = make(map[string]int)
		}
		typeCounts[name][version]++
	}
	var minerTypes []MinerTypeView
	for name, versions := range typeCounts {
		mt := MinerTypeView{Name: name}
		for v, count := range versions {
			mt.Versions = append(mt.Versions, MinerTypeVersionView{
				Version: v,
				Workers: count,
			})
			mt.Total += count
		}
		sort.Slice(mt.Versions, func(i, j int) bool {
			if mt.Versions[i].Workers == mt.Versions[j].Workers {
				return mt.Versions[i].Version < mt.Versions[j].Version
			}
			return mt.Versions[i].Workers > mt.Versions[j].Workers
		})
		minerTypes = append(minerTypes, mt)
	}
	sort.Slice(minerTypes, func(i, j int) bool {
		if minerTypes[i].Total == minerTypes[j].Total {
			return minerTypes[i].Name < minerTypes[j].Name
		}
		return minerTypes[i].Total > minerTypes[j].Total
	})

	var windowAccepted, windowSubmissions uint64
	var windowStart time.Time
	for _, w := range allWorkers {
		windowAccepted += uint64(w.WindowAccepted)
		windowSubmissions += uint64(w.WindowSubmissions)
		if w.WindowStart.IsZero() {
			continue
		}
		if windowStart.IsZero() || w.WindowStart.Before(windowStart) {
			windowStart = w.WindowStart
		}
	}
	windowStartStr := ""
	if !windowStart.IsZero() {
		windowStartStr = windowStart.UTC().Format("2006-01-02 15:04:05 MST")
	}

	// Pool-wide share rates derived directly from per-connection stats.
	var (
		sharesPerMinute float64
		sharesPerSecond float64
	)
	for _, w := range allWorkers {
		if w.ShareRate > 0 {
			sharesPerMinute += w.ShareRate
		}
	}
	sharesPerSecond = sharesPerMinute / 60
	poolHashrate := s.computePoolHashrate()

	// Limit the number of workers displayed on the main status page to
	// keep the UI responsive. Workers are already sorted so that those
	// with the most recent shares appear first; we keep only the first
	// N here for display as "recent workers". Additionally, hide workers
	// whose last share was more than recentWorkerWindow ago so the panel
	// reflects only currently active miners.
	const recentWorkerWindow = 30 * time.Minute
	if len(workers) > 0 {
		dst := workers[:0]
		for _, w := range workers {
			if w.LastShare.IsZero() {
				continue
			}
			if now.Sub(w.LastShare) > recentWorkerWindow {
				continue
			}
			dst = append(dst, w)
		}
		workers = dst
	}
	const maxWorkersOnStatus = 15
	if len(workers) > maxWorkersOnStatus {
		workers = workers[:maxWorkersOnStatus]
	}

	var rpcErr, acctErr string
	if s.rpc != nil {
		if err := s.rpc.LastError(); err != nil {
			rpcErr = err.Error()
		}
	}
	var nodeNetwork string
	var nodeSubversion string
	var nodeBlocks, nodeHeaders int64
	var nodeIBD bool
	var nodePruned bool
	var nodeSizeOnDisk uint64
	var nodeConns, nodeConnsIn, nodeConnsOut int
	var genesisHash, bestHash string
	var nodePeers []string
	if s.rpc != nil {
		info := s.ensureNodeInfo()
		nodeNetwork = info.network
		nodeSubversion = info.subversion
		nodeBlocks = info.blocks
		nodeHeaders = info.headers
		nodeIBD = info.ibd
		nodePruned = info.pruned
		nodeSizeOnDisk = info.sizeOnDisk
		nodeConns = info.conns
		nodeConnsIn = info.connsIn
		nodeConnsOut = info.connsOut
		if len(info.peerAddresses) > 0 {
			nodePeers = append([]string(nil), info.peerAddresses...)
		}
		genesisHash = info.genesisHash
		bestHash = info.bestHash
	}
	if s.accounting != nil {
		if err := s.accounting.LastError(); err != nil {
			acctErr = err.Error()
		}
	}

	// Best-effort BTC price lookup for display only. If CoinGecko is
	// unavailable or the lookup fails, we leave the price at zero and do
	// not fail the status page.
	var btcPrice float64
	var btcPriceUpdated string
	if s.priceSvc != nil {
		if price, err := s.priceSvc.BTCPrice(s.cfg.FiatCurrency); err == nil && price > 0 {
			btcPrice = price
			if ts := s.priceSvc.LastUpdate(); !ts.IsZero() {
				btcPriceUpdated = ts.UTC().Format("2006-01-02 15:04:05 MST")
			}
		}
	}

	var jobFeed JobFeedView
	if s.jobMgr != nil {
		fs := s.jobMgr.FeedStatus()
		jobFeed.Ready = fs.Ready
		jobFeed.ZMQHealthy = fs.ZMQHealthy
		payload := fs.Payload
		if !fs.LastSuccess.IsZero() {
			jobFeed.LastSuccess = fs.LastSuccess.UTC().Format("2006-01-02 15:04:05 MST")
		}
		if fs.LastError != nil {
			jobFeed.LastError = fs.LastError.Error()
		}
		if !fs.LastErrorAt.IsZero() {
			jobFeed.LastErrorAt = fs.LastErrorAt.UTC().Format("2006-01-02 15:04:05 MST")
		}
		jobFeed.ZMQDisconnects = fs.ZMQDisconnects
		jobFeed.ZMQReconnects = fs.ZMQReconnects
		blockTip := payload.BlockTip
		if blockTip.Hash != "" {
			jobFeed.BlockHash = blockTip.Hash
		}
		if blockTip.Height > 0 {
			jobFeed.BlockHeight = blockTip.Height
		}
		if !blockTip.Time.IsZero() {
			jobFeed.BlockTime = blockTip.Time.UTC().Format("2006-01-02 15:04:05 MST")
		}
		if blockTip.Bits != "" {
			jobFeed.BlockBits = blockTip.Bits
		}
		if blockTip.Difficulty > 0 {
			jobFeed.BlockDifficulty = blockTip.Difficulty
		}
		if !payload.LastRawBlockAt.IsZero() {
			jobFeed.LastRawBlockAt = payload.LastRawBlockAt.UTC().Format("2006-01-02 15:04:05 MST")
		}
		if payload.LastRawBlockBytes > 0 {
			jobFeed.LastRawBlockBytes = payload.LastRawBlockBytes
		}
		if payload.LastHashTx != "" {
			jobFeed.LastHashTx = payload.LastHashTx
		}
		if !payload.LastHashTxAt.IsZero() {
			jobFeed.LastHashTxAt = payload.LastHashTxAt.UTC().Format("2006-01-02 15:04:05 MST")
		}
		if !payload.LastRawTxAt.IsZero() {
			jobFeed.LastRawTxAt = payload.LastRawTxAt.UTC().Format("2006-01-02 15:04:05 MST")
		}
		if payload.LastRawTxBytes > 0 {
			jobFeed.LastRawTxBytes = payload.LastRawTxBytes
		}
		jobFeed.ErrorHistory = fs.ErrorHistory
	}

	brandName := strings.TrimSpace(s.cfg.StatusBrandName)
	if brandName == "" {
		brandName = "Solo Pool"
	}
	brandDomain := strings.TrimSpace(s.cfg.StatusBrandDomain)

	activeMiners := 0
	if s.jobMgr != nil {
		activeMiners = s.jobMgr.ActiveMiners()
	}
	activeTLSMiners := 0
	if s.registry != nil {
		for _, mc := range s.registry.Snapshot() {
			if mc != nil && mc.isTLSConnection {
				activeTLSMiners++
			}
		}
	}

	bt := strings.TrimSpace(buildTime)
	if bt == "" {
		bt = "(dev build)"
	}

	displayPayout := shortDisplayID(s.cfg.PayoutAddress, payoutAddrPrefix, payoutAddrSuffix)
	displayDonation := shortDisplayID(s.cfg.OperatorDonationAddress, payoutAddrPrefix, payoutAddrSuffix)
	displayCoinbase := shortDisplayID(s.cfg.CoinbaseMsg, coinbaseMsgPrefix, coinbaseMsgSuffix)

	expectedGenesis := ""
	if nodeNetwork != "" {
		expectedGenesis = knownGenesis[nodeNetwork]
	}
	genesisMatch := false
	if genesisHash != "" && expectedGenesis != "" {
		genesisMatch = strings.EqualFold(genesisHash, expectedGenesis)
	}

	// Collect configuration warnings for potentially risky or surprising
	// setups so the UI can show a prominent banner.
	var warnings []string
	if s.cfg.PoolFeePercent > 10 {
		warnings = append(warnings, "Pool fee is configured above 10%. Verify this is intentional and clearly disclosed to miners.")
	}
	if strings.EqualFold(nodeNetwork, "mainnet") && s.cfg.MinDifficulty < 1 {
		warnings = append(warnings, "Minimum difficulty is configured below 1 on mainnet. This can flood the pool and node with tiny shares; verify you really need CPU-style difficulties on mainnet.")
	}
	if nodeNetwork != "" && !strings.EqualFold(nodeNetwork, "mainnet") {
		warnings = append(warnings, "Pool is connected to a non-mainnet Bitcoin network ("+nodeNetwork+"). Verify you intend to mine on this network.")
	}
	if expectedGenesis != "" && genesisHash != "" && !genesisMatch {
		warnings = append(warnings, "Connected node's genesis hash does not match the expected Bitcoin genesis for network "+nodeNetwork+". Verify the node is on the genuine Bitcoin chain, not a fork or alt network.")
	}
	if s.cfg.MaxAcceptsPerSecond == 0 && s.cfg.MaxConns == 0 {
		warnings = append(warnings, "No connection rate limit and no max connection cap are configured. This can make the pool vulnerable to connection floods or accidental overload.")
	}

	return StatusData{
		ListenAddr:                     s.cfg.ListenAddr,
		StratumTLSListen:               s.cfg.StratumTLSListen,
		BrandName:                      brandName,
		BrandDomain:                    brandDomain,
		Tagline:                        s.cfg.StatusTagline,
		FiatCurrency:                   s.cfg.FiatCurrency,
		BTCPriceFiat:                   btcPrice,
		BTCPriceUpdatedAt:              btcPriceUpdated,
		PoolDonationAddress:            s.cfg.PoolDonationAddress,
		DiscordURL:                     s.cfg.DiscordURL,
		NodeNetwork:                    nodeNetwork,
		NodeSubversion:                 nodeSubversion,
		NodeBlocks:                     nodeBlocks,
		NodeHeaders:                    nodeHeaders,
		NodeInitialBlockDownload:       nodeIBD,
		NodeRPCURL:                     s.cfg.RPCURL,
		NodeZMQAddr:                    s.cfg.ZMQBlockAddr,
		PayoutAddress:                  s.cfg.PayoutAddress,
		PoolFeePercent:                 s.cfg.PoolFeePercent,
		OperatorDonationPercent:        s.cfg.OperatorDonationPercent,
		OperatorDonationAddress:        s.cfg.OperatorDonationAddress,
		OperatorDonationName:           s.cfg.OperatorDonationName,
		OperatorDonationURL:            s.cfg.OperatorDonationURL,
		CoinbaseMessage:                s.cfg.CoinbaseMsg,
		DisplayPayoutAddress:           displayPayout,
		DisplayOperatorDonationAddress: displayDonation,
		DisplayCoinbaseMessage:         displayCoinbase,
		NodeConnections:                nodeConns,
		NodeConnectionsIn:              nodeConnsIn,
		NodeConnectionsOut:             nodeConnsOut,
		NodePeerAddresses:              nodePeers,
		NodePruned:                     nodePruned,
		NodeSizeOnDiskBytes:            nodeSizeOnDisk,
		GenesisHash:                    genesisHash,
		GenesisExpected:                expectedGenesis,
		GenesisMatch:                   genesisMatch,
		BestBlockHash:                  bestHash,
		PoolSoftware:                   poolSoftwareName,
		BuildTime:                      bt,
		ActiveMiners:                   activeMiners,
		ActiveTLSMiners:                activeTLSMiners,
		SharesPerSecond:                sharesPerSecond,
		SharesPerMinute:                sharesPerMinute,
		Accepted:                       accepted,
		Rejected:                       nonStaleNonLowRejected,
		StaleShares:                    stale,
		LowDiffShares:                  lowDiff,
		RejectReasons:                  filteredReasons,
		CurrentJob:                     currentJob,
		Uptime:                         time.Since(s.start),
		JobCreated:                     jobCreated,
		TemplateTime:                   templateTime,
		Workers:                        workers,
		BannedWorkers:                  bannedWorkers,
		WindowAccepted:                 windowAccepted,
		WindowSubmissions:              windowSubmissions,
		WindowStart:                    windowStartStr,
		RPCError:                       rpcErr,
		AccountingError:                acctErr,
		JobFeed:                        jobFeed,
		BestShares:                     bestShares,
		FoundBlocks:                    foundBlocks,
		MinerTypes:                     minerTypes,
		VardiffUp:                      vardiffUp,
		VardiffDown:                    vardiffDown,
		PoolHashrate:                   poolHashrate,
		BlocksAccepted:                 blocksAccepted,
		BlocksErrored:                  blocksErrored,
		RPCGBTLastSec:                  rpcGBTLast,
		RPCGBTMaxSec:                   rpcGBTMax,
		RPCGBTCount:                    rpcGBTCount,
		RPCSubmitLastSec:               rpcSubmitLast,
		RPCSubmitMaxSec:                rpcSubmitMax,
		RPCSubmitCount:                 rpcSubmitCount,
		RPCErrors:                      rpcErrors,
		ShareErrors:                    shareErrors,
		ProcessGoroutines:              procGoroutines,
		ProcessCPUPercent:              cpuPercent,
		GoMemAllocBytes:                ms.Alloc,
		GoMemSysBytes:                  ms.Sys,
		ProcessRSSBytes:                procRSS,
		SystemMemTotalBytes:            sysMemTotal,
		SystemMemFreeBytes:             sysMemFree,
		SystemMemUsedBytes:             sysMemUsed,
		SystemLoad1:                    load1,
		SystemLoad5:                    load5,
		SystemLoad15:                   load15,
		MaxConns:                       s.cfg.MaxConns,
		MaxAcceptsPerSecond:            s.cfg.MaxAcceptsPerSecond,
		MaxAcceptBurst:                 s.cfg.MaxAcceptBurst,
		MinDifficulty:                  s.cfg.MinDifficulty,
		MaxDifficulty:                  s.cfg.MaxDifficulty,
		LockSuggestedDifficulty:        s.cfg.LockSuggestedDifficulty,
		WorkerDatabase:                 workerDBStats,
		Warnings:                       warnings,
	}
}

// baseTemplateData constructs a lightweight StatusData view for HTML templates
// that only need static configuration/branding fields. It intentionally avoids
// hitting the expensive statusData/metrics paths so that HTML pages rely on
// cached JSON endpoints for dynamic data.
func (s *StatusServer) baseTemplateData(start time.Time) StatusData {
	brandName := strings.TrimSpace(s.cfg.StatusBrandName)
	if brandName == "" {
		brandName = "Solo Pool"
	}
	brandDomain := strings.TrimSpace(s.cfg.StatusBrandDomain)

	bt := strings.TrimSpace(buildTime)
	if bt == "" {
		bt = "(dev build)"
	}

	displayPayout := shortDisplayID(s.cfg.PayoutAddress, payoutAddrPrefix, payoutAddrSuffix)
	displayDonation := shortDisplayID(s.cfg.OperatorDonationAddress, payoutAddrPrefix, payoutAddrSuffix)
	displayCoinbase := shortDisplayID(s.cfg.CoinbaseMsg, coinbaseMsgPrefix, coinbaseMsgSuffix)

	var warnings []string
	if s.cfg.PoolFeePercent > 10 {
		warnings = append(warnings, "Pool fee is configured above 10%. Verify this is intentional and clearly disclosed to miners.")
	}
	if s.cfg.MaxAcceptsPerSecond == 0 && s.cfg.MaxConns == 0 {
		warnings = append(warnings, "No connection rate limit and no max connection cap are configured. This can make the pool vulnerable to connection floods or accidental overload.")
	}

	return StatusData{
		ListenAddr:                     s.cfg.ListenAddr,
		StratumTLSListen:               s.cfg.StratumTLSListen,
		BrandName:                      brandName,
		BrandDomain:                    brandDomain,
		Tagline:                        s.cfg.StatusTagline,
		ServerLocation:                 s.cfg.ServerLocation,
		FiatCurrency:                   s.cfg.FiatCurrency,
		PoolDonationAddress:            s.cfg.PoolDonationAddress,
		DiscordURL:                     s.cfg.DiscordURL,
		NodeRPCURL:                     s.cfg.RPCURL,
		NodeZMQAddr:                    s.cfg.ZMQBlockAddr,
		PayoutAddress:                  s.cfg.PayoutAddress,
		PoolFeePercent:                 s.cfg.PoolFeePercent,
		OperatorDonationPercent:        s.cfg.OperatorDonationPercent,
		OperatorDonationAddress:        s.cfg.OperatorDonationAddress,
		OperatorDonationName:           s.cfg.OperatorDonationName,
		OperatorDonationURL:            s.cfg.OperatorDonationURL,
		CoinbaseMessage:                s.cfg.CoinbaseMsg,
		DisplayPayoutAddress:           displayPayout,
		DisplayOperatorDonationAddress: displayDonation,
		DisplayCoinbaseMessage:         displayCoinbase,
		PoolSoftware:                   poolSoftwareName,
		BuildTime:                      bt,
		MaxConns:                       s.cfg.MaxConns,
		MaxAcceptsPerSecond:            s.cfg.MaxAcceptsPerSecond,
		MaxAcceptBurst:                 s.cfg.MaxAcceptBurst,
		MinDifficulty:                  s.cfg.MinDifficulty,
		MaxDifficulty:                  s.cfg.MaxDifficulty,
		LockSuggestedDifficulty:        s.cfg.LockSuggestedDifficulty,
		HashrateEMATauSeconds:          s.cfg.HashrateEMATauSeconds,
		NTimeForwardSlackSec:           s.cfg.NTimeForwardSlackSeconds,
		RenderDuration:                 time.Since(start),
		Warnings:                       warnings,
	}
}

// buildCensoredStatusData builds status data with all sensitive information censored
// for public API endpoints. This ensures censoring happens at the source.
func (s *StatusServer) buildCensoredStatusData() StatusData {
	data := s.statusData()

	// Censor all workers
	for i := range data.Workers {
		data.Workers[i] = censorWorkerView(data.Workers[i])
	}

	// Censor banned workers
	for i := range data.BannedWorkers {
		data.BannedWorkers[i] = censorWorkerView(data.BannedWorkers[i])
	}

	// Censor best shares
	for i := range data.BestShares {
		data.BestShares[i] = censorBestShare(data.BestShares[i])
	}

	// Censor found blocks
	for i := range data.FoundBlocks {
		if data.FoundBlocks[i].Hash != "" {
			data.FoundBlocks[i].Hash = shortDisplayID(data.FoundBlocks[i].Hash, hashPrefix, hashSuffix)
			data.FoundBlocks[i].DisplayHash = shortDisplayID(data.FoundBlocks[i].Hash, hashPrefix, hashSuffix)
		}
		if data.FoundBlocks[i].Worker != "" {
			data.FoundBlocks[i].Worker = shortWorkerName(data.FoundBlocks[i].Worker, workerNamePrefix, workerNameSuffix)
			data.FoundBlocks[i].DisplayWorker = shortWorkerName(data.FoundBlocks[i].Worker, workerNamePrefix, workerNameSuffix)
		}
	}

	// Remove/censor sensitive configuration
	// Keep minimal job info (just height for display), remove sensitive mining data
	if data.CurrentJob != nil {
		data.CurrentJob = &Job{
			JobID:    data.CurrentJob.JobID,
			Template: data.CurrentJob.Template, // Contains height
			// All other sensitive fields omitted
		}
	}
	data.PayoutAddress = shortDisplayID(data.PayoutAddress, payoutAddrPrefix, payoutAddrSuffix)
	data.CoinbaseMessage = shortDisplayID(data.CoinbaseMessage, coinbaseMsgPrefix, coinbaseMsgSuffix)
	// PoolDonationAddress is intentionally NOT censored - pools want to show this publicly

	// Censor node configuration
	if data.NodeRPCURL != "" && !strings.Contains(data.NodeRPCURL, "127.0.0.1") && !strings.Contains(data.NodeRPCURL, "localhost") {
		data.NodeRPCURL = "(external node)"
	}
	if data.NodeZMQAddr != "" && !strings.Contains(data.NodeZMQAddr, "127.0.0.1") && !strings.Contains(data.NodeZMQAddr, "localhost") {
		data.NodeZMQAddr = "(external node)"
	}

	return data
}

func (s *StatusServer) logShareTotals(accepted, rejected uint64) {
	s.lastStatsMu.Lock()
	defer s.lastStatsMu.Unlock()
	if accepted != s.lastAccepted || rejected != s.lastRejected {
		logger.Info("share totals", "accepted", accepted, "rejected", rejected)
		s.lastAccepted = accepted
		s.lastRejected = rejected
	}
}

const (
	nodeInfoTTL          = 30 * time.Second
	nodeInfoRPCTimeout   = 5 * time.Second
	maxNodePeerAddresses = 64
)

// ensureNodeInfo returns the cached node snapshot and schedules a non-blocking
// refresh when the data is stale so HTTP handlers avoid direct RPC calls.
func (s *StatusServer) ensureNodeInfo() cachedNodeInfo {
	s.nodeInfoMu.Lock()
	info := s.nodeInfo
	s.nodeInfoMu.Unlock()

	if info.fetchedAt.IsZero() || time.Since(info.fetchedAt) >= nodeInfoTTL {
		s.scheduleNodeInfoRefresh()
	}
	return info
}

func (s *StatusServer) scheduleNodeInfoRefresh() {
	if s.rpc == nil {
		return
	}
	if !atomic.CompareAndSwapInt32(&s.nodeInfoRefreshing, 0, 1) {
		return
	}
	go func() {
		defer atomic.StoreInt32(&s.nodeInfoRefreshing, 0)
		s.refreshNodeInfo()
	}()
}

// refreshNodeInfo refreshes the cached node information using bounded,
// context-aware RPC calls so shutdown and overload do not block handlers.
func (s *StatusServer) refreshNodeInfo() {
	if s.rpc == nil {
		return
	}
	s.nodeInfoMu.Lock()
	info := s.nodeInfo
	s.nodeInfoMu.Unlock()

	var updated bool

	var bc struct {
		Chain                string  `json:"chain"`
		Blocks               int64   `json:"blocks"`
		Headers              int64   `json:"headers"`
		InitialBlockDownload bool    `json:"initialblockdownload"`
		Pruned               bool    `json:"pruned"`
		SizeOnDisk           float64 `json:"size_on_disk"`
	}
	if err := s.rpcCallCtx("getblockchaininfo", nil, &bc); err == nil {
		chain := strings.ToLower(strings.TrimSpace(bc.Chain))
		switch chain {
		case "main", "mainnet", "":
			info.network = "mainnet"
		case "test", "testnet", "testnet3", "testnet4":
			info.network = "testnet"
		case "signet":
			info.network = "signet"
		case "regtest":
			info.network = "regtest"
		default:
			info.network = bc.Chain
		}
		info.blocks = bc.Blocks
		info.headers = bc.Headers
		info.ibd = bc.InitialBlockDownload
		info.pruned = bc.Pruned
		if bc.SizeOnDisk > 0 {
			info.sizeOnDisk = uint64(bc.SizeOnDisk)
		}
		updated = true
	}

	var netInfo struct {
		Subversion     string `json:"subversion"`
		Connections    int    `json:"connections"`
		ConnectionsIn  int    `json:"connections_in"`
		ConnectionsOut int    `json:"connections_out"`
	}
	if err := s.rpcCallCtx("getnetworkinfo", nil, &netInfo); err == nil {
		info.subversion = strings.TrimSpace(netInfo.Subversion)
		info.conns = netInfo.Connections
		info.connsIn = netInfo.ConnectionsIn
		info.connsOut = netInfo.ConnectionsOut
		updated = true
	}

	var peerList []struct {
		Addr string `json:"addr"`
	}
	if err := s.rpcCallCtx("getpeerinfo", nil, &peerList); err == nil {
		addrs := make([]string, 0, len(peerList))
		for _, p := range peerList {
			if host := stripPeerPort(p.Addr); host != "" {
				addrs = append(addrs, host)
			}
		}
		if len(addrs) > 1 {
			sort.Strings(addrs)
		}
		if len(addrs) > maxNodePeerAddresses {
			addrs = append([]string(nil), addrs[:maxNodePeerAddresses]...)
		}
		info.peerAddresses = addrs
		updated = true
	}

	var genesis string
	if err := s.rpcCallCtx("getblockhash", []interface{}{0}, &genesis); err == nil {
		genesis = strings.TrimSpace(genesis)
		if genesis != "" {
			info.genesisHash = genesis
			updated = true
		}
	}

	var best string
	if err := s.rpcCallCtx("getbestblockhash", nil, &best); err == nil {
		best = strings.TrimSpace(best)
		if best != "" {
			info.bestHash = best
			updated = true
		}
	}

	if !updated {
		return
	}

	info.fetchedAt = time.Now()
	s.nodeInfoMu.Lock()
	s.nodeInfo = info
	s.nodeInfoMu.Unlock()
}

// rpcCallCtx issues a single RPC with a short timeout derived from the
// StatusServer's context so node-info refreshes don't block shutdown.
func (s *StatusServer) rpcCallCtx(method string, params interface{}, out interface{}) error {
	if s == nil || s.rpc == nil {
		return fmt.Errorf("rpc client not configured")
	}
	parent := s.ctx
	if parent == nil {
		parent = context.Background()
	}
	callCtx, cancel := context.WithTimeout(parent, nodeInfoRPCTimeout)
	defer cancel()
	return s.rpc.callCtx(callCtx, method, params, out)
}

// handleNodeInfo renders a simple node accountability page showing which
// Bitcoin node the pool is connected to, its network, and basic sync info.
func (s *StatusServer) handleNodeInfo(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	data := s.baseTemplateData(start)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "node", data); err != nil {
		logger.Error("node info template error", "error", err)
		s.renderErrorPage(w, r, http.StatusInternalServerError,
			"Node info error",
			"We couldn't render the node info page.",
			"Template error while rendering the node info view.")
	}
}

// loadFoundBlocks reads the append-only found_blocks.jsonl log and returns up
// to limit most recent entries for display on the status page. It is
// best-effort: parse errors or missing files simply result in an empty slice.
func loadFoundBlocks(dataDir string, limit int) []FoundBlockView {
	if dataDir == "" {
		dataDir = defaultDataDir
	}
	path := filepath.Join(dataDir, "state", "found_blocks.jsonl")
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	type foundRecord struct {
		Timestamp        time.Time `json:"timestamp"`
		Height           int64     `json:"height"`
		Hash             string    `json:"hash"`
		Worker           string    `json:"worker"`
		ShareDiff        float64   `json:"share_diff"`
		PoolFeeSats      int64     `json:"pool_fee_sats"`
		WorkerPayoutSats int64     `json:"worker_payout_sats"`
	}

	var recs []FoundBlockView
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var r foundRecord
		if err := sonic.Unmarshal(line, &r); err != nil {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(r.Hash), "dummyhash") {
			continue
		}
		recs = append(recs, FoundBlockView{
			Height:           r.Height,
			Hash:             r.Hash,
			DisplayHash:      shortDisplayID(r.Hash, hashPrefix, hashSuffix),
			Worker:           r.Worker,
			DisplayWorker:    shortWorkerName(r.Worker, 12, 6),
			Timestamp:        r.Timestamp,
			ShareDiff:        r.ShareDiff,
			PoolFeeSats:      r.PoolFeeSats,
			WorkerPayoutSats: r.WorkerPayoutSats,
		})
	}
	if len(recs) == 0 {
		return nil
	}
	sort.Slice(recs, func(i, j int) bool {
		return recs[i].Timestamp.After(recs[j].Timestamp)
	})
	if limit > 0 && len(recs) > limit {
		recs = recs[:limit]
	}
	return recs
}

// readProcessRSS returns the current process resident set size (RSS) in bytes.
// It parses /proc/self/statm, which is Linux-specific; on failure it returns 0.
func readProcessRSS() uint64 {
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return 0
	}
	residentPages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	pageSize := uint64(os.Getpagesize())
	return residentPages * pageSize
}

// sampleCPUPercent returns an approximate process CPU usage percentage by
// taking a ratio of deltas between /proc/self/stat (process ticks) and
// /proc/stat (system ticks) across calls. It is Linux-specific and best-effort.
func (s *StatusServer) sampleCPUPercent() float64 {
	procTicks, err1 := readProcessCPUTicks()
	totalTicks, err2 := readSystemCPUTicks()
	if err1 != nil || err2 != nil {
		return s.lastCPUUsage
	}

	s.cpuMu.Lock()
	defer s.cpuMu.Unlock()

	if s.lastCPUTotal != 0 && totalTicks > s.lastCPUTotal && procTicks >= s.lastCPUProc {
		dProc := procTicks - s.lastCPUProc
		dTotal := totalTicks - s.lastCPUTotal
		if dTotal > 0 {
			s.lastCPUUsage = (float64(dProc) / float64(dTotal)) * 100.0
		}
	}
	s.lastCPUProc = procTicks
	s.lastCPUTotal = totalTicks
	return s.lastCPUUsage
}

func readProcessCPUTicks() (uint64, error) {
	data, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0, err
	}
	line := string(data)
	// /proc/self/stat has the form: pid (comm) state ... utime stime ...
	// Find the closing ')' and split from there so spaces in comm don't break parsing.
	idx := strings.LastIndex(line, ")")
	if idx == -1 || idx+2 >= len(line) {
		return 0, fmt.Errorf("invalid /proc/self/stat format")
	}
	rest := line[idx+2:]
	fields := strings.Fields(rest)
	// After ") ", fields start at position 3 (state). utime is field 14, stime is field 15,
	// so within "rest" they are indices 11 and 12.
	if len(fields) < 13 {
		return 0, fmt.Errorf("short /proc/self/stat")
	}
	utime, err := strconv.ParseUint(fields[11], 10, 64)
	if err != nil {
		return 0, err
	}
	stime, err := strconv.ParseUint(fields[12], 10, 64)
	if err != nil {
		return 0, err
	}
	return utime + stime, nil
}

func readSystemCPUTicks() (uint64, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, err
	}
	defer f.Close()

	buf := make([]byte, 4096)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		return 0, err
	}
	lines := strings.Split(string(buf[:n]), "\n")
	if len(lines) == 0 {
		return 0, fmt.Errorf("empty /proc/stat")
	}
	fields := strings.Fields(lines[0])
	if len(fields) < 2 || fields[0] != "cpu" {
		return 0, fmt.Errorf("invalid /proc/stat cpu line")
	}
	var total uint64
	for _, f := range fields[1:] {
		v, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			continue
		}
		total += v
	}
	return total, nil
}

// readSystemMemory parses /proc/meminfo and returns total and "available"
// memory in bytes. On error it returns zero values.
func readSystemMemory() (total, free uint64) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	var (
		memTotalKB     uint64
		memAvailableKB uint64
		memFreeKB      uint64
	)
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		val, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch key {
		case "MemTotal":
			memTotalKB = val
		case "MemAvailable":
			memAvailableKB = val
		case "MemFree":
			memFreeKB = val
		}
	}
	if memTotalKB == 0 {
		return 0, 0
	}
	total = memTotalKB * 1024
	// Prefer MemAvailable when present; fall back to MemFree.
	if memAvailableKB > 0 {
		free = memAvailableKB * 1024
	} else {
		free = memFreeKB * 1024
	}
	return total, free
}

// readLoadAverages parses /proc/loadavg and returns the 1/5/15 minute load
// averages. On error it returns zeros.
func readLoadAverages() (float64, float64, float64) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, 0
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return 0, 0, 0
	}
	parse := func(s string) float64 {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0
		}
		return v
	}
	return parse(fields[0]), parse(fields[1]), parse(fields[2])
}

// renderErrorPage renders a generic error page for HTML endpoints using the
// shared layout and styling. It falls back to http.Error if rendering fails.
func (s *StatusServer) renderErrorPage(w http.ResponseWriter, r *http.Request, statusCode int, title, message, detail string) {
	start := time.Now()
	base := s.baseTemplateData(start)
	data := ErrorPageData{
		StatusData: base,
		StatusCode: statusCode,
		Title:      title,
		Message:    message,
		Detail:     detail,
		Path:       r.URL.Path,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(statusCode)
	if err := s.tmpl.ExecuteTemplate(w, "error", data); err != nil {
		logger.Error("error page template error", "error", err)
		http.Error(w, message, statusCode)
	}
}

func (s *StatusServer) Ready() bool {
	if s.jobMgr == nil || !s.jobMgr.Ready() {
		return false
	}
	if s.accounting != nil && !s.accounting.Ready() {
		return false
	}
	return true
}
