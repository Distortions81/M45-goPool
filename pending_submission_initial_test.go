package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func openPendingSubmissionTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := openStateDB(filepath.Join(t.TempDir(), "state", "workers.db"))
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cleanup := setSharedStateDBForTest(db)
	t.Cleanup(cleanup)
	return db
}

func solvedBlockPersistenceTestJob() *Job {
	return &Job{
		JobID: "persisted-solved-job",
		Template: GetBlockTemplateResult{
			Height:        840_001,
			CoinbaseValue: 50 * 1e8,
		},
		Transactions:         []GBTTransaction{{Data: "aabbcc"}},
		CoinbaseValue:        50 * 1e8,
		PayoutPolicyCaptured: true,
		PoolFeePercent:       1.5,
		PayoutAddress:        "wallet-from-advertised-job",
	}
}

func runSolvedBlockPersistenceTest(mc *MinerConn, job *Job, header, coinbase []byte, hash string) {
	mc.handleBlockShare(
		1,
		job,
		job.JobID,
		"wallet-from-advertised-job.rig-1",
		nil,
		"00000000",
		"00000000",
		0x20000000,
		job.ScriptTime,
		header,
		coinbase,
		hash,
		1,
		time.Now(),
	)
}

func TestSolvedBlockPersistedBeforeRPCAndNotReplayedConcurrently(t *testing.T) {
	db := openPendingSubmissionTestDB(t)
	job := solvedBlockPersistenceTestJob()
	header := make([]byte, 80)
	for i := range header {
		header[i] = byte(i)
	}
	coinbase := []byte{0x01, 0x02, 0x03, 0x04}
	expectedBlock, err := assembleSolvedBlock(job, header, coinbase)
	if err != nil {
		t.Fatalf("assemble expected block: %v", err)
	}
	hash := strings.Repeat("1", 64)

	type observation struct {
		status string
		block  string
		worker string
		payout string
		err    error
	}
	observed := make(chan observation, 1)
	releaseRPC := make(chan struct{})
	var submitCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, readErr := io.ReadAll(r.Body)
		var req rpcRequest
		if readErr == nil {
			readErr = json.Unmarshal(body, &req)
		}
		if readErr != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if req.Method != "submitblock" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"result":null,"error":{"code":-5,"message":"not found"},"id":1}`))
			return
		}
		call := submitCalls.Add(1)
		if call == 1 {
			var got observation
			got.err = db.QueryRow(`
				SELECT status, block_hex, worker, payout_addr
				FROM pending_submissions WHERE submission_key = ?
			`, hash).Scan(&got.status, &got.block, &got.worker, &got.payout)
			observed <- got
			<-releaseRPC
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":null,"error":null,"id":1}`))
	}))
	defer server.Close()

	rpc := &RPCClient{url: server.URL, client: server.Client(), lp: server.Client(), nextID: 1}
	mc := &MinerConn{
		id:   "persist-before-rpc",
		conn: nopConn{},
		rpc:  rpc,
		cfg: Config{
			RPCURL:        server.URL,
			PayoutAddress: "wallet-from-new-config",
		},
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		runSolvedBlockPersistenceTest(mc, job, header, coinbase, hash)
	}()

	var got observation
	select {
	case got = <-observed:
	case <-time.After(5 * time.Second):
		t.Fatal("submitblock RPC was not reached")
	}
	if got.err != nil {
		t.Fatalf("query persisted block from RPC handler: %v", got.err)
	}
	if got.status != pendingSubmissionStatusSubmitting {
		t.Fatalf("status at first RPC = %q, want %q", got.status, pendingSubmissionStatusSubmitting)
	}
	if got.block != expectedBlock {
		t.Fatalf("persisted block differs from exact reconstructed block\ngot:  %s\nwant: %s", got.block, expectedBlock)
	}
	if got.worker != "wallet-from-advertised-job.rig-1" {
		t.Fatalf("persisted worker = %q", got.worker)
	}
	if got.payout != job.PayoutAddress {
		t.Fatalf("persisted payout = %q, want advertised job payout %q", got.payout, job.PayoutAddress)
	}

	// A periodic scan while the initial RPC is in flight must not submit the
	// same durable row a second time.
	replayPendingSubmissions(context.Background(), rpc)
	if calls := submitCalls.Load(); calls != 1 {
		t.Fatalf("submitblock calls while initial submission active = %d, want 1", calls)
	}
	close(releaseRPC)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("initial block submission did not finish")
	}
	flushFoundBlockLog(t)

	var status, storedBlock string
	if err := db.QueryRow(`SELECT status, block_hex FROM pending_submissions WHERE submission_key = ?`, hash).Scan(&status, &storedBlock); err != nil {
		t.Fatalf("query completed submission: %v", err)
	}
	if status != pendingSubmissionStatusSubmitted {
		t.Fatalf("completed status = %q, want %q", status, pendingSubmissionStatusSubmitted)
	}
	if storedBlock != expectedBlock {
		t.Fatal("terminal state update changed the persisted block bytes")
	}
}

