package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/gopkg/util/logger"
)

// pendingSubmissionRecord mirrors the JSON structure written to
// pending_submissions.jsonl. Older entries may omit Status; in that case,
// they are treated as "pending" unless a newer record for the same hash
// marks them as "submitted".
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

var pendingLogMu sync.Mutex

func pendingSubmissionsPath(cfg Config) string {
	dir := cfg.DataDir
	if dir == "" {
		dir = defaultDataDir
	}
	return filepath.Join(dir, "pending_submissions.jsonl")
}

// startPendingSubmissionReplayer periodically scans pending_submissions.jsonl
// and attempts to resubmit any entries that are still marked as pending.
// On successful submitblock, it appends a "submitted" record so future scans
// skip that block. This is best-effort and does not guarantee eventual
// submission, but provides a recovery path when the node RPC was down.
func startPendingSubmissionReplayer(ctx context.Context, cfg Config, rpc *RPCClient) {
	if rpc == nil {
		return
	}
	path := pendingSubmissionsPath(cfg)
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
				replayPendingSubmissions(ctx, rpc, path)
			}
		}
	}()
}

func replayPendingSubmissions(ctx context.Context, rpc *RPCClient, path string) {
	f, err := os.Open(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logger.Warn("pending block open", "error", err)
		}
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// Allow reasonably large lines in case of large blocks.
	const maxLine = 4 * 1024 * 1024
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, maxLine)

	// For each unique block (keyed by hash or block hex), keep only the
	// last record so that later "submitted" entries override earlier
	// "pending" ones.
	byKey := make(map[string]pendingSubmissionRecord)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var rec pendingSubmissionRecord
		if err := fastJSONUnmarshal(line, &rec); err != nil {
			continue
		}
		key := strings.TrimSpace(rec.Hash)
		if key == "" {
			key = strings.TrimSpace(rec.BlockHex)
		}
		if key == "" {
			continue
		}
		byKey[key] = rec
	}
	if err := scanner.Err(); err != nil {
		logger.Warn("pending block scan", "error", err)
	}
	if len(byKey) == 0 {
		return
	}

	for _, rec := range byKey {
		if strings.EqualFold(rec.Status, "submitted") {
			continue
		}
		if rec.BlockHex == "" {
			continue
		}
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
		// Append a "submitted" record so later scans know this block no
		// longer needs retry.
		rec.Status = "submitted"
		rec.RPCError = ""
		appendPendingSubmissionRecord(path, rec)
	}
}

func appendPendingSubmissionRecord(path string, rec pendingSubmissionRecord) {
	data, err := fastJSONMarshal(rec)
	if err != nil {
		logger.Warn("pending block status marshal", "error", err)
		return
	}

	pendingLogMu.Lock()
	defer pendingLogMu.Unlock()

	// Ensure the directory exists
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		logger.Warn("pending block status mkdir", "error", err)
		return
	}

	// Open file in append mode with O_CREATE flag
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		logger.Warn("pending block status open", "error", err)
		return
	}
	defer f.Close()

	// Write the JSON record followed by a newline
	if _, err := f.Write(data); err != nil {
		logger.Warn("pending block status write", "error", err)
		return
	}
	if _, err := f.Write([]byte("\n")); err != nil {
		logger.Warn("pending block status write newline", "error", err)
		return
	}

	// Sync to ensure the data is persisted
	if err := f.Sync(); err != nil {
		logger.Warn("pending block status sync", "error", err)
	}
}
