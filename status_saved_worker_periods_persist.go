package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var savedWorkerPeriodsSnapshotMagic = [8]byte{'G', 'P', 'S', 'W', 'P', 'R', '0', '1'}
var savedWorkerPeriodsSnapshotEndMagic = [8]byte{'G', 'P', 'S', 'W', 'P', 'R', 'E', '1'}

const (
	savedWorkerPeriodsSnapshotVersionV1  = uint16(1)
	savedWorkerPeriodsSnapshotVersionV2  = uint16(2)
	savedWorkerPeriodsSnapshotVersion    = savedWorkerPeriodsSnapshotVersionV2
	savedWorkerPeriodsSnapshotMaxWorkers = 100_000
	savedWorkerPeriodsSnapshotMaxHashLen = 256
)

type savedWorkerPeriodSnapshotEntry struct {
	hash string
	ring savedWorkerPeriodRing
}

func savedWorkerHistoryFlushIntervalFromConfig(cfg Config) time.Duration {
	interval := cfg.SavedWorkerHistoryFlushInterval
	if interval <= 0 {
		interval = defaultSavedWorkerHistoryFlushInterval
	}
	return interval
}

func (s *StatusServer) savedWorkerPeriodsSnapshotPath() string {
	if s == nil {
		return filepath.Join(defaultDataDir, "state", "saved_worker_periods_history.bin")
	}
	dataDir := strings.TrimSpace(s.Config().DataDir)
	if dataDir == "" {
		dataDir = defaultDataDir
	}
	return filepath.Join(dataDir, "state", "saved_worker_periods_history.bin")
}

