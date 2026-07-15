package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testPendingSubmissionSpoolRecord(blockHex string) pendingSubmissionRecord {
	return pendingSubmissionRecord{
		Timestamp:  time.Unix(1_700_000_000, 0).UTC(),
		Height:     840_100,
		Hash:       strings.Repeat("a", 64),
		Worker:     "wallet.rig-1",
		BlockHex:   blockHex,
		RPCError:   "database unavailable",
		RPCURL:     "http://127.0.0.1:8332",
		PayoutAddr: "wallet",
		Status:     pendingSubmissionStatusPending,
	}
}

func TestPendingSubmissionSpoolRecoversExactBlockOnce(t *testing.T) {
	db := openPendingSubmissionTestDB(t)
	dataDir := t.TempDir()
	rec := testPendingSubmissionSpoolRecord("01020304")

	if err := writePendingSubmissionSpool(dataDir, rec); err != nil {
		t.Fatalf("write emergency spool: %v", err)
	}
	path, err := pendingSubmissionSpoolPath(dataDir, rec.BlockHex)
	if err != nil {
		t.Fatalf("spool path: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat emergency spool: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("spool permissions = %o, want 600", got)
	}

	recovered, err := recoverPendingSubmissionSpool(dataDir)
	if err != nil {
		t.Fatalf("recover emergency spool: %v", err)
	}
	if recovered != 1 {
		t.Fatalf("recovered records = %d, want 1", recovered)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovered spool still exists: %v", err)
	}

	var blockHex, status string
	if err := db.QueryRow(`SELECT block_hex, status FROM pending_submissions WHERE submission_key = ?`, rec.Hash).Scan(&blockHex, &status); err != nil {
		t.Fatalf("query recovered record: %v", err)
	}
	if blockHex != rec.BlockHex {
		t.Fatalf("recovered block = %q, want exact %q", blockHex, rec.BlockHex)
	}
	if status != pendingSubmissionStatusPending {
		t.Fatalf("recovered status = %q, want pending", status)
	}
	if recovered, err := recoverPendingSubmissionSpool(dataDir); err != nil || recovered != 0 {
		t.Fatalf("second recovery = (%d, %v), want (0, nil)", recovered, err)
	}
}

func TestPendingSubmissionSpoolDoesNotDowngradeSubmittedRow(t *testing.T) {
	db := openPendingSubmissionTestDB(t)
	dataDir := t.TempDir()
	rec := testPendingSubmissionSpoolRecord("05060708")
	submitted := rec
	submitted.Status = pendingSubmissionStatusSubmitted
	if err := appendPendingSubmissionRecord(submitted); err != nil {
		t.Fatalf("append submitted record: %v", err)
	}
	if err := writePendingSubmissionSpool(dataDir, rec); err != nil {
		t.Fatalf("write emergency spool: %v", err)
	}

	if recovered, err := recoverPendingSubmissionSpool(dataDir); err != nil || recovered != 1 {
		t.Fatalf("recover submitted duplicate = (%d, %v), want (1, nil)", recovered, err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM pending_submissions WHERE submission_key = ?`, rec.Hash).Scan(&status); err != nil {
		t.Fatalf("query submitted record: %v", err)
	}
	if status != pendingSubmissionStatusSubmitted {
		t.Fatalf("submitted record was downgraded to %q", status)
	}
}

func TestPendingSubmissionSpoolPreservesAmbiguousSubmittingRowForReclaim(t *testing.T) {
	db := openPendingSubmissionTestDB(t)
	dataDir := t.TempDir()
	rec := testPendingSubmissionSpoolRecord("0708090a")
	submitting := rec
	submitting.Status = pendingSubmissionStatusSubmitting
	if err := appendPendingSubmissionRecord(submitting); err != nil {
		t.Fatalf("append ambiguously committed row: %v", err)
	}
	if err := writePendingSubmissionSpool(dataDir, rec); err != nil {
		t.Fatalf("write duplicate emergency spool: %v", err)
	}

	if recovered, err := recoverPendingSubmissionSpool(dataDir); err != nil || recovered != 1 {
		t.Fatalf("recover ambiguous duplicate = (%d, %v), want (1, nil)", recovered, err)
	}
	var status, blockHex string
	if err := db.QueryRow(`SELECT status, block_hex FROM pending_submissions WHERE submission_key = ?`, rec.Hash).Scan(&status, &blockHex); err != nil {
		t.Fatalf("query ambiguous row: %v", err)
	}
	if status != pendingSubmissionStatusSubmitting || blockHex != rec.BlockHex {
		t.Fatalf("ambiguous row after spool import = (%q, %q)", status, blockHex)
	}

	if err := reclaimSubmittingPendingSubmissions(context.Background()); err != nil {
		t.Fatalf("reclaim ambiguous submitting row: %v", err)
	}
	if err := db.QueryRow(`SELECT status, block_hex FROM pending_submissions WHERE submission_key = ?`, rec.Hash).Scan(&status, &blockHex); err != nil {
		t.Fatalf("query reclaimed ambiguous row: %v", err)
	}
	if status != pendingSubmissionStatusPending || blockHex != rec.BlockHex {
		t.Fatalf("ambiguous row after startup reclaim = (%q, %q)", status, blockHex)
	}
}

func TestPendingSubmissionSpoolRefusesDifferentBlockForSameKey(t *testing.T) {
	_ = openPendingSubmissionTestDB(t)
	dataDir := t.TempDir()
	existing := testPendingSubmissionSpoolRecord("090a0b0c")
	if err := appendPendingSubmissionRecord(existing); err != nil {
		t.Fatalf("append existing record: %v", err)
	}
	conflict := existing
	conflict.BlockHex = "0d0e0f10"
	if err := writePendingSubmissionSpool(dataDir, conflict); err != nil {
		t.Fatalf("write conflicting spool: %v", err)
	}
	path, _ := pendingSubmissionSpoolPath(dataDir, conflict.BlockHex)

	if recovered, err := recoverPendingSubmissionSpool(dataDir); err == nil || recovered != 0 {
		t.Fatalf("conflicting recovery = (%d, %v), want (0, error)", recovered, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("conflicting exact block was discarded: %v", err)
	}
}

func TestPendingSubmissionSpoolRecoversSyncedOrphanTemp(t *testing.T) {
	_ = openPendingSubmissionTestDB(t)
	dataDir := t.TempDir()
	rec := testPendingSubmissionSpoolRecord("11121314")
	if err := writePendingSubmissionSpool(dataDir, rec); err != nil {
		t.Fatalf("write emergency spool: %v", err)
	}
	path, _ := pendingSubmissionSpoolPath(dataDir, rec.BlockHex)
	orphan := filepath.Join(filepath.Dir(path), ".pending-block-orphan.tmp")
	if err := os.Rename(path, orphan); err != nil {
		t.Fatalf("make orphan temp: %v", err)
	}

	if recovered, err := recoverPendingSubmissionSpool(dataDir); err != nil || recovered != 1 {
		t.Fatalf("recover orphan temp = (%d, %v), want (1, nil)", recovered, err)
	}
	if _, err := os.Stat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovered orphan still exists: %v", err)
	}
}

func TestPendingSubmissionSpoolQuarantinesMalformedFile(t *testing.T) {
	_ = openPendingSubmissionTestDB(t)
	dataDir := t.TempDir()
	dir := pendingSubmissionSpoolDir(dataDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("make spool dir: %v", err)
	}
	badPath := filepath.Join(dir, strings.Repeat("0", 64)+".json")
	if err := os.WriteFile(badPath, []byte("not-json"), 0o600); err != nil {
		t.Fatalf("write malformed spool: %v", err)
	}

	if recovered, err := recoverPendingSubmissionSpool(dataDir); err != nil || recovered != 0 {
		t.Fatalf("recover malformed spool = (%d, %v), want (0, nil)", recovered, err)
	}
	if _, err := os.Stat(badPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("malformed active spool still exists: %v", err)
	}
	matches, err := filepath.Glob(badPath + ".corrupt-*")
	if err != nil || len(matches) != 1 {
		t.Fatalf("quarantined files = %v, error %v", matches, err)
	}
}

func TestPendingSubmissionSpoolConcurrentRefreshRemainsValid(t *testing.T) {
	dataDir := t.TempDir()
	rec := testPendingSubmissionSpoolRecord("15161718")
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			updated := rec
			updated.RPCError = time.Now().String()
			errs <- writePendingSubmissionSpool(dataDir, updated)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent spool refresh: %v", err)
		}
	}
	path, _ := pendingSubmissionSpoolPath(dataDir, rec.BlockHex)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read refreshed spool: %v", err)
	}
	if _, err := decodePendingSubmissionSpoolRecord(filepath.Base(path), data); err != nil {
		t.Fatalf("decode refreshed spool: %v", err)
	}
}

func TestPendingSubmissionSpoolRecoveryGateStopsOnCancellation(t *testing.T) {
	dataDir := t.TempDir()
	rec := testPendingSubmissionSpoolRecord("191a1b1c")
	if err := writePendingSubmissionSpool(dataDir, rec); err != nil {
		t.Fatalf("write emergency spool: %v", err)
	}
	restoreDB := setSharedStateDBForTest(nil)
	defer restoreDB()
	previousDelay := pendingSpoolRecoveryRetryDelay
	pendingSpoolRecoveryRetryDelay = time.Millisecond
	defer func() { pendingSpoolRecoveryRetryDelay = previousDelay }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	err := waitForPendingSubmissionSpoolRecovery(ctx, dataDir)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("recovery gate error = %v, want deadline exceeded", err)
	}
	path, _ := pendingSubmissionSpoolPath(dataDir, rec.BlockHex)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("failed recovery discarded spool: %v", err)
	}
}