func TestSolvedBlockSQLiteContentionSpoolsBeforeRPC(t *testing.T) {
	db := openPendingSubmissionTestDB(t)
	dataDir := t.TempDir()
	job := solvedBlockPersistenceTestJob()
	header := make([]byte, 80)
	coinbase := []byte{0x41, 0x42}
	expectedBlock, err := assembleSolvedBlock(job, header, coinbase)
	if err != nil {
		t.Fatalf("assemble expected block: %v", err)
	}

	// Hold the singleton database connection. An unbounded initial upsert would
	// prevent submitblock from being called until this connection is released.
	heldConn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("hold state db connection: %v", err)
	}
	t.Cleanup(func() { _ = heldConn.Close() })
	var one int
	if err := heldConn.QueryRowContext(context.Background(), `SELECT 1`).Scan(&one); err != nil || one != 1 {
		t.Fatalf("verify held state db connection: value=%d error=%v", one, err)
	}

	type rpcObservation struct {
		elapsed time.Duration
		err     error
	}
	var started time.Time
	var submitCalls atomic.Int32
	observed := make(chan rpcObservation, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		submitCalls.Add(1)
		observeErr := observeExactEmergencySpool(dataDir, expectedBlock)
		if closeErr := heldConn.Close(); observeErr == nil && closeErr != nil {
			observeErr = fmt.Errorf("release held state db connection: %w", closeErr)
		}
		observed <- rpcObservation{elapsed: time.Since(started), err: observeErr}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":null,"error":null,"id":1}`))
	}))
	defer server.Close()

	hash := strings.Repeat("d", 64)
	rpc := &RPCClient{url: server.URL, client: server.Client(), lp: server.Client(), nextID: 1}
	mc := &MinerConn{id: "sqlite-contention", conn: nopConn{}, rpc: rpc, cfg: Config{RPCURL: server.URL, DataDir: dataDir}}
	done := make(chan struct{})
	started = time.Now()
	go func() {
		defer close(done)
		runSolvedBlockPersistenceTest(mc, job, header, coinbase, hash)
	}()

	select {
	case got := <-observed:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if maxWait := solvedBlockPersistenceTimeout + 250*time.Millisecond; got.elapsed > maxWait {
			t.Fatalf("submitblock reached after %s, want no more than %s", got.elapsed, maxWait)
		}
	case <-time.After(2 * time.Second):
		_ = heldConn.Close()
		t.Fatal("submitblock remained blocked behind the state database connection")
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("solved block path did not finish after releasing database contention")
	}
	flushFoundBlockLog(t)
	if calls := submitCalls.Load(); calls != 1 {
		t.Fatalf("submitblock calls = %d, want 1", calls)
	}

	var status, storedBlock string
	if err := db.QueryRow(`SELECT status, block_hex FROM pending_submissions WHERE submission_key = ?`, hash).Scan(&status, &storedBlock); err != nil {
		t.Fatalf("query terminal submission: %v", err)
	}
	if status != pendingSubmissionStatusSubmitted || storedBlock != expectedBlock {
		t.Fatalf("terminal submission = (%q, %q), want submitted exact block", status, storedBlock)
	}
	spoolPath, err := pendingSubmissionSpoolPath(dataDir, expectedBlock)
	if err != nil {
		t.Fatalf("emergency spool path: %v", err)
	}
	if _, err := os.Stat(spoolPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("accepted emergency spool was not removed after SQLite recovery: %v", err)
	}
}

