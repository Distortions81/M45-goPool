package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"math/big"

	"github.com/btcsuite/btcd/btcutil"
)

func (jm *JobManager) ensureTemplateFresh(ctx context.Context, tpl GetBlockTemplateResult) error {
	if tpl.CurTime <= 0 {
		return fmt.Errorf("template curtime invalid: %d", tpl.CurTime)
	}

	var bestHash string
	if err := jm.rpc.callCtx(ctx, "getbestblockhash", nil, &bestHash); err != nil {
		return fmt.Errorf("getbestblockhash: %w", err)
	}

	if tpl.Previous != "" && bestHash != "" && tpl.Previous != bestHash {
		return fmt.Errorf("%w: prev hash %s does not match best %s", errStaleTemplate, tpl.Previous, bestHash)
	}

	// Do not compare height or curtime with the previous job here. A legitimate
	// chain reorganization can move either value backward. Matching bitcoind's
	// current best hash is the authoritative freshness check.
	return nil
}

func validateWitnessCommitment(commitment string) error {
	if commitment == "" {
		return fmt.Errorf("template missing default witness commitment")
	}
	raw, err := hex.DecodeString(commitment)
	if err != nil {
		return fmt.Errorf("invalid default witness commitment: %w", err)
	}
	if len(raw) == 0 {
		return fmt.Errorf("default witness commitment empty")
	}
	return nil
}

func validateTransactions(txs []GBTTransaction) ([]*btcutil.Tx, error) {
	transactions := make([]*btcutil.Tx, len(txs))
	for i, tx := range txs {
		if len(tx.Txid) != 64 {
			return nil, fmt.Errorf("tx %d has invalid txid length: %d bytes", i, len(tx.Txid)/2)
		}
		txidBytes, err := hex.DecodeString(tx.Txid)
		if err != nil {
			return nil, fmt.Errorf("decode txid %s: %w", tx.Txid, err)
		}
		if len(txidBytes) != 32 {
			return nil, fmt.Errorf("tx %d txid must be 32 bytes, got %d", i, len(txidBytes))
		}

		raw, err := hex.DecodeString(tx.Data)
		if err != nil {
			return nil, fmt.Errorf("decode tx %d data: %w", i, err)
		}
		if len(raw) == 0 {
			return nil, fmt.Errorf("tx %d data empty", i)
		}

		parsedTx, err := btcutil.NewTxFromBytes(raw)
		if err != nil {
			return nil, fmt.Errorf("tx %d decode: %w", i, err)
		}

		computedTxID := parsedTx.Hash()
		if !bytes.Equal(reverseBytes(computedTxID[:]), txidBytes) {
			return nil, fmt.Errorf("tx %d txid mismatch with provided data", i)
		}

		if tx.Hash != "" {
			wtxidBytes, err := hex.DecodeString(tx.Hash)
			if err != nil {
				return nil, fmt.Errorf("decode wtxid %s: %w", tx.Hash, err)
			}
			if len(wtxidBytes) != 32 {
				return nil, fmt.Errorf("tx %d wtxid must be 32 bytes, got %d", i, len(wtxidBytes))
			}
			computedWTxID := parsedTx.MsgTx().WitnessHash()
			if !bytes.Equal(reverseBytes(computedWTxID[:]), wtxidBytes) {
				return nil, fmt.Errorf("tx %d wtxid mismatch with provided data", i)
			}
		}

		transactions[i] = parsedTx
	}
	return transactions, nil
}

func validateBits(bitsStr, targetStr string) (*big.Int, error) {
	if len(bitsStr) != 8 {
		return nil, fmt.Errorf("bits must be 8 hex characters, got %d", len(bitsStr))
	}
	target, err := targetFromBits(bitsStr)
	if err != nil {
		return nil, err
	}
	if target.Sign() <= 0 {
		return nil, fmt.Errorf("bits produced non-positive target")
	}
	if targetStr == "" {
		return target, nil
	}

	tplTarget := new(big.Int)
	if _, ok := tplTarget.SetString(targetStr, 16); !ok {
		return nil, fmt.Errorf("invalid template target %s", targetStr)
	}
	if tplTarget.Sign() <= 0 {
		return nil, fmt.Errorf("template target non-positive")
	}
	if tplTarget.Cmp(target) != 0 {
		return nil, fmt.Errorf("bits target %s mismatches template target %s", target.Text(16), tplTarget.Text(16))
	}
	return target, nil
}

// templateChanged returns (needsNewJob, clean).
// needsNewJob is true if any meaningful change occurred (prev/height/bits/transactions).
// clean is true only if prev/height/bits changed, indicating miners must discard old work.
// Transaction-only changes require a new job (for updated merkle branches) but not clean=true,
// allowing miners to continue using their current nonce range.
func (jm *JobManager) templateChanged(tpl GetBlockTemplateResult) (needsNewJob, clean bool) {
	jm.mu.RLock()
	cur := jm.curJob
	jm.mu.RUnlock()

	if cur == nil {
		return true, true
	}
	prev := cur.Template

	// Check if previousblockhash, height, or bits changed - these require clean=true.
	if tpl.Previous != prev.Previous ||
		tpl.Height != prev.Height ||
		tpl.Bits != prev.Bits {
		return true, true
	}

	// Check if transactions changed - requires new job but not clean.
	if len(tpl.Transactions) != len(prev.Transactions) {
		return true, false
	}
	for i, tx := range tpl.Transactions {
		if tx.Txid != prev.Transactions[i].Txid {
			return true, false
		}
	}

	// No meaningful changes.
	return false, false
}
