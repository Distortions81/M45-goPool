package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	pendingSubmissionSpoolDirName = "pending-block-spool"
	pendingSubmissionSpoolVersion = 1
)

var (
	pendingSubmissionSpoolMu       sync.Mutex
	pendingSpoolRecoveryRetryDelay = time.Second
)

type pendingSubmissionSpoolRecord struct {
	Version       int                     `json:"version"`
	SubmissionKey string                  `json:"submission_key"`
	BlockSHA256   string                  `json:"block_sha256"`
	Submission    pendingSubmissionRecord `json:"submission"`
}

func pendingSubmissionSpoolDir(dataDir string) string {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		dataDir = defaultDataDir
	}
	return filepath.Join(dataDir, "state", pendingSubmissionSpoolDirName)
}

func pendingSubmissionBlockDigest(blockHex string) (string, error) {
	block, err := hex.DecodeString(strings.TrimSpace(blockHex))
	if err != nil {
		return "", fmt.Errorf("decode pending submission block: %w", err)
	}
	if len(block) == 0 {
		return "", fmt.Errorf("pending submission block is empty")
	}
	sum := sha256.Sum256(block)
	return hex.EncodeToString(sum[:]), nil
}

func pendingSubmissionSpoolPath(dataDir, blockHex string) (string, error) {
	digest, err := pendingSubmissionBlockDigest(blockHex)
	if err != nil {
		return "", err
	}
	return filepath.Join(pendingSubmissionSpoolDir(dataDir), digest+".json"), nil
}

