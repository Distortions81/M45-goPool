package main

import (
	"encoding/hex"
	"math/big"
)

// buildShareDetailFromCoinbase constructs a ShareDetail using an already-built
// coinbase transaction. This avoids re-serializing the coinbase on the hot path
// (submit processing), while still providing enough information for the worker
// status page to decode outputs on demand.
func (mc *MinerConn) buildShareDetailFromCoinbase(job *Job, worker string, header []byte, hash []byte, target *big.Int, merkleRoot []byte, coinbaseTx []byte) *ShareDetail {
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

	if len(coinbaseTx) > 0 {
		detail.Coinbase = hex.EncodeToString(coinbaseTx)
	}

	// DecodeCoinbaseFields is intentionally not called here unless we're in a
	// debug/verbose mode. The status UI decodes outputs on demand.
	if (debugLogging || verboseLogging) && detail.Coinbase != "" {
		detail.DecodeCoinbaseFields()
	}
	return detail
}
