package main

func (mc *MinerConn) prepareShareContextSolo(task submissionTask) (shareContext, bool) {
	return mc.prepareShareContextSoloWithHooks(task, (&task).hooksOrDefault(mc))
}

func (mc *MinerConn) prepareShareContextSoloWithHooks(task submissionTask, hooks miningShareSubmitHooks) (shareContext, bool) {
	// Solo mode keeps this hot path minimal: build header, compute hash/diff, and
	// detect block candidates. We intentionally skip strict verification and
	// avoid returning large buffers not needed for stats/accounting.
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
		hooks.writeInvalidRequest(reqID, "invalid extranonce2")
		return shareContext{}, false
	}

	if scriptTime == 0 {
		scriptTime = mc.scriptTimeForJob(jobID, job.ScriptTime)
	}

	var (
		header []byte
		cbTxid []byte
		err    error
	)

	// Rebuild coinbase+header. Dual/Triple payout paths are kept because they
	// affect the coinbase txid (and thus merkle root and header hash).
	if poolScript, workerScript, totalValue, feePercent, ok := mc.dualPayoutParams(job, workerName); ok {
		var merkleRoot [32]byte
		var merkleOK bool
		if job.OperatorDonationPercent > 0 && len(job.DonationScript) > 0 {
			_, cbTxid, err = serializeTripleCoinbaseTxPredecoded(
				job.Template.Height,
				mc.stratumV1.extranonce1,
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
			_, cbTxid, err = serializeDualCoinbaseTxPredecoded(
				job.Template.Height,
				mc.stratumV1.extranonce1,
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
		}
	}

	if header == nil || err != nil || len(cbTxid) != 32 {
		_, cbTxid, err = serializeCoinbaseTxPredecoded(
			job.Template.Height,
			mc.stratumV1.extranonce1,
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
			hooks.writeInvalidRequest(reqID, "invalid coinbase")
			return shareContext{}, false
		}
		var merkleRoot [32]byte
		var merkleOK bool
		if job.merkleBranchesBytes != nil {
			merkleRoot, merkleOK = computeMerkleRootFromBranchesBytes32(cbTxid, job.merkleBranchesBytes)
		} else {
			merkleRoot, merkleOK = computeMerkleRootFromBranches32(cbTxid, job.MerkleBranches)
		}
		if !merkleOK {
			logger.Warn("submit merkle build failed", "remote", mc.id)
			mc.recordShare(workerName, false, 0, 0, rejectInvalidMerkle.String(), "", nil, now)
			hooks.writeInvalidRequest(reqID, "invalid merkle")
			return shareContext{}, false
		}
		header, err = job.buildBlockHeaderU32(merkleRoot[:], ntimeVal, nonceVal, int32(useVersion))
		if err != nil {
			logger.Error("submit header build error", "remote", mc.id, "error", err)
			mc.recordShare(workerName, false, 0, 0, err.Error(), "", nil, now)
			if banned, invalids := mc.noteInvalidSubmit(now, rejectInvalidCoinbase); banned {
				mc.sendClientShowMessage("Banned: " + mc.ban.banReason)
				mc.logBan(rejectInvalidCoinbase.String(), workerName, invalids)
				hooks.writeBanned(reqID)
			} else {
				hooks.writeInvalidRequest(reqID, err.Error())
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
		// Defensive fallback: some tests override job.Target after construction.
		// Avoid mutating shared job state on the hot path.
		targetBE = uint256BEFromBigInt(job.Target)
	}
	isBlock := uint256BELessOrEqual(headerHashLE, targetBE)

	hashHex := hexEncode32LowerString(&headerHashLE)

	return shareContext{
		hashHex:   hashHex,
		shareDiff: difficultyFromHash(headerHashArray[:]),
		isBlock:   isBlock,
	}, true
}

func (mc *MinerConn) prepareShareContextStrict(task submissionTask) (shareContext, bool) {
	return mc.prepareShareContextStrictWithHooks(task, (&task).hooksOrDefault(mc))
}

func (mc *MinerConn) prepareShareContextStrictWithHooks(task submissionTask, hooks miningShareSubmitHooks) (shareContext, bool) {
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
		hooks.writeInvalidRequest(reqID, "invalid extranonce2")
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

	if poolScript, workerScript, totalValue, feePercent, ok := mc.dualPayoutParams(job, workerName); ok {
		if job.OperatorDonationPercent > 0 && len(job.DonationScript) > 0 {
			cbTx, cbTxid, err = serializeTripleCoinbaseTxPredecoded(
				job.Template.Height,
				mc.stratumV1.extranonce1,
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
				mc.stratumV1.extranonce1,
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

	if header == nil || !merkleOK || err != nil || len(cbTxid) != 32 {
		if err != nil && usedDualCoinbase {
			logger.Warn("dual-payout header build failed, falling back to single-output header",
				"error", err,
				"worker", workerName,
			)
		}
		cbTx, cbTxid, err = serializeCoinbaseTxPredecoded(
			job.Template.Height,
			mc.stratumV1.extranonce1,
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
			hooks.writeInvalidRequest(reqID, "invalid coinbase")
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
			hooks.writeInvalidRequest(reqID, "invalid merkle")
			return shareContext{}, false
		}
		header, err = job.buildBlockHeaderU32(merkleRoot[:], ntimeVal, nonceVal, int32(useVersion))
		if err != nil {
			logger.Error("submit header build error", "remote", mc.id, "error", err)
			mc.recordShare(workerName, false, 0, 0, err.Error(), "", nil, now)
			if banned, invalids := mc.noteInvalidSubmit(now, rejectInvalidCoinbase); banned {
				mc.sendClientShowMessage("Banned: " + mc.ban.banReason)
				mc.logBan(rejectInvalidCoinbase.String(), workerName, invalids)
				hooks.writeBanned(reqID)
			} else {
				hooks.writeInvalidRequest(reqID, err.Error())
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
		hashHex:   hashHex,
		shareDiff: difficultyFromHash(headerHashArray[:]),
		isBlock:   isBlock,
	}
	// Only keep large buffers when detail logging is enabled.
	if debugLogging || verboseRuntimeLogging {
		hashLE := make([]byte, len(headerHashLE))
		copy(hashLE, headerHashLE[:])
		ctx.header = header
		ctx.cbTx = cbTx
		ctx.merkleRoot = append([]byte(nil), merkleRoot[:]...)
		ctx.hashLE = hashLE
	}
	return ctx, true
}
