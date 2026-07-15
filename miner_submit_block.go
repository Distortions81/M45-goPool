package main

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math"
	"time"
)

// handleBlockShare processes a share that satisfies the network target. It
// builds the full block (reusing any dual-payout header/coinbase when
// available), submits it via RPC, logs the reward split and found-block
// record, and sends the final Stratum response.
func (mc *MinerConn) handleBlockShare(reqID any, job *Job, stratumJobID string, workerName string, en2 []byte, ntime string, nonce string, useVersion uint32, scriptTime int64, solvedHeader, solvedCoinbase []byte, hashHex string, shareDiff float64, now time.Time) {
	var (
		blockHex  string
		submitRes any
		err       error
	)
	if scriptTime == 0 {
		scriptTime = mc.scriptTimeForJob(stratumJobID, job.ScriptTime)
	}
	if len(solvedHeader) > 0 || len(solvedCoinbase) > 0 {
		blockHex, err = assembleSolvedBlock(job, solvedHeader, solvedCoinbase)
		if err != nil {
			if mc.metrics != nil {
				mc.metrics.RecordBlockSubmission("error")
				mc.metrics.RecordErrorEvent("submitblock", err.Error(), now)
			}
			logger.Error("assemble solved block", "remote", mc.id, "error", err)
			mc.writeResponse(StratumResponse{ID: reqID, Result: false, Error: newStratumError(stratumErrCodeInvalidRequest, err.Error())})
			return
		}
	}

	// Only construct the full block (including all non-coinbase transactions)
	// when the share actually satisfies the network target.
	if blockHex == "" {
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
				var merkleRoot [32]byte
				var merkleOK bool
				if job.merkleBranchesBytes != nil {
					merkleRoot, merkleOK = computeMerkleRootFromBranchesBytes32(cbTxid, job.merkleBranchesBytes)
				} else {
					merkleRoot, merkleOK = computeMerkleRootFromBranches32(cbTxid, job.MerkleBranches)
				}
				if merkleOK {
					header, err := job.buildBlockHeader(merkleRoot[:], ntime, nonce, int32(useVersion))
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
			mc.writeResponse(StratumResponse{ID: reqID, Result: false, Error: newStratumError(stratumErrCodeInvalidRequest, err.Error())})
			return
		}
	}

	// Persist the exact bytes reconstructed from the advertised job before the
	// first submitblock attempt. The submitting state is deliberately excluded
	// from periodic replay; if this process exits mid-attempt, startup changes it
	// back to pending. Persistence is a recovery aid and must never delay an RPC
	// attempt beyond the synchronous SQLite write or prevent submission when the
	// state database is unavailable.
	pendingRec := mc.pendingSubmissionRecord(job, workerName, hashHex, blockHex, "", pendingSubmissionStatusSubmitting)
	pendingKey := pendingSubmissionKey(pendingRec)
	persistErr := appendPendingSubmissionRecord(pendingRec)
	pendingPersisted := persistErr == nil
	if !pendingPersisted {
		spoolRec := pendingRec
		spoolRec.Status = pendingSubmissionStatusPending
		spoolRec.RPCError = persistErr.Error()
		if spoolErr := writePendingSubmissionSpool(mc.cfg.DataDir, spoolRec); spoolErr != nil {
			logger.Error("solved block emergency spool failed",
				"height", job.Template.Height,
				"hash", hashHex,
				"sqlite_error", persistErr,
				"spool_error", spoolErr,
			)
		}
		logger.Warn("solved block persistence failed; submitting immediately",
			"height", job.Template.Height,
			"hash", hashHex,
			"error", persistErr,
		)
	}
	submissionFinished := false
	if pendingPersisted {
		defer func() {
			if submissionFinished {
				return
			}
			// This also runs if an unexpected panic or future early return exits
			// the path after persistence but before an RPC outcome is recorded.
			if changed, updateErr := transitionPendingSubmissionStatus(pendingKey, pendingSubmissionStatusSubmitting, pendingSubmissionStatusPending, "initial submitblock path interrupted"); updateErr != nil {
				logger.Warn("pending submission interruption state update failed", "key", pendingKey, "height", job.Template.Height, "hash", hashHex, "error", updateErr)
			} else if !changed {
				logger.Warn("pending submission interruption state changed unexpectedly", "key", pendingKey, "height", job.Template.Height, "hash", hashHex)
			}
		}()
	}

	// Submit the block via RPC using an aggressive, no-backoff retry loop
	// so we race the rest of the network as hard as possible. This path is
	// intentionally not tied to the miner or process context so shutdown
	// signals do not cancel in-flight submissions.
	err = mc.submitBlockWithFastRetry(job, workerName, hashHex, blockHex, &submitRes)
	if err != nil {
		if pendingPersisted {
			changed, updateErr := transitionPendingSubmissionStatus(pendingKey, pendingSubmissionStatusSubmitting, pendingSubmissionStatusPending, err.Error())
			submissionFinished = updateErr == nil && changed
			if updateErr != nil || !changed {
				logger.Warn("pending submission failure state update failed", "key", pendingKey, "height", job.Template.Height, "hash", hashHex, "changed", changed, "error", updateErr)
				// Retry the durable write with the complete record in case the
				// compare-and-transition failed for a transient database reason.
				fallbackRec := mc.pendingSubmissionRecord(job, workerName, hashHex, blockHex, err.Error(), pendingSubmissionStatusPending)
				if fallbackErr := appendPendingSubmissionRecord(fallbackRec); fallbackErr != nil {
					logger.Warn("pending submission failure fallback write failed", "key", pendingKey, "height", job.Template.Height, "hash", hashHex, "error", fallbackErr)
				} else {
					submissionFinished = true
				}
			}
		}
		if mc.metrics != nil {
			mc.metrics.RecordBlockSubmission("error")
			mc.metrics.RecordErrorEvent("submitblock", err.Error(), time.Now())
		}
		logger.Error("submitblock error", "error", err)
		// Best-effort: record this block for manual or future retry when the
		// node RPC is unavailable or submitblock fails. This does not imply
		// that the block was accepted; it only preserves the data needed for
		// a later submitblock attempt.
		if !pendingPersisted {
			spoolRec := mc.pendingSubmissionRecord(job, workerName, hashHex, blockHex, err.Error(), pendingSubmissionStatusPending)
			if spoolErr := writePendingSubmissionSpool(mc.cfg.DataDir, spoolRec); spoolErr != nil {
				logger.Error("failed block emergency spool update failed", "height", job.Template.Height, "hash", hashHex, "error", spoolErr)
			}
			if fallbackErr := mc.logPendingSubmission(job, workerName, hashHex, blockHex, err); fallbackErr == nil {
				if spoolErr := removePendingSubmissionSpool(mc.cfg.DataDir, blockHex); spoolErr != nil {
					logger.Warn("failed block emergency spool cleanup failed", "height", job.Template.Height, "hash", hashHex, "error", spoolErr)
				}
			}
		}
		mc.writeResponse(StratumResponse{ID: reqID, Result: false, Error: newStratumError(stratumErrCodeInvalidRequest, err.Error())})
		return
	}
	if pendingPersisted {
		changed, updateErr := transitionPendingSubmissionStatus(pendingKey, pendingSubmissionStatusSubmitting, pendingSubmissionStatusSubmitted, "")
		submissionFinished = updateErr == nil && changed
		if updateErr != nil {
			logger.Warn("pending submission success state update failed", "key", pendingKey, "height", job.Template.Height, "hash", hashHex, "error", updateErr)
		} else if !changed {
			logger.Warn("pending submission success state changed unexpectedly", "key", pendingKey, "height", job.Template.Height, "hash", hashHex)
		}
		if !submissionFinished {
			fallbackRec := mc.pendingSubmissionRecord(job, workerName, hashHex, blockHex, "", pendingSubmissionStatusSubmitted)
			if fallbackErr := appendPendingSubmissionRecord(fallbackRec); fallbackErr != nil {
				logger.Warn("pending submission success fallback write failed", "key", pendingKey, "height", job.Template.Height, "hash", hashHex, "error", fallbackErr)
			} else {
				submissionFinished = true
			}
		}
	}
	if !pendingPersisted {
		acceptedRec := mc.pendingSubmissionRecord(job, workerName, hashHex, blockHex, "", pendingSubmissionStatusSubmitted)
		if acceptedPersistErr := appendPendingSubmissionRecord(acceptedRec); acceptedPersistErr != nil {
			// Keep the pending spool until SQLite owns the exact accepted bytes.
			// A startup duplicate submission is safe even across a side-chain reorg.
			logger.Warn("accepted block sqlite fallback failed; retaining emergency spool", "height", job.Template.Height, "hash", hashHex, "error", acceptedPersistErr)
		} else if spoolErr := removePendingSubmissionSpool(mc.cfg.DataDir, blockHex); spoolErr != nil {
			logger.Warn("accepted block emergency spool cleanup failed", "height", job.Template.Height, "hash", hashHex, "error", spoolErr)
		}
	}
	if mc.metrics != nil {
		mc.metrics.RecordBlockSubmission("accepted")
	}

	// For solo mining, treat the worker that submitted the block as the
	// beneficiary of the block reward. We always split the reward between
	// the pool fee and worker payout for logging purposes.
	if logger.Enabled(logLevelInfo) && workerName != "" && job != nil && job.CoinbaseValue > 0 {
		total := job.CoinbaseValue
		feePct, _ := mc.jobPayoutPolicy(job)
		if feePct < 0 {
			feePct = 0
		}
		if feePct > 99.99 {
			feePct = 99.99
		}
		poolFee := max(int64(math.Round(float64(total)*feePct/100.0)), 0)
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

func assembleSolvedBlock(job *Job, header, coinbase []byte) (string, error) {
	if job == nil {
		return "", fmt.Errorf("missing job for solved block")
	}
	if len(header) != 80 {
		return "", fmt.Errorf("solved block header must be 80 bytes, got %d", len(header))
	}
	if len(coinbase) == 0 {
		return "", fmt.Errorf("solved block coinbase is empty")
	}

	var buf bytes.Buffer
	buf.Write(header)
	writeVarInt(&buf, uint64(1+len(job.Transactions)))
	buf.Write(coinbase)
	for _, tx := range job.Transactions {
		raw, err := hex.DecodeString(tx.Data)
		if err != nil {
			return "", fmt.Errorf("decode tx data: %w", err)
		}
		buf.Write(raw)
	}
	return hex.EncodeToString(buf.Bytes()), nil
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
	feePct, payoutAddress := mc.jobPayoutPolicy(job)
	if feePct < 0 {
		feePct = 0
	}
	if feePct > 99.99 {
		feePct = 99.99
	}
	poolFee := max(int64(math.Round(float64(total)*feePct/100.0)), 0)
	if poolFee > total {
		poolFee = total
	}
	workerAmt := total - poolFee
	// If dual payout is disabled, treat the full reward as a worker payout
	// ("Single" mode = miner only). When dual payout is enabled but the
	// worker has no cached script or the worker and pool scripts resolve to
	// the same beneficiary, treat this block as pool-only and record the full
	// amount as pool_fee_sats with dual_payout_fallback=true.
	dualFallback := false
	workerScript := mc.workerPayoutScript(worker)
	// Check if we fell back to a single-output coinbase because no worker
	// script was available or both outputs had the same decoded beneficiary.
	if len(workerScript) == 0 || bytes.Equal(workerScript, job.PayoutScript) {
		poolFee = total
		workerAmt = 0
		dualFallback = true
	}

	rec := map[string]any{
		"timestamp":            now,
		"height":               job.Template.Height,
		"hash":                 hashHex,
		"worker":               workerName,
		"share_diff":           shareDiff,
		"job_id":               job.JobID,
		"payout_address":       payoutAddress,
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
func (mc *MinerConn) logPendingSubmission(job *Job, worker, hashHex, blockHex string, submitErr error) error {
	if job == nil || blockHex == "" {
		return fmt.Errorf("pending submission is missing job or block")
	}
	rec := mc.pendingSubmissionRecord(job, worker, hashHex, blockHex, submitErr.Error(), pendingSubmissionStatusPending)
	return appendPendingSubmissionRecord(rec)
}

func (mc *MinerConn) pendingSubmissionRecord(job *Job, worker, hashHex, blockHex, rpcError, status string) pendingSubmissionRecord {
	_, payoutAddress := mc.jobPayoutPolicy(job)
	return pendingSubmissionRecord{
		Timestamp:  time.Now().UTC(),
		Height:     job.Template.Height,
		Hash:       hashHex,
		Worker:     mc.minerName(worker),
		BlockHex:   blockHex,
		RPCError:   rpcError,
		RPCURL:     mc.cfg.RPCURL,
		PayoutAddr: payoutAddress,
		Status:     status,
	}
}
