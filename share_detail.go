package main

import (
	"encoding/hex"
)

// buildShareDetailFromCoinbase constructs a ShareDetail using an already-built
// coinbase transaction. This avoids re-serializing the coinbase on the hot path
// (submit processing), while still providing enough information for the worker
// status page to decode outputs on demand.
func (mc *MinerConn) buildShareDetailFromCoinbase(job *Job, coinbaseTx []byte) *ShareDetail {
	if job == nil {
		return nil
	}

	// Share detail capture is intentionally disabled unless debug/verbose
	// logging is enabled, to avoid per-share allocations and large hex strings
	// (coinbase/header payloads) being retained in memory.
	if !debugLogging && !verboseLogging {
		return nil
	}

	detail := &ShareDetail{}

	if len(coinbaseTx) > 0 {
		detail.Coinbase = hex.EncodeToString(coinbaseTx)
	}

	if detail.Coinbase != "" {
		detail.DecodeCoinbaseFields()
	}
	return detail
}
