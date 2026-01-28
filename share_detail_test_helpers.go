package main

import (
	"encoding/hex"
	"math/big"
)

// buildShareDetail is a test-only helper; production share processing uses
// buildShareDetailFromCoinbase to avoid re-serializing the coinbase.
func (mc *MinerConn) buildShareDetail(job *Job, worker string, header []byte, hash []byte, target *big.Int, extranonce2 string, merkleRoot []byte) *ShareDetail {
	if job == nil {
		return nil
	}

	detail := &ShareDetail{}

	if debugLogging || verboseLogging {
		detail.Header = hex.EncodeToString(header)
		detail.ShareHash = hex.EncodeToString(hash)
		detail.MerkleBranches = append([]string{}, job.MerkleBranches...)

		if target != nil {
			var targetBuf [32]byte
			detail.Target = hex.EncodeToString(target.FillBytes(targetBuf[:]))
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
	if debugLogging || verboseLogging {
		detail.DecodeCoinbaseFields()
	}
	return detail
}