func TestSolvedBlockRejectionTransitionsToPending(t *testing.T) {
	db := openPendingSubmissionTestDB(t)
	job := solvedBlockPersistenceTestJob()
	header := make([]byte, 80)
	coinbase := []byte{0x01}
	hash := strings.Repeat("2", 64)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req rpcRequest
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")
		if req.Method == "submitblock" {
			_, _ = w.Write([]byte(`{"result":"bad-cb-amount","error":null,"id":1}`))
			return
		}
		_, _ = w.Write([]byte(`{"result":null,"error":{"code":-5,"message":"not found"},"id":1}`))
	}))
	defer server.Close()

	rpc := &RPCClient{url: server.URL, client: server.Client(), lp: server.Client(), nextID: 1}
	mc := &MinerConn{id: "rejected-solved-block", conn: nopConn{}, rpc: rpc, cfg: Config{RPCURL: server.URL}}
	runSolvedBlockPersistenceTest(mc, job, header, coinbase, hash)

	var status, rpcError, blockHex string
	if err := db.QueryRow(`
		SELECT status, rpc_error, block_hex FROM pending_submissions WHERE submission_key = ?
	`, hash).Scan(&status, &rpcError, &blockHex); err != nil {
		t.Fatalf("query rejected submission: %v", err)
	}
	if status != pendingSubmissionStatusPending {
		t.Fatalf("rejected status = %q, want %q", status, pendingSubmissionStatusPending)
	}
	if !strings.Contains(rpcError, "bad-cb-amount") {
		t.Fatalf("pending RPC error = %q", rpcError)
	}
	expectedBlock, err := assembleSolvedBlock(job, header, coinbase)
	if err != nil {
		t.Fatalf("assemble expected block: %v", err)
	}
	if blockHex != expectedBlock {
		t.Fatal("rejection transition did not preserve exact block bytes")
	}
}

func TestSolvedBlockSubmissionContinuesWhenPersistenceFails(t *testing.T) {
	db := openPendingSubmissionTestDB(t)
	dataDir := t.TempDir()
	if _, err := db.Exec(`
		CREATE TRIGGER fail_solved_block_persistence
		BEFORE INSERT ON pending_submissions
		BEGIN
			SELECT RAISE(FAIL, 'forced persistence failure');
		END
	`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}

	var submitCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req rpcRequest
		_ = json.Unmarshal(body, &req)
		if req.Method == "submitblock" {
			submitCalls.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":null,"error":null,"id":1}`))
	}))
	defer server.Close()

	rpc := &RPCClient{url: server.URL, client: server.Client(), lp: server.Client(), nextID: 1}
	job := solvedBlockPersistenceTestJob()
	header := make([]byte, 80)
	coinbase := []byte{0x01}
	hash := strings.Repeat("3", 64)
	mc := &MinerConn{id: "persistence-failure", conn: nopConn{}, rpc: rpc, cfg: Config{RPCURL: server.URL, DataDir: dataDir}}
	runSolvedBlockPersistenceTest(mc, job, header, coinbase, hash)
	flushFoundBlockLog(t)
	if calls := submitCalls.Load(); calls != 1 {
		t.Fatalf("submitblock calls after persistence failure = %d, want 1", calls)
	}
	expectedBlock, err := assembleSolvedBlock(job, header, coinbase)
	if err != nil {
		t.Fatalf("assemble expected block: %v", err)
	}
	spoolPath, err := pendingSubmissionSpoolPath(dataDir, expectedBlock)
	if err != nil {
		t.Fatalf("emergency spool path: %v", err)
	}
	if _, err := os.Stat(spoolPath); err != nil {
		t.Fatalf("accepted block was not retained while SQLite remained unavailable: %v", err)
	}
}

