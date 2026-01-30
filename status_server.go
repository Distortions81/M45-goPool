package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bytedance/sonic"
	"github.com/hako/durafmt"
	"github.com/pelletier/go-toml"
)

func (s *StatusServer) SetJobManager(jm *JobManager) {
	s.jobMgr = jm
	// Set up callback to invalidate status cache when new blocks arrive
	jm.onNewBlock = s.invalidateStatusCache
}

func (s *StatusServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/favicon.png":
		http.ServeFile(w, r, "logo.png")

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
	APIVersion              string            `json:"api_version"`
	BrandName               string            `json:"brand_name"`
	BrandDomain             string            `json:"brand_domain"`
	ListenAddr              string            `json:"listen_addr"`
	StratumTLSListen        string            `json:"stratum_tls_listen,omitempty"`
	PoolSoftware            string            `json:"pool_software"`
	BuildVersion            string            `json:"build_version,omitempty"`
	BuildTime               string            `json:"build_time"`
	Uptime                  time.Duration     `json:"uptime"`
	ActiveMiners            int               `json:"active_miners"`
	PoolHashrate            float64           `json:"pool_hashrate"`
	SharesPerSecond         float64           `json:"shares_per_second"`
	Accepted                uint64            `json:"accepted"`
	Rejected                uint64            `json:"rejected"`
	StaleShares             uint64            `json:"stale_shares"`
	LowDiffShares           uint64            `json:"low_diff_shares"`
	RejectReasons           map[string]uint64 `json:"reject_reasons,omitempty"`
	WindowAccepted          uint64            `json:"window_accepted"`
	WindowSubmissions       uint64            `json:"window_submissions"`
	WindowStart             string            `json:"window_start"`
	VardiffUp               uint64            `json:"vardiff_up"`
	VardiffDown             uint64            `json:"vardiff_down"`
	BlocksAccepted          uint64            `json:"blocks_accepted"`
	BlocksErrored           uint64            `json:"blocks_errored"`
	MinDifficulty           float64           `json:"min_difficulty"`
	MaxDifficulty           float64           `json:"max_difficulty"`
	PoolFeePercent          float64           `json:"pool_fee_percent"`
	OperatorDonationPercent float64           `json:"operator_donation_percent,omitempty"`
	OperatorDonationName    string            `json:"operator_donation_name,omitempty"`
	OperatorDonationURL     string            `json:"operator_donation_url,omitempty"`
	CurrentJob              *Job              `json:"current_job,omitempty"`
	JobCreated              string            `json:"job_created"`
	TemplateTime            string            `json:"template_time"`
	JobFeed                 JobFeedView       `json:"job_feed"`
	BTCPriceFiat            float64           `json:"btc_price_fiat,omitempty"`
	BTCPriceUpdatedAt       string            `json:"btc_price_updated_at,omitempty"`
	FiatCurrency            string            `json:"fiat_currency,omitempty"`
	Warnings                []string          `json:"warnings,omitempty"`
}

// NodePageData contains Bitcoin node information for the node page
type NodePageData struct {
	APIVersion               string         `json:"api_version"`
	NodeNetwork              string         `json:"node_network,omitempty"`
	NodeSubversion           string         `json:"node_subversion,omitempty"`
	NodeBlocks               int64          `json:"node_blocks"`
	NodeHeaders              int64          `json:"node_headers"`
	NodeInitialBlockDownload bool           `json:"node_initial_block_download"`
	NodeConnections          int            `json:"node_connections"`
	NodeConnectionsIn        int            `json:"node_connections_in"`
	NodeConnectionsOut       int            `json:"node_connections_out"`
	NodePeers                []NodePeerInfo `json:"node_peers,omitempty"`
	NodePruned               bool           `json:"node_pruned"`
	NodeSizeOnDiskBytes      uint64         `json:"node_size_on_disk_bytes"`
	NodePeerCleanupEnabled   bool           `json:"node_peer_cleanup_enabled"`
	NodePeerCleanupMaxPingMs float64        `json:"node_peer_cleanup_max_ping_ms"`
	NodePeerCleanupMinPeers  int            `json:"node_peer_cleanup_min_peers"`
	GenesisHash              string         `json:"genesis_hash,omitempty"`
	GenesisExpected          string         `json:"genesis_expected,omitempty"`
	GenesisMatch             bool           `json:"genesis_match"`
	BestBlockHash            string         `json:"best_block_hash,omitempty"`
}

type NodePeerInfo struct {
	Display     string  `json:"display"`
	PingMs      float64 `json:"ping_ms"`
	ConnectedAt int64   `json:"connected_at"`
}

type nextDifficultyRetarget struct {
	Height           int64  `json:"height"`
	BlocksAway       int64  `json:"blocks_away"`
	DurationEstimate string `json:"duration_estimate,omitempty"`
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
		view := s.statusDataView()
		data := PoolStatsData{
			APIVersion:              apiVersion,
			BrandName:               view.BrandName,
			BrandDomain:             view.BrandDomain,
			ListenAddr:              view.ListenAddr,
			StratumTLSListen:        view.StratumTLSListen,
			PoolSoftware:            view.PoolSoftware,
			BuildVersion:            view.BuildVersion,
			BuildTime:               view.BuildTime,
			Uptime:                  view.Uptime,
			ActiveMiners:            view.ActiveMiners,
			PoolHashrate:            view.PoolHashrate,
			SharesPerSecond:         view.SharesPerSecond,
			Accepted:                view.Accepted,
			Rejected:                view.Rejected,
			StaleShares:             view.StaleShares,
			LowDiffShares:           view.LowDiffShares,
			RejectReasons:           view.RejectReasons,
			WindowAccepted:          view.WindowAccepted,
			WindowSubmissions:       view.WindowSubmissions,
			WindowStart:             view.WindowStart,
			VardiffUp:               view.VardiffUp,
			VardiffDown:             view.VardiffDown,
			BlocksAccepted:          view.BlocksAccepted,
			BlocksErrored:           view.BlocksErrored,
			MinDifficulty:           view.MinDifficulty,
			MaxDifficulty:           view.MaxDifficulty,
			PoolFeePercent:          view.PoolFeePercent,
			OperatorDonationPercent: view.OperatorDonationPercent,
			OperatorDonationName:    view.OperatorDonationName,
			OperatorDonationURL:     view.OperatorDonationURL,
			CurrentJob:              nil, // Excluded for security
			JobCreated:              view.JobCreated,
			TemplateTime:            view.TemplateTime,
			JobFeed:                 view.JobFeed,
			BTCPriceFiat:            view.BTCPriceFiat,
			BTCPriceUpdatedAt:       view.BTCPriceUpdatedAt,
			FiatCurrency:            view.FiatCurrency,
			Warnings:                view.Warnings,
		}
		return sonic.Marshal(data)
	})
}

