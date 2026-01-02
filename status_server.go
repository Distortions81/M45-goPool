package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/hako/durafmt"
)

func (s *StatusServer) SetJobManager(jm *JobManager) {
	s.jobMgr = jm
	// Set up callback to invalidate status cache when new blocks arrive
	jm.onNewBlock = s.invalidateStatusCache
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
	Warnings                []string            `json:"warnings,omitempty"`
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
	s.serveCachedJSON(w, key, overviewRefreshInterval, func() ([]byte, error) {
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
				LastHashTx:        view.JobFeed.LastHashTx,
				LastHashTxAt:      view.JobFeed.LastHashTxAt,
				LastRawTxAt:       view.JobFeed.LastRawTxAt,
				LastRawTxBytes:    view.JobFeed.LastRawTxBytes,
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
		login := s.clerk.LoginURL(r, s.clerk.CallbackPath(), s.Config().ClerkFrontendAPIURL)
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
	scheme := "http"
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	} else if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	if host == "" {
		host = "localhost"
	}
	return (&url.URL{
		Scheme: scheme,
		Host:   host,
		Path:   redirectPath,
	}).String()
}
