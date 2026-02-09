package main

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"time"
)

// handleBlockShare processes a share that satisfies the network target. It
// builds the full block (reusing any dual-payout header/coinbase when
// available), submits it via RPC, logs the reward split and found-block
// record, and sends the final Stratum response.
func (mc *MinerConn) handleBlockShare(reqID interface{}, job *Job, workerName string, en2 []byte, ntime string, nonce string, useVersion uint32, hashHex string, shareDiff float64, now time.Time) {
	var (
		blockHex  string
		submitRes interface{}
		err       error
	)
	scriptTime := mc.scriptTimeForJob(job.JobID, job.ScriptTime)

	// Only construct the full block (including all non-coinbase transactions)
	// when the share actually satisfies the network target.
	if poolScript, workerScript, totalValue, feePercent, ok := mc.dualPayoutParams(job, workerName); ok {
		var cbTx, cbTxid []byte
		var err error
		if job.OperatorDonationPercent > 0 && len(job.DonationScript) > 0 {
			cbTx, cbTxid, err = serializeTripleCoinbaseTxPredecoded(
				job.Template.Height,
				mc.extranonce1,
				en2,
				job.TemplateExtraNonce2Size,
				poolScript,
				job.DonationScript,
				workerScript,
				totalValue,
				feePercent,
				job.OperatorDonationPercent,
				job.witnessCommitScript,
				job.coinbaseFlagsBytes,
				job.CoinbaseMsg,
				scriptTime,
			)
		} else {
			cbTx, cbTxid, err = serializeDualCoinbaseTxPredecoded(
				job.Template.Height,
				mc.extranonce1,
				en2,
				job.TemplateExtraNonce2Size,
				poolScript,
				workerScript,
				totalValue,
				feePercent,
				job.witnessCommitScript,
				job.coinbaseFlagsBytes,
				job.CoinbaseMsg,
				scriptTime,
			)
		}
		if err == nil && len(cbTxid) == 32 {
			merkleRoot := computeMerkleRootFromBranches(cbTxid, job.MerkleBranches)
			header, err := job.buildBlockHeader(merkleRoot, ntime, nonce, int32(useVersion))
			if err == nil {
				var buf bytes.Buffer

				buf.Write(header)
				writeVarInt(&buf, uint64(1+len(job.Transactions)))
				buf.Write(cbTx)
				for _, tx := range job.Transactions {
					raw, derr := hex.DecodeString(tx.Data)
					if derr != nil {
						err = fmt.Errorf("decode tx data: %w", derr)
						break
					}
					buf.Write(raw)
				}
				if err == nil {
					blockHex = hex.EncodeToString(buf.Bytes())
				}
			}
		}
	}
	if blockHex == "" {
		// Fallback to single-output block build if dual-payout params are
		// unavailable or any step fails. This reuses the existing helper that
		// constructs a canonical block for submission.
		blockHex, _, _, _, err = buildBlockWithScriptTime(job, mc.extranonce1, en2, ntime, nonce, int32(useVersion), mc.singlePayoutScript(job, workerName), scriptTime)
		if err != nil {
			if mc.metrics != nil {
				mc.metrics.RecordBlockSubmission("error")
				mc.metrics.RecordErrorEvent("submitblock", err.Error(), now)
			}
			logger.Error("submitblock build error", "remote", mc.id, "error", err)
			mc.writeResponse(StratumResponse{ID: reqID, Result: false, Error: newStratumError(20, err.Error())})
			return
		}
	}

	// Submit the block via RPC using an aggressive, no-backoff retry loop
	// so we race the rest of the network as hard as possible. This path is
	// intentionally not tied to the miner or process context so shutdown
	// signals do not cancel in-flight submissions.
	err = mc.submitBlockWithFastRetry(job, workerName, hashHex, blockHex, &submitRes)
	if err != nil {
		if mc.metrics != nil {
			mc.metrics.RecordBlockSubmission("error")
			mc.metrics.RecordErrorEvent("submitblock", err.Error(), time.Now())
		}
		logger.Error("submitblock error", "error", err)
		// Best-effort: record this block for manual or future retry when the
		// node RPC is unavailable or submitblock fails. This does not imply
		// that the block was accepted; it only preserves the data needed for
		// a later submitblock attempt.
		mc.logPendingSubmission(job, workerName, hashHex, blockHex, err)
		mc.writeResponse(StratumResponse{ID: reqID, Result: false, Error: newStratumError(20, err.Error())})
		return
	}
	if mc.metrics != nil {
		mc.metrics.RecordBlockSubmission("accepted")
	}

	// For solo mining, treat the worker that submitted the block as the
	// beneficiary of the block reward. We always split the reward between
	// the pool fee and worker payout for logging purposes.
	if logger.Enabled(logLevelInfo) && workerName != "" && job != nil && job.CoinbaseValue > 0 {
		total := job.CoinbaseValue
		feePct := mc.cfg.PoolFeePercent
		if feePct < 0 {
			feePct = 0
		}
		if feePct > 99.99 {
			feePct = 99.99
		}
		poolFee := int64(math.Round(float64(total) * feePct / 100.0))
		if poolFee < 0 {
			poolFee = 0
		}
		if poolFee > total {
			poolFee = total
		}
		minerAmt := total - poolFee
		if minerAmt > 0 {
			logger.Info("block reward split",
				"miner", mc.minerName(workerName),
				"worker_address", workerName,
				"height", job.Template.Height,
				"block_value_sats", total,
				"pool_fee_sats", poolFee,
				"worker_payout_sats", minerAmt,
				"fee_percent", feePct,
			)
		}
	}

	var stats MinerStats
	if logger.Enabled(logLevelInfo) {
		stats = mc.snapshotStats()
	}
	mc.logFoundBlock(job, workerName, hashHex, shareDiff)
	if logger.Enabled(logLevelInfo) {
		logger.Info("block found",
			"miner", mc.minerName(workerName),
			"height", job.Template.Height,
			"hash", hashHex,
			"accepted_total", stats.Accepted,
			"rejected_total", stats.Rejected,
			"worker_difficulty", stats.TotalDifficulty,
		)
	}
	mc.writeTrueResponse(reqID)
}