// handleNodeStatsJSON returns Bitcoin node information
func (s *StatusServer) handleNodePageJSON(w http.ResponseWriter, r *http.Request) {
	key := "node_page"
	s.serveCachedJSON(w, key, overviewRefreshInterval, func() ([]byte, error) {
		view := s.statusDataView()
		data := NodePageData{
			APIVersion:               apiVersion,
			NodeNetwork:              view.NodeNetwork,
			NodeSubversion:           view.NodeSubversion,
			NodeBlocks:               view.NodeBlocks,
			NodeHeaders:              view.NodeHeaders,
			NodeInitialBlockDownload: view.NodeInitialBlockDownload,
			NodeConnections:          view.NodeConnections,
			NodeConnectionsIn:        view.NodeConnectionsIn,
			NodeConnectionsOut:       view.NodeConnectionsOut,
			NodePeers:                view.NodePeerInfos,
			NodePruned:               view.NodePruned,
			NodeSizeOnDiskBytes:      view.NodeSizeOnDiskBytes,
			NodePeerCleanupEnabled:   view.NodePeerCleanupEnabled,
			NodePeerCleanupMaxPingMs: view.NodePeerCleanupMaxPingMs,
			NodePeerCleanupMinPeers:  view.NodePeerCleanupMinPeers,
			GenesisHash:              view.GenesisHash,
			GenesisExpected:          view.GenesisExpected,
			GenesisMatch:             view.GenesisMatch,
			BestBlockHash:            view.BestBlockHash,
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

func censorRecentWork(w RecentWorkView) RecentWorkView {
	if w.Name != "" {
		w.Name = shortWorkerName(w.Name, workerNamePrefix, workerNameSuffix)
	}
	if w.DisplayName != "" {
		w.DisplayName = shortWorkerName(w.DisplayName, workerNamePrefix, workerNameSuffix)
	}
	return w
}

func censorFoundBlock(b FoundBlockView) FoundBlockView {
	if b.Hash != "" {
		b.Hash = shortDisplayID(b.Hash, hashPrefix, hashSuffix)
		b.DisplayHash = shortDisplayID(b.Hash, hashPrefix, hashSuffix)
	}
	if b.Worker != "" {
		b.Worker = shortWorkerName(b.Worker, workerNamePrefix, workerNameSuffix)
		b.DisplayWorker = shortWorkerName(b.Worker, workerNamePrefix, workerNameSuffix)
	}
	return b
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
	s.serveCachedJSON(w, key, blocksRefreshInterval, func() ([]byte, error) {
		view := s.statusDataView()
		blocks := view.FoundBlocks
		if len(blocks) > limit {
			blocks = blocks[:limit]
		}
		out := make([]FoundBlockView, 0, len(blocks))
		for _, b := range blocks {
			out = append(out, censorFoundBlock(b))
		}
		return sonic.Marshal(out)
	})
}

// handleOverviewPageJSON returns minimal data for the overview page
func (s *StatusServer) handleOverviewPageJSON(w http.ResponseWriter, r *http.Request) {
	key := "overview_page"
	s.serveCachedJSON(w, key, overviewRefreshInterval, func() ([]byte, error) {
		start := time.Now()
		view := s.statusDataView()
		var btcFiat float64
		var btcUpdated string
		fiatCurrency := strings.TrimSpace(s.Config().FiatCurrency)
		if fiatCurrency == "" {
			fiatCurrency = defaultFiatCurrency
		}
		if s.priceSvc != nil {
			if price, err := s.priceSvc.BTCPrice(fiatCurrency); err == nil && price > 0 {
				btcFiat = price
				if ts := s.priceSvc.LastUpdate(); !ts.IsZero() {
					btcUpdated = ts.UTC().Format(time.RFC3339)
				}
			}
		}

		recentWork := make([]RecentWorkView, 0, len(view.RecentWork))
		for _, wv := range view.RecentWork {
			recentWork = append(recentWork, censorRecentWork(wv))
		}

		bestShares := make([]BestShare, 0, len(view.BestShares))
		for _, bs := range view.BestShares {
			bestShares = append(bestShares, censorBestShare(bs))
		}

		foundBlocks := make([]FoundBlockView, 0, len(view.FoundBlocks))
		for _, fb := range view.FoundBlocks {
			foundBlocks = append(foundBlocks, censorFoundBlock(fb))
		}

		poolTag := displayPoolTagFromCoinbaseMessage(view.CoinbaseMessage)

		// Keep banned-worker payloads bounded; the UI only needs a small sample.
		const maxBannedOnOverview = 200
		bannedWorkers := view.BannedWorkers
		if len(bannedWorkers) > maxBannedOnOverview {
			bannedWorkers = bannedWorkers[:maxBannedOnOverview]
		}
		censoredBanned := make([]WorkerView, 0, len(bannedWorkers))
		for _, bw := range bannedWorkers {
			censoredBanned = append(censoredBanned, censorWorkerView(bw))
		}

		data := OverviewPageData{
			APIVersion:      apiVersion,
			ActiveMiners:    view.ActiveMiners,
			ActiveTLSMiners: view.ActiveTLSMiners,
			SharesPerMinute: view.SharesPerMinute,
			PoolHashrate:    view.PoolHashrate,
			PoolTag:         poolTag,
			BTCPriceFiat:    btcFiat,
			BTCPriceUpdated: btcUpdated,
			FiatCurrency:    fiatCurrency,
			RenderDuration:  time.Since(start),
			Workers:         recentWork,
			BannedWorkers:   censoredBanned,
			BestShares:      bestShares,
			FoundBlocks:     foundBlocks,
			MinerTypes:      view.MinerTypes,
		}
		return sonic.Marshal(data)
	})
}

// handlePoolPageJSON returns pool configuration data for the pool info page
func (s *StatusServer) handlePoolPageJSON(w http.ResponseWriter, r *http.Request) {
	key := "pool_page"
	s.serveCachedJSON(w, key, overviewRefreshInterval, func() ([]byte, error) {
		view := s.statusDataView()
		data := PoolPageData{
			APIVersion:       apiVersion,
			BlocksAccepted:   view.BlocksAccepted,
			BlocksErrored:    view.BlocksErrored,
			RPCGBTLastSec:    view.RPCGBTLastSec,
			RPCGBTMaxSec:     view.RPCGBTMaxSec,
			RPCGBTCount:      view.RPCGBTCount,
			RPCSubmitLastSec: view.RPCSubmitLastSec,
			RPCSubmitMaxSec:  view.RPCSubmitMaxSec,
			RPCSubmitCount:   view.RPCSubmitCount,
			RPCErrors:        view.RPCErrors,
			ShareErrors:      view.ShareErrors,
			RPCGBTMin1hSec:   view.RPCGBTMin1hSec,
			RPCGBTAvg1hSec:   view.RPCGBTAvg1hSec,
			RPCGBTMax1hSec:   view.RPCGBTMax1hSec,
			ErrorHistory:     view.ErrorHistory,
		}
		return sonic.Marshal(data)
	})
}

// handleServerPageJSON returns combined status and diagnostics for the server page
func (s *StatusServer) handleServerPageJSON(w http.ResponseWriter, r *http.Request) {
	key := "server_page"
	s.serveCachedJSON(w, key, overviewRefreshInterval, func() ([]byte, error) {
		view := s.statusDataView()
		data := ServerPageData{
			APIVersion:      apiVersion,
			Uptime:          view.Uptime,
			RPCError:        view.RPCError,
			AccountingError: view.AccountingError,
			JobFeed: ServerPageJobFeed{
				LastError:         view.JobFeed.LastError,
				LastErrorAt:       view.JobFeed.LastErrorAt,
				ErrorHistory:      view.JobFeed.ErrorHistory,
				ZMQHealthy:        view.JobFeed.ZMQHealthy,
				ZMQDisconnects:    view.JobFeed.ZMQDisconnects,
				ZMQReconnects:     view.JobFeed.ZMQReconnects,
				LastRawBlockAt:    view.JobFeed.LastRawBlockAt,
				LastRawBlockBytes: view.JobFeed.LastRawBlockBytes,
				BlockHash:         view.JobFeed.BlockHash,
				BlockHeight:       view.JobFeed.BlockHeight,
				BlockTime:         view.JobFeed.BlockTime,
				BlockBits:         view.JobFeed.BlockBits,
				BlockDifficulty:   view.JobFeed.BlockDifficulty,
			},
			ProcessGoroutines:   view.ProcessGoroutines,
			ProcessCPUPercent:   view.ProcessCPUPercent,
			GoMemAllocBytes:     view.GoMemAllocBytes,
			GoMemSysBytes:       view.GoMemSysBytes,
			ProcessRSSBytes:     view.ProcessRSSBytes,
			SystemMemTotalBytes: view.SystemMemTotalBytes,
			SystemMemFreeBytes:  view.SystemMemFreeBytes,
			SystemMemUsedBytes:  view.SystemMemUsedBytes,
			SystemLoad1:         view.SystemLoad1,
			SystemLoad5:         view.SystemLoad5,
			SystemLoad15:        view.SystemLoad15,
		}
		return sonic.Marshal(data)
	})
}

// handleDiagnosticsJSON returns system diagnostics
func (s *StatusServer) handlePoolHashrateJSON(w http.ResponseWriter, r *http.Request) {
	key := "pool_hashrate"
	s.serveCachedJSON(w, key, poolHashrateTTL, func() ([]byte, error) {
		var blockHeight int64
		var blockDifficulty float64
		blockTimeLeftSec := int64(-1)
		var templateTxFeesSats *int64
		var templateUpdatedAt string
		const targetBlockInterval = 10 * time.Minute
		now := time.Now()

		signedCeilSeconds := func(d time.Duration) int64 {
			if d == 0 {
				return 0
			}
			if d > 0 {
				return int64((d + time.Second - 1) / time.Second)
			}
			overdue := -d
			return -int64((overdue + time.Second - 1) / time.Second)
		}

		var recentBlockTimes []string
		if s.jobMgr != nil {
			fs := s.jobMgr.FeedStatus()
			if !fs.LastSuccess.IsZero() {
				templateUpdatedAt = fs.LastSuccess.UTC().Format(time.RFC3339)
			}
			if job := s.jobMgr.CurrentJob(); job != nil {
				tpl := job.Template
				if tpl.Height > 0 && job.CoinbaseValue > 0 {
					subsidy := calculateBlockSubsidy(tpl.Height)
					fees := job.CoinbaseValue - subsidy
					if fees < 0 {
						fees = 0
					}
					templateTxFeesSats = &fees
				}
				if templateUpdatedAt == "" && !job.CreatedAt.IsZero() {
					templateUpdatedAt = job.CreatedAt.UTC().Format(time.RFC3339)
				}
			}
			blockTip := fs.Payload.BlockTip
			if blockTip.Height > 0 {
				blockHeight = blockTip.Height
			}
			if blockTip.Difficulty > 0 {
				blockDifficulty = blockTip.Difficulty
			}
			// Only calculate time left if the block timer has been activated (after first new block)
			if fs.Payload.BlockTimerActive && !blockTip.Time.IsZero() {
				remaining := blockTip.Time.Add(targetBlockInterval).Sub(now)
				blockTimeLeftSec = signedCeilSeconds(remaining)
			}
			if blockHeight == 0 || blockDifficulty == 0 || (blockTimeLeftSec < 0 && !fs.Payload.BlockTimerActive) {
				if job := s.jobMgr.CurrentJob(); job != nil {
					tpl := job.Template
					if blockHeight == 0 && tpl.Height > 0 {
						blockHeight = tpl.Height
					}
					if blockDifficulty == 0 && tpl.Bits != "" {
						if bits, err := strconv.ParseUint(strings.TrimSpace(tpl.Bits), 16, 32); err == nil {
							blockDifficulty = difficultyFromBits(uint32(bits))
						}
					}
					// Don't calculate time left from template if timer isn't active yet
					if blockTimeLeftSec < 0 && !fs.Payload.BlockTimerActive && tpl.CurTime > 0 {
						// Keep blockTimeLeftSec at -1 to indicate timer not started
					}
				}
			}
			// Get recent block times (formatted as ISO8601)
			for _, bt := range fs.Payload.RecentBlockTimes {
				recentBlockTimes = append(recentBlockTimes, bt.Format(time.RFC3339))
			}
		}
		var nextRetarget *nextDifficultyRetarget
		if blockHeight > 0 {
			const retargetInterval = 2016
			next := ((blockHeight / retargetInterval) + 1) * retargetInterval
			remaining := next - blockHeight
			if remaining < 0 {
				remaining = 0
			}
			nextRetarget = &nextDifficultyRetarget{
				Height:     next,
				BlocksAway: remaining,
			}
			if remaining > 0 {
				duration := time.Duration(int64(targetBlockInterval) * remaining)
				nextRetarget.DurationEstimate = durafmt.Parse(duration).LimitFirstN(2).String()
			}
		}
		data := struct {
			APIVersion             string                  `json:"api_version"`
			PoolHashrate           float64                 `json:"pool_hashrate"`
			BlockHeight            int64                   `json:"block_height"`
			BlockDifficulty        float64                 `json:"block_difficulty"`
			BlockTimeLeftSec       int64                   `json:"block_time_left_sec"`
			RecentBlockTimes       []string                `json:"recent_block_times"`
			NextDifficultyRetarget *nextDifficultyRetarget `json:"next_difficulty_retarget,omitempty"`
			TemplateTxFeesSats     *int64                  `json:"template_tx_fees_sats,omitempty"`
			TemplateUpdatedAt      string                  `json:"template_updated_at,omitempty"`
			UpdatedAt              string                  `json:"updated_at"`
		}{
			APIVersion:             apiVersion,
			PoolHashrate:           s.computePoolHashrate(),
			BlockHeight:            blockHeight,
			BlockDifficulty:        blockDifficulty,
			BlockTimeLeftSec:       blockTimeLeftSec,
			RecentBlockTimes:       recentBlockTimes,
			NextDifficultyRetarget: nextRetarget,
			TemplateTxFeesSats:     templateTxFeesSats,
			TemplateUpdatedAt:      templateUpdatedAt,
			UpdatedAt:              time.Now().UTC().Format(time.RFC3339),
		}
		return sonic.Marshal(data)
	})
}

const adminSessionCookieName = "admin_session"

type AdminPageData struct {
	StatusData
	AdminEnabled         bool
	AdminConfigPath      string
	LoggedIn             bool
	AdminLoginError      string
	AdminApplyError      string
	AdminPersistError    string
	AdminRebootError     string
	AdminNotice          string
	Settings             AdminSettingsData
	AdminSection         string
	AdminMinerRows       []AdminMinerRow
	AdminSavedWorkerRows []AdminSavedWorkerRow
	AdminMinerPagination AdminPagination
	AdminLoginPagination AdminPagination
	AdminPerPageOptions  []int
}

type AdminSettingsData struct {
	// Branding/UI
	StatusBrandName   string
	StatusBrandDomain string
	StatusPublicURL   string
	StatusTagline     string
	FiatCurrency      string
	GitHubURL         string
	DiscordURL        string
	ServerLocation    string

	// Listeners
	ListenAddr       string
	StatusAddr       string
	StatusTLSAddr    string
	StratumTLSListen string

	// Rate limits
	MaxConns                          int
	MaxAcceptsPerSecond               int
	MaxAcceptBurst                    int
	AutoAcceptRateLimits              bool
	AcceptReconnectWindow             int
	AcceptBurstWindow                 int
	AcceptSteadyStateWindow           int
	AcceptSteadyStateRate             int
	AcceptSteadyStateReconnectPercent float64
	AcceptSteadyStateReconnectWindow  int

	// Timeouts
	ConnectionTimeoutSeconds int

	// Difficulty / mining toggles
	MinDifficulty           float64
	MaxDifficulty           float64
	LockSuggestedDifficulty bool
	SoloMode                bool
	DirectSubmitProcessing  bool
	CheckDuplicateShares    bool

	// Peer cleanup
	PeerCleanupEnabled   bool
	PeerCleanupMaxPingMs float64
	PeerCleanupMinPeers  int

	// Bans
	CleanExpiredBansOnStartup            bool
	BanInvalidSubmissionsAfter           int
	BanInvalidSubmissionsWindowSeconds   int
	BanInvalidSubmissionsDurationSeconds int
	ReconnectBanThreshold                int
	ReconnectBanWindowSeconds            int
	ReconnectBanDurationSeconds          int

	// Logging
	LogLevel string
}

type AdminMinerRow struct {
	ConnectionSeq       uint64
	ConnectionLabel     string
	RemoteAddr          string
	Listener            string
	Worker              string
	WorkerHash          string
	ClientName          string
	ClientVersion       string
	Difficulty          float64
	Hashrate            float64
	AcceptRatePerMinute float64
	SubmitRatePerMinute float64
	Stats               MinerStats
	ConnectedAt         time.Time
	LastActivity        time.Time
	LastShare           time.Time
	Banned              bool
	BanReason           string
	BanUntil            time.Time
}

type AdminSavedWorkerRow struct {
	UserID            string
	Workers           []SavedWorkerEntry
	NotifyCount       int
	WorkerHashes      []string
	OnlineConnections []AdminMinerConnection
}

type AdminMinerConnection struct {
	ConnectionSeq   uint64
	ConnectionLabel string
	RemoteAddr      string
	Listener        string
}

type AdminPagination struct {
	Page        int
	PerPage     int
	TotalItems  int
	TotalPages  int
	RangeStart  int
	RangeEnd    int
	HasPrevPage bool
	HasNextPage bool
	PrevPage    int
	NextPage    int
}

const (
	defaultAdminPerPage = 25
	maxAdminPerPage     = 200
)

var adminPerPageOptions = []int{10, 25, 50, 100}

func (s *StatusServer) handleAdminPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	data, _, _ := s.buildAdminPageData(r, r.URL.Query().Get("notice"))
	s.renderAdminPage(w, r, data)
}

func (s *StatusServer) handleAdminMinersPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Redirect(w, r, "/admin/miners", http.StatusSeeOther)
		return
	}
	data, _, _ := s.buildAdminPageData(r, r.URL.Query().Get("notice"))
	if !data.AdminEnabled {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if !data.LoggedIn {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	data.AdminSection = "miners"
	page, perPage := adminPaginationFromRequest(r)
	allRows := s.buildAdminMinerRows()
	data.AdminMinerRows, data.AdminMinerPagination = paginateAdminSlice(allRows, page, perPage)
	s.renderAdminPageTemplate(w, r, data, "admin_miners")
}

func (s *StatusServer) handleAdminLoginsPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Redirect(w, r, "/admin/logins", http.StatusSeeOther)
		return
	}
	data, _, _ := s.buildAdminPageData(r, r.URL.Query().Get("notice"))
	if !data.AdminEnabled {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if !data.LoggedIn {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	data.AdminSection = "logins"
	page, perPage := adminPaginationFromRequest(r)
	allRows := s.buildAdminSavedWorkerRows()
	data.AdminSavedWorkerRows, data.AdminLoginPagination = paginateAdminSlice(allRows, page, perPage)
	s.renderAdminPageTemplate(w, r, data, "admin_logins")
}

func (s *StatusServer) handleAdminLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if !s.allowAdminLoginAttempt() {
		data, _, _ := s.buildAdminPageData(r, "")
		data.AdminLoginError = "Too many login attempts. Please wait a moment and try again."
		w.WriteHeader(http.StatusTooManyRequests)
		s.renderAdminPage(w, r, data)
		return
	}
	if err := r.ParseForm(); err != nil {
		logger.Warn("parse admin login form", "error", err)
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	adminCfg, err := loadAdminConfigFile(s.adminConfigPath)
	data, _, _ := s.buildAdminPageData(r, "")
	if err != nil {
		data.AdminApplyError = fmt.Sprintf("Failed to read admin config: %v", err)
		s.renderAdminPage(w, r, data)
		return
	}
	if !adminCfg.Enabled {
		data.AdminApplyError = "Admin control panel is disabled (set enabled = true in admin.toml)."
		s.renderAdminPage(w, r, data)
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	if username == "" || password == "" || !s.adminCredentialsMatch(adminCfg, username, password) {
		data.AdminLoginError = "Invalid username or password."
		s.renderAdminPage(w, r, data)
		return
	}
	token, expiry, err := s.createAdminSession(adminCfg.sessionDuration())
	if err != nil {
		logger.Error("create admin session failed", "error", err)
		data.AdminLoginError = "Unable to start admin session."
		s.renderAdminPage(w, r, data)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     adminSessionCookieName,
		Value:    token,
		Path:     "/admin",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
		Expires:  expiry,
	})
	http.Redirect(w, r, "/admin?notice=logged_in", http.StatusSeeOther)
}

func (s *StatusServer) allowAdminLoginAttempt() bool {
	if s == nil {
		return false
	}
	now := time.Now()
	s.adminLoginMu.Lock()
	defer s.adminLoginMu.Unlock()
	if !s.adminLoginNext.IsZero() && now.Before(s.adminLoginNext) {
		return false
	}
	s.adminLoginNext = now.Add(100 * time.Millisecond)
	return true
}

func (s *StatusServer) handleAdminLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if token, ok := s.adminSessionToken(r); ok {
		s.invalidateAdminSession(token)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     adminSessionCookieName,
		Path:     "/admin",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
		Expires:  time.Unix(0, 0),
	})
	http.Redirect(w, r, "/admin?notice=logged_out", http.StatusSeeOther)
}

func (s *StatusServer) handleAdminApplySettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		logger.Warn("parse admin settings form", "error", err)
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	data, adminCfg, err := s.buildAdminPageData(r, "")
	if err != nil {
		s.renderAdminPage(w, r, data)
		return
	}
	if !adminCfg.Enabled {
		data.AdminApplyError = "Admin control panel is disabled."
		s.renderAdminPage(w, r, data)
		return
	}
	if !s.isAdminAuthenticated(r) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	password := r.FormValue("password")
	if password == "" || !s.adminPasswordMatches(adminCfg, password) {
		data.AdminApplyError = "Password is required to apply live settings."
		s.renderAdminPage(w, r, data)
		return
	}

	cfg := s.Config()
	if err := applyAdminSettingsForm(&cfg, r); err != nil {
		data.AdminApplyError = err.Error()
		data.Settings = buildAdminSettingsData(cfg)
		s.renderAdminPage(w, r, data)
		return
	}

	// Best-effort helper: keep accept limits consistent when auto mode is enabled.
	autoConfigureAcceptRateLimits(&cfg, tuningFileConfig{}, false)

	if err := validateConfig(cfg); err != nil {
		data.AdminApplyError = fmt.Sprintf("Validation error: %v", err)
		data.Settings = buildAdminSettingsData(cfg)
		s.renderAdminPage(w, r, data)
		return
	}

	s.UpdateConfig(cfg)
	logger.Info("admin applied live settings (in memory)")
	http.Redirect(w, r, "/admin?notice=settings_applied", http.StatusSeeOther)
}