func TestSolvedBlockSQLiteAndRPCFailureRecoversEmergencySpool(t *testing.T) {
	db := openPendingSubmissionTestDB(t)
	dataDir := t.TempDir()
	if _, err := db.Exec(`
		CREATE TRIGGER fail_solved_block_persistence_and_retry
		BEFORE INSERT ON pending_submissions
		BEGIN
			SELECT RAISE(FAIL, 'forced persistence failure');
		END
	`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":"bad-cb-amount","error":null,"id":1}`))
	}))
	defer server.Close()

	job := solvedBlockPersistenceTestJob()
	header := make([]byte, 80)
	for i := range header {
		header[i] = byte(255 - i)
	}
	coinbase := []byte{0x02, 0x03, 0x04}
	hash := strings.Repeat("8", 64)
	rpc := &RPCClient{url: server.URL, client: server.Client(), lp: server.Client(), nextID: 1}
	mc := &MinerConn{id: "double-persistence-failure", conn: nopConn{}, rpc: rpc, cfg: Config{RPCURL: server.URL, DataDir: dataDir}}
	runSolvedBlockPersistenceTest(mc, job, header, coinbase, hash)

	expectedBlock, err := assembleSolvedBlock(job, header, coinbase)
	if err != nil {
		t.Fatalf("assemble expected block: %v", err)
	}
	spoolPath, err := pendingSubmissionSpoolPath(dataDir, expectedBlock)
	if err != nil {
		t.Fatalf("emergency spool path: %v", err)
	}
	data, err := os.ReadFile(spoolPath)
	if err != nil {
		t.Fatalf("read emergency spool: %v", err)
	}
	spoolRec, err := decodePendingSubmissionSpoolRecord(filepath.Base(spoolPath), data)
	if err != nil {
		t.Fatalf("decode emergency spool: %v", err)
	}
	if spoolRec.Submission.BlockHex != expectedBlock {
		t.Fatal("emergency spool did not preserve exact reconstructed block")
	}
	if !strings.Contains(spoolRec.Submission.RPCError, "bad-cb-amount") {
		t.Fatalf("emergency spool RPC error = %q", spoolRec.Submission.RPCError)
	}

	if _, err := db.Exec(`DROP TRIGGER fail_solved_block_persistence_and_retry`); err != nil {
		t.Fatalf("drop failure trigger: %v", err)
	}
	if recovered, err := recoverPendingSubmissionSpool(dataDir); err != nil || recovered != 1 {
		t.Fatalf("recover emergency spool = (%d, %v), want (1, nil)", recovered, err)
	}
	var blockHex, status string
	if err := db.QueryRow(`SELECT block_hex, status FROM pending_submissions WHERE submission_key = ?`, hash).Scan(&blockHex, &status); err != nil {
		t.Fatalf("query recovered block: %v", err)
	}
	if blockHex != expectedBlock || status != pendingSubmissionStatusPending {
		t.Fatalf("recovered block/status = (%q, %q)", blockHex, status)
	}
}

func installControllablePendingPersistenceFailure(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`
		CREATE TABLE pending_persistence_test_control (enabled INTEGER NOT NULL);
		INSERT INTO pending_persistence_test_control (enabled) VALUES (1);
		CREATE TRIGGER fail_pending_persistence_while_enabled
		BEFORE INSERT ON pending_submissions
		WHEN (SELECT enabled FROM pending_persistence_test_control LIMIT 1) = 1
		BEGIN
			SELECT RAISE(FAIL, 'forced first persistence failure');
		END;
	`); err != nil {
		t.Fatalf("install controllable persistence failure: %v", err)
	}
}

