package main

import (
	"encoding/hex"
)

// buildShareDetail is a test-only helper; production share processing uses
// buildShareDetailFromCoinbase to avoid re-serializing the coinbase.
func (mc *MinerConn) buildShareDetail(job *Job, worker string, extranonce2 string) *ShareDetail {
	if job == nil {
		return nil
	}

	detail := &ShareDetail{}

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
	detail.DecodeCoinbaseFields()
	return detail
}
