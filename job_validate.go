package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"sync"

	"github.com/btcsuite/btcd/btcutil"
)

func (jm *JobManager) ensureTemplateFresh(ctx context.Context, tpl GetBlockTemplateResult) error {
	return jm.ensureTemplateFreshWithParent(ctx, tpl, "")
}

// ensureTemplateFreshWithParent validates a template against either a recent,
// trusted ZMQ active-tip proof or an explicit getbestblockhash RPC. The proof
// is only supplied for a changed-parent transition; unchanged templates still
// require RPC verification so a missed ZMQ notification cannot mask staleness.
func (jm *JobManager) ensureTemplateFreshWithParent(ctx context.Context, tpl GetBlockTemplateResult, expectedParent string) error {
	if tpl.CurTime <= 0 {
		return fmt.Errorf("template curtime invalid: %d", tpl.CurTime)
	}
	if expectedParent != "" {
		if tpl.Previous != expectedParent {
			return fmt.Errorf("%w: prev hash %s does not match announced %s", errStaleTemplate, tpl.Previous, expectedParent)
		}
		return nil
	}

	if ctx == nil {
		ctx = context.Background()
	}
	timeout := jm.refreshRPCTimeout
	if timeout <= 0 {
		timeout = jobTemplateRefreshTimeout
	}
	verifyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var bestHash string
	if err := jm.rpc.callCtx(verifyCtx, "getbestblockhash", nil, &bestHash); err != nil {
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
	return validateTransactionsWithCache(txs, nil)
}

func (jm *JobManager) validateTransactions(txs []GBTTransaction) ([]*btcutil.Tx, error) {
	if jm == nil {
		return validateTransactions(txs)
	}
	return validateTransactionsWithCache(txs, jm.txValidationCache)
}

type validatedTransactionCacheKey struct {
	txid          [32]byte
	wtxid         [32]byte
	hasWTxID      bool
	dataHexDigest [32]byte
}

type validatedTransactionCache struct {
	mu      sync.Mutex
	entries map[validatedTransactionCacheKey]*btcutil.Tx
	order   []validatedTransactionCacheKey
	next    int
	limit   int
}

const defaultValidatedTransactionCacheSize = 32768

func newValidatedTransactionCache(limit int) *validatedTransactionCache {
	if limit <= 0 {
		return nil
	}
	return &validatedTransactionCache{
		entries: make(map[validatedTransactionCacheKey]*btcutil.Tx, limit),
		order:   make([]validatedTransactionCacheKey, 0, limit),
		limit:   limit,
	}
}

func (c *validatedTransactionCache) get(key validatedTransactionCacheKey) (*btcutil.Tx, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	tx, ok := c.entries[key]
	c.mu.Unlock()
	return tx, ok
}

func (c *validatedTransactionCache) add(key validatedTransactionCacheKey, tx *btcutil.Tx) {
	if c == nil || tx == nil || c.limit <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[key]; exists {
		return
	}
	if len(c.order) < c.limit {
		c.order = append(c.order, key)
	} else {
		delete(c.entries, c.order[c.next])
		c.order[c.next] = key
		c.next++
		if c.next == c.limit {
			c.next = 0
		}
	}
	c.entries[key] = tx
}

func validateTransactionsWithCache(txs []GBTTransaction, cache *validatedTransactionCache) ([]*btcutil.Tx, error) {
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
		var key validatedTransactionCacheKey
		copy(key.txid[:], txidBytes)
		key.dataHexDigest = sha256.Sum256([]byte(tx.Data))

		if tx.Hash != "" {
			wtxidBytes, err := hex.DecodeString(tx.Hash)
			if err != nil {
				return nil, fmt.Errorf("decode wtxid %s: %w", tx.Hash, err)
			}
			if len(wtxidBytes) != 32 {
				return nil, fmt.Errorf("tx %d wtxid must be 32 bytes, got %d", i, len(wtxidBytes))
			}
			copy(key.wtxid[:], wtxidBytes)
			key.hasWTxID = true
		}
		if cached, ok := cache.get(key); ok {
			transactions[i] = cached
			continue
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

		if key.hasWTxID {
			computedWTxID := parsedTx.MsgTx().WitnessHash()
			if !bytes.Equal(reverseBytes(computedWTxID[:]), key.wtxid[:]) {
				return nil, fmt.Errorf("tx %d wtxid mismatch with provided data", i)
			}
		}

		transactions[i] = parsedTx
		cache.add(key, parsedTx)
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
// needsNewJob is true when the effective header policy or coherent
// coinbase/transaction payload changes. clean is reserved for changes that
// invalidate the prior header search space; payload-only updates keep old work
// valid while advertising a new candidate.
func (jm *JobManager) templateChanged(tpl GetBlockTemplateResult) (needsNewJob, clean bool) {
	jm.applyMu.Lock()
	defer jm.applyMu.Unlock()
	return jm.templateChangedLocked(tpl)
}

// templateChangedLocked compares against one coherent runtime configuration
// snapshot. The caller must hold jm.applyMu.
func (jm *JobManager) templateChangedLocked(tpl GetBlockTemplateResult) (needsNewJob, clean bool) {
	jm.mu.RLock()
	cur := jm.curJob
	jm.mu.RUnlock()

	if cur == nil {
		return true, true
	}
	prev := cur.Template
	next := tpl
	next.Version = applyConfiguredVersionBits(tpl.Version, jm.cfg)
	nextVersionMask := computePoolMask(tpl, jm.cfg)

	// Header and version-policy changes require miners to discard previously
	// advertised work. Keep this definition aligned with cleanFlagFor so a
	// coalesced subscriber update cannot hide an intervening clean transition.
	if miningTemplateRequiresClean(prev, cur.VersionMask, next, nextVersionMask) {
		return true, true
	}
	// A Core-authored full template must always replace the temporary valid
	// coinbase-only job, even when the node's mempool is empty. Header policy is
	// unchanged, so miners may finish or submit work from either job.
	if cur.FastEmpty {
		return true, false
	}

	// Coinbase and transaction updates form a new, internally coherent job, but
	// old work on the same header policy remains valid and need not be discarded.
	if tpl.CoinbaseValue != prev.CoinbaseValue ||
		tpl.DefaultWitnessCommitment != prev.DefaultWitnessCommitment ||
		tpl.CoinbaseAux.Flags != prev.CoinbaseAux.Flags {
		return true, false
	}

	// Check if transactions changed - requires new job but not clean.
	if len(tpl.Transactions) != len(prev.Transactions) {
		return true, false
	}
	for i, tx := range tpl.Transactions {
		prevTx := prev.Transactions[i]
		if tx.Txid != prevTx.Txid || tx.Hash != prevTx.Hash || tx.Data != prevTx.Data {
			return true, false
		}
	}

	// No meaningful changes.
	return false, false
}

func miningTemplateRequiresClean(prev GetBlockTemplateResult, prevVersionMask uint32, next GetBlockTemplateResult, nextVersionMask uint32) bool {
	return next.Previous != prev.Previous ||
		next.Height != prev.Height ||
		next.Mintime != prev.Mintime ||
		next.Bits != prev.Bits ||
		next.Target != prev.Target ||
		next.Version != prev.Version ||
		next.VbRequired != prev.VbRequired ||
		nextVersionMask != prevVersionMask
}