func observeExactEmergencySpool(dataDir, expectedBlock string) error {
	path, err := pendingSubmissionSpoolPath(dataDir, expectedBlock)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read final spool before RPC: %w", err)
	}
	spoolRec, err := decodePendingSubmissionSpoolRecord(filepath.Base(path), data)
	if err != nil {
		return fmt.Errorf("decode final spool before RPC: %w", err)
	}
	if spoolRec.Submission.BlockHex != expectedBlock {
		return fmt.Errorf("spooled block differs from exact RPC block")
	}
	return nil
}

func TestSolvedBlockSpoolPrecedesRPCAndAcceptedCleanupWaitsForSQLite(t *testing.T) {
	db := openPendingSubmissionTestDB(t)
	dataDir := t.TempDir()
	installControllablePendingPersistenceFailure(t, db)
	job := solvedBlockPersistenceTestJob()
	header := make([]byte, 80)
	coinbase := []byte{0x21, 0x22}
	expectedBlock, err := assembleSolvedBlock(job, header, coinbase)
	if err != nil {
		t.Fatalf("assemble expected block: %v", err)
	}

	observed := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		observeErr := observeExactEmergencySpool(dataDir, expectedBlock)
		if _, err := db.Exec(`UPDATE pending_persistence_test_control SET enabled = 0`); observeErr == nil && err != nil {
			observeErr = fmt.Errorf("allow terminal SQLite write: %w", err)
		}
		observed <- observeErr
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":null,"error":null,"id":1}`))
	}))
	defer server.Close()

	hash := strings.Repeat("9", 64)
	rpc := &RPCClient{url: server.URL, client: server.Client(), lp: server.Client(), nextID: 1}
	mc := &MinerConn{id: "spool-before-accepted-rpc", conn: nopConn{}, rpc: rpc, cfg: Config{RPCURL: server.URL, DataDir: dataDir}}
	runSolvedBlockPersistenceTest(mc, job, header, coinbase, hash)
	if err := <-observed; err != nil {
		t.Fatal(err)
	}

	var blockHex, status string
	if err := db.QueryRow(`SELECT block_hex, status FROM pending_submissions WHERE submission_key = ?`, hash).Scan(&blockHex, &status); err != nil {
		t.Fatalf("query accepted fallback: %v", err)
	}
	if blockHex != expectedBlock || status != pendingSubmissionStatusSubmitted {
		t.Fatalf("accepted fallback block/status = (%q, %q)", blockHex, status)
	}
	spoolPath, _ := pendingSubmissionSpoolPath(dataDir, expectedBlock)
	if _, err := os.Stat(spoolPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("accepted spool was not removed after SQLite commit: %v", err)
	}
}

func TestSolvedBlockSpoolFailureCleanupWaitsForSQLite(t *testing.T) {
	db := openPendingSubmissionTestDB(t)
	dataDir := t.TempDir()
	installControllablePendingPersistenceFailure(t, db)
	job := solvedBlockPersistenceTestJob()
	header := make([]byte, 80)
	coinbase := []byte{0x31, 0x32}
	expectedBlock, err := assembleSolvedBlock(job, header, coinbase)
	if err != nil {
		t.Fatalf("assemble expected block: %v", err)
	}

	observed := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		observeErr := observeExactEmergencySpool(dataDir, expectedBlock)
		if _, err := db.Exec(`UPDATE pending_persistence_test_control SET enabled = 0`); observeErr == nil && err != nil {
			observeErr = fmt.Errorf("allow pending SQLite write: %w", err)
		}
		observed <- observeErr
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":"bad-cb-amount","error":null,"id":1}`))
	}))
	defer server.Close()

	hash := strings.Repeat("b", 64)
	rpc := &RPCClient{url: server.URL, client: server.Client(), lp: server.Client(), nextID: 1}
	mc := &MinerConn{id: "spool-before-rejected-rpc", conn: nopConn{}, rpc: rpc, cfg: Config{RPCURL: server.URL, DataDir: dataDir}}
	runSolvedBlockPersistenceTest(mc, job, header, coinbase, hash)
	if err := <-observed; err != nil {
		t.Fatal(err)
	}

	var blockHex, status, rpcError string
	if err := db.QueryRow(`SELECT block_hex, status, rpc_error FROM pending_submissions WHERE submission_key = ?`, hash).Scan(&blockHex, &status, &rpcError); err != nil {
		t.Fatalf("query rejected fallback: %v", err)
	}
	if blockHex != expectedBlock || status != pendingSubmissionStatusPending || !strings.Contains(rpcError, "bad-cb-amount") {
		t.Fatalf("rejected fallback block/status/error = (%q, %q, %q)", blockHex, status, rpcError)
	}
	spoolPath, _ := pendingSubmissionSpoolPath(dataDir, expectedBlock)
	if _, err := os.Stat(spoolPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed spool was not removed after SQLite commit: %v", err)
	}
}

