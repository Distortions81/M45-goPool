package main

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"
)

// pendingSubmissionRecord mirrors the pending_submissions SQLite table schema.
// Older entries may omit Status; in that case, they are treated as "pending"
// unless a newer record for the same hash marks them as "submitted".
type pendingSubmissionRecord struct {
	Timestamp  time.Time `json:"timestamp"`
	Height     int64     `json:"height"`
	Hash       string    `json:"hash"`
	Worker     string    `json:"worker"`
	BlockHex   string    `json:"block_hex"`
	RPCError   string    `json:"rpc_error,omitempty"`
	RPCURL     string    `json:"rpc_url,omitempty"`
	PayoutAddr string    `json:"payout_addr,omitempty"`
	Status     string    `json:"status,omitempty"`
}

const (
	pendingSubmissionStatusPending    = "pending"
	pendingSubmissionStatusSubmitting = "submitting"
	pendingSubmissionStatusSubmitted  = "submitted"
)

var (
	pendingReplayBaseDelay           = 5 * time.Second
	pendingReplayMaxDelay            = 5 * time.Minute
	pendingReplayJitterFrac          = 0.2
	pendingReplayBackoff             = newPendingReplayBackoff()
	pendingReplayInterval            = 5 * time.Second
	pendingStartupRecoveryRetryDelay = time.Second
)

type pendingReplayState struct {
	failures    int
	nextAllowed time.Time
}

type pendingReplayBackoffState struct {
	mu    sync.Mutex
	state map[string]pendingReplayState
	rng   *rand.Rand
}

