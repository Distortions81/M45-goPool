package main

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math"
	"math/bits"
	"strconv"
	"time"
)

// Handle mining.subscribe request.
// Very minimal: return fake subscription and extranonce1/size per docs/protocols/stratum-v1.mediawiki.
func (mc *MinerConn) handleSubscribe(req *StratumRequest) {
	// Many miners send a client identifier as the first subscribe parameter.
	// Capture it so we can summarize miner types on the status page.
	if len(req.Params) > 0 {
		if id, ok := req.Params[0].(string); ok {
			// Validate client ID length to prevent abuse
			if len(id) > maxMinerClientIDLen {
				logger.Warn("subscribe rejected: client identifier too long", "remote", mc.id, "len", len(id))
				mc.writeResponse(StratumResponse{
					ID:     req.ID,
					Result: nil,
					Error:  newStratumError(20, "client identifier too long"),
				})
				mc.Close("client identifier too long")
				return
			}
			if id != "" {
				mc.minerType = id
				// Best-effort split into name/version for nicer aggregation.
				name, ver := parseMinerID(id)
				if name != "" {
					mc.minerClientName = name
				}
				if ver != "" {
					mc.minerClientVersion = ver
				}
			}
		}
	}

	mc.subscribed = true

	// Result spec (simplified):
	// [
	//   [ ["mining.set_difficulty", "1"], ["mining.notify", "1"] ],
	//   "extranonce1",
	//   extranonce2_size
	// ]
	ex1 := hex.EncodeToString(mc.extranonce1)
	en2Size := mc.cfg.Extranonce2Size
	if en2Size <= 0 {
		en2Size = 4
	}

	var initialJob *Job
	select {
	case job := <-mc.jobCh:
		initialJob = job
	default:
		initialJob = mc.jobMgr.CurrentJob()
	}

	if !mc.listenerOn {
		mc.listenerOn = true
		// Goroutine lifecycle: listenJobs reads from mc.jobCh until the channel is closed.
		// Channel is closed via mc.jobMgr.Unsubscribe(mc.jobCh) in cleanup().
		go mc.listenJobs()
	}

	mc.writeResponse(StratumResponse{
		ID: req.ID,
		Result: []interface{}{
			[][]interface{}{
				{"mining.set_difficulty", "1"},
				{"mining.notify", "1"},
			},
			ex1,
			en2Size,
		},
		Error: nil,
	})
	if initialJob != nil {
		mc.updateVersionMask(initialJob.VersionMask)
	}
	mc.sendSetExtranonce(ex1, en2Size)
	if initialJob == nil {
		status := mc.jobMgr.FeedStatus()
		fields := []interface{}{"remote", mc.id, "reason", "no job available"}
		if status.LastError != nil {
			fields = append(fields, "job_error", status.LastError.Error())
		}
		if !status.LastSuccess.IsZero() {
			fields = append(fields, "last_job_at", status.LastSuccess)
		}
		logger.Warn("miner subscribed but no job ready", fields...)
	}
}

