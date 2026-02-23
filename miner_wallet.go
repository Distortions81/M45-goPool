package main

import "strings"

// workerWalletDataRef returns a validated worker wallet entry without copying
// the stored script. The returned script must be treated as read-only.
//
// This exists to avoid per-share allocations in hot paths (coinbase/header
// rebuild). For external/state snapshot callers, prefer workerWalletData.
func (mc *MinerConn) workerWalletDataRef(worker string) (string, []byte, bool) {
	if worker == "" {
		return "", nil, false
	}
	mc.walletMu.Lock()
	defer mc.walletMu.Unlock()
	info, ok := mc.workerWallets[worker]
	if !ok || !info.validated {
		return "", nil, false
	}
	return info.address, info.script, true
}

// workerPayoutScript returns the cached payout script for a worker, if any.
// This is populated during wallet validation and will be used in a future
// dual-payout coinbase layout.
func (mc *MinerConn) workerPayoutScript(worker string) []byte {
	if worker == "" {
		return nil
	}
	_, script, ok := mc.workerWalletDataRef(worker)
	if !ok {
		return nil
	}
	return script
}

func workerBaseAddress(worker string) string {
	raw := strings.TrimSpace(worker)
	if raw == "" {
		return ""
	}
	if parts := strings.SplitN(raw, ".", 2); len(parts) > 1 {
		raw = parts[0]
	}
	return sanitizePayoutAddress(raw)
}

func cloneBytes(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	c := make([]byte, len(b))
	copy(c, b)
	return c
}

func (mc *MinerConn) workerWalletData(worker string) (string, []byte, bool) {
	if worker == "" {
		return "", nil, false
	}
	mc.walletMu.Lock()
	defer mc.walletMu.Unlock()
	info, ok := mc.workerWallets[worker]
	if !ok || !info.validated {
		return "", nil, false
	}
	return info.address, cloneBytes(info.script), true
}

func (mc *MinerConn) setWorkerWallet(worker, addr string, script []byte) {
	if worker == "" || addr == "" || len(script) == 0 {
		return
	}
	mc.walletMu.Lock()
	if mc.workerWallets == nil {
		mc.workerWallets = make(map[string]workerWalletState, 4) // Pre-allocate for typical worker count
	}
	mc.workerWallets[worker] = workerWalletState{
		address:   addr,
		script:    cloneBytes(script),
		validated: true,
	}
	mc.walletMu.Unlock()
}

func (mc *MinerConn) ensureWorkerWallet(worker string) (string, []byte, bool) {
	if worker == "" {
		return "", nil, false
	}
	if addr, script, ok := mc.workerWalletData(worker); ok {
		return addr, script, true
	}
	base := workerBaseAddress(worker)
	if base == "" {
		return "", nil, false
	}
	script, err := scriptForAddress(base, ChainParams())
	if err != nil {
		logger.Warn("derive worker payout script failed",
			"remote", mc.id,
			"worker", worker,
			"address", base,
			"error", err,
		)
		return "", nil, false
	}
	mc.setWorkerWallet(worker, base, script)
	return base, cloneBytes(script), true
}

func (mc *MinerConn) registerWorker(worker string) *MinerConn {
	if worker == "" || mc.workerRegistry == nil {
		return nil
	}
	hash := ""
	mc.statsMu.Lock()
	if mc.stats.Worker == worker {
		hash = strings.TrimSpace(mc.stats.WorkerSHA256)
	}
	mc.statsMu.Unlock()
	if hash == "" {
		hash = workerNameHash(worker)
	}
	if hash == "" {
		return nil
	}
	if mc.registeredWorkerHash == hash {
		return nil
	}
	if mc.registeredWorkerHash != "" {
		prevWallet := workerBaseAddress(mc.registeredWorker)
		prevWalletHash := workerNameHash(prevWallet)
		mc.workerRegistry.unregister(mc.registeredWorkerHash, prevWalletHash, mc)
	}
	wallet := workerBaseAddress(worker)
	walletHash := workerNameHash(wallet)
	prev := mc.workerRegistry.register(hash, walletHash, mc)
	mc.registeredWorker = worker
	mc.registeredWorkerHash = hash
	mc.syncSavedWorkerState(hash)
	return prev
}

