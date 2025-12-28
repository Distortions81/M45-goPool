package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
)

const defaultBestShareLimit = 12
const poolErrorHistorySize = 6
const rpcGBTRollingWindowSeconds = 24 * 60 * 60
const shareRateWindowSeconds = 60
const startupErrorIgnoreDuration = 2 * time.Minute

type ErrorEvent struct {
	At      time.Time
	Type    string
	Message string
}

type shareRateBucket struct {
	sec      int64
	accepted uint64
}

type latencyBucket struct {
	sec   int64
	count uint64
	sum   float64
	min   float64
	max   float64
}

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
	start            time.Time

	errorHistory []ErrorEvent

	bestShares     [defaultBestShareLimit]BestShare
	bestShareCount int
	bestSharesMu   sync.RWMutex
	bestSharesFile string
	bestShareChan  chan BestShare

	// Simple RPC latency summaries for diagnostics (seconds).
	rpcGBTLast     float64
	rpcGBTMax      float64
	rpcGBTCount    uint64
	rpcSubmitLast  float64
	rpcSubmitMax   float64
	rpcSubmitCount uint64

	rpcGBTBuckets []latencyBucket

	shareRateBuckets []shareRateBucket
}

func NewPoolMetrics() *PoolMetrics {
	m := &PoolMetrics{
		bestShareChan: make(chan BestShare, 64),
	}
	go m.bestShareWorker()
	return m
}

func (m *PoolMetrics) SetBestSharesFile(path string) {
	if m == nil || path == "" {
		return
	}
	m.bestSharesFile = path
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		logger.Warn("create best shares directory", "error", err, "path", filepath.Dir(path))
	}
	if err := m.loadBestSharesFile(path); err != nil {
		logger.Warn("load best shares file", "error", err, "path", path)
	}
}

func (m *PoolMetrics) loadBestSharesFile(path string) error {
	if m == nil || path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var shares []BestShare
	if err := sonic.Unmarshal(data, &shares); err != nil {
		return err
	}
	m.bestSharesMu.Lock()
	defer m.bestSharesMu.Unlock()
	m.bestShareCount = 0
	for _, share := range shares {
		if share.Difficulty <= 0 {
			continue
		}
		if m.bestShareCount >= defaultBestShareLimit {
			break
		}
		m.bestShares[m.bestShareCount] = share
		m.bestShareCount++
	}
	return nil
}

func (m *PoolMetrics) RecordShare(accepted bool, reason string) {
	if m == nil {
		return
	}
	if accepted {
		m.mu.Lock()
		m.accepted++
		m.observeAcceptedShareLocked(time.Now())
		m.mu.Unlock()
		return
	}
	m.mu.Lock()
	if m.shouldIgnoreStartupRejectLocked(reason) {
		m.mu.Unlock()
		return
	}
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

func (m *PoolMetrics) observeAcceptedShareLocked(now time.Time) {
	if m == nil {
		return
	}
	if m.shareRateBuckets == nil {
		m.shareRateBuckets = make([]shareRateBucket, shareRateWindowSeconds)
	}
	sec := now.Unix()
	idx := int(sec % int64(len(m.shareRateBuckets)))
	b := &m.shareRateBuckets[idx]
	if b.sec != sec {
		b.sec = sec
		b.accepted = 0
	}
	b.accepted++
}

// SnapshotShareRates returns approximate pool-wide accepted share rates over the
// last minute. It is a best-effort view meant for status/UI display only.
func (m *PoolMetrics) SnapshotShareRates(now time.Time) (sharesPerSecond float64, sharesPerMinute float64) {
	if m == nil {
		return 0, 0
	}
	if now.IsZero() {
		now = time.Now()
	}
	cutoff := now.Unix() - (shareRateWindowSeconds - 1)

	m.mu.RLock()
	buckets := m.shareRateBuckets
	m.mu.RUnlock()
	if len(buckets) == 0 {
		return 0, 0
	}

	var (
		total   uint64
		minSec  int64
		seenSec bool
	)
	for i := range buckets {
		b := buckets[i]
		if b.sec < cutoff || b.accepted == 0 {
			continue
		}
		total += b.accepted
		if !seenSec || b.sec < minSec {
			minSec = b.sec
			seenSec = true
		}
	}
	if total == 0 || !seenSec {
		return 0, 0
	}
	spanSeconds := float64(now.Unix() - minSec + 1)
	if spanSeconds <= 0 {
		return 0, 0
	}
	sharesPerSecond = float64(total) / spanSeconds
	sharesPerMinute = sharesPerSecond * 60
	return sharesPerSecond, sharesPerMinute
}

func (m *PoolMetrics) SetStartTime(start time.Time) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.start = start
	m.mu.Unlock()
}

func (m *PoolMetrics) shouldIgnoreStartupRejectLocked(reason string) bool {
	if m.start.IsZero() {
		return false
	}
	if time.Since(m.start) >= startupErrorIgnoreDuration {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "lowdiff", "low difficulty share", "stale job":
		return true
	}
	return false
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
	m.recordErrorEventLocked("share", reason, time.Now())
	m.mu.Unlock()
}