func (s *StatusServer) handleAdminPersist(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		logger.Warn("parse admin persist form", "error", err)
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	data, adminCfg, err := s.buildAdminPageData(r, "")
	if err != nil {
		s.renderAdminPage(w, r, data)
		return
	}
	if !adminCfg.Enabled {
		data.AdminPersistError = "Admin control panel is disabled."
		s.renderAdminPage(w, r, data)
		return
	}
	if !s.isAdminAuthenticated(r) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if !s.adminPasswordMatches(adminCfg, r.FormValue("password")) {
		data.AdminPersistError = "Password is required to save to disk."
		s.renderAdminPage(w, r, data)
		return
	}
	if !strings.EqualFold(strings.TrimSpace(r.FormValue("confirm")), "SAVE") {
		data.AdminPersistError = "Please type SAVE to confirm."
		s.renderAdminPage(w, r, data)
		return
	}

	cfg := s.Config()
	if err := rewriteConfigFile(s.configPath, cfg); err != nil {
		data.AdminPersistError = fmt.Sprintf("Failed to write config.toml: %v", err)
		s.renderAdminPage(w, r, data)
		return
	}
	if err := rewriteTuningFile(s.tuningPath, cfg); err != nil {
		data.AdminPersistError = fmt.Sprintf("Failed to write tuning.toml: %v", err)
		s.renderAdminPage(w, r, data)
		return
	}

	logger.Warn("admin persisted in-memory config to disk", "config_path", s.configPath, "tuning_path", s.tuningPath)
	http.Redirect(w, r, "/admin?notice=saved_to_disk", http.StatusSeeOther)
}

