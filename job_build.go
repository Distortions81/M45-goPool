package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

func (jm *JobManager) buildJob(ctx context.Context, tpl GetBlockTemplateResult) (*Job, error) {
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

	txids, err := validateTransactions(tpl.Transactions)
	if err != nil {
		return nil, err
	}

	merkleBranches := buildMerkleBranches(txids)
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
	if jm.cfg.CoinbaseScriptSigMaxBytes > 0 {
		trimmed, truncated, err := clampCoinbaseMessage(coinbaseMsg, jm.cfg.CoinbaseScriptSigMaxBytes, tpl.Height, scriptTime, tpl.CoinbaseAux.Flags, jm.cfg.Extranonce2Size, jm.cfg.TemplateExtraNonce2Size)
		if err != nil {
			return nil, fmt.Errorf("coinbase scriptsig limit: %w", err)
		}
		if truncated {
			logger.Debug("clamped coinbase message to meet scriptSig limit", "limit", jm.cfg.CoinbaseScriptSigMaxBytes, "message", trimmed)
		}
		coinbaseMsg = trimmed
	}

	var prevBytes [32]byte
	if len(tpl.Previous) != 64 {
		return nil, fmt.Errorf("previousblockhash hex must be 64 chars")
	}
	if n, err := hex.Decode(prevBytes[:], []byte(tpl.Previous)); err != nil || n != 32 {
		return nil, fmt.Errorf("decode previousblockhash: %w", err)
	}

	var bitsBytes [4]byte
	if len(tpl.Bits) != 8 {
		return nil, fmt.Errorf("bits hex must be 8 chars")
	}
	if n, err := hex.Decode(bitsBytes[:], []byte(tpl.Bits)); err != nil || n != 4 {
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
		JobID:                   fmt.Sprintf("%d", time.Now().UnixNano()),
		Template:                tpl,
		Target:                  target,
		targetBE:                uint256BEFromBigInt(target),
		CreatedAt:               time.Now(),
		ScriptTime:              scriptTime,
		Extranonce2Size:         jm.cfg.Extranonce2Size,
		CoinbaseValue:           tpl.CoinbaseValue,
		WitnessCommitment:       tpl.DefaultWitnessCommitment,
		CoinbaseMsg:             coinbaseMsg,
		MerkleBranches:          merkleBranches,
		merkleBranchesBytes:     merkleBranchesBytes,
		Transactions:            tpl.Transactions,
		TransactionIDs:          txids,
		PayoutScript:            jm.payoutScript,
		DonationScript:          jm.donationScript,
		OperatorDonationPercent: jm.cfg.OperatorDonationPercent,
		VersionMask:             computePoolMask(tpl, jm.cfg),
		PrevHash:                tpl.Previous,
		prevHashBytes:           prevBytes,
		bitsBytes:               bitsBytes,
		coinbaseFlagsBytes:      flagsBytes,
		witnessCommitScript:     commitScript,
		TemplateExtraNonce2Size: jm.cfg.TemplateExtraNonce2Size,
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
	base := defaultVersionMask
	if cfg.VersionMaskConfigured {
		base = cfg.VersionMask
	}
	if base == 0 {
		return 0
	}

	// Keep version rolling available to miners even when the template does not
	// advertise version mutability, since some bitcoind templates omit that
	// flag. Falling back to the configured base mask avoids sending a zero mask
	// (which would disable ASIC rolling on miners like ESP-Miner).
	if !versionMutable(tpl.Mutable) {
		return base
	}

	mask := base
	mask &^= uint32(tpl.VbRequired)

	active := make(map[string]struct{})
	for _, rule := range tpl.Rules {
		active[rule] = struct{}{}
	}
	for name, bit := range tpl.VbAvailable {
		if _, ok := active[name]; !ok {
			continue
		}
		if bit < 0 || bit >= 32 {
			continue
		}
		mask &^= uint32(1) << uint(bit)
	}

	if mask == 0 {
		// Avoid broadcasting a zero mask that would turn off version rolling on
		// miners which assume a non-zero range (e.g., ESP-Miner). Fall back to the
		// configured base mask in that rare case.
		return base
	}

	return mask
}
