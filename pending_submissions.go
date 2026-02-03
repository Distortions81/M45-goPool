package main

import (
	"context"
	"database/sql"
	"strings"
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

// startPendingSubmissionReplayer periodically scans the pending_submissions
// SQLite table and attempts to resubmit any entries that are still marked as
// pending. On successful submitblock, it marks the row as "submitted" so
// future scans skip that block. This is best-effort and does not guarantee
// eventual submission, but provides a recovery path when the node RPC was down.
func startPendingSubmissionReplayer(ctx context.Context, rpc *RPCClient) {
	if rpc == nil {
		return
	}
	// Use a short but modest interval; blocks are rare and we don't want to
	// hammer the node when it's unhealthy, but we also want to resubmit
	// quickly once RPC is back.
	const interval = 5 * time.Second

	go func() {
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
		if strings.EqualFold(strings.TrimSpace(status), "submitted") {
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
		rec := item.Rec
		// Respect shutdown signals between attempts.
		select {
		case <-ctx.Done():
			return
		default:
		}

		var submitRes interface{}
		// Bound each submitblock call so a slow or unresponsive node
		// doesn't block shutdown or delay retries for other entries.
		parent := ctx
		if parent == nil {
			parent = context.Background()
		}
		callCtx, cancel := context.WithTimeout(parent, 30*time.Second)
		err := rpc.callCtx(callCtx, "submitblock", []interface{}{rec.BlockHex}, &submitRes)
		cancel()
		if err != nil {
			logger.Error("pending submitblock error", "height", rec.Height, "hash", rec.Hash, "error", err)
			continue
		}
		logger.Info("pending block submitted", "height", rec.Height, "hash", rec.Hash)
		_, _ = db.Exec("UPDATE pending_submissions SET status = 'submitted', rpc_error = '' WHERE submission_key = ?", item.Key)
	}
}

func appendPendingSubmissionRecord(rec pendingSubmissionRecord) {
	// Use the shared state database connection
	db := getSharedStateDB()
	if db == nil {
		logger.Warn("pending block: shared state db not initialized")
		return
	}

	key := strings.TrimSpace(rec.Hash)
	if key == "" {
		key = strings.TrimSpace(rec.BlockHex)
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	blockHex := strings.TrimSpace(rec.BlockHex)
	if blockHex == "" {
		return
	}
	status := strings.TrimSpace(rec.Status)
	if status == "" {
		status = "pending"
	}
	if _, err := db.Exec(`
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
	}
}