func (s *StatusServer) handleAdminReboot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		logger.Warn("parse admin reboot form", "error", err)
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	data, adminCfg, err := s.buildAdminPageData(r, "reboot_requested")
	if err != nil {
		s.renderAdminPage(w, r, data)
		return
	}
	if !adminCfg.Enabled {
		data.AdminRebootError = "Admin control panel is disabled."
		s.renderAdminPage(w, r, data)
		return
	}
	if !s.isAdminAuthenticated(r) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if !s.adminPasswordMatches(adminCfg, r.FormValue("password")) {
		data.AdminRebootError = "Password is required to reboot."
		s.renderAdminPage(w, r, data)
		return
	}
	if !strings.EqualFold(strings.TrimSpace(r.FormValue("confirm")), "REBOOT") {
		data.AdminRebootError = "Please type REBOOT to confirm."
		s.renderAdminPage(w, r, data)
		return
	}
	logger.Warn("admin requested reboot")
	s.renderAdminPage(w, r, data)
	if s.requestShutdown != nil {
		s.requestShutdown()
	}
}

func (s *StatusServer) handleAdminMinerDisconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin/miners", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		logger.Warn("parse admin miner disconnect form", "error", err)
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	data, adminCfg, _ := s.buildAdminPageData(r, "")
	data.AdminSection = "miners"
	page, perPage := adminPaginationFromRequest(r)
	allRows := s.buildAdminMinerRows()
	data.AdminMinerRows, data.AdminMinerPagination = paginateAdminSlice(allRows, page, perPage)
	if !adminCfg.Enabled {
		data.AdminApplyError = "Admin control panel is disabled."
		s.renderAdminPageTemplate(w, r, data, "admin_miners")
		return
	}
	if !s.isAdminAuthenticated(r) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if r.FormValue("password") == "" || !s.adminPasswordMatches(adminCfg, r.FormValue("password")) {
		data.AdminApplyError = "Password is required to disconnect miners."
		s.renderAdminPageTemplate(w, r, data, "admin_miners")
		return
	}
	seq, err := strconv.ParseUint(strings.TrimSpace(r.FormValue("connection_seq")), 10, 64)
	if err != nil || seq == 0 || s.workerRegistry == nil {
		data.AdminApplyError = "Connection not found."
		s.renderAdminPageTemplate(w, r, data, "admin_miners")
		return
	}
	if mc := s.workerRegistry.connectionBySeq(seq); mc != nil {
		mc.Close("admin disconnect")
		http.Redirect(w, r, "/admin/miners?notice=miner_disconnected", http.StatusSeeOther)
		return
	}
	data.AdminApplyError = "Connection not found."
	s.renderAdminPageTemplate(w, r, data, "admin_miners")
}