// Handle mining.authorize.
func (mc *MinerConn) handleAuthorize(req *StratumRequest) {
	worker := ""
	if len(req.Params) > 0 {
		if w, ok := req.Params[0].(string); ok {
			worker = w
		}
	}

	// Validate worker name length to prevent abuse
	if len(worker) == 0 {
		logger.Warn("authorize rejected: empty worker name", "remote", mc.id)
		mc.writeResponse(StratumResponse{
			ID:     req.ID,
			Result: false,
			Error:  newStratumError(20, "worker name required"),
		})
		mc.Close("empty worker name")
		return
	}
	if len(worker) > maxWorkerNameLen {
		logger.Warn("authorize rejected: worker name too long", "remote", mc.id, "len", len(worker))
		mc.writeResponse(StratumResponse{
			ID:     req.ID,
			Result: false,
			Error:  newStratumError(20, "worker name too long"),
		})
		mc.Close("worker name too long")
		return
	}

	workerName := mc.updateWorker(worker)

	// Before allowing hashing, ensure the worker name is a valid wallet-style
		// address so we can construct dual-payout coinbases. Invalid workers are
		// rejected immediately.
		if workerName != "" {
			if _, _, ok := mc.ensureWorkerWallet(workerName); !ok {
			addr := workerBaseAddress(workerName)
			if addr == "" {
				addr = "(invalid)"
			}
			logger.Warn("worker has invalid wallet-style name",
				"worker", workerName,
				"addr", addr,
			)
			resp := StratumResponse{
				ID:     req.ID,
				Result: false,
				Error:  newStratumError(20, "wallet worker validation failed"),
			}
			mc.writeResponse(resp)
				mc.Close("wallet validation failed")
				return
			}
			// Assign a connection sequence before registering so the saved-workers
			// dashboard can look up active connections via the worker registry.
			mc.assignConnectionSeq()
			mc.registerWorker(workerName)
		}

	// Force difficulty to the configured min on authorize so new connections
	// always start at the lowest target we allow.

		mc.authorized = true

	resp := StratumResponse{
		ID:     req.ID,
		Result: true,
		Error:  nil,
	}
	mc.writeResponse(resp)

	// Now that the worker is authorized and its wallet-style ID is known
	// to be valid, send initial difficulty and a job so hashing can start.
	// First job always has clean_jobs=true so the miner starts fresh.
	if job := mc.jobMgr.CurrentJob(); job != nil {
		mc.setDifficulty(mc.vardiff.MinDiff)
		mc.sendNotifyFor(job, true)
	}
}

func (mc *MinerConn) suggestDifficulty(req *StratumRequest) {
	resp := StratumResponse{ID: req.ID}
	if len(req.Params) == 0 {
		resp.Error = newStratumError(20, "invalid params")
		mc.writeResponse(resp)
		return
	}

	diff, ok := parseSuggestedDifficulty(req.Params[0])
	if !ok || diff <= 0 {
		resp.Error = newStratumError(20, "invalid params")
		mc.writeResponse(resp)
		return
	}

	// Treat suggested difficulty as the miner's preferred starting point,
	// clamped to the pool's min/max difficulty settings. Vardiff will still
	// adjust difficulty up or down from this baseline over time, unless
	// lock_suggested_difficulty is enabled.
	resp.Result = true
	mc.writeResponse(resp)

	// If we just restored a recent difficulty for this worker on a short
	// reconnect, ignore suggested-difficulty overrides and keep the
	// existing difficulty so we don't fight the remembered setting.
	if mc.restoredRecentDiff {
		return
	}

	if mc.cfg.LockSuggestedDifficulty {
		// Lock this miner to the requested difficulty (within min/max).
		mc.lockDifficulty = true
	}
	mc.setDifficulty(diff)
}

func parseSuggestedDifficulty(value interface{}) (float64, bool) {
	switch v := value.(type) {
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return 0, false
		}
		return v, true
	case string:
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, false
		}
		return f, true
	case jsonNumber:
		f, err := v.Float64()
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

