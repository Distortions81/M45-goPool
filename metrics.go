package main

import (
	"sort"
	"strings"
	"sync"
	"time"
)

const defaultBestShareLimit = 12

type PoolMetrics struct {
	accepted uint64
	rejected uint64

	mu               sync.RWMutex
	rejectReasons    map[string]uint64
	vardiffUp        uint64
	vardiffDown      uint64
	blockSubAccepted uint64
	blockSubErrored  uint64
	rpcErrorCount    uint64
	shareErrorCount  uint64

	bestShares     [defaultBestShareLimit]BestShare
	bestShareCount int
	bestSharesMu   sync.RWMutex

	// Simple RPC latency summaries for diagnostics (seconds).
	rpcGBTLast     float64
	rpcGBTMax      float64
	rpcGBTCount    uint64
	rpcSubmitLast  float64
	rpcSubmitMax   float64
	rpcSubmitCount uint64
}

func NewPoolMetrics() *PoolMetrics {
	return &PoolMetrics{}
}

func (m *PoolMetrics) RecordShare(accepted bool, reason string) {
	if m == nil {
		return
	}
	if accepted {
		m.mu.Lock()
		m.accepted++
		m.mu.Unlock()
		return
	}
	m.mu.Lock()
	m.rejected++
	if m.rejectReasons == nil {
		m.rejectReasons = make(map[string]uint64)
	}
	if reason == "" {
		reason = "unspecified"
	}
	m.rejectReasons[reason]++
	m.mu.Unlock()

	m.RecordSubmitError(reason)
}

func (m *PoolMetrics) RecordSubmitError(reason string) {
	if m == nil {
		return
	}
	// We still normalize the label so that in-memory statistics remain
	// consistent even without Prometheus.
	_ = sanitizeLabel(reason, "unspecified")
	m.mu.Lock()
	m.shareErrorCount++
	m.mu.Unlock()
}

func (m *PoolMetrics) ObserveRPCLatency(method string, longPoll bool, dur time.Duration) {
	if m == nil {
		return
	}
	seconds := dur.Seconds()
	// Track simple summaries for a few key methods for the server dashboard.
	m.mu.Lock()
	switch method {
	case "getblocktemplate":
		if longPoll {
			m.mu.Unlock()
			return
		}
		m.rpcGBTLast = seconds
		if seconds > m.rpcGBTMax {
			m.rpcGBTMax = seconds
		}
		m.rpcGBTCount++
	case "submitblock":
		m.rpcSubmitLast = seconds
		if seconds > m.rpcSubmitMax {
			m.rpcSubmitMax = seconds
		}
		m.rpcSubmitCount++
	}
	m.mu.Unlock()
}

func (m *PoolMetrics) RecordVardiffMove(direction string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	direction = sanitizeLabel(direction, "unknown")
	switch direction {
	case "up":
		m.vardiffUp++
	case "down":
		m.vardiffDown++
	}
	m.mu.Unlock()
}

func (m *PoolMetrics) RecordBlockSubmission(result string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	result = sanitizeLabel(result, "unknown")
	switch result {
	case "accepted":
		m.blockSubAccepted++
	case "error":
		m.blockSubErrored++
	}
	m.mu.Unlock()
}

func (m *PoolMetrics) Snapshot() (uint64, uint64, map[string]uint64) {
	if m == nil {
		return 0, 0, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	reasons := make(map[string]uint64, len(m.rejectReasons))
	for k, v := range m.rejectReasons {
		reasons[k] = v
	}
	return m.accepted, m.rejected, reasons
}

// SnapshotDiagnostics returns a compact set of metrics for the server dashboard:
// vardiff adjustment counts, block submission results, simple RPC latency
// summaries for getblocktemplate and submitblock, and aggregate error counts.
func (m *PoolMetrics) SnapshotDiagnostics() (vardiffUp, vardiffDown, blocksAccepted, blocksErrored uint64, gbtLast, gbtMax float64, gbtCount uint64, submitLast, submitMax float64, submitCount uint64, rpcErrors, shareErrors uint64) {
	if m == nil {
		return
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.vardiffUp, m.vardiffDown, m.blockSubAccepted, m.blockSubErrored,
		m.rpcGBTLast, m.rpcGBTMax, m.rpcGBTCount,
		m.rpcSubmitLast, m.rpcSubmitMax, m.rpcSubmitCount,
		m.rpcErrorCount, m.shareErrorCount
}

// SnapshotBestShares returns the best-share list sorted by descending difficulty.
func (m *PoolMetrics) SnapshotBestShares() []BestShare {
	if m == nil {
		return nil
	}
	m.bestSharesMu.RLock()
	defer m.bestSharesMu.RUnlock()
	if m.bestShareCount == 0 {
		return nil
	}
	out := make([]BestShare, m.bestShareCount)
	copy(out, m.bestShares[:m.bestShareCount])
	return out
}

// TrackBestShare normalizes a share entry and records it if it ranks in the top N.
func (m *PoolMetrics) TrackBestShare(worker, hash string, difficulty float64, timestamp time.Time) {
	if m == nil {
		return
	}
	if difficulty <= 0 {
		return
	}
	share := BestShare{
		Worker:        worker,
		DisplayWorker: shortWorkerName(worker, workerNamePrefix, workerNameSuffix),
		Difficulty:    difficulty,
		Timestamp:     timestamp,
		Hash:          hash,
		DisplayHash:   shortDisplayID(hash, hashPrefix, hashSuffix),
	}
	m.bestSharesMu.RLock()
	count := m.bestShareCount
	var worst float64
	if count >= defaultBestShareLimit {
		worst = m.bestShares[count-1].Difficulty
	}
	m.bestSharesMu.RUnlock()

	if count >= defaultBestShareLimit && share.Difficulty <= worst {
		return
	}

	go m.RecordBestShare(share)
}

// RecordBestShare inserts the provided entry into the sorted best-share list.
func (m *PoolMetrics) RecordBestShare(share BestShare) {
	if m == nil {
		return
	}
	if share.Difficulty <= 0 {
		return
	}

	m.bestSharesMu.RLock()
	if m.bestShareCount >= defaultBestShareLimit && share.Difficulty <= m.bestShares[m.bestShareCount-1].Difficulty {
		m.bestSharesMu.RUnlock()
		return
	}
	m.bestSharesMu.RUnlock()

	m.bestSharesMu.Lock()
	defer m.bestSharesMu.Unlock()
	if m.bestShareCount >= defaultBestShareLimit && share.Difficulty <= m.bestShares[m.bestShareCount-1].Difficulty {
		return
	}

	idx := sort.Search(m.bestShareCount, func(i int) bool {
		return share.Difficulty >= m.bestShares[i].Difficulty
	})
	if idx == m.bestShareCount {
		if m.bestShareCount < defaultBestShareLimit {
			m.bestShares[idx] = share
			m.bestShareCount++
		}
		return
	}

	end := m.bestShareCount
	if end >= defaultBestShareLimit {
		end = defaultBestShareLimit - 1
	}
	for i := end; i > idx; i-- {
		m.bestShares[i] = m.bestShares[i-1]
	}
	m.bestShares[idx] = share
	if m.bestShareCount < defaultBestShareLimit {
		m.bestShareCount++
	}
}

func sanitizeLabel(val, fallback string) string {
	if val == "" {
		return fallback
	}
	val = strings.ToLower(val)
	val = strings.ReplaceAll(val, " ", "_")
	return val
}