func newPendingReplayBackoff() *pendingReplayBackoffState {
	return &pendingReplayBackoffState{
		state: make(map[string]pendingReplayState),
		rng:   rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

func (b *pendingReplayBackoffState) allow(key string, now time.Time) bool {
	if b == nil || key == "" {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	st, ok := b.state[key]
	if !ok {
		return true
	}
	return !st.nextAllowed.After(now)
}

func (b *pendingReplayBackoffState) fail(key string, now time.Time) time.Duration {
	if b == nil || key == "" {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.state[key]
	st.failures++

	delay := pendingReplayBaseDelay
	if delay <= 0 {
		delay = time.Second
	}
	for i := 1; i < st.failures; i++ {
		delay *= 2
		if pendingReplayMaxDelay > 0 && delay >= pendingReplayMaxDelay {
			delay = pendingReplayMaxDelay
			break
		}
	}

	if pendingReplayJitterFrac > 0 && b.rng != nil {
		low := 1 - pendingReplayJitterFrac
		high := 1 + pendingReplayJitterFrac
		jitter := low + (high-low)*b.rng.Float64()
		delay = time.Duration(float64(delay) * jitter)
		if delay <= 0 {
			delay = time.Millisecond
		}
	}

	st.nextAllowed = now.Add(delay)
	b.state[key] = st
	return delay
}

func (b *pendingReplayBackoffState) reset(key string) {
	if b == nil || key == "" {
		return
	}
	b.mu.Lock()
	delete(b.state, key)
	b.mu.Unlock()
}

// startPendingSubmissionReplayer periodically scans the pending_submissions
// SQLite table and attempts to resubmit any entries that are still marked as
// pending. On successful submitblock, it marks the row as "submitted" so
// future scans skip that block. This is best-effort and does not guarantee
// eventual submission, but provides a recovery path when the node RPC was down.
func startPendingSubmissionReplayer(ctx context.Context, rpc *RPCClient) (<-chan struct{}, error) {
	if rpc == nil {
		done := make(chan struct{})
		close(done)
		return done, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// Mining listeners are not started until after this function returns. Any
	// submitting row found here therefore belongs to a process that stopped
	// before recording the outcome of its initial submitblock attempt.
	if err := retryPendingSubmissionStartupRecovery(ctx, pendingStartupRecoveryRetryDelay, reclaimSubmittingPendingSubmissions); err != nil {
		return nil, err
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Do not sacrifice a fixed ticker interval after safely reclaiming a
		// block interrupted by the previous process.
		replayPendingSubmissions(ctx, rpc)
		interval := pendingReplayInterval
		if interval <= 0 {
			interval = 5 * time.Second
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				replayPendingSubmissions(ctx, rpc)
			}
		}
	}()
	return done, nil
}

func retryPendingSubmissionStartupRecovery(ctx context.Context, retryDelay time.Duration, recoverFn func(context.Context) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if recoverFn == nil {
		return fmt.Errorf("pending submission startup recovery function is nil")
	}
	if retryDelay <= 0 {
		retryDelay = time.Second
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if err := recoverFn(ctx); err == nil {
			return nil
		} else {
			logger.Error("pending submission startup recovery failed; retrying before mining", "error", err)
		}

		timer := time.NewTimer(retryDelay)
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

func replayPendingSubmissions(ctx context.Context, rpc *RPCClient) {
	// Use the shared state database connection
	db := getSharedStateDB()
	if db == nil {
		logger.Warn("pending block: shared state db not initialized")
		return
	}

	rows, err := db.Query(`
		SELECT submission_key, timestamp_unix, height, hash, worker, block_hex, rpc_error, rpc_url, payout_addr, status
		FROM pending_submissions
		WHERE LOWER(TRIM(COALESCE(status, ''))) IN ('', 'pending')
	`)
	if err != nil {
		logger.Warn("pending block sqlite query", "error", err)
		return
	}
	defer rows.Close()

	type rowRec struct {
		Key string
		Rec pendingSubmissionRecord
	}
	var pending []rowRec
	for rows.Next() {
		var (
			key      string
			tsUnix   int64
			height   int64
			hash     sql.NullString
			worker   sql.NullString
			blockHex string
			rpcError sql.NullString
			rpcURL   sql.NullString
			payout   sql.NullString
			status   string
		)
		if err := rows.Scan(&key, &tsUnix, &height, &hash, &worker, &blockHex, &rpcError, &rpcURL, &payout, &status); err != nil {
			continue
		}
		blockHex = strings.TrimSpace(blockHex)
		if blockHex == "" {
			continue
		}
		rec := pendingSubmissionRecord{
			Height:     height,
			Hash:       strings.TrimSpace(hash.String),
			Worker:     strings.TrimSpace(worker.String),
			BlockHex:   blockHex,
			RPCError:   strings.TrimSpace(rpcError.String),
			RPCURL:     strings.TrimSpace(rpcURL.String),
			PayoutAddr: strings.TrimSpace(payout.String),
			Status:     strings.TrimSpace(status),
		}
		if tsUnix > 0 {
			rec.Timestamp = time.Unix(tsUnix, 0).UTC()
		}
		key = strings.TrimSpace(key)
		if key == "" {
			key = strings.TrimSpace(rec.Hash)
			if key == "" {
				key = rec.BlockHex
			}
		}
		if key == "" {
			continue
		}
		pending = append(pending, rowRec{Key: key, Rec: rec})
	}
	if err := rows.Err(); err != nil {
		logger.Warn("pending block sqlite rows", "error", err)
		return
	}
	if len(pending) == 0 {
		return
	}

	for _, item := range pending {
		// Keep the claim cleanup scoped to one row. If a future early return or
		// an unexpected panic exits this attempt, the row becomes replayable
		// again instead of remaining submitting until process restart.
		func() {
			rec := item.Rec
			// Respect shutdown signals between attempts.
			select {
			case <-ctx.Done():
				return
			default:
			}

			now := time.Now()
			if !pendingReplayBackoff.allow(item.Key, now) {
				return
			}
			// Atomically claim the row before making the RPC call. This prevents
			// overlapping replay scans from submitting the same block and makes the
			// durable submitting state unavailable to the periodic scanner.
			claimed, err := transitionPendingSubmissionStatus(item.Key, pendingSubmissionStatusPending, pendingSubmissionStatusSubmitting, rec.RPCError)
			if err != nil {
				logger.Warn("pending submission claim failed", "key", item.Key, "height", rec.Height, "hash", rec.Hash, "error", err)
				return
			}
			if !claimed {
				return
			}
			attemptFinished := false
			defer func() {
				if attemptFinished {
					return
				}
				if changed, updateErr := transitionPendingSubmissionStatus(item.Key, pendingSubmissionStatusSubmitting, pendingSubmissionStatusPending, "pending submitblock replay interrupted"); updateErr != nil {
					logger.Warn("pending submission interrupted replay state update failed", "key", item.Key, "height", rec.Height, "hash", rec.Hash, "error", updateErr)
				} else if !changed {
					logger.Warn("pending submission interrupted replay state changed unexpectedly", "key", item.Key, "height", rec.Height, "hash", rec.Hash)
				}
			}()

			var submitRes any
			// Bound each submitblock call so a slow or unresponsive node
			// doesn't block shutdown or delay retries for other entries.
			parent := ctx
			if parent == nil {
				parent = context.Background()
			}
			callCtx, cancel := context.WithTimeout(parent, 30*time.Second)
			err = rpc.callCtx(callCtx, "submitblock", []any{rec.BlockHex}, &submitRes)
			cancel()
			if err == nil {
				err = submitBlockResultError(&submitRes)
			}
			if err != nil {
				changed, updateErr := transitionPendingSubmissionStatus(item.Key, pendingSubmissionStatusSubmitting, pendingSubmissionStatusPending, err.Error())
				attemptFinished = updateErr == nil && changed
				if updateErr != nil {
					logger.Warn("pending submission retry state update failed", "key", item.Key, "height", rec.Height, "hash", rec.Hash, "error", updateErr)
				} else if !changed {
					logger.Warn("pending submission retry state changed unexpectedly", "key", item.Key, "height", rec.Height, "hash", rec.Hash)
				}
				retryIn := pendingReplayBackoff.fail(item.Key, time.Now())
				logger.Error("pending submitblock error", "height", rec.Height, "hash", rec.Hash, "error", err, "retry_in", retryIn)
				if rpc.metrics != nil {
					rpc.metrics.RecordErrorEvent("pending_submit", err.Error(), time.Now())
				}
				return
			}
			logger.Info("pending block submitted", "height", rec.Height, "hash", rec.Hash)
			pendingReplayBackoff.reset(item.Key)
			changed, updateErr := transitionPendingSubmissionStatus(item.Key, pendingSubmissionStatusSubmitting, pendingSubmissionStatusSubmitted, "")
			attemptFinished = updateErr == nil && changed
			if updateErr != nil {
				logger.Warn("pending submission status update failed", "key", item.Key, "height", rec.Height, "hash", rec.Hash, "error", updateErr)
			} else if !changed {
				logger.Warn("pending submission status changed unexpectedly", "key", item.Key, "height", rec.Height, "hash", rec.Hash)
			}
		}()
	}
}

func pendingSubmissionKey(rec pendingSubmissionRecord) string {
	key := strings.TrimSpace(rec.Hash)
	if key == "" {
		key = strings.TrimSpace(rec.BlockHex)
	}
	return strings.TrimSpace(key)
}

func appendPendingSubmissionRecord(rec pendingSubmissionRecord) error {
	return appendPendingSubmissionRecordCtx(context.Background(), rec)
}

func appendPendingSubmissionRecordCtx(ctx context.Context, rec pendingSubmissionRecord) error {
	// Use the shared state database connection
	db := getSharedStateDB()
	if db == nil {
		logger.Warn("pending block: shared state db not initialized")
		return fmt.Errorf("shared state db not initialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	key := pendingSubmissionKey(rec)
	if key == "" {
		return fmt.Errorf("pending submission key is empty")
	}
	blockHex := strings.TrimSpace(rec.BlockHex)
	if blockHex == "" {
		return fmt.Errorf("pending submission block is empty")
	}
	status := strings.TrimSpace(rec.Status)
	if status == "" {
		status = pendingSubmissionStatusPending
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO pending_submissions (
			submission_key, timestamp_unix, height, hash, worker, block_hex, rpc_error, rpc_url, payout_addr, status
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(submission_key) DO UPDATE SET
			timestamp_unix = excluded.timestamp_unix,
			height = excluded.height,
			hash = excluded.hash,
			worker = excluded.worker,
			block_hex = excluded.block_hex,
			rpc_error = excluded.rpc_error,
			rpc_url = excluded.rpc_url,
			payout_addr = excluded.payout_addr,
			status = excluded.status
	`, key, unixOrZero(rec.Timestamp), rec.Height, strings.TrimSpace(rec.Hash), strings.TrimSpace(rec.Worker), blockHex,
		strings.TrimSpace(rec.RPCError), strings.TrimSpace(rec.RPCURL), strings.TrimSpace(rec.PayoutAddr), status); err != nil {
		logger.Warn("pending block sqlite upsert", "error", err)
		return err
	}
	return nil
}

// transitionPendingSubmissionStatus performs a compare-and-transition update.
// The comparison keeps the initial submitter and the periodic replayer from
// overwriting one another's terminal state.
func transitionPendingSubmissionStatus(key, fromStatus, toStatus, rpcError string) (bool, error) {
	db := getSharedStateDB()
	if db == nil {
		return false, fmt.Errorf("shared state db not initialized")
	}
	key = strings.TrimSpace(key)
	fromStatus = strings.TrimSpace(fromStatus)
	toStatus = strings.TrimSpace(toStatus)
	if key == "" || fromStatus == "" || toStatus == "" {
		return false, fmt.Errorf("invalid pending submission state transition")
	}
	res, err := db.Exec(`
		UPDATE pending_submissions
		SET status = ?, rpc_error = ?
		WHERE submission_key = ? AND (
			LOWER(TRIM(COALESCE(status, ''))) = LOWER(?)
			OR (LOWER(?) = 'pending' AND TRIM(COALESCE(status, '')) = '')
		)
	`, toStatus, strings.TrimSpace(rpcError), key, fromStatus, fromStatus)
	if err != nil {
		return false, err
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return changed == 1, nil
}

// reclaimSubmittingPendingSubmissions makes initial submissions interrupted by
// a previous process eligible for replay. It must run before mining listeners
// can create submitting rows for the current process.
func reclaimSubmittingPendingSubmissions(ctx context.Context) error {
	db := getSharedStateDB()
	if db == nil {
		return fmt.Errorf("shared state db not initialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE pending_submissions
		SET status = 'pending',
			rpc_error = CASE
				WHEN TRIM(COALESCE(rpc_error, '')) = '' THEN 'initial submitblock outcome interrupted by process exit'
				ELSE rpc_error
			END
		WHERE LOWER(TRIM(status)) = 'submitting'
	`); err != nil {
		return err
	}
	return nil
}