// logFoundBlock appends a JSON line describing a found block to a log file in
// the data directory. This is purely for operator audit/debugging and is best
// effort; failures are logged but do not affect pool operation.
func (mc *MinerConn) logFoundBlock(job *Job, worker, hashHex string, shareDiff float64) {
	dir := mc.cfg.DataDir
	if dir == "" {
		dir = defaultDataDir
	}
	workerName := mc.minerName(worker)
	now := time.Now().UTC()
	// Compute a simple view of the payout split used for this block. In
	// dual-payout mode with a validated worker script, the coinbase uses a
	// pool-fee + worker output; otherwise the entire reward is logically
	// treated as a worker payout in single mode, or sent to the pool in
	// dual-payout fallback cases.
	total := job.Template.CoinbaseValue
	feePct := mc.cfg.PoolFeePercent
	if feePct < 0 {
		feePct = 0
	}
	if feePct > 99.99 {
		feePct = 99.99
	}
	poolFee := int64(math.Round(float64(total) * feePct / 100.0))
	if poolFee < 0 {
		poolFee = 0
	}
	if poolFee > total {
		poolFee = total
	}
	workerAmt := total - poolFee
	// If dual payout is disabled, treat the full reward as a worker payout
	// ("Single" mode = miner only). When dual payout is enabled but the
	// worker has no cached script or the worker wallet equals the pool
	// payout address, treat this block as pool-only and record the full
	// amount as pool_fee_sats with dual_payout_fallback=true.
	dualFallback := false
	workerAddr := ""
	if worker != "" {
		raw := strings.TrimSpace(worker)
		if parts := strings.SplitN(raw, ".", 2); len(parts) > 1 {
			raw = parts[0]
		}
		workerAddr = sanitizePayoutAddress(raw)
	}
	// Check if we fell back to single-output coinbase (worker wallet matches pool wallet)
	if len(mc.workerPayoutScript(worker)) == 0 || (workerAddr != "" && strings.EqualFold(workerAddr, mc.cfg.PayoutAddress)) {
		poolFee = total
		workerAmt = 0
		dualFallback = true
	}

	rec := map[string]interface{}{
		"timestamp":            now,
		"height":               job.Template.Height,
		"hash":                 hashHex,
		"worker":               workerName,
		"share_diff":           shareDiff,
		"job_id":               job.JobID,
		"payout_address":       mc.cfg.PayoutAddress,
		"coinbase_value_sats":  total,
		"pool_fee_sats":        poolFee,
		"worker_payout_sats":   workerAmt,
		"dual_payout_fallback": dualFallback,
	}
	data, err := fastJSONMarshal(rec)
	if err != nil {
		logger.Warn("found block log marshal", "error", err)
		return
	}
	line := append(data, '\n')
	select {
	case foundBlockLogCh <- foundBlockLogEntry{Dir: dir, Line: line}:
	default:
		// If the queue is full, drop the log entry rather than blocking
		// the submit path; this log is best-effort operator metadata.
		logger.Warn("found block log queue full; dropping entry")
	}

	mc.notifyDiscordFoundBlock(workerName, job.Template.Height, hashHex, now)
}

func (mc *MinerConn) notifyDiscordFoundBlock(worker string, height int64, hashHex string, now time.Time) {
	if mc == nil || mc.discordNotifier == nil {
		return
	}
	mc.discordNotifier.NotifyFoundBlock(worker, height, hashHex, now)
}

// logPendingSubmission appends a JSON line describing a block that failed
// submitblock to a log file in the data directory. This allows operators to
// manually retry submission with bitcoin-cli or future tooling when the node
// RPC is down or returns an error. It is best effort only.
func (mc *MinerConn) logPendingSubmission(job *Job, worker, hashHex, blockHex string, submitErr error) {
	if job == nil || blockHex == "" {
		return
	}
	rec := pendingSubmissionRecord{
		Timestamp:  time.Now().UTC(),
		Height:     job.Template.Height,
		Hash:       hashHex,
		Worker:     mc.minerName(worker),
		BlockHex:   blockHex,
		RPCError:   submitErr.Error(),
		RPCURL:     mc.cfg.RPCURL,
		PayoutAddr: mc.cfg.PayoutAddress,
		Status:     "pending",
	}
	appendPendingSubmissionRecord(rec)
}
