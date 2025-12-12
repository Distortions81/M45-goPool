//go:build debug

package main

import (
	"encoding/hex"
	"math/big"

	"github.com/bytedance/gopkg/util/logger"
)

// buildShareDebug constructs a rich ShareDebug payload for a share, including
// a fully decoded coinbase transaction. This is compiled only in debug builds
// so production binaries do not pay the cost of per-share coinbase decoding.
func (mc *MinerConn) buildShareDebug(job *Job, worker string, header []byte, hash []byte, target *big.Int, extranonce2 string, merkleRoot []byte) *ShareDebug {
	if job == nil {
		return nil
	}

	debug := &ShareDebug{
		Header:         hex.EncodeToString(header),
		ShareHash:      hex.EncodeToString(hash),
		MerkleBranches: append([]string{}, job.MerkleBranches...),
	}

	if target != nil {
		debug.Target = hex.EncodeToString(target.FillBytes(make([]byte, 32)))
	}
	if len(merkleRoot) == 32 {
		debug.MerkleRootBE = hex.EncodeToString(merkleRoot)
		debug.MerkleRootLE = hex.EncodeToString(reverseBytes(merkleRoot))
	}

	en2, err := hex.DecodeString(extranonce2)
	if err != nil {
		logger.Warn("share debug extranonce2 decode", "error", err)
		return debug
	}

	var cbTx []byte
	if poolScript, workerScript, totalValue, feePercent, ok := mc.dualPayoutParams(job, worker); ok {
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
			logger.Warn("share debug dual-payout coinbase", "error", err)
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
			logger.Warn("share debug single-output coinbase", "error", err)
			return debug
		}
	}
	debug.Coinbase = hex.EncodeToString(cbTx)
	debug.DecodeCoinbaseFields()
	return debug
}