func (s *StatusServer) handleAdminMinerBan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin/miners", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		logger.Warn("parse admin miner ban form", "error", err)
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	data, adminCfg, _ := s.buildAdminPageData(r, "")
	data.AdminSection = "miners"
	page, perPage := adminPaginationFromRequest(r)
	allRows := s.buildAdminMinerRows()
	data.AdminMinerRows, data.AdminMinerPagination = paginateAdminSlice(allRows, page, perPage)
	if !adminCfg.Enabled {
		data.AdminApplyError = "Admin control panel is disabled."
		s.renderAdminPageTemplate(w, r, data, "admin_miners")
		return
	}
	if !s.isAdminAuthenticated(r) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if r.FormValue("password") == "" || !s.adminPasswordMatches(adminCfg, r.FormValue("password")) {
		data.AdminApplyError = "Password is required to ban miners."
		s.renderAdminPageTemplate(w, r, data, "admin_miners")
		return
	}
	seq, err := strconv.ParseUint(strings.TrimSpace(r.FormValue("connection_seq")), 10, 64)
	if err != nil || seq == 0 || s.workerRegistry == nil {
		data.AdminApplyError = "Connection not found."
		s.renderAdminPageTemplate(w, r, data, "admin_miners")
		return
	}
	if mc := s.workerRegistry.connectionBySeq(seq); mc != nil {
		duration := s.Config().BanInvalidSubmissionsDuration
		mc.adminBan("admin ban", duration)
		mc.Close("admin ban")
		http.Redirect(w, r, "/admin/miners?notice=miner_banned", http.StatusSeeOther)
		return
	}
	data.AdminApplyError = "Connection not found."
	s.renderAdminPageTemplate(w, r, data, "admin_miners")
}

func (s *StatusServer) handleAdminLoginDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin/logins", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		logger.Warn("parse admin login delete form", "error", err)
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	data, adminCfg, _ := s.buildAdminPageData(r, "")
	data.AdminSection = "logins"
	page, perPage := adminPaginationFromRequest(r)
	allRows := s.buildAdminSavedWorkerRows()
	data.AdminSavedWorkerRows, data.AdminLoginPagination = paginateAdminSlice(allRows, page, perPage)
	if !adminCfg.Enabled {
		data.AdminApplyError = "Admin control panel is disabled."
		s.renderAdminPageTemplate(w, r, data, "admin_logins")
		return
	}
	if !s.isAdminAuthenticated(r) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if r.FormValue("password") == "" || !s.adminPasswordMatches(adminCfg, r.FormValue("password")) {
		data.AdminApplyError = "Password is required to delete saved workers."
		s.renderAdminPageTemplate(w, r, data, "admin_logins")
		return
	}
	userIDs := r.Form["user_id"]
	if len(userIDs) == 0 || s.workerLists == nil {
		data.AdminApplyError = "Saved worker record not found."
		s.renderAdminPageTemplate(w, r, data, "admin_logins")
		return
	}
	seen := make(map[string]struct{})
	for _, rawID := range userIDs {
		id := strings.TrimSpace(rawID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		if err := s.workerLists.RemoveUser(id); err != nil {
			logger.Warn("delete saved worker", "error", err, "user_id", id)
		}
	}
	http.Redirect(w, r, "/admin/logins?notice=saved_worker_deleted", http.StatusSeeOther)
}

func (s *StatusServer) handleAdminLoginBan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin/logins", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		logger.Warn("parse admin login ban form", "error", err)
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	data, adminCfg, _ := s.buildAdminPageData(r, "")
	data.AdminSection = "logins"
	page, perPage := adminPaginationFromRequest(r)
	allRows := s.buildAdminSavedWorkerRows()
	data.AdminSavedWorkerRows, data.AdminLoginPagination = paginateAdminSlice(allRows, page, perPage)
	if !adminCfg.Enabled {
		data.AdminApplyError = "Admin control panel is disabled."
		s.renderAdminPageTemplate(w, r, data, "admin_logins")
		return
	}
	if !s.isAdminAuthenticated(r) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if r.FormValue("password") == "" || !s.adminPasswordMatches(adminCfg, r.FormValue("password")) {
		data.AdminApplyError = "Password is required to ban saved workers."
		s.renderAdminPageTemplate(w, r, data, "admin_logins")
		return
	}
	hashes := r.Form["worker_hash"]
	userID := strings.TrimSpace(r.FormValue("user_id"))
	if len(hashes) == 0 || s.workerRegistry == nil {
		data.AdminApplyError = "Worker hash is required."
		s.renderAdminPageTemplate(w, r, data, "admin_logins")
		return
	}
	connsFound := false
	for _, hash := range hashes {
		if hash == "" {
			continue
		}
		hash = strings.ToLower(hash)
		conns := s.workerRegistry.getConnectionsByHash(hash)
		if len(conns) == 0 {
			continue
		}
		connsFound = true
		duration := s.Config().BanInvalidSubmissionsDuration
		reason := "admin login ban"
		if userID != "" {
			reason = fmt.Sprintf("admin login ban (%s)", userID)
		}
		for _, mc := range conns {
			if mc == nil {
				continue
			}
			mc.adminBan(reason, duration)
			mc.Close("admin login ban")
		}
	}
	if !connsFound {
		data.AdminApplyError = "No active connections for this worker."
		s.renderAdminPageTemplate(w, r, data, "admin_logins")
		return
	}
	http.Redirect(w, r, "/admin/logins?notice=saved_worker_banned", http.StatusSeeOther)
}

func adminPaginationFromRequest(r *http.Request) (int, int) {
	page := 1
	perPage := defaultAdminPerPage
	if r == nil {
		return page, perPage
	}
	query := r.URL.Query()
	if v := strings.TrimSpace(query.Get("page")); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 {
			page = p
		}
	}
	if v := strings.TrimSpace(query.Get("per_page")); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			if p < 1 {
				p = defaultAdminPerPage
			}
			if p > maxAdminPerPage {
				p = maxAdminPerPage
			}
			perPage = p
		}
	}
	return page, perPage
}

func paginateAdminSlice[T any](items []T, page, perPage int) ([]T, AdminPagination) {
	total := len(items)
	if perPage <= 0 {
		perPage = defaultAdminPerPage
	}
	totalPages := (total + perPage - 1) / perPage
	if totalPages == 0 {
		totalPages = 1
	}
	if page < 1 {
		page = 1
	}
	if page > totalPages {
		page = totalPages
	}
	start := (page - 1) * perPage
	if start > total {
		start = total
	}
	end := start + perPage
	if end > total {
		end = total
	}
	var paged []T
	if end > start {
		paged = items[start:end]
	}
	pagination := AdminPagination{
		Page:        page,
		PerPage:     perPage,
		TotalItems:  total,
		TotalPages:  totalPages,
		RangeStart:  0,
		RangeEnd:    end,
		HasPrevPage: page > 1,
		HasNextPage: end < total,
		PrevPage:    page - 1,
		NextPage:    page + 1,
	}
	if total > 0 {
		pagination.RangeStart = start + 1
	}
	return paged, pagination
}

func (s *StatusServer) buildAdminPageData(r *http.Request, noticeKey string) (AdminPageData, adminFileConfig, error) {
	start := time.Now()
	data := AdminPageData{
		StatusData:          s.baseTemplateData(start),
		AdminConfigPath:     s.adminConfigPath,
		AdminNotice:         adminNoticeMessage(noticeKey),
		AdminPerPageOptions: adminPerPageOptions,
	}
	cfg, err := loadAdminConfigFile(s.adminConfigPath)
	if err != nil {
		logger.Warn("load admin config failed", "error", err, "path", s.adminConfigPath)
		data.AdminEnabled = false
		data.AdminApplyError = fmt.Sprintf("Failed to read admin config: %v", err)
		return data, cfg, err
	}
	data.AdminEnabled = cfg.Enabled
	data.LoggedIn = s.isAdminAuthenticated(r)
	data.Settings = buildAdminSettingsData(s.Config())
	data.AdminSection = "settings"
	return data, cfg, nil
}

