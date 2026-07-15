package main

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// submitBlockWithFastRetry aggressively retries submitblock without backoff
// to maximize the chance of winning the propagation race. It retries every
// 100ms until either submitblock succeeds or a safety window elapses.
//
// A newer template must not cancel delivery: a block extending a displaced
// branch can add enough work to make that branch the best chain again.
func (mc *MinerConn) submitBlockWithFastRetry(job *Job, workerName, hashHex, blockHex string, submitRes *any) error {
	const (
		retryInterval = 100 * time.Millisecond
		// rpcCallTimeout bounds each individual RPC call so a hung bitcoind
		// doesn't block the retry loop indefinitely.
		rpcCallTimeout = 5 * time.Second
		// confirmTimeout bounds getblockheader checks used to detect cases where
		// submitblock may have succeeded but the RPC response timed out.
		confirmTimeout = 2 * time.Second
		// maxRetryWindow is a final safety cap. Using a full block interval
		// keeps us racing hard for rare finds through transient RPC failures.
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
		if submitRes != nil {
			*submitRes = nil
		}
		callCtx, cancel := context.WithTimeout(context.Background(), rpcCallTimeout)
		err := mc.rpc.callCtx(callCtx, "submitblock", []any{blockHex}, submitRes)
		cancel()

		var attemptErr error
		if err == nil {
			disposition, resultErr := classifySubmitBlockResult(submitRes)
			switch disposition {
			case submitBlockResultAccepted:
				if attempt > 1 {
					logger.Info("submitblock succeeded after retries",
						"attempts", attempt,
						"worker", mc.minerName(workerName),
						"hash", hashHex,
					)
				}
				return nil
			case submitBlockResultRetryable:
				attemptErr = resultErr
			case submitBlockResultRejected:
				if blockAccepted() {
					logger.Warn("submitblock returned rejection but block is in chain; treating as success",
						"attempts", attempt,
						"worker", mc.minerName(workerName),
						"hash", hashHex,
						"result", resultErr.Error(),
					)
					return nil
				}
				return resultErr
			}
		} else {
			attemptErr = err

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
		}
		lastErr = attemptErr

		// Log the first failure loudly; subsequent failures are summarized
		// when we eventually give up.
		if attempt == 1 {
			logger.Error("submitblock error; retrying aggressively",
				"error", attemptErr,
				"worker", mc.minerName(workerName),
				"hash", hashHex,
			)
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

type submitBlockResultDisposition uint8

const (
	submitBlockResultAccepted submitBlockResultDisposition = iota
	submitBlockResultRetryable
	submitBlockResultRejected
)

// classifySubmitBlockResult interprets Bitcoin Core's BIP22 submitblock
// result. A valid block that Core already processed returns "duplicate",
// including when it is on a side chain, so delivery is complete. The two
// inconclusive results do not establish either acceptance or rejection and
// must be retried while the fast submission window remains open.
func classifySubmitBlockResult(submitRes *any) (submitBlockResultDisposition, error) {
	if submitRes == nil || *submitRes == nil {
		return submitBlockResultAccepted, nil
	}
	v, ok := (*submitRes).(string)
	if !ok {
		return submitBlockResultRejected, fmt.Errorf("submitblock returned unexpected result %T: %v", *submitRes, *submitRes)
	}
	switch v {
	case "duplicate":
		return submitBlockResultAccepted, nil
	case "inconclusive", "duplicate-inconclusive":
		return submitBlockResultRetryable, fmt.Errorf("submitblock inconclusive: %s", v)
	case "":
		return submitBlockResultRetryable, fmt.Errorf("submitblock returned empty result")
	default:
		return submitBlockResultRejected, fmt.Errorf("submitblock rejected: %s", v)
	}
}

func submitBlockResultError(submitRes *any) error {
	_, err := classifySubmitBlockResult(submitRes)
	return err
}