// loadSavedWorkerPeriodsSnapshot restores the in-memory saved-worker hashrate /
// best-share history ring state from a custom binary file, if present.
func (s *StatusServer) loadSavedWorkerPeriodsSnapshot() (int, error) {
	if s == nil {
		return 0, nil
	}
	path := s.savedWorkerPeriodsSnapshotPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) == 0 {
		return 0, nil
	}
	r := bytes.NewReader(data)

	var magic [8]byte
	if _, err := r.Read(magic[:]); err != nil {
		return 0, fmt.Errorf("read snapshot magic: %w", err)
	}
	if magic != savedWorkerPeriodsSnapshotMagic {
		return 0, fmt.Errorf("invalid snapshot magic")
	}

	var version uint16
	if err := binary.Read(r, binary.LittleEndian, &version); err != nil {
		return 0, fmt.Errorf("read snapshot version: %w", err)
	}
	if version != savedWorkerPeriodsSnapshotVersionV1 && version != savedWorkerPeriodsSnapshotVersionV2 {
		return 0, fmt.Errorf("unsupported snapshot version %d", version)
	}

	var reserved uint16
	if err := binary.Read(r, binary.LittleEndian, &reserved); err != nil {
		return 0, fmt.Errorf("read snapshot reserved: %w", err)
	}

	var lastBucketUnix int64
	if err := binary.Read(r, binary.LittleEndian, &lastBucketUnix); err != nil {
		return 0, fmt.Errorf("read snapshot last bucket: %w", err)
	}

	var workerCount uint32
	if err := binary.Read(r, binary.LittleEndian, &workerCount); err != nil {
		return 0, fmt.Errorf("read snapshot worker count: %w", err)
	}
	if workerCount > savedWorkerPeriodsSnapshotMaxWorkers {
		return 0, fmt.Errorf("snapshot worker count %d exceeds limit", workerCount)
	}

	loaded := make(map[string]*savedWorkerPeriodRing, int(workerCount))
	for i := uint32(0); i < workerCount; i++ {
		var hashLen uint16
		var slotCount uint16
		var lastMinute uint32
		if err := binary.Read(r, binary.LittleEndian, &hashLen); err != nil {
			return 0, fmt.Errorf("read worker %d hash len: %w", i, err)
		}
		if err := binary.Read(r, binary.LittleEndian, &slotCount); err != nil {
			return 0, fmt.Errorf("read worker %d slot count: %w", i, err)
		}
		if err := binary.Read(r, binary.LittleEndian, &lastMinute); err != nil {
			return 0, fmt.Errorf("read worker %d last minute: %w", i, err)
		}
		if hashLen == 0 || hashLen > savedWorkerPeriodsSnapshotMaxHashLen {
			return 0, fmt.Errorf("invalid worker hash len %d", hashLen)
		}
		if slotCount > uint16(savedWorkerPeriodSlots) {
			return 0, fmt.Errorf("invalid slot count %d", slotCount)
		}

		hashBytes := make([]byte, int(hashLen))
		if _, err := r.Read(hashBytes); err != nil {
			return 0, fmt.Errorf("read worker %d hash: %w", i, err)
		}
		hash := strings.ToLower(strings.TrimSpace(string(hashBytes)))
		if hash == "" {
			return 0, fmt.Errorf("empty worker hash in snapshot")
		}

		ring := &savedWorkerPeriodRing{lastMinute: lastMinute}
		for j := uint16(0); j < slotCount; j++ {
			var minute uint32
			var hashrateQ uint16
			var bestQ uint16
			if err := binary.Read(r, binary.LittleEndian, &minute); err != nil {
				return 0, fmt.Errorf("read worker %d slot %d minute: %w", i, j, err)
			}
			if err := binary.Read(r, binary.LittleEndian, &hashrateQ); err != nil {
				return 0, fmt.Errorf("read worker %d slot %d hashrate: %w", i, j, err)
			}
			if err := binary.Read(r, binary.LittleEndian, &bestQ); err != nil {
				return 0, fmt.Errorf("read worker %d slot %d best diff: %w", i, j, err)
			}
			if minute == 0 {
				continue
			}
			idx := savedWorkerRingIndex(minute)
			// If a malformed snapshot contains duplicates/collisions, keep the
			// newest minute so the ring remains self-consistent.
			if ring.minutes[idx] == 0 || minute >= ring.minutes[idx] {
				ring.minutes[idx] = minute
				ring.hashrateQ[idx] = hashrateQ
				ring.bestDifficultyQ[idx] = bestQ
			}
			if minute > ring.lastMinute {
				ring.lastMinute = minute
			}
		}
		if ring.lastMinute == 0 {
			continue
		}
		loaded[hash] = ring
	}

	if version >= savedWorkerPeriodsSnapshotVersionV2 {
		var endMagic [8]byte
		if _, err := r.Read(endMagic[:]); err != nil {
			return 0, fmt.Errorf("read snapshot end magic: %w", err)
		}
		if endMagic != savedWorkerPeriodsSnapshotEndMagic {
			return 0, fmt.Errorf("invalid snapshot end magic")
		}
	}
	if r.Len() != 0 {
		return 0, fmt.Errorf("snapshot trailing bytes: %d", r.Len())
	}

	lastBucket := time.Time{}
	if lastBucketUnix > 0 {
		lastBucket = time.Unix(lastBucketUnix, 0).UTC()
	}
	nowMinute := savedWorkerUnixMinute(time.Now().UTC())

	s.savedWorkerPeriodsMu.Lock()
	s.savedWorkerPeriods = loaded
	s.savedWorkerPeriodsLastBucket = lastBucket
	if nowMinute > 0 {
		s.pruneSavedWorkerPeriodsLocked(nowMinute)
	}
	count := len(s.savedWorkerPeriods)
	s.savedWorkerPeriodsMu.Unlock()
	return count, nil
}