func (mc *MinerConn) handleConfigure(req *StratumRequest) {
	if len(req.Params) == 0 {
		mc.writeResponse(StratumResponse{ID: req.ID, Result: nil, Error: newStratumError(20, "invalid params")})
		return
	}

	rawExts, ok := req.Params[0].([]interface{})
	if !ok {
		mc.writeResponse(StratumResponse{ID: req.ID, Result: nil, Error: newStratumError(20, "invalid params")})
		return
	}
	var opts map[string]interface{}
	if len(req.Params) > 1 {
		if o, ok := req.Params[1].(map[string]interface{}); ok {
			opts = o
		}
	}

	result := make(map[string]interface{})
	shouldSendVersionMask := false
	for _, ext := range rawExts {
		name, ok := ext.(string)
		if !ok {
			continue
		}
		switch name {
		case "version-rolling":
			// BIP310 version-rolling negotiation (docs/protocols/bip-0310.mediawiki).
			if mc.poolMask == 0 {
				result["version-rolling"] = false
				break
			}
			requestMask := mc.poolMask
			if opts != nil {
				if maskStr, ok := opts["version-rolling.mask"].(string); ok {
					if parsed, err := strconv.ParseUint(maskStr, 16, 32); err == nil {
						requestMask = uint32(parsed)
					}
				}
				if minBits, ok := opts["version-rolling.min-bit-count"].(float64); ok && int(minBits) > 0 {
					mc.minVerBits = int(minBits)
				}
			}
			mask := requestMask & mc.poolMask
			if mask == 0 {
				result["version-rolling"] = false
				mc.versionRoll = false
				mc.minerMask = requestMask
				mc.updateVersionMask(mc.poolMask)
				break
			}
			available := bits.OnesCount32(mask)
			if mc.minVerBits <= 0 {
				mc.minVerBits = 1
			}
			if mc.minVerBits > available {
				mc.minVerBits = available
			}
			mc.minerMask = requestMask
			mc.versionRoll = true
			mc.versionMask = mask
			result["version-rolling"] = true
			result["version-rolling.mask"] = fmt.Sprintf("%08x", mask)
			result["version-rolling.min-bit-count"] = mc.minVerBits
			// Important: some miners (including some cgminer-based firmwares)
			// expect the immediate next line after mining.configure to be its
			// JSON-RPC response. If we send an unsolicited notification before
			// the response, they may treat configure as failed and reconnect.
			shouldSendVersionMask = true
		default:
			// Unknown extension; explicitly deny so miners don't retry forever.
			result[name] = false
		}
	}

	mc.writeResponse(StratumResponse{ID: req.ID, Result: result, Error: nil})
	if shouldSendVersionMask {
		mc.sendVersionMask()
	}
}