// writePendingSubmissionSpool atomically preserves one exact block record
// outside SQLite. The file and its directory entry are synced before return so
// a caller can proceed to submitblock without leaving the block only in memory.
func writePendingSubmissionSpool(dataDir string, rec pendingSubmissionRecord) error {
	pendingSubmissionSpoolMu.Lock()
	defer pendingSubmissionSpoolMu.Unlock()

	key := pendingSubmissionKey(rec)
	if key == "" {
		return fmt.Errorf("pending submission spool key is empty")
	}
	rec.BlockHex = strings.TrimSpace(rec.BlockHex)
	digest, err := pendingSubmissionBlockDigest(rec.BlockHex)
	if err != nil {
		return err
	}
	path := filepath.Join(pendingSubmissionSpoolDir(dataDir), digest+".json")
	rec.Status = pendingSubmissionStatusPending
	spoolRec := pendingSubmissionSpoolRecord{
		Version:       pendingSubmissionSpoolVersion,
		SubmissionKey: key,
		BlockSHA256:   digest,
		Submission:    rec,
	}
	data, err := fastJSONMarshal(spoolRec)
	if err != nil {
		return fmt.Errorf("marshal pending submission spool: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create pending submission spool directory: %w", err)
	}
	// Persist creation of the spool directory itself. The state directory is
	// normally already durable because it contains workers.db, but the spool
	// subdirectory may be new on the first database failure.
	if err := syncPendingSubmissionSpoolDir(filepath.Dir(dir)); err != nil {
		return fmt.Errorf("sync pending submission spool parent: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".pending-block-*.tmp")
	if err != nil {
		return fmt.Errorf("create pending submission spool temp file: %w", err)
	}
	tmpPath := tmp.Name()
	keepTemp := true
	defer func() {
		_ = tmp.Close()
		if keepTemp {
			_ = os.Remove(tmpPath)
		}
	}()

	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("chmod pending submission spool: %w", err)
	}
	if err := writePendingSpoolAll(tmp, data); err != nil {
		return fmt.Errorf("write pending submission spool: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync pending submission spool: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close pending submission spool: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("install pending submission spool: %w", err)
	}
	keepTemp = false
	if err := syncPendingSubmissionSpoolDir(dir); err != nil {
		return fmt.Errorf("sync pending submission spool directory: %w", err)
	}
	return nil
}

func writePendingSpoolAll(f *os.File, data []byte) error {
	for len(data) > 0 {
		n, err := f.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return errors.New("short write")
		}
		data = data[n:]
	}
	return nil
}

func removePendingSubmissionSpool(dataDir, blockHex string) error {
	pendingSubmissionSpoolMu.Lock()
	defer pendingSubmissionSpoolMu.Unlock()

	path, err := pendingSubmissionSpoolPath(dataDir, blockHex)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return syncPendingSubmissionSpoolDir(filepath.Dir(path))
}

func syncPendingSubmissionSpoolDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// waitForPendingSubmissionSpoolRecovery is a startup safety gate. A valid
// emergency record is never ignored for the lifetime of a new mining process
// merely because its first SQLite import encountered a transient error.
func waitForPendingSubmissionSpoolRecovery(ctx context.Context, dataDir string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		recovered, err := recoverPendingSubmissionSpool(dataDir)
		if recovered > 0 {
			logger.Warn("recovered emergency pending blocks", "component", "startup", "kind", "block_recovery", "count", recovered)
		}
		if err == nil {
			return nil
		}
		logger.Error("recover emergency pending blocks; retrying before mining", "component", "startup", "kind", "block_recovery", "error", err)
		delay := pendingSpoolRecoveryRetryDelay
		if delay <= 0 {
			delay = time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// recoverPendingSubmissionSpool imports emergency records before mining
// listeners start. Each file is removed only after SQLite contains the same
// exact block bytes, making repeated recovery after a crash idempotent.
func recoverPendingSubmissionSpool(dataDir string) (int, error) {
	pendingSubmissionSpoolMu.Lock()
	defer pendingSubmissionSpoolMu.Unlock()

	dir := pendingSubmissionSpoolDir(dataDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("read pending submission spool: %w", err)
	}

	recovered := 0
	removedAny := false
	var recoveryErrs []error
	for _, entry := range entries {
		if entry.IsDir() || !isPendingSubmissionSpoolCandidate(entry.Name()) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			recoveryErrs = append(recoveryErrs, fmt.Errorf("read %s: %w", entry.Name(), readErr))
			continue
		}
		spoolRec, validateErr := decodePendingSubmissionSpoolRecord(entry.Name(), data)
		if validateErr != nil {
			quarantinePendingSubmissionSpoolFile(dir, path, entry.Name(), validateErr)
			continue
		}
		if importErr := importPendingSubmissionSpoolRecord(spoolRec.Submission); importErr != nil {
			recoveryErrs = append(recoveryErrs, fmt.Errorf("import %s: %w", entry.Name(), importErr))
			continue
		}
		if removeErr := os.Remove(path); removeErr != nil {
			logger.Warn("remove recovered pending block spool", "path", path, "error", removeErr)
		} else {
			removedAny = true
		}
		recovered++
	}
	if removedAny {
		if syncErr := syncPendingSubmissionSpoolDir(dir); syncErr != nil {
			logger.Warn("sync recovered pending block spool directory", "path", dir, "error", syncErr)
		}
	}
	return recovered, errors.Join(recoveryErrs...)
}

func isPendingSubmissionSpoolCandidate(name string) bool {
	return strings.HasSuffix(name, ".json") ||
		(strings.HasPrefix(name, ".pending-block-") && strings.HasSuffix(name, ".tmp"))
}

func decodePendingSubmissionSpoolRecord(name string, data []byte) (pendingSubmissionSpoolRecord, error) {
	var spoolRec pendingSubmissionSpoolRecord
	if err := fastJSONUnmarshal(data, &spoolRec); err != nil {
		return spoolRec, fmt.Errorf("decode: %w", err)
	}
	if spoolRec.Version != pendingSubmissionSpoolVersion {
		return spoolRec, fmt.Errorf("unsupported version %d", spoolRec.Version)
	}
	key := pendingSubmissionKey(spoolRec.Submission)
	if key == "" || key != strings.TrimSpace(spoolRec.SubmissionKey) {
		return spoolRec, fmt.Errorf("submission key mismatch")
	}
	digest, err := pendingSubmissionBlockDigest(spoolRec.Submission.BlockHex)
	if err != nil {
		return spoolRec, err
	}
	if !strings.EqualFold(digest, strings.TrimSpace(spoolRec.BlockSHA256)) {
		return spoolRec, fmt.Errorf("block digest mismatch")
	}
	if strings.HasSuffix(name, ".json") && name != digest+".json" {
		return spoolRec, fmt.Errorf("filename does not match block digest")
	}
	spoolRec.Submission.Status = pendingSubmissionStatusPending
	return spoolRec, nil
}

func quarantinePendingSubmissionSpoolFile(dir, path, name string, cause error) {
	target := path + fmt.Sprintf(".corrupt-%d", time.Now().UnixNano())
	if err := os.Rename(path, target); err != nil {
		logger.Error("invalid emergency pending block spool", "path", path, "error", cause, "quarantine_error", err)
		return
	}
	if err := syncPendingSubmissionSpoolDir(dir); err != nil {
		logger.Error("quarantine emergency pending block spool sync failed", "path", target, "error", err)
	}
	logger.Error("quarantined invalid emergency pending block spool", "path", target, "source", name, "error", cause)
}

func importPendingSubmissionSpoolRecord(rec pendingSubmissionRecord) error {
	db := getSharedStateDB()
	if db == nil {
		return fmt.Errorf("shared state db not initialized")
	}
	key := pendingSubmissionKey(rec)
	if key == "" {
		return fmt.Errorf("pending submission spool key is empty")
	}
	rec.BlockHex = strings.TrimSpace(rec.BlockHex)
	if _, err := pendingSubmissionBlockDigest(rec.BlockHex); err != nil {
		return err
	}
	rec.Status = pendingSubmissionStatusPending
	_, err := db.Exec(`
		INSERT INTO pending_submissions (
			submission_key, timestamp_unix, height, hash, worker, block_hex, rpc_error, rpc_url, payout_addr, status
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(submission_key) DO NOTHING
	`, key, unixOrZero(rec.Timestamp), rec.Height, strings.TrimSpace(rec.Hash), strings.TrimSpace(rec.Worker), rec.BlockHex,
		strings.TrimSpace(rec.RPCError), strings.TrimSpace(rec.RPCURL), strings.TrimSpace(rec.PayoutAddr), rec.Status)
	if err != nil {
		return err
	}

	var storedBlock string
	if err := db.QueryRow(`SELECT block_hex FROM pending_submissions WHERE submission_key = ?`, key).Scan(&storedBlock); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("pending submission insert did not create a row")
		}
		return err
	}
	same, err := samePendingSubmissionBlock(storedBlock, rec.BlockHex)
	if err != nil {
		return err
	}
	if !same {
		return fmt.Errorf("submission key collision has different exact block bytes")
	}
	return nil
}

func samePendingSubmissionBlock(a, b string) (bool, error) {
	aBlock, err := hex.DecodeString(strings.TrimSpace(a))
	if err != nil {
		return false, fmt.Errorf("decode stored pending block: %w", err)
	}
	bBlock, err := hex.DecodeString(strings.TrimSpace(b))
	if err != nil {
		return false, fmt.Errorf("decode spooled pending block: %w", err)
	}
	return bytes.Equal(aBlock, bBlock), nil
}