// persistSavedWorkerPeriodsSnapshot writes the in-memory saved-worker hashrate /
// best-share history ring state to a custom binary file for restart recovery.
func (s *StatusServer) persistSavedWorkerPeriodsSnapshot() (int, error) {
	if s == nil {
		return 0, nil
	}
	nowMinute := savedWorkerUnixMinute(time.Now().UTC())

	s.savedWorkerPeriodsMu.Lock()
	if nowMinute > 0 {
		s.pruneSavedWorkerPeriodsLocked(nowMinute)
	}
	lastBucket := s.savedWorkerPeriodsLastBucket
	entries := make([]savedWorkerPeriodSnapshotEntry, 0, len(s.savedWorkerPeriods))
	for hash, ring := range s.savedWorkerPeriods {
		if ring == nil || strings.TrimSpace(hash) == "" || ring.lastMinute == 0 {
			continue
		}
		entries = append(entries, savedWorkerPeriodSnapshotEntry{
			hash: hash,
			ring: *ring,
		})
	}
	s.savedWorkerPeriodsMu.Unlock()

	sort.Slice(entries, func(i, j int) bool { return entries[i].hash < entries[j].hash })

	var buf bytes.Buffer
	write := func(v any) error {
		return binary.Write(&buf, binary.LittleEndian, v)
	}
	if _, err := buf.Write(savedWorkerPeriodsSnapshotMagic[:]); err != nil {
		return 0, fmt.Errorf("write snapshot magic: %w", err)
	}
	if err := write(savedWorkerPeriodsSnapshotVersion); err != nil {
		return 0, fmt.Errorf("write snapshot version: %w", err)
	}
	if err := write(uint16(0)); err != nil {
		return 0, fmt.Errorf("write snapshot reserved: %w", err)
	}
	lastBucketUnix := int64(0)
	if !lastBucket.IsZero() {
		lastBucketUnix = lastBucket.UTC().Unix()
	}
	if err := write(lastBucketUnix); err != nil {
		return 0, fmt.Errorf("write snapshot last bucket: %w", err)
	}
	if len(entries) > int(^uint32(0)) {
		return 0, errors.New("too many entries to persist")
	}
	if err := write(uint32(len(entries))); err != nil {
		return 0, fmt.Errorf("write snapshot worker count: %w", err)
	}

	for _, entry := range entries {
		hashBytes := []byte(entry.hash)
		if len(hashBytes) == 0 || len(hashBytes) > int(^uint16(0)) {
			return 0, fmt.Errorf("invalid worker hash length %d for %q", len(hashBytes), entry.hash)
		}
		slotCount := 0
		for i := 0; i < savedWorkerPeriodSlots; i++ {
			if entry.ring.minutes[i] != 0 {
				slotCount++
			}
		}
		if slotCount > int(^uint16(0)) {
			return 0, fmt.Errorf("slot count overflow for worker %q", entry.hash)
		}
		if err := write(uint16(len(hashBytes))); err != nil {
			return 0, fmt.Errorf("write worker hash len: %w", err)
		}
		if err := write(uint16(slotCount)); err != nil {
			return 0, fmt.Errorf("write worker slot count: %w", err)
		}
		if err := write(entry.ring.lastMinute); err != nil {
			return 0, fmt.Errorf("write worker last minute: %w", err)
		}
		if _, err := buf.Write(hashBytes); err != nil {
			return 0, fmt.Errorf("write worker hash: %w", err)
		}
		for i := 0; i < savedWorkerPeriodSlots; i++ {
			minute := entry.ring.minutes[i]
			if minute == 0 {
				continue
			}
			if err := write(minute); err != nil {
				return 0, fmt.Errorf("write worker slot minute: %w", err)
			}
			if err := write(entry.ring.hashrateQ[i]); err != nil {
				return 0, fmt.Errorf("write worker slot hashrate: %w", err)
			}
			if err := write(entry.ring.bestDifficultyQ[i]); err != nil {
				return 0, fmt.Errorf("write worker slot best diff: %w", err)
			}
		}
	}
	if _, err := buf.Write(savedWorkerPeriodsSnapshotEndMagic[:]); err != nil {
		return 0, fmt.Errorf("write snapshot end magic: %w", err)
	}

	path := s.savedWorkerPeriodsSnapshotPath()
	if err := atomicWriteFile(path, buf.Bytes()); err != nil {
		return 0, fmt.Errorf("write %s: %w", path, err)
	}
	return len(entries), nil
}

func (s *StatusServer) runSavedWorkerPeriodsSnapshotFlusher(ctx context.Context) {
	if s == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	timer := time.NewTimer(savedWorkerHistoryFlushIntervalFromConfig(s.Config()))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if _, err := s.persistSavedWorkerPeriodsSnapshot(); err != nil {
				logger.Warn("periodic saved worker history snapshot flush failed", "error", err, "path", s.savedWorkerPeriodsSnapshotPath())
			}
			timer.Reset(savedWorkerHistoryFlushIntervalFromConfig(s.Config()))
		}
	}
}