func (mc *MinerConn) sendNotifyFor(job *Job, forceClean bool) {
	// Opportunistically adjust difficulty before notifying about the job.
	// If difficulty changed, force clean so the miner uses the new difficulty.
	if mc.maybeAdjustDifficulty(time.Now()) {
		forceClean = true
	}

	maskChanged := mc.updateVersionMask(job.VersionMask)
	if maskChanged && mc.versionRoll {
		mc.sendVersionMask()
	}

	worker := mc.currentWorker()
	var (
		coinb1 string
		coinb2 string
		err    error
	)
	if poolScript, workerScript, totalValue, feePercent, ok := mc.dualPayoutParams(job, worker); ok {
		logger.Debug("payout check", "donation_percent", job.OperatorDonationPercent, "donation_script_len", len(job.DonationScript))
		if job.OperatorDonationPercent > 0 && len(job.DonationScript) > 0 {
			logger.Info("using triple payout", "worker", worker, "donation_percent", job.OperatorDonationPercent)
			coinb1, coinb2, err = buildTriplePayoutCoinbaseParts(
				job.Template.Height,
				mc.extranonce1,
				job.Extranonce2Size,
				job.TemplateExtraNonce2Size,
				poolScript,
				job.DonationScript,
				workerScript,
				totalValue,
				feePercent,
				job.OperatorDonationPercent,
				job.WitnessCommitment,
				job.Template.CoinbaseAux.Flags,
				job.CoinbaseMsg,
				job.ScriptTime,
			)
		} else {
			coinb1, coinb2, err = buildDualPayoutCoinbaseParts(
				job.Template.Height,
				mc.extranonce1,
				job.Extranonce2Size,
				job.TemplateExtraNonce2Size,
				poolScript,
				workerScript,
				totalValue,
				feePercent,
				job.WitnessCommitment,
				job.Template.CoinbaseAux.Flags,
				job.CoinbaseMsg,
				job.ScriptTime,
			)
		}
	}
	// Fallback to single-output coinbase if any required dual-payout parameter is missing.
	if coinb1 == "" || coinb2 == "" || err != nil {
		if err != nil {
			logger.Warn("dual-payout coinbase build failed, falling back to single-output coinbase",
				"error", err,
				"worker", worker,
			)
		}
		coinb1, coinb2, err = buildCoinbaseParts(
			job.Template.Height,
			mc.extranonce1,
			job.Extranonce2Size,
			job.TemplateExtraNonce2Size,
			job.PayoutScript,
			job.CoinbaseValue,
			job.WitnessCommitment,
			job.Template.CoinbaseAux.Flags,
			job.CoinbaseMsg,
			job.ScriptTime,
		)
	}
	if err != nil {
		logger.Error("notify coinbase parts", "error", err)
		return
	}

	prevhashLE := hexToLEHex(job.PrevHash)
	shareTarget := mc.shareTargetOrDefault()

	// clean_jobs should only be true when the template actually changed (prevhash/height)
	// unless we're forcing a clean notify to pair with a difficulty change.
	cleanJobs := forceClean || (job.Clean && mc.cleanFlagFor(job))
	mc.trackJob(job, cleanJobs)
	mc.setJobDifficulty(job.JobID, mc.currentDifficulty())

	// Stratum notify shape per docs/protocols/stratum-v1.mediawiki:
	// [job_id, prevhash, coinb1, coinb2, merkle_branch[], version, nbits, ntime, clean_jobs].
	// Version, bits and ntime are sent as big-endian hex, matching the usual
	// Stratum pool conventions.
	versionBE := int32ToBEHex(int32(job.Template.Version))
	bitsBE := job.Template.Bits // bits is already a raw hex string, don't reverse it
	ntimeBE := uint32ToBEHex(uint32(job.Template.CurTime))

	params := []interface{}{
		job.JobID,
		prevhashLE,
		coinb1,
		coinb2,
		job.MerkleBranches,
		versionBE,
		bitsBE,
		ntimeBE,
		cleanJobs,
	}

	if debugLogging || verboseLogging {
		merkleRoot := computeMerkleRootBE(coinb1, coinb2, job.MerkleBranches)
		headerHashLE := headerHashFromNotify(prevhashLE, merkleRoot, uint32(job.Template.Version), job.Template.Bits, job.Template.CurTime)
		logger.Info("notify payload",
			"job", job.JobID,
			"prevhash", prevhashLE,
			"coinb1", coinb1,
			"coinb2", coinb2,
			"branches", job.MerkleBranches,
			"version", versionBE,
			"bits", bitsBE,
			"ntime", ntimeBE,
			"clean", cleanJobs,
			"share_target", fmt.Sprintf("%064x", shareTarget),
			"merkle_root_be", hex.EncodeToString(merkleRoot),
			"header_hash_le", hex.EncodeToString(headerHashLE),
		)
	}

	_ = mc.writeJSON(map[string]interface{}{
		"id":     nil,
		"method": "mining.notify",
		"params": params,
	})
}

// computeMerkleRootBE rebuilds the merkle root (big-endian) from coinb1/coinb2 and branches.
func computeMerkleRootBE(coinb1, coinb2 string, branches []string) []byte {
	c1, _ := hex.DecodeString(coinb1)
	c2, _ := hex.DecodeString(coinb2)
	cb := append(c1, c2...)
	txid := doubleSHA256(cb)
	return computeMerkleRootFromBranches(txid, branches)
}

// headerHashFromNotify rebuilds the block header hash (LE) from notify fields.
func headerHashFromNotify(prevhash string, merkleRoot []byte, version uint32, bits string, ntime int64) []byte {
	prev, err := hex.DecodeString(prevhash)
	if err != nil || len(prev) != 32 || len(merkleRoot) != 32 {
		return nil
	}
	bitsVal, err := strconv.ParseUint(bits, 16, 32)
	if err != nil {
		return nil
	}
	var hdr bytes.Buffer
	writeUint32LE(&hdr, version)
	hdr.Write(reverseBytes(prev))
	hdr.Write(reverseBytes(merkleRoot))
	writeUint32LE(&hdr, uint32(ntime))
	writeUint32LE(&hdr, uint32(bitsVal))
	writeUint32LE(&hdr, 0) // dummy nonce for hash preview
	h := doubleSHA256(hdr.Bytes())
	return reverseBytes(h)
}
