package main

import (
	"fmt"
	"runtime"
	"sync"
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
		BTCPriceFiat:    0,
		BTCPriceUpdated: "",
		FiatCurrency:    defaultFiatCurrency,
		RenderDuration:  time.Since(start),
		Workers:         recentWork,
		BannedWorkers:   censoredBanned,
		BestShares:      bestShares,
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

func benchmarkStatusServerWithConns(connCount int) *StatusServer {
	now := time.Now()
	cfg := defaultConfig()
	reg := NewMinerRegistry()
	metrics := NewPoolMetrics()

	for i := range connCount {
		mc := &MinerConn{
			cfg:         cfg,
			vardiff:     defaultVarDiff,
			metrics:     metrics,
			connectedAt: now.Add(-time.Minute),
		}
		mc.stats = MinerStats{
			Worker:            fmt.Sprintf("worker-%d", i),
			WorkerSHA256:      fmt.Sprintf("%064x", i),
			WindowStart:       now.Add(-30 * time.Second),
			WindowAccepted:    10,
			WindowSubmissions: 12,
			LastShare:         now,
		}
		mc.rollingHashrateValue = float64(1e12 + float64(i%1000))
		mc.connectionSeq = uint64(i + 1)

		metrics.UpdateConnectionHashrate(uint64(i+1), mc.rollingHashrateValue)
		reg.Add(mc)
	}

	s := &StatusServer{
		registry: reg,
		metrics:  metrics,
		start:    now.Add(-time.Hour),
	}
	s.UpdateConfig(cfg)
	return s
}

func BenchmarkBuildStatusData(b *testing.B) {
	connSizes := []int{1000, 10000, 50000, 100000}
	for _, size := range connSizes {
		b.Run(fmt.Sprintf("%d_conns", size), func(b *testing.B) {
			s := benchmarkStatusServerWithConns(size)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = s.buildStatusData()
			}
			b.StopTimer()

			if size > 0 && b.N > 0 {
				nsPerOp := float64(b.Elapsed().Nanoseconds()) / float64(b.N)
				nsPerConn := nsPerOp / float64(size)
				b.ReportMetric(nsPerConn, "ns/conn")
				if nsPerConn > 0 {
					b.ReportMetric(benchBudgetNS/nsPerConn, "conns@10ms")
					b.ReportMetric((5*float64(time.Millisecond))/nsPerConn, "conns@5ms")
					b.ReportMetric((15*float64(time.Millisecond))/nsPerConn, "conns@15ms")
					b.ReportMetric((30*float64(time.Millisecond))/nsPerConn, "conns@30ms")
					b.ReportMetric((60*float64(time.Millisecond))/nsPerConn, "conns@60ms")
				}
			}
		})
	}
}

var (
	memBench500kOnce   sync.Once
	memBench500kServer *StatusServer
	memBench500kDelta  runtime.MemStats
	memBench500kLive   runtime.MemStats
)

func initMemBench500k() {
	const workerCount = 500_000

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	memBench500kServer = benchmarkStatusServerWithConns(workerCount)

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	memBench500kDelta.HeapAlloc = after.HeapAlloc - before.HeapAlloc
	memBench500kDelta.HeapInuse = after.HeapInuse - before.HeapInuse
	memBench500kDelta.HeapObjects = after.HeapObjects - before.HeapObjects
	memBench500kLive = after
}

// BenchmarkEstimateMemory500kWorkers reports a rough estimate of live heap
// memory used to keep 500k worker connections in memory.
//
// Notes:
//   - This benchmark intentionally does most work in a sync.Once setup step so
//     the Go benchmark runner doesn't allocate 500k workers multiple times while
//     calibrating b.N.
//   - Run explicitly (it is expensive): `go test -run '^$' -bench BenchmarkEstimateMemory500kWorkers -count=1`.
func BenchmarkEstimateMemory500kWorkers(b *testing.B) {
	const workerCount = 500_000
	memBench500kOnce.Do(initMemBench500k)

	miB := float64(1024 * 1024)
	kiB := float64(1024)
	b.ReportMetric(float64(memBench500kDelta.HeapAlloc)/miB, "heapAllocMiB")
	b.ReportMetric(float64(memBench500kDelta.HeapInuse)/miB, "heapInuseMiB")
	b.ReportMetric(float64(memBench500kDelta.HeapObjects), "heapObjects")
	b.ReportMetric(float64(memBench500kDelta.HeapAlloc)/float64(workerCount), "B/worker")
	b.ReportMetric(float64(memBench500kDelta.HeapAlloc)/float64(workerCount)/kiB, "KiB/worker")
	b.ReportMetric(float64(memBench500kDelta.HeapInuse)/float64(workerCount), "inuseB/worker")
	b.ReportMetric(float64(memBench500kDelta.HeapInuse)/float64(workerCount)/kiB, "inuseKiB/worker")
	b.ReportMetric(float64(memBench500kLive.HeapAlloc)/miB, "liveHeapAllocMiB")

	b.Logf("Estimated 500k workers memory: heapAlloc=%s heapInuse=%s objects=%d (alloc≈%s/worker inuse≈%s/worker)",
		formatBytesIEC(memBench500kDelta.HeapAlloc),
		formatBytesIEC(memBench500kDelta.HeapInuse),
		memBench500kDelta.HeapObjects,
		formatBytesIEC(uint64(float64(memBench500kDelta.HeapAlloc)/float64(workerCount))),
		formatBytesIEC(uint64(float64(memBench500kDelta.HeapInuse)/float64(workerCount))),
	)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		runtime.KeepAlive(memBench500kServer)
	}
}

func formatBytesIEC(n uint64) string {
	const (
		KiB = 1024
		MiB = 1024 * KiB
		GiB = 1024 * MiB
		TiB = 1024 * GiB
	)
	switch {
	case n >= TiB:
		return fmt.Sprintf("%.2f TiB", float64(n)/float64(TiB))
	case n >= GiB:
		return fmt.Sprintf("%.2f GiB", float64(n)/float64(GiB))
	case n >= MiB:
		return fmt.Sprintf("%.2f MiB", float64(n)/float64(MiB))
	case n >= KiB:
		return fmt.Sprintf("%.2f KiB", float64(n)/float64(KiB))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
