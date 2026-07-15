package main

import "fmt"

func buildNotifiedCoinbaseTx(parts notifiedCoinbaseParts, extranonce2 []byte) ([]byte, []byte, error) {
	if len(parts.prefix) == 0 || len(parts.suffix) == 0 {
		return nil, nil, fmt.Errorf("notified coinbase parts missing")
	}
	coinbase := make([]byte, 0, len(parts.prefix)+len(extranonce2)+len(parts.suffix))
	coinbase = append(coinbase, parts.prefix...)
	coinbase = append(coinbase, extranonce2...)
	coinbase = append(coinbase, parts.suffix...)
	return coinbase, doubleSHA256(coinbase), nil
}

func (mc *MinerConn) prepareShareContext(task submissionTask) (shareContext, bool) {
	job := task.job
	workerName := task.workerName
	jobID := task.jobID
	ntimeVal := task.ntimeVal
	nonceVal := task.nonceVal
	useVersion := task.useVersion
	scriptTime := task.scriptTime
	en2 := (&task).extranonce2Decoded()
	reqID := task.reqID
	now := task.receivedAt
	if job == nil || job.Extranonce2Size <= 0 || len(en2) != job.Extranonce2Size {
		logger.Warn("submit bad extranonce2", "remote", mc.id)
		mc.recordShare(workerName, false, 0, 0, rejectInvalidExtranonce2.String(), "", nil, now)
		mc.writeResponse(StratumResponse{ID: reqID, Result: false, Error: newStratumError(stratumErrCodeInvalidRequest, "invalid extranonce2")})
		return shareContext{}, false
	}

	if scriptTime == 0 {
		scriptTime = mc.scriptTimeForJob(jobID, job.ScriptTime)
	}

	var (
		header           []byte
		merkleRoot       [32]byte
		merkleOK         bool
		cbTx             []byte
		cbTxid           []byte
		usedDualCoinbase bool
		err              error
	)

	if task.hasNotifiedCoinbase {
		cbTx, cbTxid, err = buildNotifiedCoinbaseTx(task.notifiedCoinbase, en2)
		if err == nil && len(cbTxid) == 32 {
			if job.merkleBranchesBytes != nil {
				merkleRoot, merkleOK = computeMerkleRootFromBranchesBytes32(cbTxid, job.merkleBranchesBytes)
			} else {
				merkleRoot, merkleOK = computeMerkleRootFromBranches32(cbTxid, job.MerkleBranches)
			}
			if merkleOK {
				header, err = job.buildBlockHeaderU32(merkleRoot[:], ntimeVal, nonceVal, int32(useVersion))
			}
		}
	}

	if !task.hasNotifiedCoinbase {
		if poolScript, workerScript, totalValue, feePercent, ok := mc.dualPayoutParams(job, workerName); ok {
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
				if job.merkleBranchesBytes != nil {
					merkleRoot, merkleOK = computeMerkleRootFromBranchesBytes32(cbTxid, job.merkleBranchesBytes)
				} else {
					merkleRoot, merkleOK = computeMerkleRootFromBranches32(cbTxid, job.MerkleBranches)
				}
				if merkleOK {
					header, err = job.buildBlockHeaderU32(merkleRoot[:], ntimeVal, nonceVal, int32(useVersion))
				}
				if err == nil {
					usedDualCoinbase = true
				}
			}
		}
	}

	if task.hasNotifiedCoinbase && (header == nil || !merkleOK || err != nil || len(cbTxid) != 32) {
		logger.Warn("submit notified coinbase rebuild failed", "remote", mc.id, "job", jobID, "error", err)
		mc.recordShare(workerName, false, 0, 0, rejectInvalidCoinbase.String(), "", nil, now)
		mc.writeResponse(StratumResponse{
			ID:     reqID,
			Result: false,
			Error:  newStratumError(stratumErrCodeInvalidRequest, "invalid notified coinbase"),
		})
		return shareContext{}, false
	}

	if !task.hasNotifiedCoinbase && (header == nil || !merkleOK || err != nil || len(cbTxid) != 32) {
		if err != nil && usedDualCoinbase {
			logger.Warn("dual-payout header build failed, falling back to single-output header",
				"error", err,
				"worker", workerName,
			)
		}
		cbTx, cbTxid, err = serializeCoinbaseTxPredecoded(
			job.Template.Height,
			mc.extranonce1,
			en2,
			job.TemplateExtraNonce2Size,
			mc.singlePayoutScript(job, workerName),
			job.CoinbaseValue,
			job.witnessCommitScript,
			job.coinbaseFlagsBytes,
			job.CoinbaseMsg,
			scriptTime,
		)
		if err != nil || len(cbTxid) != 32 {
			logger.Warn("submit coinbase rebuild failed", "remote", mc.id, "error", err)
			mc.recordShare(workerName, false, 0, 0, rejectInvalidCoinbase.String(), "", nil, now)
			mc.writeResponse(StratumResponse{
				ID:     reqID,
				Result: false,
				Error:  newStratumError(stratumErrCodeInvalidRequest, "invalid coinbase"),
			})
			return shareContext{}, false
		}
		if job.merkleBranchesBytes != nil {
			merkleRoot, merkleOK = computeMerkleRootFromBranchesBytes32(cbTxid, job.merkleBranchesBytes)
		} else {
			merkleRoot, merkleOK = computeMerkleRootFromBranches32(cbTxid, job.MerkleBranches)
		}
		if !merkleOK {
			logger.Warn("submit merkle build failed", "remote", mc.id)
			mc.recordShare(workerName, false, 0, 0, rejectInvalidMerkle.String(), "", nil, now)
			mc.writeResponse(StratumResponse{ID: reqID, Result: false, Error: newStratumError(stratumErrCodeInvalidRequest, "invalid merkle")})
			return shareContext{}, false
		}
		header, err = job.buildBlockHeaderU32(merkleRoot[:], ntimeVal, nonceVal, int32(useVersion))
		if err != nil {
			logger.Error("submit header build error", "remote", mc.id, "error", err)
			mc.recordShare(workerName, false, 0, 0, err.Error(), "", nil, now)
			if banned, invalids := mc.noteInvalidSubmit(now, rejectInvalidCoinbase); banned {
				mc.sendClientShowMessage("Banned: " + mc.banReason)
				mc.logBan(rejectInvalidCoinbase.String(), workerName, invalids)
				mc.writeResponse(StratumResponse{ID: reqID, Result: false, Error: mc.bannedStratumError()})
			} else {
				mc.writeResponse(StratumResponse{ID: reqID, Result: false, Error: newStratumError(stratumErrCodeInvalidRequest, err.Error())})
			}
			return shareContext{}, false
		}
	}

	headerHashArray := doubleSHA256Array(header)

	var headerHashLE [32]byte
	copy(headerHashLE[:], headerHashArray[:])
	reverseBytes32(&headerHashLE)

	targetBE := job.targetBE
	if targetBE == ([32]byte{}) && job.Target != nil && job.Target.Sign() != 0 {
		targetBE = uint256BEFromBigInt(job.Target)
	}
	isBlock := uint256BELessOrEqual(headerHashLE, targetBE)

	hashHex := hexEncode32LowerString(&headerHashLE)

	ctx := shareContext{
		hashHex:          hashHex,
		shareDiff:        difficultyFromHash(headerHashArray[:]),
		isBlock:          isBlock,
		rescueCoinbase:   cbTx,
		rescueMerkleRoot: merkleRoot,
	}
	// A block must be assembled from the exact header and coinbase that passed
	// PoW. Non-block shares retain large buffers only for detail logging.
	if isBlock || debugLogging || verboseRuntimeLogging {
		hashLE := make([]byte, len(headerHashLE))
		copy(hashLE, headerHashLE[:])
		ctx.header = header
		ctx.cbTx = cbTx
		ctx.merkleRoot = append([]byte(nil), merkleRoot[:]...)
		ctx.hashLE = hashLE
	}
	return ctx, true
}