func (s *StatusServer) renderAdminPage(w http.ResponseWriter, r *http.Request, data AdminPageData) {
	s.renderAdminPageTemplate(w, r, data, "admin")
}

func (s *StatusServer) renderAdminPageTemplate(w http.ResponseWriter, r *http.Request, data AdminPageData, templateName string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, templateName, data); err != nil {
		logger.Error("admin template error", "error", err)
		s.renderErrorPage(w, r, http.StatusInternalServerError,
			"Admin panel error",
			"We couldn't render the admin control panel.",
			"Template error while rendering the admin interface.")
	}
}

func adminNoticeMessage(key string) string {
	switch key {
	case "settings_applied":
		return "Live settings applied in memory."
	case "saved_to_disk":
		return "Saved current in-memory settings to config.toml and tuning.toml."
	case "reboot_requested":
		return "Reboot requested. goPool is shutting down now."
	case "logged_in":
		return "Admin session unlocked."
	case "logged_out":
		return "Admin session cleared."
	case "miner_disconnected":
		return "Miner connection disconnected."
	case "miner_banned":
		return "Miner connection banned and closed."
	case "saved_worker_deleted":
		return "Saved worker entry deleted."
	case "saved_worker_banned":
		return "Worker was banned from saved logins."
	default:
		return ""
	}
}

func buildAdminSettingsData(cfg Config) AdminSettingsData {
	timeoutSec := int(cfg.ConnectionTimeout / time.Second)
	if timeoutSec < 0 {
		timeoutSec = 0
	}
	return AdminSettingsData{
		StatusBrandName:                      cfg.StatusBrandName,
		StatusBrandDomain:                    cfg.StatusBrandDomain,
		StatusPublicURL:                      cfg.StatusPublicURL,
		StatusTagline:                        cfg.StatusTagline,
		FiatCurrency:                         cfg.FiatCurrency,
		GitHubURL:                            cfg.GitHubURL,
		DiscordURL:                           cfg.DiscordURL,
		ServerLocation:                       cfg.ServerLocation,
		ListenAddr:                           cfg.ListenAddr,
		StatusAddr:                           cfg.StatusAddr,
		StatusTLSAddr:                        cfg.StatusTLSAddr,
		StratumTLSListen:                     cfg.StratumTLSListen,
		MaxConns:                             cfg.MaxConns,
		MaxAcceptsPerSecond:                  cfg.MaxAcceptsPerSecond,
		MaxAcceptBurst:                       cfg.MaxAcceptBurst,
		AutoAcceptRateLimits:                 cfg.AutoAcceptRateLimits,
		AcceptReconnectWindow:                cfg.AcceptReconnectWindow,
		AcceptBurstWindow:                    cfg.AcceptBurstWindow,
		AcceptSteadyStateWindow:              cfg.AcceptSteadyStateWindow,
		AcceptSteadyStateRate:                cfg.AcceptSteadyStateRate,
		AcceptSteadyStateReconnectPercent:    cfg.AcceptSteadyStateReconnectPercent,
		AcceptSteadyStateReconnectWindow:     cfg.AcceptSteadyStateReconnectWindow,
		ConnectionTimeoutSeconds:             timeoutSec,
		MinDifficulty:                        cfg.MinDifficulty,
		MaxDifficulty:                        cfg.MaxDifficulty,
		LockSuggestedDifficulty:              cfg.LockSuggestedDifficulty,
		SoloMode:                             cfg.SoloMode,
		DirectSubmitProcessing:               cfg.DirectSubmitProcessing,
		CheckDuplicateShares:                 cfg.CheckDuplicateShares,
		PeerCleanupEnabled:                   cfg.PeerCleanupEnabled,
		PeerCleanupMaxPingMs:                 cfg.PeerCleanupMaxPingMs,
		PeerCleanupMinPeers:                  cfg.PeerCleanupMinPeers,
		CleanExpiredBansOnStartup:            cfg.CleanExpiredBansOnStartup,
		BanInvalidSubmissionsAfter:           cfg.BanInvalidSubmissionsAfter,
		BanInvalidSubmissionsWindowSeconds:   int(cfg.BanInvalidSubmissionsWindow / time.Second),
		BanInvalidSubmissionsDurationSeconds: int(cfg.BanInvalidSubmissionsDuration / time.Second),
		ReconnectBanThreshold:                cfg.ReconnectBanThreshold,
		ReconnectBanWindowSeconds:            cfg.ReconnectBanWindowSeconds,
		ReconnectBanDurationSeconds:          cfg.ReconnectBanDurationSeconds,
		LogLevel:                             cfg.LogLevel,
	}
}

func (s *StatusServer) buildAdminMinerRows() []AdminMinerRow {
	if s == nil || s.registry == nil {
		return nil
	}
	now := time.Now()
	conns := s.registry.Snapshot()
	if len(conns) == 0 {
		return nil
	}
	rows := make([]AdminMinerRow, 0, len(conns))
	for _, mc := range conns {
		if mc == nil {
			continue
		}
		seq := atomic.LoadUint64(&mc.connectionSeq)
		listener := "Stratum"
		if mc.isTLSConnection {
			listener = "Stratum TLS"
		}
		stats, acceptRate, submitRate := mc.snapshotStatsWithRates(now)
		snap := mc.snapshotShareInfo()
		until, reason, _ := mc.banDetails()
		rows = append(rows, AdminMinerRow{
			ConnectionSeq:       seq,
			ConnectionLabel:     mc.connectionIDString(),
			RemoteAddr:          mc.id,
			Listener:            listener,
			Worker:              mc.currentWorker(),
			WorkerHash:          workerNameHash(mc.currentWorker()),
			ClientName:          strings.TrimSpace(mc.minerClientName),
			ClientVersion:       strings.TrimSpace(mc.minerClientVersion),
			Difficulty:          atomicLoadFloat64(&mc.difficulty),
			Hashrate:            snap.RollingHashrate,
			AcceptRatePerMinute: acceptRate,
			SubmitRatePerMinute: submitRate,
			Stats:               stats,
			ConnectedAt:         mc.connectedAt,
			LastActivity:        mc.lastActivity,
			LastShare:           stats.LastShare,
			Banned:              mc.isBanned(now),
			BanReason:           reason,
			BanUntil:            until,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].ConnectionSeq < rows[j].ConnectionSeq
	})
	return rows
}

func (s *StatusServer) buildAdminSavedWorkerRows() []AdminSavedWorkerRow {
	if s == nil || s.workerLists == nil {
		return nil
	}
	records, err := s.workerLists.ListAllSavedWorkers()
	if err != nil {
		logger.Warn("list saved workers for admin", "error", err)
		return nil
	}
	if len(records) == 0 {
		return nil
	}
	rowsByUser := make(map[string]*AdminSavedWorkerRow)
	for _, record := range records {
		if record.UserID == "" {
			continue
		}
		row, exists := rowsByUser[record.UserID]
		if !exists {
			row = &AdminSavedWorkerRow{UserID: record.UserID}
			rowsByUser[record.UserID] = row
		}
		row.Workers = append(row.Workers, record.SavedWorkerEntry)
		if record.NotifyEnabled {
			row.NotifyCount++
		}
		if record.Hash != "" {
			row.WorkerHashes = append(row.WorkerHashes, record.Hash)
		}
	}

	rows := make([]AdminSavedWorkerRow, 0, len(rowsByUser))
	for _, row := range rowsByUser {
		seenHashes := make(map[string]struct{})
		dedup := row.WorkerHashes[:0]
		for _, h := range row.WorkerHashes {
			if h == "" {
				continue
			}
			lower := strings.ToLower(h)
			if _, ok := seenHashes[lower]; ok {
				continue
			}
			seenHashes[lower] = struct{}{}
			dedup = append(dedup, lower)
		}
		row.WorkerHashes = dedup
		rows = append(rows, *row)
	}

	if s.workerRegistry != nil {
		for i := range rows {
			seen := make(map[uint64]struct{})
			for _, hash := range rows[i].WorkerHashes {
				if hash == "" {
					continue
				}
				conns := s.workerRegistry.getConnectionsByHash(hash)
				for _, mc := range conns {
					if mc == nil {
						continue
					}
					seq := atomic.LoadUint64(&mc.connectionSeq)
					if seq == 0 {
						continue
					}
					if _, duplicate := seen[seq]; duplicate {
						continue
					}
					seen[seq] = struct{}{}
					listener := "Stratum"
					if mc.isTLSConnection {
						listener = "Stratum TLS"
					}
					rows[i].OnlineConnections = append(rows[i].OnlineConnections, AdminMinerConnection{
						ConnectionSeq:   seq,
						ConnectionLabel: mc.connectionIDString(),
						RemoteAddr:      mc.id,
						Listener:        listener,
					})
				}
			}
		}
	}

	sort.Slice(rows, func(i, j int) bool {
		return rows[i].UserID < rows[j].UserID
	})
	return rows
}

