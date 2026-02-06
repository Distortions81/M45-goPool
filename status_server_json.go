package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/hako/durafmt"
)

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
			RPCHealthy:      view.RPCHealthy,
			RPCDisconnects:  view.RPCDisconnects,
			RPCReconnects:   view.RPCReconnects,
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
