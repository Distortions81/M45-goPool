package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func BenchmarkSavedWorkersJSON_15Saved(b *testing.B) {
	tmp := b.TempDir()
	store, err := newWorkerListStore(tmp + "/saved_workers.sqlite")
	if err != nil {
		b.Fatalf("newWorkerListStore: %v", err)
	}
	b.Cleanup(func() { _ = store.Close() })

	const (
		userID           = "bench-user"
		savedPerUser     = 15
		refreshIntervalS = 5.0
	)

	reg := newWorkerConnectionRegistry()
	now := time.Now()
	cfg := defaultConfig()

	// Simulate 10/15 saved workers online with one connection each.
	for i := 0; i < savedPerUser; i++ {
		name := fmt.Sprintf("worker-%d", i)
		if err := store.Add(userID, name); err != nil {
			b.Fatalf("store.Add(%q): %v", name, err)
		}
		if i >= 10 {
			continue
		}
		mc := &MinerConn{
			cfg:         cfg,
			metrics:     nil,
			vardiff:     defaultVarDiff,
			connectedAt: now.Add(-time.Minute),
		}
		mc.stats = MinerStats{
			Worker:            name,
			WorkerSHA256:      workerNameHash(name),
			LastShare:         now,
			WindowStart:       now.Add(-30 * time.Second),
			WindowAccepted:    10,
			WindowSubmissions: 10,
		}
		mc.connectionSeq = uint64(i + 1)
		wallet := workerBaseAddress(name)
		walletHash := workerNameHash(wallet)
		reg.register(mc.stats.WorkerSHA256, walletHash, mc)
	}

	s := &StatusServer{
		workerLists:    store,
		workerRegistry: reg,
	}

	req := httptest.NewRequest(http.MethodGet, "/api/saved-workers", nil)
	req = req.WithContext(contextWithClerkUser(req.Context(), &ClerkUser{UserID: userID, SessionID: "bench"}))

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rr := httptest.NewRecorder()
		s.handleSavedWorkersJSON(rr, req)
		if rr.Code != http.StatusOK {
			b.Fatalf("unexpected status: %d", rr.Code)
		}
	}
	b.StopTimer()

	if b.N > 0 {
		nsPerReq := float64(b.Elapsed().Nanoseconds()) / float64(b.N)
		if nsPerReq > 0 {
			reqPerSecond := 1e9 / nsPerReq
			b.ReportMetric(reqPerSecond, "req/s")
			b.ReportMetric(reqPerSecond*refreshIntervalS, "viewers@5s")
			b.ReportMetric(reqPerSecond*refreshIntervalS*0.7, "viewers@5s_70pct")
		}
	}
}
