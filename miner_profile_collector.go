package main

import (
	"context"
	stdjson "encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var activeMinerProfiler atomic.Pointer[minerProfileCollector]

func setMinerProfileCollector(c *minerProfileCollector) {
	activeMinerProfiler.Store(c)
}

func getMinerProfileCollector() *minerProfileCollector {
	return activeMinerProfiler.Load()
}

type profileNumericSummary struct {
	Min   float64 `json:"min,omitempty"`
	Max   float64 `json:"max,omitempty"`
	Avg   float64 `json:"avg,omitempty"`
	Count int64   `json:"count,omitempty"`
	sum   float64
}

func (s *profileNumericSummary) add(v float64) {
	if !isFinitePositive(v) {
		return
	}
	if s.Count == 0 {
		s.Min = v
		s.Max = v
	} else {
		if v < s.Min {
			s.Min = v
		}
		if v > s.Max {
			s.Max = v
		}
	}
	s.Count++
	s.sum += v
	s.Avg = s.sum / float64(s.Count)
}

type minerProfileAggregate struct {
	MinerType                 string `json:"miner_type,omitempty"`
	ClientName                string `json:"client_name,omitempty"`
	ClientVersion             string `json:"client_version,omitempty"`
	Sessions                  int64  `json:"sessions"`
	WorkersSample             []string
	WorkerHashesSample        []string
	StartingDifficulty        profileNumericSummary `json:"starting_difficulty"`
	IdealDifficultySettled    profileNumericSummary `json:"ideal_difficulty_settled"`
	CurrentDifficultySettled  profileNumericSummary `json:"current_difficulty_settled"`
	HashrateSettledHS         profileNumericSummary `json:"hashrate_settled_hs"`
	SharesPerMinSettled       profileNumericSummary `json:"shares_per_min_settled"`
	WorkStartP50MSSettled     profileNumericSummary `json:"work_start_p50_ms_settled"`
	MinerResponseP50MSSettled profileNumericSummary `json:"miner_response_p50_ms_settled"`
	LastUpdatedAt             time.Time
	workerSet                 map[string]struct{}
	workerHashSet             map[string]struct{}
}

type minerProfileSession struct {
	ProfileKey        string
	Counted           bool
	StartDiffRecorded bool
}

type minerProfileCollector struct {
	path        string
	mu          sync.Mutex
	aggregates  map[string]*minerProfileAggregate
	sessions    map[string]*minerProfileSession
	lastFlush   time.Time
	initialized bool
	dirty       bool
}

func newMinerProfileCollector(path string) *minerProfileCollector {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	return &minerProfileCollector{
		path:       path,
		aggregates: make(map[string]*minerProfileAggregate, 16),
		sessions:   make(map[string]*minerProfileSession, 64),
	}
}

func (c *minerProfileCollector) Run(ctx context.Context) {
	if c == nil {
		return
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = c.Flush()
		}
	}
}

func (c *minerProfileCollector) Flush() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.dirty && c.initialized {
		return nil
	}
	payload := c.buildPayloadLocked()
	out, err := stdjson.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, c.path); err != nil {
		return err
	}
	c.lastFlush = time.Now()
	c.initialized = true
	c.dirty = false
	return nil
}

func (c *minerProfileCollector) ObserveAuthorize(mc *MinerConn, worker string) {
	if c == nil || mc == nil {
		return
	}
	now := time.Now()
	startDiff := mc.currentDifficulty()
	minerType, clientName, clientVersion := mc.minerClientInfo()
	workerHash := workerNameHash(worker)
	sessionID := profileSessionID(mc)
	if sessionID == "" {
		return
	}
	profileKey := profileIdentityKey(minerType, clientName, clientVersion)

	c.mu.Lock()
	defer c.mu.Unlock()
	agg := c.ensureAggregateLocked(profileKey, minerType, clientName, clientVersion)
	if worker != "" {
		c.addWorkerLocked(agg, worker)
	}
	if workerHash != "" {
		c.addWorkerHashLocked(agg, workerHash)
	}
	s := c.ensureSessionLocked(sessionID, profileKey)
	if !s.Counted {
		agg.Sessions++
		s.Counted = true
	}
	if !s.StartDiffRecorded {
		agg.StartingDifficulty.add(startDiff)
		s.StartDiffRecorded = true
	}
	agg.LastUpdatedAt = now
	c.dirty = true
}