// prepareVersionShareContext reuses the primary submission's exact coinbase
// and merkle root. Version rescue is on the untrusted hot path, so each extra
// interpretation should cost only one header hash rather than another complete
// coinbase serialization and merkle walk.
func (mc *MinerConn) prepareVersionShareContext(task submissionTask, base shareContext) (shareContext, bool) {
	job := task.job
	if job == nil || len(base.rescueCoinbase) == 0 {
		return shareContext{}, false
	}
	header, err := job.buildBlockHeaderU32(
		base.rescueMerkleRoot[:],
		task.ntimeVal,
		task.nonceVal,
		int32(task.useVersion),
	)
	if err != nil {
		logger.Warn("submit version-rescue header build failed", "remote", mc.id, "job", task.jobID, "error", err)
		return shareContext{}, false
	}

	headerHashArray := doubleSHA256Array(header)
	var headerHashLE [32]byte
	copy(headerHashLE[:], headerHashArray[:])
	reverseBytes32(&headerHashLE)
	targetBE := job.targetBE
	if targetBE == ([32]byte{}) && job.Target != nil && job.Target.Sign() != 0 {
		targetBE = uint256BEFromBigInt(job.Target)
	}
	isBlock := uint256BELessOrEqual(headerHashLE, targetBE)
	ctx := shareContext{
		hashHex:          hexEncode32LowerString(&headerHashLE),
		shareDiff:        difficultyFromHash(headerHashArray[:]),
		isBlock:          isBlock,
		rescueCoinbase:   base.rescueCoinbase,
		rescueMerkleRoot: base.rescueMerkleRoot,
	}
	if isBlock || debugLogging || verboseRuntimeLogging {
		hashLE := make([]byte, len(headerHashLE))
		copy(hashLE, headerHashLE[:])
		ctx.header = header
		ctx.cbTx = base.rescueCoinbase
		ctx.merkleRoot = append([]byte(nil), base.rescueMerkleRoot[:]...)
		ctx.hashLE = hashLE
	}
	return ctx, true
}
