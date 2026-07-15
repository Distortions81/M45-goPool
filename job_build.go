package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

func (jm *JobManager) buildJob(ctx context.Context, tpl GetBlockTemplateResult) (*Job, error) {
	jm.applyMu.Lock()
	defer jm.applyMu.Unlock()
	return jm.buildJobLocked(ctx, tpl)
}

// buildJobLocked builds from one coherent runtime configuration snapshot.
// The caller must hold jm.applyMu.
func (jm *JobManager) buildJobLocked(ctx context.Context, tpl GetBlockTemplateResult) (*Job, error) {
	if len(jm.payoutScript) == 0 {
		return nil, fmt.Errorf("payout script not configured")
	}

	if err := jm.ensureTemplateFresh(ctx, tpl); err != nil {
		return nil, err
	}

	target, err := validateBits(tpl.Bits, tpl.Target)
	if err != nil {
		return nil, err
	}

	if err := validateWitnessCommitment(tpl.DefaultWitnessCommitment); err != nil {
		return nil, err
	}

	transactions, err := validateTransactions(tpl.Transactions)
	if err != nil {
		return nil, err
	}
	tpl.Version = applyConfiguredVersionBits(tpl.Version, jm.cfg)

	merkleBranches := buildMerkleBranches(transactions)
	merkleBranchesBytes, err := decodeMerkleBranchesBytes(merkleBranches)
	if err != nil {
		return nil, err
	}

	scriptTime := time.Now().Unix()
	coinbaseMsg := jm.cfg.CoinbaseMsg
	if jm.cfg.JobEntropy > 0 {
		msg, err := buildCoinbaseMsgWithSuffix(coinbaseMsg, jm.cfg.PoolEntropy, jm.cfg.JobEntropy)
		if err != nil {
			return nil, err
		}
		coinbaseMsg = msg
	}
	coinbaseScriptSigMaxBytes := jm.cfg.CoinbaseScriptSigMaxBytes
	if coinbaseScriptSigMaxBytes == 0 {
		// Config validation rejects an explicit zero. Keep direct/internal
		// JobManager construction safe by treating an omitted zero value as the
		// consensus maximum instead of disabling the limit.
		coinbaseScriptSigMaxBytes = maxCoinbaseScriptSigBytes
	}
	trimmed, truncated, err := clampCoinbaseMessage(coinbaseMsg, coinbaseScriptSigMaxBytes, tpl.Height, scriptTime, tpl.CoinbaseAux.Flags, jm.cfg.Extranonce2Size, jm.cfg.TemplateExtraNonce2Size)
	if err != nil {
		return nil, fmt.Errorf("coinbase scriptsig limit: %w", err)
	}
	if truncated {
		logger.Debug("clamped coinbase message to meet scriptSig limit", "limit", coinbaseScriptSigMaxBytes, "message", trimmed)
	}
	coinbaseMsg = trimmed

	var prevBytes [32]byte
	if len(tpl.Previous) != 64 {
		return nil, fmt.Errorf("previousblockhash hex must be 64 chars")
	}
	if err := decodeHexToFixedBytes(prevBytes[:], tpl.Previous); err != nil {
		return nil, fmt.Errorf("decode previousblockhash: %w", err)
	}

	var bitsBytes [4]byte
	if err := decodeHex8To4(&bitsBytes, tpl.Bits); err != nil {
		return nil, fmt.Errorf("decode bits: %w", err)
	}

	var flagsBytes []byte
	if tpl.CoinbaseAux.Flags != "" {
		b, err := hex.DecodeString(tpl.CoinbaseAux.Flags)
		if err != nil {
			return nil, fmt.Errorf("decode coinbase flags: %w", err)
		}
		flagsBytes = b
	}

	var commitScript []byte
	if tpl.DefaultWitnessCommitment != "" {
		b, err := hex.DecodeString(tpl.DefaultWitnessCommitment)
		if err != nil {
			return nil, fmt.Errorf("decode witness commitment: %w", err)
		}
		commitScript = b
	}

	job := &Job{
		JobID:                     jm.nextJobID(),
		Generation:                atomic.AddUint64(&jm.jobGeneration, 1),
		Template:                  tpl,
		Target:                    target,
		targetBE:                  uint256BEFromBigInt(target),
		CreatedAt:                 time.Now(),
		ScriptTime:                scriptTime,
		Extranonce2Size:           jm.cfg.Extranonce2Size,
		CoinbaseValue:             tpl.CoinbaseValue,
		WitnessCommitment:         tpl.DefaultWitnessCommitment,
		CoinbaseMsg:               coinbaseMsg,
		MerkleBranches:            merkleBranches,
		merkleBranchesBytes:       merkleBranchesBytes,
		Transactions:              tpl.Transactions,
		TransactionIDs:            transactionIDs(transactions),
		PayoutScript:              append([]byte(nil), jm.payoutScript...),
		PayoutAddress:             jm.cfg.PayoutAddress,
		PoolFeePercent:            jm.cfg.PoolFeePercent,
		PayoutPolicyCaptured:      true,
		DonationScript:            append([]byte(nil), jm.donationScript...),
		OperatorDonationPercent:   jm.cfg.OperatorDonationPercent,
		VersionMask:               computePoolMask(tpl, jm.cfg),
		PrevHash:                  tpl.Previous,
		prevHashBytes:             prevBytes,
		bitsBytes:                 bitsBytes,
		coinbaseFlagsBytes:        flagsBytes,
		witnessCommitScript:       commitScript,
		TemplateExtraNonce2Size:   jm.cfg.TemplateExtraNonce2Size,
		CoinbaseScriptSigMaxBytes: coinbaseScriptSigMaxBytes,
	}

	return job, nil
}

func buildCoinbaseMsgWithSuffix(base, poolEntropy string, jobEntropy int) (string, error) {
	suffix, err := buildPoolSuffix(poolEntropy, jobEntropy)
	if err != nil {
		return "", fmt.Errorf("coinbase suffix: %w", err)
	}
	if base == "" {
		return suffix, nil
	}
	if suffix == "" {
		return base, nil
	}
	if strings.HasSuffix(base, "/") {
		return base + suffix, nil
	}
	return base + "/" + suffix, nil
}

func buildPoolSuffix(poolEntropy string, jobEntropy int) (string, error) {
	if jobEntropy < 0 {
		jobEntropy = 0
	}
	randomPart := ""
	if jobEntropy > 0 {
		part, err := randomAlnumString(jobEntropy)
		if err != nil {
			return "", err
		}
		randomPart = part
	}
	if poolEntropy == "" {
		return randomPart, nil
	}
	if randomPart == "" {
		return poolEntropy, nil
	}
	// Format as "<pool entropy>-<job entropy>" when both parts are present.
	return poolEntropy + "-" + randomPart, nil
}

func computePoolMask(tpl GetBlockTemplateResult, cfg Config) uint32 {
	base := cfg.VersionMask
	if base == 0 && !cfg.VersionMaskConfigured {
		base = defaultVersionMask
	}
	mask := base

	// Version-rolling is useful even though Bitcoin Core commonly omits
	// version/* from mutable. Regardless of that metadata, never delegate bits
	// whose values are controlled by the node or by pool policy.
	mask &^= uint32(tpl.VbRequired)
	for _, bit := range tpl.VbAvailable {
		if bit < 0 || bit >= 32 {
			continue
		}
		mask &^= uint32(1) << uint(bit)
	}
	for bit := range cfg.VersionBitOverrides {
		if bit <= 31 {
			mask &^= uint32(1) << bit
		}
	}

	return mask
}