func (c *minerProfileCollector) ObserveVardiff(mc *MinerConn, now time.Time, snap minerShareSnapshot, currentDiff, idealDiff float64) {
	if c == nil || mc == nil {
		return
	}
	minerType, clientName, clientVersion := mc.minerClientInfo()
	worker := strings.TrimSpace(snap.Stats.Worker)
	if worker == "" {
		worker = strings.TrimSpace(mc.currentWorker())
	}
	workerHash := strings.TrimSpace(snap.Stats.WorkerSHA256)
	if workerHash == "" && worker != "" {
		workerHash = workerNameHash(worker)
	}
	sessionID := profileSessionID(mc)
	if sessionID == "" {
		return
	}
	profileKey := profileIdentityKey(minerType, clientName, clientVersion)

	modeledRate := modeledShareRatePerMinute(snap.RollingHashrate, currentDiff)
	confLevel := hashrateConfidenceLevel(snap.Stats, now, modeledRate, snap.RollingHashrate, mc.connectedAt)
	settled := confLevel >= 2
	workStartMS := snap.NotifyToFirstShareP50MS
	if workStartMS <= 0 {
		workStartMS = snap.NotifyToFirstShareMS
	}
	minerResponseMS := snap.PingRTTP50MS
	if minerResponseMS <= 0 {
		minerResponseMS = snap.PingRTTP95MS
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	agg := c.ensureAggregateLocked(profileKey, minerType, clientName, clientVersion)
	if worker != "" {
		c.addWorkerLocked(agg, worker)
	}
	if workerHash != "" {
		c.addWorkerHashLocked(agg, workerHash)
	}
	s := c.ensureSessionLocked(sessionID, profileKey)
	if !s.Counted {
		agg.Sessions++
		s.Counted = true
	}
	if !s.StartDiffRecorded {
		agg.StartingDifficulty.add(currentDiff)
		s.StartDiffRecorded = true
	}

	if settled {
		agg.HashrateSettledHS.add(snap.RollingHashrate)
		agg.SharesPerMinSettled.add(modeledRate)
		agg.IdealDifficultySettled.add(idealDiff)
		agg.CurrentDifficultySettled.add(currentDiff)
		agg.WorkStartP50MSSettled.add(workStartMS)
		agg.MinerResponseP50MSSettled.add(minerResponseMS)
	}
	agg.LastUpdatedAt = now
	c.dirty = true
}

func (c *minerProfileCollector) ensureAggregateLocked(key, minerType, clientName, clientVersion string) *minerProfileAggregate {
	if agg := c.aggregates[key]; agg != nil {
		return agg
	}
	agg := &minerProfileAggregate{
		MinerType:          minerType,
		ClientName:         clientName,
		ClientVersion:      clientVersion,
		workerSet:          make(map[string]struct{}, 8),
		workerHashSet:      make(map[string]struct{}, 8),
		WorkersSample:      make([]string, 0, 8),
		WorkerHashesSample: make([]string, 0, 8),
	}
	c.aggregates[key] = agg
	return agg
}

func (c *minerProfileCollector) ensureSessionLocked(sessionID, profileKey string) *minerProfileSession {
	if s := c.sessions[sessionID]; s != nil {
		return s
	}
	s := &minerProfileSession{ProfileKey: profileKey}
	c.sessions[sessionID] = s
	return s
}

func (c *minerProfileCollector) addWorkerLocked(agg *minerProfileAggregate, worker string) {
	worker = strings.TrimSpace(worker)
	if worker == "" {
		return
	}
	if _, ok := agg.workerSet[worker]; ok {
		return
	}
	agg.workerSet[worker] = struct{}{}
	if len(agg.WorkersSample) < 8 {
		agg.WorkersSample = append(agg.WorkersSample, worker)
	}
}

func (c *minerProfileCollector) addWorkerHashLocked(agg *minerProfileAggregate, workerHash string) {
	workerHash = strings.ToLower(strings.TrimSpace(workerHash))
	if workerHash == "" {
		return
	}
	if _, ok := agg.workerHashSet[workerHash]; ok {
		return
	}
	agg.workerHashSet[workerHash] = struct{}{}
	if len(agg.WorkerHashesSample) < 8 {
		agg.WorkerHashesSample = append(agg.WorkerHashesSample, workerHash)
	}
}

type minerProfilePayload struct {
	GeneratedAt string               `json:"generated_at"`
	Profiles    []minerProfileExport `json:"profiles"`
}

type minerProfileExport struct {
	MinerType                 string                `json:"miner_type,omitempty"`
	ClientName                string                `json:"client_name,omitempty"`
	ClientVersion             string                `json:"client_version,omitempty"`
	Sessions                  int64                 `json:"sessions"`
	WorkersSample             []string              `json:"workers_sample,omitempty"`
	WorkerHashesSample        []string              `json:"worker_hashes_sample,omitempty"`
	StartingDifficulty        profileNumericSummary `json:"starting_difficulty"`
	IdealDifficultySettled    profileNumericSummary `json:"ideal_difficulty_settled"`
	CurrentDifficultySettled  profileNumericSummary `json:"current_difficulty_settled"`
	HashrateSettledHS         profileNumericSummary `json:"hashrate_settled_hs"`
	SharesPerMinSettled       profileNumericSummary `json:"shares_per_min_settled"`
	WorkStartP50MSSettled     profileNumericSummary `json:"work_start_p50_ms_settled"`
	MinerResponseP50MSSettled profileNumericSummary `json:"miner_response_p50_ms_settled"`
	LastUpdatedAt             string                `json:"last_updated_at,omitempty"`
}

func (c *minerProfileCollector) buildPayloadLocked() minerProfilePayload {
	rows := make([]minerProfileExport, 0, len(c.aggregates))
	for _, agg := range c.aggregates {
		row := minerProfileExport{
			MinerType:                 agg.MinerType,
			ClientName:                agg.ClientName,
			ClientVersion:             agg.ClientVersion,
			Sessions:                  agg.Sessions,
			WorkersSample:             append([]string(nil), agg.WorkersSample...),
			WorkerHashesSample:        append([]string(nil), agg.WorkerHashesSample...),
			StartingDifficulty:        agg.StartingDifficulty,
			IdealDifficultySettled:    agg.IdealDifficultySettled,
			CurrentDifficultySettled:  agg.CurrentDifficultySettled,
			HashrateSettledHS:         agg.HashrateSettledHS,
			SharesPerMinSettled:       agg.SharesPerMinSettled,
			WorkStartP50MSSettled:     agg.WorkStartP50MSSettled,
			MinerResponseP50MSSettled: agg.MinerResponseP50MSSettled,
		}
		if !agg.LastUpdatedAt.IsZero() {
			row.LastUpdatedAt = agg.LastUpdatedAt.UTC().Format(time.RFC3339)
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Sessions != rows[j].Sessions {
			return rows[i].Sessions > rows[j].Sessions
		}
		if rows[i].MinerType != rows[j].MinerType {
			return rows[i].MinerType < rows[j].MinerType
		}
		return rows[i].ClientName < rows[j].ClientName
	})
	return minerProfilePayload{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Profiles:    rows,
	}
}

func profileIdentityKey(minerType, clientName, clientVersion string) string {
	minerType = strings.TrimSpace(strings.ToLower(minerType))
	clientName = strings.TrimSpace(strings.ToLower(clientName))
	clientVersion = strings.TrimSpace(strings.ToLower(clientVersion))
	return minerType + "|" + clientName + "|" + clientVersion
}

func profileSessionID(mc *MinerConn) string {
	if mc == nil {
		return ""
	}
	seq := atomic.LoadUint64(&mc.connectionSeq)
	if seq > 0 {
		return "seq:" + mc.connectionIDString()
	}
	if mc.id == "" || mc.connectedAt.IsZero() {
		return ""
	}
	return mc.id + "|" + mc.connectedAt.UTC().Format(time.RFC3339Nano)
}

func isFinitePositive(v float64) bool {
	return v > 0 && !math.IsNaN(v) && !math.IsInf(v, 0)
}