type panickingSubmitRPC struct{}

func (panickingSubmitRPC) callCtx(context.Context, string, any, any) error {
	panic("forced submitblock panic")
}

func TestSolvedBlockPanicDoesNotStrandSubmittingRow(t *testing.T) {
	db := openPendingSubmissionTestDB(t)
	hash := strings.Repeat("4", 64)
	mc := &MinerConn{id: "panic-after-persist", conn: nopConn{}, rpc: panickingSubmitRPC{}}

	func() {
		defer func() {
			if recover() == nil {
				t.Error("expected forced RPC panic")
			}
		}()
		runSolvedBlockPersistenceTest(mc, solvedBlockPersistenceTestJob(), make([]byte, 80), []byte{0x01}, hash)
	}()

	var status, rpcError string
	if err := db.QueryRow(`SELECT status, rpc_error FROM pending_submissions WHERE submission_key = ?`, hash).Scan(&status, &rpcError); err != nil {
		t.Fatalf("query interrupted submission: %v", err)
	}
	if status != pendingSubmissionStatusPending {
		t.Fatalf("panic status = %q, want %q", status, pendingSubmissionStatusPending)
	}
	if !strings.Contains(rpcError, "interrupted") {
		t.Fatalf("panic RPC error = %q", rpcError)
	}
}

func TestPendingSubmissionStartupReclaimsSubmittingRows(t *testing.T) {
	db := openPendingSubmissionTestDB(t)
	rec := pendingSubmissionRecord{
		Timestamp: time.Now().UTC(),
		Height:    840_002,
		Hash:      strings.Repeat("5", 64),
		BlockHex:  "deadbeef",
		Status:    pendingSubmissionStatusSubmitting,
	}
	if err := appendPendingSubmissionRecord(rec); err != nil {
		t.Fatalf("append submitting record: %v", err)
	}

	if err := reclaimSubmittingPendingSubmissions(context.Background()); err != nil {
		t.Fatalf("reclaim submitting rows: %v", err)
	}

	var status, rpcError string
	if err := db.QueryRow(`SELECT status, rpc_error FROM pending_submissions WHERE submission_key = ?`, rec.Hash).Scan(&status, &rpcError); err != nil {
		t.Fatalf("query reclaimed submission: %v", err)
	}
	if status != pendingSubmissionStatusPending {
		t.Fatalf("startup status = %q, want %q", status, pendingSubmissionStatusPending)
	}
	if !strings.Contains(rpcError, "process exit") {
		t.Fatalf("startup recovery error = %q", rpcError)
	}
}

