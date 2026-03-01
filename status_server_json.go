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

type compactHashrateSeries struct {
	I    int      `json:"i"`  // interval seconds
	S    uint32   `json:"s"`  // start unix-minute
	N    int      `json:"n"`  // number of buckets
	P    []uint16 `json:"p"`  // presence bitset
	HMin float64  `json:"h0"` // hashrate min
	HMax float64  `json:"h1"` // hashrate max
	HQ   []uint16 `json:"hq"` // hashrate q8
	BMin float64  `json:"b0"` // best-share min
	BMax float64  `json:"b1"` // best-share max
	BQ   []uint16 `json:"bq"` // best-share q8
}

// handleNodePageJSON returns Bitcoin node information.
func (s *StatusServer) handleNodePageJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
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

// handleBlocksListJSON returns found blocks.
func (s *StatusServer) handleBlocksListJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
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

// handleOverviewPageJSON returns minimal data for the overview page.
func (s *StatusServer) handleOverviewPageJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
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

		poolTag := displayPoolTagFromCoinbaseMessage(view.CoinbaseMessage)

		// Keep banned-worker payloads bounded; the UI only needs a small sample.
		const maxBannedOnOverview = 25
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
			MinerTypes:      view.MinerTypes,
		}
		return sonic.Marshal(data)
	})
}

// handlePoolPageJSON returns pool configuration data for the pool info page.
func (s *StatusServer) handlePoolPageJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	key := "pool_page"
	s.serveCachedJSON(w, key, overviewRefreshInterval, func() ([]byte, error) {
		view := s.statusDataView()
		safeguardDisconnectCount, safeguardDisconnects := s.stratumSafeguardDisconnectSnapshot()
		data := PoolPageData{
			APIVersion:                      apiVersion,
			BlocksAccepted:                  view.BlocksAccepted,
			BlocksErrored:                   view.BlocksErrored,
			RPCGBTLastSec:                   view.RPCGBTLastSec,
			RPCGBTMaxSec:                    view.RPCGBTMaxSec,
			RPCGBTCount:                     view.RPCGBTCount,
			RPCSubmitLastSec:                view.RPCSubmitLastSec,
			RPCSubmitMaxSec:                 view.RPCSubmitMaxSec,
			RPCSubmitCount:                  view.RPCSubmitCount,
			RPCErrors:                       view.RPCErrors,
			ShareErrors:                     view.ShareErrors,
			RPCGBTMin1hSec:                  view.RPCGBTMin1hSec,
			RPCGBTAvg1hSec:                  view.RPCGBTAvg1hSec,
			RPCGBTMax1hSec:                  view.RPCGBTMax1hSec,
			StratumSafeguardDisconnectCount: safeguardDisconnectCount,
			StratumSafeguardDisconnects:     safeguardDisconnects,
			ErrorHistory:                    view.ErrorHistory,
		}
		return sonic.Marshal(data)
	})
}

// handleServerPageJSON returns combined status and diagnostics for the server page.
func (s *StatusServer) handleServerPageJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
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

// handlePoolHashrateJSON returns hashrate and block-time telemetry.
func (s *StatusServer) handlePoolHashrateJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	key := "pool_hashrate"
	includeHistory := false
	if r != nil {
		switch r.URL.Query().Get("include_history") {
		case "2":
			includeHistory = true
		}
	}
	if includeHistory {
		key += "_with_history"
		key += "_quant"
	}
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
					fees := max(job.CoinbaseValue-subsidy, 0)
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
			remaining := max(next-blockHeight, 0)
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
			PoolHashrateHistoryQ   []uint16                `json:"phh,omitempty"`
			PoolHashrateHistory24h *compactHashrateSeries  `json:"ph24,omitempty"`
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
			BlockHeight:            blockHeight,
			BlockDifficulty:        blockDifficulty,
			BlockTimeLeftSec:       blockTimeLeftSec,
			RecentBlockTimes:       recentBlockTimes,
			NextDifficultyRetarget: nextRetarget,
			TemplateTxFeesSats:     templateTxFeesSats,
			TemplateUpdatedAt:      templateUpdatedAt,
			UpdatedAt:              time.Now().UTC().Format(time.RFC3339),
		}
		computedHashrate := s.computePoolHashrate()
		if computedHashrate > 0 {
			data.PoolHashrate = computedHashrate
			s.appendPoolHashrateHistory(computedHashrate, blockHeight, now)
		} else if fallbackHashrate, fallbackHeight, ok := s.latestPoolHashrateHistorySince(now, poolHashrateDisplayFallbackMaxAge); ok {
			data.PoolHashrate = fallbackHashrate
			if data.BlockHeight <= 0 && fallbackHeight > 0 {
				data.BlockHeight = fallbackHeight
			}
		}
		if includeHistory {
			data.PoolHashrateHistoryQ = s.poolHashrateHistorySnapshot(now)
			data.PoolHashrateHistory24h = s.compactPoolHashrateSeries(now.UTC())
		}
		return sonic.Marshal(data)
	})
}

func (s *StatusServer) compactPoolHashrateSeries(now time.Time) *compactHashrateSeries {
	if s == nil {
		return nil
	}
	samples := s.savedWorkerPeriodHistory(savedWorkerPeriodPoolKey, now)
	if len(samples) == 0 {
		return nil
	}
	nowBucketMinute := savedWorkerUnixMinute(now.Truncate(savedWorkerPeriodBucket))
	if nowBucketMinute == 0 {
		return nil
	}
	spanMinutes := (savedWorkerPeriodSlots - 1) * savedWorkerPeriodBucketMinutes
	startMinute := uint32(0)
	if nowBucketMinute >= uint32(spanMinutes) {
		startMinute = nowBucketMinute - uint32(spanMinutes)
	}
	count := savedWorkerPeriodSlots
	present := make([]bool, count)
	presentBits := make([]uint8, (count+7)/8)
	hashrateVals := make([]float64, count)
	bestVals := make([]float64, count)
	for _, sample := range samples {
		if sample.At.IsZero() {
			continue
		}
		m := savedWorkerUnixMinute(sample.At)
		if m < startMinute {
			continue
		}
		offsetMin := int(m - startMinute)
		if savedWorkerPeriodBucketMinutes <= 0 || offsetMin < 0 {
			continue
		}
		idx := offsetMin / savedWorkerPeriodBucketMinutes
		if idx < 0 || idx >= count {
			continue
		}
		present[idx] = true
		setBit(presentBits, idx)
		hashrateVals[idx] = decodeHashrateSI16(sample.HashrateQ)
		bestVals[idx] = decodeBestShareSI16(sample.BestDifficultyQ)
	}
	hMin, hMax, hQ := quantizeSeriesToUint8(hashrateVals, present)
	bMin, bMax, bQ := quantizeSeriesToUint8(bestVals, present)
	return &compactHashrateSeries{
		I:    int(savedWorkerPeriodBucket / time.Second),
		S:    startMinute,
		N:    count,
		P:    widenUint8ForJSON(presentBits),
		HMin: hMin,
		HMax: hMax,
		HQ:   widenUint8ForJSON(hQ),
		BMin: bMin,
		BMax: bMax,
		BQ:   widenUint8ForJSON(bQ),
	}
}
