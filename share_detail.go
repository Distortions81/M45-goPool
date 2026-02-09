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

// buildCurrentJobCoinbaseDetail reconstructs the coinbase transaction exactly
// as this miner connection currently builds it for share/block processing, with
// extranonce2 zeroed for deterministic display.
func (mc *MinerConn) buildCurrentJobCoinbaseDetail(job *Job) *ShareDetail {
	if mc == nil || job == nil || job.CoinbaseValue <= 0 {
		return nil
	}
	extranonce2Size := job.Extranonce2Size
	if extranonce2Size < 0 {
		extranonce2Size = 0
	}
	en2 := make([]byte, extranonce2Size)
	mc.jobMu.Lock()
	parts, ok := mc.jobNotifyCoinbase[job.JobID]
	mc.jobMu.Unlock()
	if !ok || parts.coinb1 == "" || parts.coinb2 == "" {
		return nil
	}
	coinbaseHex := parts.coinb1 + hex.EncodeToString(mc.extranonce1) + hex.EncodeToString(en2) + parts.coinb2
	detail := &ShareDetail{Coinbase: coinbaseHex}
	detail.DecodeCoinbaseFields()
	return detail
}
