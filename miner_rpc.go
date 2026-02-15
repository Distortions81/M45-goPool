package main

import (
	"context"
	"errors"
	"time"
)

// submitBlockWithFastRetry aggressively retries submitblock without backoff
// to maximize the chance of winning the propagation race. It retries every
// 100ms until either submitblock succeeds, a newer job height is observed,
// or a safety window elapses.
func (mc *MinerConn) submitBlockWithFastRetry(job *Job, workerName, hashHex, blockHex string, submitRes *any) error {
	const (
		retryInterval = 100 * time.Millisecond
		// rpcCallTimeout bounds each individual RPC call so a hung bitcoind
		// doesn't block the retry loop indefinitely.
		rpcCallTimeout = 5 * time.Second
		// confirmTimeout bounds getblockheader checks used to detect cases where
		// submitblock may have succeeded but the RPC response timed out.
		confirmTimeout = 2 * time.Second
		// maxRetryWindow is a final safety cap; in practice we expect to
		// stop much sooner when a new block is seen. Using a full block
		// interval keeps us racing hard for rare finds.
		maxRetryWindow = 10 * time.Minute
	)

	start := time.Now()
	attempt := 0
	var lastErr error

	blockAccepted := func() bool {
		if mc.rpc == nil || hashHex == "" {
			return false
		}
		// If bitcoind is overloaded, it's possible for submitblock to
		// succeed server-side while our client-side context times out.
		// Confirm by checking whether the block hash is now known.
		var header struct {
			Confirmations int64 `json:"confirmations"`
		}
		ctx, cancel := context.WithTimeout(context.Background(), confirmTimeout)
		err := mc.rpc.callCtx(ctx, "getblockheader", []any{hashHex, true}, &header)
		cancel()
		// Only treat as success when the block is in the best chain.
		// (Orphaned blocks can still be "known" but will have confirmations=-1.)
		return err == nil && header.Confirmations >= 1
	}

	for {
		attempt++

		// Use a per-call timeout to prevent indefinite hangs on unresponsive RPC.
		// The retry loop continues regardless; we just don't want one call to block forever.
		callCtx, cancel := context.WithTimeout(context.Background(), rpcCallTimeout)
		err := mc.rpc.callCtx(callCtx, "submitblock", []any{blockHex}, submitRes)
		cancel()

		if err == nil {
			if attempt > 1 {
				logger.Info("submitblock succeeded after retries",
					"attempts", attempt,
					"worker", mc.minerName(workerName),
					"hash", hashHex,
				)
			}
			return nil
		}
		lastErr = err

		// If submitblock timed out client-side, check whether the block was
		// accepted anyway. This commonly happens when bitcoind's RPC work
		// queue is saturated.
		if errors.Is(err, context.DeadlineExceeded) && blockAccepted() {
			logger.Warn("submitblock timed out but block is in chain; treating as success",
				"attempts", attempt,
				"worker", mc.minerName(workerName),
				"hash", hashHex,
			)
			return nil
		}

		// Log the first failure loudly; subsequent failures are summarized
		// when we eventually give up.
		if attempt == 1 {
			logger.Error("submitblock error; retrying aggressively",
				"error", err,
				"worker", mc.minerName(workerName),
				"hash", hashHex,
			)
		}

		// If we've already seen a newer template height, there's no point
		// continuing to spam submitblock for this block.
		if mc.jobMgr != nil && job != nil {
			if cur := mc.jobMgr.CurrentJob(); cur != nil && cur.Template.Height > job.Template.Height {
				logger.Warn("submitblock giving up after new block seen",
					"original_height", job.Template.Height,
					"current_height", cur.Template.Height,
					"attempts", attempt,
					"error", err,
				)
				return err
			}
		}

		// Safety stop: avoid spinning forever if the node is persistently
		// unreachable or rejects the block.
		if time.Since(start) >= maxRetryWindow {
			logger.Error("submitblock giving up after retry window",
				"attempts", attempt,
				"duration", time.Since(start),
				"error", lastErr,
			)
			return lastErr
		}

		time.Sleep(retryInterval)
	}
}
