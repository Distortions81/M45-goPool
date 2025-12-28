package main

import (
	"fmt"
	"testing"
	"time"

	"github.com/bytedance/sonic"
)

const benchSharesPerWorker = 15.0
const benchBudgetNS = 10 * float64(time.Millisecond)

// approximateMaxMiners converts a total share rate back into how many miners
// are represented if each miner emits sharesPerWorker shares per minute.
func approximateMaxMiners(workers []WorkerView, sharesPerWorker float64) int {
	if sharesPerWorker <= 0 {
		return 0
	}
	var totalShareRate float64
	for _, w := range workers {
		totalShareRate += w.ShareRate
	}
	return int(totalShareRate / sharesPerWorker)
}

func BenchmarkApproximateMinerCapacity(b *testing.B) {
	workerSizes := []int{100, 1000, 10000, 100000, 1000000}
	var baselineNSPerWorker float64
	for _, size := range workerSizes {
		var measuredNSPerWorker float64
		b.Run(fmt.Sprintf("%d_workers", size), func(b *testing.B) {
			workers := make([]WorkerView, size)
			for i := range workers {
				workers[i].ShareRate = benchSharesPerWorker + float64(i%5)
			}
			var expectedTotal float64
			for _, w := range workers {
				expectedTotal += w.ShareRate
			}
			expected := int(expectedTotal / benchSharesPerWorker)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if got := approximateMaxMiners(workers, benchSharesPerWorker); got != expected {
					b.Fatalf("unexpected miner estimate: got %d want %d", got, expected)
				}
			}
			b.StopTimer()

			// Surface a derived metric directly in the benchmark output.
			// Each op processes `size` workers, so ns/worker = (ns/op) / size.
			if size > 0 && b.N > 0 {
				nsPerOp := float64(b.Elapsed().Nanoseconds()) / float64(b.N)
				nsPerWorker := nsPerOp / float64(size)
				measuredNSPerWorker = nsPerWorker
				b.ReportMetric(nsPerWorker, "ns/worker")
				if baselineNSPerWorker > 0 {
					overheadPct := ((nsPerWorker - baselineNSPerWorker) / baselineNSPerWorker) * 100
					b.ReportMetric(overheadPct, "overhead_pct")
				} else {
					// The first benchmark size establishes the baseline.
					b.ReportMetric(0, "overhead_pct")
				}
			}
		})
		if baselineNSPerWorker == 0 && measuredNSPerWorker > 0 {
			baselineNSPerWorker = measuredNSPerWorker
		}
	}
}

func benchmarkStatusServerWithWorkers(workerCount int) *StatusServer {
	now := time.Now()

	workers := make([]WorkerView, workerCount)
	for i := range workers {
		workers[i] = WorkerView{
			Name:         fmt.Sprintf("worker-%d", i),
			DisplayName:  fmt.Sprintf("worker-%d", i),
			WorkerSHA256: fmt.Sprintf("%064x", i),
			ShareRate:    benchSharesPerWorker + float64(i%5),
			Difficulty:   1,
			LastShare:    now,
		}
	}

	// RecentWork is what the overview page returns, so keep it small.
	recentCount := 50
	if workerCount < recentCount {
		recentCount = workerCount
	}
	recent := make([]RecentWorkView, recentCount)
	for i := range recent {
		recent[i] = RecentWorkView{
			Name:            workers[i].Name,
			DisplayName:     workers[i].DisplayName,
			RollingHashrate: 0,
			Difficulty:      1,
			ShareRate:       workers[i].ShareRate,
			Accepted:        0,
			ConnectionID:    "",
		}
	}

	s := &StatusServer{
		jsonCache: make(map[string]cachedJSONResponse),
	}
	s.cachedStatus = StatusData{
		Workers:    workers,
		RecentWork: recent,
	}
	s.lastStatusBuild = time.Now()
	return s
}

func buildOverviewPagePayloadForBench(s *StatusServer) ([]byte, error) {
	view := s.statusDataView()
	start := time.Now()

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
		BTCPriceUSD:     0,
		BTCPriceUpdated: "",
		RenderDuration:  time.Since(start),
		Workers:         recentWork,
		BannedWorkers:   censoredBanned,
		BestShares:      bestShares,
		FoundBlocks:     foundBlocks,
		MinerTypes:      view.MinerTypes,
	}
	return sonic.Marshal(data)
}

func BenchmarkOverviewPagePayload(b *testing.B) {
	workerSizes := []int{100, 1000, 10000, 100000, 1000000}
	for _, size := range workerSizes {
		b.Run(fmt.Sprintf("%d_workers", size), func(b *testing.B) {
			s := benchmarkStatusServerWithWorkers(size)
			recentCount := len(s.statusDataView().RecentWork)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := buildOverviewPagePayloadForBench(s); err != nil {
					b.Fatalf("build overview payload: %v", err)
				}
			}
			b.StopTimer()

			if size > 0 && b.N > 0 {
				nsPerOp := float64(b.Elapsed().Nanoseconds()) / float64(b.N)
				if recentCount > 0 {
					nsPerRecentWorker := nsPerOp / float64(recentCount)
					b.ReportMetric(nsPerRecentWorker, "ns/recent_worker")
				}
			}
		})
	}
}