func (m *PoolMetrics) ObserveRPCLatency(method string, longPoll bool, dur time.Duration) {
	if m == nil {
		return
	}
	seconds := dur.Seconds()
	// Track simple summaries for a few key methods for the server dashboard.
	now := time.Now()
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
		m.observeGBTRollingLocked(seconds, now)
	case "submitblock":
		m.rpcSubmitLast = seconds
		if seconds > m.rpcSubmitMax {
			m.rpcSubmitMax = seconds
		}
		m.rpcSubmitCount++
	}
	m.mu.Unlock()
}

func (m *PoolMetrics) RecordRPCError(err error) {
	if m == nil || err == nil {
		return
	}
	m.mu.Lock()
	m.rpcErrorCount++
	m.recordErrorEventLocked("rpc", err.Error(), time.Now())
	m.mu.Unlock()
}

func (m *PoolMetrics) RecordErrorEvent(kind, message string, at time.Time) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.recordErrorEventLocked(kind, message, at)
	m.mu.Unlock()
}

func (m *PoolMetrics) recordErrorEventLocked(kind, message string, at time.Time) {
	if kind == "" {
		kind = "unknown"
	}
	if message == "" {
		message = "unspecified"
	}
	m.errorHistory = append(m.errorHistory, ErrorEvent{
		At:      at,
		Type:    kind,
		Message: message,
	})
	if len(m.errorHistory) > poolErrorHistorySize {
		m.errorHistory = m.errorHistory[len(m.errorHistory)-poolErrorHistorySize:]
	}
}

func (m *PoolMetrics) observeGBTRollingLocked(seconds float64, now time.Time) {
	if m.rpcGBTBuckets == nil {
		m.rpcGBTBuckets = make([]latencyBucket, rpcGBTRollingWindowSeconds)
	}
	sec := now.Unix()
	idx := int(sec % int64(len(m.rpcGBTBuckets)))
	b := &m.rpcGBTBuckets[idx]
	if b.sec != sec {
		b.sec = sec
		b.count = 0
		b.sum = 0
		b.min = 0
		b.max = 0
	}
	b.count++
	b.sum += seconds
	if b.min == 0 || seconds < b.min {
		b.min = seconds
	}
	if seconds > b.max {
		b.max = seconds
	}
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

func (m *PoolMetrics) SnapshotErrorHistory() []ErrorEvent {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.errorHistory) == 0 {
		return nil
	}
	out := make([]ErrorEvent, len(m.errorHistory))
	copy(out, m.errorHistory)
	return out
}

func (m *PoolMetrics) SnapshotGBTRollingStats(now time.Time) (min1h, avg1h, max1h float64) {
	if m == nil {
		return
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.rpcGBTBuckets) == 0 {
		return
	}
	sec := now.Unix()
	min1h, avg1h, max1h = snapshotLatencyWindow(m.rpcGBTBuckets, sec, 60*60)
	return
}

func snapshotLatencyWindow(buckets []latencyBucket, nowSec int64, windowSec int64) (min, avg, max float64) {
	if len(buckets) == 0 || windowSec <= 0 {
		return 0, 0, 0
	}
	var sum float64
	var count uint64
	size := int64(len(buckets))
	for i := int64(0); i < windowSec; i++ {
		sec := nowSec - i
		idx := int(sec % size)
		b := buckets[idx]
		if b.sec != sec || b.count == 0 {
			continue
		}
		count += b.count
		sum += b.sum
		if min == 0 || b.min < min {
			min = b.min
		}
		if b.max > max {
			max = b.max
		}
	}
	if count == 0 {
		return 0, 0, 0
	}
	return min, sum / float64(count), max
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

	if ch := m.bestShareChan; ch != nil {
		select {
		case ch <- share:
		default:
			go m.recordBestShare(share)
		}
		return
	}
	m.recordBestShare(share)
}

func (m *PoolMetrics) bestShareWorker() {
	if m.bestShareChan == nil {
		return
	}
	for share := range m.bestShareChan {
		m.recordBestShare(share)
	}
}

// recordBestShare inserts the provided entry into the sorted best-share list.
func (m *PoolMetrics) recordBestShare(share BestShare) {
	if m == nil {
		return
	}
	if share.Difficulty <= 0 {
		return
	}

	m.bestSharesMu.Lock()
	if m.bestShareCount >= defaultBestShareLimit && share.Difficulty <= m.bestShares[m.bestShareCount-1].Difficulty {
		m.bestSharesMu.Unlock()
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
	} else {
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

	var snapshot []BestShare
	if m.bestSharesFile != "" && m.bestShareCount > 0 {
		snapshot = make([]BestShare, m.bestShareCount)
		copy(snapshot, m.bestShares[:m.bestShareCount])
	}
	m.bestSharesMu.Unlock()

	if len(snapshot) > 0 {
		m.persistBestShares(snapshot)
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

func (m *PoolMetrics) persistBestShares(shares []BestShare) {
	if m == nil || len(shares) == 0 || m.bestSharesFile == "" {
		return
	}
	data, err := sonic.ConfigDefault.MarshalIndent(shares, "", "  ")
	if err != nil {
		logger.Warn("marshal best shares", "error", err)
		return
	}
	tmp := m.bestSharesFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		logger.Warn("write best shares temp file", "error", err, "path", tmp)
		return
	}
	if err := os.Rename(tmp, m.bestSharesFile); err != nil {
		logger.Warn("rename best shares file", "error", err, "tmp", tmp, "target", m.bestSharesFile)
		return
	}
}
