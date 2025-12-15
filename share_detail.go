package main

import (
	"encoding/hex"
	"math/big"

	"github.com/bytedance/gopkg/util/logger"
)

// buildShareDetail constructs a ShareDetail payload for a share, including the
// decoded coinbase transaction so the worker info page can always show it.
// When not in detail/verbose mode, only the coinbase transaction and outputs are populated.
func (mc *MinerConn) buildShareDetail(job *Job, worker string, header []byte, hash []byte, target *big.Int, extranonce2 string, merkleRoot []byte) *ShareDetail {
	if job == nil {
		return nil
	}

	detail := &ShareDetail{}

	// Only populate detail-specific fields when in detail or verbose mode
	if debugLogging || verboseLogging {
		detail.Header = hex.EncodeToString(header)
		detail.ShareHash = hex.EncodeToString(hash)
		detail.MerkleBranches = append([]string{}, job.MerkleBranches...)

		if target != nil {
			detail.Target = hex.EncodeToString(target.FillBytes(make([]byte, 32)))
		}
		if len(merkleRoot) == 32 {
			detail.MerkleRootBE = hex.EncodeToString(merkleRoot)
			detail.MerkleRootLE = hex.EncodeToString(reverseBytes(merkleRoot))
		}
	}

	en2, err := hex.DecodeString(extranonce2)
	if err != nil {
		logger.Warn("share detail extranonce2 decode", "error", err)
		return detail
	}

	var cbTx []byte
	if poolScript, workerScript, totalValue, feePercent, ok := mc.dualPayoutParams(job, worker); ok {
		// Check if donation is enabled and we should use triple payout
		if job.OperatorDonationPercent > 0 && len(job.DonationScript) > 0 {
			cbTx, _, err = serializeTripleCoinbaseTx(
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
				job.WitnessCommitment,
				job.Template.CoinbaseAux.Flags,
				job.CoinbaseMsg,
				job.ScriptTime,
			)
			if err != nil {
				logger.Warn("share detail triple-payout coinbase", "error", err)
			}
		} else {
			cbTx, _, err = serializeDualCoinbaseTx(
				job.Template.Height,
				mc.extranonce1,
				en2,
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
			if err != nil {
				logger.Warn("share detail dual-payout coinbase", "error", err)
			}
		}
	}
	if len(cbTx) == 0 {
		cbTx, _, err = serializeCoinbaseTx(
			job.Template.Height,
			mc.extranonce1,
			en2,
			job.TemplateExtraNonce2Size,
			job.PayoutScript,
			job.CoinbaseValue,
			job.WitnessCommitment,
			job.Template.CoinbaseAux.Flags,
			job.CoinbaseMsg,
			job.ScriptTime,
		)
		if err != nil {
			logger.Warn("share detail single-output coinbase", "error", err)
			return detail
		}
	}
	detail.Coinbase = hex.EncodeToString(cbTx)
	detail.DecodeCoinbaseFields()
	return detail
}