func applyAdminSettingsForm(cfg *Config, r *http.Request) error {
	if cfg == nil || r == nil {
		return fmt.Errorf("missing request/config")
	}

	getTrim := func(key string) string { return strings.TrimSpace(r.FormValue(key)) }
	getBool := func(key string) bool { return strings.TrimSpace(r.FormValue(key)) != "" }

	parseInt := func(key string, current int) (int, error) {
		raw := getTrim(key)
		if raw == "" {
			return current, nil
		}
		v, err := strconv.Atoi(raw)
		if err != nil {
			return current, fmt.Errorf("%s must be an integer", key)
		}
		return v, nil
	}

	parseFloat := func(key string, current float64) (float64, error) {
		raw := getTrim(key)
		if raw == "" {
			return current, nil
		}
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return current, fmt.Errorf("%s must be a number", key)
		}
		return v, nil
	}

	normalizeListen := func(s string) string {
		s = strings.TrimSpace(s)
		if s == "" {
			return s
		}
		if !strings.Contains(s, ":") {
			return ":" + s
		}
		return s
	}

	cfg.StatusBrandName = getTrim("status_brand_name")
	cfg.StatusBrandDomain = getTrim("status_brand_domain")
	cfg.StatusTagline = getTrim("status_tagline")
	cfg.FiatCurrency = strings.ToLower(getTrim("fiat_currency"))
	cfg.GitHubURL = getTrim("github_url")
	cfg.DiscordURL = getTrim("discord_url")
	cfg.ServerLocation = getTrim("server_location")
	cfg.StatusPublicURL = getTrim("status_public_url")

	cfg.ListenAddr = normalizeListen(getTrim("pool_listen"))
	cfg.StatusAddr = normalizeListen(getTrim("status_listen"))
	cfg.StatusTLSAddr = normalizeListen(getTrim("status_tls_listen"))
	cfg.StratumTLSListen = normalizeListen(getTrim("stratum_tls_listen"))

	var err error
	if cfg.MaxConns, err = parseInt("max_conns", cfg.MaxConns); err != nil {
		return err
	}
	cfg.AutoAcceptRateLimits = getBool("auto_accept_rate_limits")
	if cfg.MaxAcceptsPerSecond, err = parseInt("max_accepts_per_second", cfg.MaxAcceptsPerSecond); err != nil {
		return err
	}
	if cfg.MaxAcceptBurst, err = parseInt("max_accept_burst", cfg.MaxAcceptBurst); err != nil {
		return err
	}
	if cfg.AcceptReconnectWindow, err = parseInt("accept_reconnect_window", cfg.AcceptReconnectWindow); err != nil {
		return err
	}
	if cfg.AcceptBurstWindow, err = parseInt("accept_burst_window", cfg.AcceptBurstWindow); err != nil {
		return err
	}
	if cfg.AcceptSteadyStateWindow, err = parseInt("accept_steady_state_window", cfg.AcceptSteadyStateWindow); err != nil {
		return err
	}
	if cfg.AcceptSteadyStateRate, err = parseInt("accept_steady_state_rate", cfg.AcceptSteadyStateRate); err != nil {
		return err
	}
	if cfg.AcceptSteadyStateReconnectPercent, err = parseFloat("accept_steady_state_reconnect_percent", cfg.AcceptSteadyStateReconnectPercent); err != nil {
		return err
	}
	if cfg.AcceptSteadyStateReconnectWindow, err = parseInt("accept_steady_state_reconnect_window", cfg.AcceptSteadyStateReconnectWindow); err != nil {
		return err
	}

	timeoutSec, err := parseInt("connection_timeout_seconds", int(cfg.ConnectionTimeout/time.Second))
	if err != nil {
		return err
	}
	cfg.ConnectionTimeout = time.Duration(timeoutSec) * time.Second

	if cfg.MinDifficulty, err = parseFloat("min_difficulty", cfg.MinDifficulty); err != nil {
		return err
	}
	if cfg.MaxDifficulty, err = parseFloat("max_difficulty", cfg.MaxDifficulty); err != nil {
		return err
	}
	cfg.LockSuggestedDifficulty = getBool("lock_suggested_difficulty")

	cfg.CleanExpiredBansOnStartup = getBool("clean_expired_on_startup")
	if cfg.BanInvalidSubmissionsAfter, err = parseInt("ban_invalid_submissions_after", cfg.BanInvalidSubmissionsAfter); err != nil {
		return err
	}
	windowSec, err := parseInt("ban_invalid_submissions_window_seconds", int(cfg.BanInvalidSubmissionsWindow/time.Second))
	if err != nil {
		return err
	}
	cfg.BanInvalidSubmissionsWindow = time.Duration(windowSec) * time.Second
	durSec, err := parseInt("ban_invalid_submissions_duration_seconds", int(cfg.BanInvalidSubmissionsDuration/time.Second))
	if err != nil {
		return err
	}
	cfg.BanInvalidSubmissionsDuration = time.Duration(durSec) * time.Second
	if cfg.ReconnectBanThreshold, err = parseInt("reconnect_ban_threshold", cfg.ReconnectBanThreshold); err != nil {
		return err
	}
	if cfg.ReconnectBanWindowSeconds, err = parseInt("reconnect_ban_window_seconds", cfg.ReconnectBanWindowSeconds); err != nil {
		return err
	}
	if cfg.ReconnectBanDurationSeconds, err = parseInt("reconnect_ban_duration_seconds", cfg.ReconnectBanDurationSeconds); err != nil {
		return err
	}

	cfg.PeerCleanupEnabled = getBool("peer_cleanup_enabled")
	if cfg.PeerCleanupMaxPingMs, err = parseFloat("peer_cleanup_max_ping_ms", cfg.PeerCleanupMaxPingMs); err != nil {
		return err
	}
	if cfg.PeerCleanupMinPeers, err = parseInt("peer_cleanup_min_peers", cfg.PeerCleanupMinPeers); err != nil {
		return err
	}

	cfg.SoloMode = getBool("solo_mode")
	cfg.DirectSubmitProcessing = getBool("direct_submit_processing")
	cfg.CheckDuplicateShares = getBool("check_duplicate_shares")

	if lvl := strings.ToLower(getTrim("log_level")); lvl != "" {
		cfg.LogLevel = lvl
	}

	return nil
}

func rewriteTuningFile(path string, cfg Config) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	tf := buildTuningFileConfig(cfg)
	data, err := toml.Marshal(tf)
	if err != nil {
		return fmt.Errorf("encode tuning: %w", err)
	}
	data = withPrependedTOMLComments(data, generatedTuningFileHeader(), tuningConfigDocComments())
	if err := atomicWriteFile(path, data); err != nil {
		return err
	}
	_ = os.Chmod(path, 0o644)
	return nil
}

func (s *StatusServer) isAdminAuthenticated(r *http.Request) bool {
	token, ok := s.adminSessionToken(r)
	if !ok {
		return false
	}
	s.adminSessionsMu.Lock()
	defer s.adminSessionsMu.Unlock()
	expiry, exists := s.adminSessions[token]
	if !exists {
		return false
	}
	if time.Now().After(expiry) {
		delete(s.adminSessions, token)
		return false
	}
	return true
}

func (s *StatusServer) adminSessionToken(r *http.Request) (string, bool) {
	if r == nil {
		return "", false
	}
	cookie, err := r.Cookie(adminSessionCookieName)
	if err != nil {
		return "", false
	}
	if cookie.Value == "" {
		return "", false
	}
	return cookie.Value, true
}

func (s *StatusServer) createAdminSession(duration time.Duration) (string, time.Time, error) {
	if duration <= 0 {
		duration = time.Duration(defaultAdminSessionExpirationSeconds) * time.Second
	}
	token, err := generateAdminToken()
	if err != nil {
		return "", time.Time{}, err
	}
	expiry := time.Now().Add(duration)
	s.adminSessionsMu.Lock()
	s.adminSessions[token] = expiry
	s.adminSessionsMu.Unlock()
	return token, expiry, nil
}

func generateAdminToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func (s *StatusServer) adminCredentialsMatch(cfg adminFileConfig, username, password string) bool {
	if cfg.Username == "" && cfg.Password == "" {
		return false
	}
	if !compareStringsConstantTime(cfg.Username, strings.TrimSpace(username)) {
		return false
	}
	return s.adminPasswordMatches(cfg, password)
}

func (s *StatusServer) adminPasswordMatches(cfg adminFileConfig, password string) bool {
	return compareStringsConstantTime(cfg.Password, password)
}

func compareStringsConstantTime(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func (s *StatusServer) invalidateAdminSession(token string) {
	if token == "" {
		return
	}
	s.adminSessionsMu.Lock()
	delete(s.adminSessions, token)
	s.adminSessionsMu.Unlock()
}

func (s *StatusServer) withClerkUser(h http.HandlerFunc) http.HandlerFunc {
	if s == nil || s.clerk == nil {
		return h
	}
	return func(w http.ResponseWriter, r *http.Request) {
		user := s.clerkUserFromRequest(r)
		if user != nil {
			r = r.WithContext(contextWithClerkUser(r.Context(), user))
		}
		h(w, r)
	}
}

func (s *StatusServer) storeStatusPublicURL(raw string) {
	parsed := parseStatusPublicURL(raw)
	s.statusPublicURL.Store(parsed)
}

func parseStatusPublicURL(raw string) *url.URL {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		logger.Warn("invalid status_public_url", "url", raw, "error", err)
		return nil
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		logger.Warn("invalid status_public_url", "url", raw, "error", "missing scheme or host")
		return nil
	}
	return parsed
}

func (s *StatusServer) getStatusPublicURL() *url.URL {
	if s == nil {
		return nil
	}
	if v := s.statusPublicURL.Load(); v != nil {
		if u, ok := v.(*url.URL); ok {
			return u
		}
	}
	return nil
}

func (s *StatusServer) baseURLForRequest(r *http.Request) *url.URL {
	if s == nil {
		return nil
	}
	if parsed := s.getStatusPublicURL(); parsed != nil {
		return parsed
	}
	if r == nil {
		return nil
	}
	scheme := "http"
	if proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); proto != "" {
		scheme = strings.ToLower(proto)
	} else if r.TLS != nil {
		scheme = "https"
	}
	host := strings.TrimSpace(r.Host)
	if host == "" {
		host = "localhost"
	}
	return &url.URL{
		Scheme: scheme,
		Host:   host,
	}
}

func (s *StatusServer) canonicalStatusHost(r *http.Request) string {
	if parsed := s.getStatusPublicURL(); parsed != nil && parsed.Host != "" {
		return parsed.Host
	}
	if r == nil {
		return "localhost"
	}
	host := strings.TrimSpace(r.Host)
	if host == "" {
		host = "localhost"
	}
	return host
}

func (s *StatusServer) httpsRedirectURL(r *http.Request) string {
	if r == nil {
		return ""
	}
	host := s.canonicalStatusHost(r)
	base := s.baseURLForRequest(r)
	redirectBase := &url.URL{
		Scheme: "https",
		Host:   host,
	}
	if base != nil && strings.TrimSpace(base.Host) != "" {
		redirectBase.Host = base.Host
	}

	path := r.URL.Path
	if path == "" {
		path = "/"
	}
	ref := &url.URL{
		Path:     path,
		RawQuery: r.URL.RawQuery,
		Fragment: r.URL.Fragment,
	}

	target := redirectBase.ResolveReference(ref)
	if target.Scheme == "" {
		target.Scheme = "https"
	}
	if target.Host == "" {
		target.Host = host
	}
	return target.String()
}

func (s *StatusServer) redirectToHTTPS(w http.ResponseWriter, r *http.Request) {
	target := s.httpsRedirectURL(r)
	if target == "" {
		http.Error(w, "Redirect target unavailable", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, target, http.StatusTemporaryRedirect)
}

func (s *StatusServer) clerkCookieSecure(r *http.Request) bool {
	if r == nil {
		return false
	}
	if base := s.baseURLForRequest(r); base != nil {
		return strings.EqualFold(base.Scheme, "https")
	}
	if proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); proto != "" {
		return strings.EqualFold(proto, "https")
	}
	return r.TLS != nil
}

func (s *StatusServer) clerkUserFromRequest(r *http.Request) *ClerkUser {
	if s == nil || s.clerk == nil {
		return nil
	}
	cookie, err := r.Cookie(s.clerk.SessionCookieName())
	if err != nil {
		if err != http.ErrNoCookie {
			logger.Warn("failed to read session cookie", "error", err, "remote_addr", r.RemoteAddr)
		}
		return nil
	}
	claims, err := s.clerk.Verify(cookie.Value)
	if err != nil {
		// In dev/test Clerk environments, tokens expiring frequently is common
		// and can create noisy logs (e.g. saved-workers polling). Silence these
		// verification warnings when using test keys.
		secret := strings.TrimSpace(s.Config().ClerkSecretKey)
		publishable := strings.TrimSpace(s.Config().ClerkPublishableKey)
		if strings.HasPrefix(secret, "sk_test_") || strings.HasPrefix(publishable, "pk_test_") {
			logger.Debug("clerk session verification failed (test keys)", "error", err, "remote_addr", r.RemoteAddr)
			return nil
		}
		logger.Warn("clerk session verification failed", "error", err, "remote_addr", r.RemoteAddr)
		return nil
	}
	return &ClerkUser{
		UserID:    claims.Subject,
		SessionID: claims.SessionID,
	}
}

func (s *StatusServer) clerkUIEnabled() bool {
	if forceClerkLoginUIForTesting {
		return true
	}
	if s == nil || s.clerk == nil {
		return false
	}
	// The worker login page's embedded sign-in experience requires a publishable
	// key from secrets.toml; when it's missing, hide the sign-in box.
	return strings.TrimSpace(s.Config().ClerkPublishableKey) != ""
}

func (s *StatusServer) enrichStatusDataWithClerk(r *http.Request, data *StatusData) {
	if s == nil || data == nil {
		return
	}
	data.ClerkEnabled = s.clerkUIEnabled()
	if !data.ClerkEnabled {
		return
	}
	redirect := safeRedirectPath(r.URL.Query().Get("redirect"))
	if redirect == "" {
		redirect = "/saved-workers"
	}
	data.ClerkLoginURL = s.clerkLoginURL(r, redirect)
	if user := ClerkUserFromContext(r.Context()); user != nil {
		data.ClerkUser = user
		if s.workerLists != nil {
			if list, err := s.workerLists.List(user.UserID); err == nil {
				data.SavedWorkers = list
			} else {
				logger.Warn("load saved workers", "error", err, "user_id", user.UserID)
			}
			if data.DiscordNotificationsEnabled {
				if _, enabled, ok, err := s.workerLists.GetDiscordLink(user.UserID); err == nil {
					data.DiscordNotificationsRegistered = ok
					data.DiscordNotificationsUserEnabled = ok && enabled
				}
			}
		}
	}
}

func (s *StatusServer) clerkLoginURL(r *http.Request, redirect string) string {
	if s == nil {
		return ""
	}
	redirectURL := s.clerkRedirectURL(r, redirect)
	if s.clerk != nil {
		callbackRedirect := s.clerkRedirectURL(r, s.clerk.CallbackPath())
		login := s.clerk.LoginURL(callbackRedirect, s.Config().ClerkFrontendAPIURL)
		if redirect == "" {
			return login
		}
		sep := "?"
		if strings.Contains(login, "?") {
			sep = "&"
		}
		return login + sep + "redirect=" + url.QueryEscape(redirect)
	}

	base := strings.TrimSpace(s.Config().ClerkSignInURL)
	if base == "" {
		base = defaultClerkSignInURL
	}
	values := url.Values{}
	values.Set("redirect_url", redirectURL)
	if frontendAPI := strings.TrimSpace(s.Config().ClerkFrontendAPIURL); frontendAPI != "" {
		values.Set("frontend_api", frontendAPI)
	}
	return base + "?" + values.Encode()
}

func (s *StatusServer) clerkRedirectURL(r *http.Request, redirect string) string {
	if s == nil {
		return "/worker"
	}
	redirectPath := safeRedirectPath(redirect)
	if redirectPath == "" {
		redirectPath = "/worker"
	}
	base := s.baseURLForRequest(r)
	if base == nil {
		return redirectPath
	}
	ref := &url.URL{Path: redirectPath}
	return base.ResolveReference(ref).String()
}