func TestPendingSubmissionStartupRecoveryRetriesThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	err := retryPendingSubmissionStartupRecovery(context.Background(), time.Millisecond, func(context.Context) error {
		if calls.Add(1) == 1 {
			return errors.New("transient recovery failure")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("retry startup recovery: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("recovery calls = %d, want 2", got)
	}
}

func TestPendingSubmissionStartupRecoveryCancellationStopsRetries(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	err := retryPendingSubmissionStartupRecovery(ctx, time.Hour, func(context.Context) error {
		calls.Add(1)
		cancel()
		return errors.New("persistent recovery failure")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("recovery error = %v, want context canceled", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("recovery calls after cancellation = %d, want 1", got)
	}
}

func TestPendingSubmissionStartupReclaimReturnsSQLiteError(t *testing.T) {
	db := openPendingSubmissionTestDB(t)
	rec := pendingSubmissionRecord{
		Timestamp: time.Now().UTC(),
		Height:    840_005,
		Hash:      strings.Repeat("c", 64),
		BlockHex:  "deadbeef",
		Status:    pendingSubmissionStatusSubmitting,
	}
	if err := appendPendingSubmissionRecord(rec); err != nil {
		t.Fatalf("append submitting record: %v", err)
	}
	if _, err := db.Exec(`
		CREATE TRIGGER fail_pending_startup_reclaim
		BEFORE UPDATE OF status ON pending_submissions
		WHEN OLD.submission_key = '` + rec.Hash + `'
		BEGIN
			SELECT RAISE(FAIL, 'forced reclaim failure');
		END
	`); err != nil {
		t.Fatalf("create reclaim failure trigger: %v", err)
	}
	if err := reclaimSubmittingPendingSubmissions(context.Background()); err == nil {
		t.Fatal("expected reclaim SQLite error")
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM pending_submissions WHERE submission_key = ?`, rec.Hash).Scan(&status); err != nil {
		t.Fatalf("query failed reclaim row: %v", err)
	}
	if status != pendingSubmissionStatusSubmitting {
		t.Fatalf("failed reclaim status = %q, want submitting", status)
	}
}

func TestPendingSubmissionTransitionRequiresExpectedState(t *testing.T) {
	_ = openPendingSubmissionTestDB(t)
	rec := pendingSubmissionRecord{
		Timestamp: time.Now().UTC(),
		Height:    840_003,
		Hash:      strings.Repeat("6", 64),
		BlockHex:  "deadbeef",
		Status:    pendingSubmissionStatusSubmitted,
	}
	if err := appendPendingSubmissionRecord(rec); err != nil {
		t.Fatalf("append submitted record: %v", err)
	}
	changed, err := transitionPendingSubmissionStatus(rec.Hash, pendingSubmissionStatusPending, pendingSubmissionStatusSubmitting, errors.New("unused").Error())
	if err != nil {
		t.Fatalf("compare-and-transition: %v", err)
	}
	if changed {
		t.Fatal("transition changed a row whose state did not match")
	}
}

func TestPendingSubmissionReplayTreatsLegacyEmptyStatusAsPending(t *testing.T) {
	db := openPendingSubmissionTestDB(t)
	rec := pendingSubmissionRecord{
		Timestamp: time.Now().UTC(),
		Height:    840_004,
		Hash:      strings.Repeat("7", 64),
		BlockHex:  "deadbeef",
		Status:    pendingSubmissionStatusPending,
	}
	if err := appendPendingSubmissionRecord(rec); err != nil {
		t.Fatalf("append legacy record: %v", err)
	}
	if _, err := db.Exec(`UPDATE pending_submissions SET status = '' WHERE submission_key = ?`, rec.Hash); err != nil {
		t.Fatalf("clear legacy status: %v", err)
	}

	var submitCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req rpcRequest
		_ = json.Unmarshal(body, &req)
		if req.Method == "submitblock" {
			submitCalls.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":null,"error":null,"id":1}`))
	}))
	defer server.Close()
	rpc := &RPCClient{url: server.URL, client: server.Client(), lp: server.Client(), nextID: 1}

	replayPendingSubmissions(context.Background(), rpc)
	if calls := submitCalls.Load(); calls != 1 {
		t.Fatalf("legacy empty-status submitblock calls = %d, want 1", calls)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM pending_submissions WHERE submission_key = ?`, rec.Hash).Scan(&status); err != nil {
		t.Fatalf("query legacy replay status: %v", err)
	}
	if status != pendingSubmissionStatusSubmitted {
		t.Fatalf("legacy replay status = %q, want %q", status, pendingSubmissionStatusSubmitted)
	}
}