func (mc *MinerConn) unregisterRegisteredWorker() {
	if mc.workerRegistry == nil || mc.registeredWorkerHash == "" {
		return
	}
	wallet := workerBaseAddress(mc.registeredWorker)
	walletHash := workerNameHash(wallet)
	mc.workerRegistry.unregister(mc.registeredWorkerHash, walletHash, mc)
	mc.registeredWorker = ""
	mc.registeredWorkerHash = ""
	mc.savedWorkerTracked = false
	mc.savedWorkerBestDiff = 0
}

func (mc *MinerConn) syncSavedWorkerState(hash string) {
	if mc == nil {
		return
	}
	mc.savedWorkerTracked = false
	mc.savedWorkerBestDiff = 0
	if mc.savedWorkerStore == nil {
		return
	}
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return
	}
	best, ok, err := mc.savedWorkerStore.BestDifficultyForHash(hash)
	if err != nil {
		logger.Warn("saved worker best difficulty lookup failed", "error", err, "hash", hash)
		return
	}
	mc.savedWorkerBestDiff = best
	mc.savedWorkerTracked = ok
}

func (mc *MinerConn) maybeUpdateSavedWorkerBestDiff(diff float64) {
	if mc == nil || mc.savedWorkerStore == nil {
		return
	}
	hash := mc.registeredWorkerHash
	if hash == "" {
		return
	}
	// Workers can be saved while a connection is already online. Lazily
	// re-check tracking here so best-share updates start flowing without forcing
	// a reconnect.
	if !mc.savedWorkerTracked {
		best, ok, err := mc.savedWorkerStore.BestDifficultyForHash(hash)
		if err != nil {
			logger.Warn("saved worker best difficulty lookup failed", "error", err, "hash", hash)
			return
		}
		if !ok {
			return
		}
		mc.savedWorkerTracked = true
		mc.savedWorkerBestDiff = best
	}
	if diff <= mc.savedWorkerBestDiff {
		return
	}
	if _, err := mc.savedWorkerStore.UpdateSavedWorkerBestDifficulty(hash, diff); err != nil {
		logger.Warn("saved worker best difficulty update failed", "error", err, "hash", hash)
	}
	mc.savedWorkerBestDiff = diff
}

// singlePayoutScript selects the output script for single-output coinbase
// paths. When pool_fee_percent is 0 (or negative), the full coinbase must go
// to the resolved worker wallet script; if no validated script is available,
// nil is returned so callers can fail fast.
func (mc *MinerConn) singlePayoutScript(job *Job, worker string) []byte {
	if job == nil || len(job.PayoutScript) == 0 {
		return nil
	}
	if mc == nil || mc.cfg.PoolFeePercent > 0 {
		return job.PayoutScript
	}
	_, script, ok := mc.workerWalletDataRef(worker)
	if !ok || len(script) == 0 {
		return nil
	}
	return script
}

// dualPayoutParams returns the pool and worker payout scripts and fee
// parameters for a job, if all required pieces are available. It does not
// mutate the Job; callers use the returned values with
// buildDualPayoutCoinbaseParts when constructing coinbase data in the
// dual-payout path.
// Returns false (single-payout) when:
// - Pool fee is 0% (entire reward goes to worker)
// - Worker wallet matches pool wallet (same beneficiary)
// - Worker has no valid payout script
func (mc *MinerConn) dualPayoutParams(job *Job, worker string) (poolScript []byte, workerScript []byte, totalValue int64, feePercent float64, ok bool) {
	if job == nil || job.CoinbaseValue <= 0 {
		return nil, nil, 0, 0, false
	}
	if len(job.PayoutScript) == 0 {
		return nil, nil, 0, 0, false
	}
	// If the pool fee is 0%, there's no need for dual-payout since the entire
	// block reward goes to the worker. Use single-output coinbase.
	if mc.cfg.PoolFeePercent <= 0 {
		return nil, nil, 0, 0, false
	}
	addr, script, ok := mc.workerWalletDataRef(worker)
	if !ok || len(script) == 0 {
		return nil, nil, 0, 0, false
	}
	// If the worker's wallet address is the same as the pool payout address,
	// there is no benefit to building a dual-payout coinbase; treat it as a
	// single-output payout to that address.
	if addr != "" && strings.EqualFold(addr, mc.cfg.PayoutAddress) {
		return nil, nil, 0, 0, false
	}

	return job.PayoutScript, script, job.CoinbaseValue, mc.cfg.PoolFeePercent, true
}
