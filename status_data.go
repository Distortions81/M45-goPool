package main

import (
	"runtime"
	"sort"
	"strings"
	"time"
)

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
	var rpcGBTMin1h, rpcGBTAvg1h, rpcGBTMax1h float64
	var errorHistory []PoolErrorEvent
	now := time.Now()
	if s.metrics != nil {
		accepted, rejected, reasons = s.metrics.Snapshot()
		s.logShareTotals(accepted, rejected)
		vardiffUp, vardiffDown, blocksAccepted, blocksErrored,
			rpcGBTLast, rpcGBTMax, rpcGBTCount,
			rpcSubmitLast, rpcSubmitMax, rpcSubmitCount,
			rpcErrors, shareErrors = s.metrics.SnapshotDiagnostics()
		rpcGBTMin1h, rpcGBTAvg1h, rpcGBTMax1h = s.metrics.SnapshotGBTRollingStats(now)
		rawErrors := s.metrics.SnapshotErrorHistory()
		if filtered := filterRecentPoolErrorEvents(rawErrors, now, poolErrorHistoryDisplayWindow); len(filtered) > 0 {
			errorHistory = filtered
		}
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

	snapshotTime := time.Now()
	var workers []WorkerView
	var bannedWorkers []WorkerView
	var bestShares []BestShare
	if s.metrics != nil {
		bestShares = s.metrics.SnapshotBestShares()
	}
	var allWorkers []WorkerView
	allWorkers = s.snapshotWorkerViews(snapshotTime)
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

	foundBlocks := loadFoundBlocks(s.Config().DataDir, 10)

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
	if s.metrics != nil {
		sharesPerSecond, sharesPerMinute = s.metrics.SnapshotShareRates(now)
	} else {
		for _, w := range allWorkers {
			if w.ShareRate > 0 {
				sharesPerMinute += w.ShareRate
			}
		}
		sharesPerSecond = sharesPerMinute / 60
	}
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

	recentWork := make([]RecentWorkView, 0, len(workers))
	for _, w := range workers {
		recentWork = append(recentWork, RecentWorkView{
			Name:            w.Name,
			DisplayName:     w.DisplayName,
			RollingHashrate: w.RollingHashrate,
			Difficulty:      w.Difficulty,
			Vardiff:         w.Vardiff,
			ShareRate:       w.ShareRate,
			Accepted:        w.Accepted,
			ConnectionID:    w.ConnectionID,
		})
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
	var nodePeers []cachedPeerInfo
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
		if len(info.peerInfos) > 0 {
			nodePeers = append([]cachedPeerInfo(nil), info.peerInfos...)
		}
		genesisHash = info.genesisHash
		bestHash = info.bestHash
	}
	if len(foundBlocks) > 0 && nodeBlocks > 0 {
		for i := range foundBlocks {
			height := foundBlocks[i].Height
			if height <= 0 {
				continue
			}
			confirms := nodeBlocks - height + 1
			if confirms < 1 {
				confirms = 0
			}
			foundBlocks[i].Confirmations = confirms
		}
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
		if price, err := s.priceSvc.BTCPrice(s.Config().FiatCurrency); err == nil && price > 0 {
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

	brandName := strings.TrimSpace(s.Config().StatusBrandName)
	if brandName == "" {
		brandName = "Solo Pool"
	}
	brandDomain := strings.TrimSpace(s.Config().StatusBrandDomain)

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

	displayPayout := shortDisplayID(s.Config().PayoutAddress, payoutAddrPrefix, payoutAddrSuffix)
	displayDonation := shortDisplayID(s.Config().OperatorDonationAddress, payoutAddrPrefix, payoutAddrSuffix)
	displayCoinbase := shortDisplayID(s.Config().CoinbaseMsg, coinbaseMsgPrefix, coinbaseMsgSuffix)

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
	if s.Config().PoolFeePercent > 10 {
		warnings = append(warnings, "Pool fee is configured above 10%. Verify this is intentional and clearly disclosed to miners.")
	}
	if strings.EqualFold(nodeNetwork, "mainnet") && s.Config().MinDifficulty < 1 {
		warnings = append(warnings, "Minimum difficulty is configured below 1 on mainnet. This can flood the pool and node with tiny shares; verify you really need CPU-style difficulties on mainnet.")
	}
	if nodeNetwork != "" && !strings.EqualFold(nodeNetwork, "mainnet") {
		warnings = append(warnings, "Pool is connected to a non-mainnet Bitcoin network ("+nodeNetwork+"). Verify you intend to mine on this network.")
	}
	if expectedGenesis != "" && genesisHash != "" && !genesisMatch {
		warnings = append(warnings, "Connected node's genesis hash does not match the expected Bitcoin genesis for network "+nodeNetwork+". Verify the node is on the genuine Bitcoin chain, not a fork or alt network.")
	}
	if s.Config().MaxAcceptsPerSecond == 0 && s.Config().MaxConns == 0 {
		warnings = append(warnings, "No connection rate limit and no max connection cap are configured. This can make the pool vulnerable to connection floods or accidental overload.")
	}

	workerLookup := buildWorkerLookupByHash(allWorkers, bannedWorkers)
	return StatusData{
		ListenAddr:                     s.Config().ListenAddr,
		StratumTLSListen:               s.Config().StratumTLSListen,
		BrandName:                      brandName,
		BrandDomain:                    brandDomain,
		Tagline:                        s.Config().StatusTagline,
		ConnectMinerTitleExtra:         strings.TrimSpace(s.Config().StatusConnectMinerTitleExtra),
		ConnectMinerTitleExtraURL:      strings.TrimSpace(s.Config().StatusConnectMinerTitleExtraURL),
		FiatCurrency:                   s.Config().FiatCurrency,
		BTCPriceFiat:                   btcPrice,
		BTCPriceUpdatedAt:              btcPriceUpdated,
		PoolDonationAddress:            s.Config().PoolDonationAddress,
		DiscordURL:                     s.Config().DiscordURL,
		GitHubURL:                      s.Config().GitHubURL,
		NodeNetwork:                    nodeNetwork,
		NodeSubversion:                 nodeSubversion,
		NodeBlocks:                     nodeBlocks,
		NodeHeaders:                    nodeHeaders,
		NodeInitialBlockDownload:       nodeIBD,
		NodeRPCURL:                     s.Config().RPCURL,
		NodeZMQAddr:                    s.Config().ZMQBlockAddr,
		PayoutAddress:                  s.Config().PayoutAddress,
		PoolFeePercent:                 s.Config().PoolFeePercent,
		OperatorDonationPercent:        s.Config().OperatorDonationPercent,
		OperatorDonationAddress:        s.Config().OperatorDonationAddress,
		OperatorDonationName:           s.Config().OperatorDonationName,
		OperatorDonationURL:            s.Config().OperatorDonationURL,
		CoinbaseMessage:                s.Config().CoinbaseMsg,
		CoinbasePoolTag:                s.Config().CoinbasePoolTag,
		DisplayPayoutAddress:           displayPayout,
		DisplayOperatorDonationAddress: displayDonation,
		DisplayCoinbaseMessage:         displayCoinbase,
		NodeConnections:                nodeConns,
		NodeConnectionsIn:              nodeConnsIn,
		NodeConnectionsOut:             nodeConnsOut,
		NodePeerInfos:                  buildNodePeerInfos(nodePeers),
		NodePruned:                     nodePruned,
		NodeSizeOnDiskBytes:            nodeSizeOnDisk,
		NodePeerCleanupEnabled:         s.Config().PeerCleanupEnabled,
		NodePeerCleanupMaxPingMs:       s.Config().PeerCleanupMaxPingMs,
		NodePeerCleanupMinPeers:        s.Config().PeerCleanupMinPeers,
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
		RecentWork:                     recentWork,
		WindowAccepted:                 windowAccepted,
		WindowSubmissions:              windowSubmissions,
		WindowStart:                    windowStartStr,
		RPCError:                       rpcErr,
		AccountingError:                acctErr,
		JobFeed:                        jobFeed,
		BestShares:                     bestShares,
		FoundBlocks:                    foundBlocks,
		MinerTypes:                     minerTypes,
		WorkerLookup:                   workerLookup,
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
		RPCGBTMin1hSec:                 rpcGBTMin1h,
		RPCGBTAvg1hSec:                 rpcGBTAvg1h,
		RPCGBTMax1hSec:                 rpcGBTMax1h,
		ErrorHistory:                   errorHistory,
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
		MaxConns:                       s.Config().MaxConns,
		MaxAcceptsPerSecond:            s.Config().MaxAcceptsPerSecond,
		MaxAcceptBurst:                 s.Config().MaxAcceptBurst,
		MinDifficulty:                  s.Config().MinDifficulty,
		MaxDifficulty:                  s.Config().MaxDifficulty,
		LockSuggestedDifficulty:        s.Config().LockSuggestedDifficulty,
		Warnings:                       warnings,
	}
}

// baseTemplateData constructs a lightweight StatusData view for HTML templates
// that only need static configuration/branding fields. It intentionally avoids
// hitting the expensive statusData/metrics paths so that HTML pages rely on
// cached JSON endpoints for dynamic data.
func (s *StatusServer) baseTemplateData(start time.Time) StatusData {
	brandName := strings.TrimSpace(s.Config().StatusBrandName)
	if brandName == "" {
		brandName = "Solo Pool"
	}
	brandDomain := strings.TrimSpace(s.Config().StatusBrandDomain)

	bt := strings.TrimSpace(buildTime)
	if bt == "" {
		bt = "(dev build)"
	}

	displayPayout := shortDisplayID(s.Config().PayoutAddress, payoutAddrPrefix, payoutAddrSuffix)
	displayDonation := shortDisplayID(s.Config().OperatorDonationAddress, payoutAddrPrefix, payoutAddrSuffix)
	displayCoinbase := shortDisplayID(s.Config().CoinbaseMsg, coinbaseMsgPrefix, coinbaseMsgSuffix)
	discordNotificationsEnabled := strings.TrimSpace(s.Config().DiscordServerID) != "" &&
		strings.TrimSpace(s.Config().DiscordBotToken) != "" &&
		strings.TrimSpace(s.Config().DiscordNotifyChannelID) != ""

	clerkPK := strings.TrimSpace(s.Config().ClerkPublishableKey)
	clerkJSURL := ""
	if clerkPK != "" {
		clerkJSHost := strings.TrimRight(strings.TrimSpace(s.Config().ClerkFrontendAPIURL), "/")
		if clerkJSHost == "" {
			clerkJSHost = strings.TrimRight(strings.TrimSpace(s.Config().ClerkIssuerURL), "/")
		}
		if clerkJSHost == "" {
			clerkJSHost = "https://clerk.clerk.com"
		}
		clerkJSURL = clerkJSHost + "/npm/@clerk/clerk-js@5/dist/clerk.browser.js"
	}

	var warnings []string
	if s.Config().PoolFeePercent > 10 {
		warnings = append(warnings, "Pool fee is configured above 10%. Verify this is intentional and clearly disclosed to miners.")
	}
	if s.Config().MaxAcceptsPerSecond == 0 && s.Config().MaxConns == 0 {
		warnings = append(warnings, "No connection rate limit and no max connection cap are configured. This can make the pool vulnerable to connection floods or accidental overload.")
	}

	return StatusData{
		ListenAddr:                     s.Config().ListenAddr,
		StratumTLSListen:               s.Config().StratumTLSListen,
		BrandName:                      brandName,
		BrandDomain:                    brandDomain,
		ClerkPublishableKey:            clerkPK,
		ClerkJSURL:                     clerkJSURL,
		Tagline:                        s.Config().StatusTagline,
		ConnectMinerTitleExtra:         strings.TrimSpace(s.Config().StatusConnectMinerTitleExtra),
		ConnectMinerTitleExtraURL:      strings.TrimSpace(s.Config().StatusConnectMinerTitleExtraURL),
		ServerLocation:                 s.Config().ServerLocation,
		FiatCurrency:                   s.Config().FiatCurrency,
		PoolDonationAddress:            s.Config().PoolDonationAddress,
		DiscordURL:                     s.Config().DiscordURL,
		DiscordNotificationsEnabled:    discordNotificationsEnabled,
		GitHubURL:                      s.Config().GitHubURL,
		NodeRPCURL:                     s.Config().RPCURL,
		NodeZMQAddr:                    s.Config().ZMQBlockAddr,
		PayoutAddress:                  s.Config().PayoutAddress,
		PoolFeePercent:                 s.Config().PoolFeePercent,
		OperatorDonationPercent:        s.Config().OperatorDonationPercent,
		OperatorDonationAddress:        s.Config().OperatorDonationAddress,
		OperatorDonationName:           s.Config().OperatorDonationName,
		OperatorDonationURL:            s.Config().OperatorDonationURL,
		CoinbaseMessage:                s.Config().CoinbaseMsg,
		CoinbasePoolTag:                s.Config().CoinbasePoolTag,
		DisplayPayoutAddress:           displayPayout,
		DisplayOperatorDonationAddress: displayDonation,
		DisplayCoinbaseMessage:         displayCoinbase,
		PoolSoftware:                   poolSoftwareName,
		BuildTime:                      bt,
		MaxConns:                       s.Config().MaxConns,
		MaxAcceptsPerSecond:            s.Config().MaxAcceptsPerSecond,
		MaxAcceptBurst:                 s.Config().MaxAcceptBurst,
		MinDifficulty:                  s.Config().MinDifficulty,
		MaxDifficulty:                  s.Config().MaxDifficulty,
		LockSuggestedDifficulty:        s.Config().LockSuggestedDifficulty,
		HashrateEMATauSeconds:          s.Config().HashrateEMATauSeconds,
		HashrateEMAMinShares:           s.Config().HashrateEMAMinShares,
		NTimeForwardSlackSec:           s.Config().NTimeForwardSlackSeconds,
		RenderDuration:                 time.Since(start),
		Warnings:                       warnings,
		NodePeerCleanupEnabled:         s.Config().PeerCleanupEnabled,
		NodePeerCleanupMaxPingMs:       s.Config().PeerCleanupMaxPingMs,
		NodePeerCleanupMinPeers:        s.Config().PeerCleanupMinPeers,
	}
}
