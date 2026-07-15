package main

import (
	"bytes"
	"strings"
	"time"
)

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
	mc.savedWorkerMu.Lock()
	if mc.registeredWorkerHash == hash {
		mc.savedWorkerMu.Unlock()
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
	generation := mc.beginSavedWorkerSyncLocked()
	mc.savedWorkerMu.Unlock()
	mc.lookupSavedWorkerState(hash, generation)
	return prev
}

func (mc *MinerConn) unregisterRegisteredWorker() {
	if mc == nil {
		return
	}
	mc.savedWorkerMu.Lock()
	defer mc.savedWorkerMu.Unlock()
	if mc.registeredWorkerHash != "" && mc.workerRegistry != nil {
		wallet := workerBaseAddress(mc.registeredWorker)
		walletHash := workerNameHash(wallet)
		mc.workerRegistry.unregister(mc.registeredWorkerHash, walletHash, mc)
	}
	mc.registeredWorker = ""
	mc.registeredWorkerHash = ""
	mc.savedWorkerTracked = false
	mc.savedWorkerBestDiff = 0
	mc.savedWorkerSyncing = false
	mc.savedWorkerSyncGen++
}

func (mc *MinerConn) syncSavedWorkerState(hash string) {
	if mc == nil {
		return
	}
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return
	}
	mc.savedWorkerMu.Lock()
	if mc.registeredWorkerHash != hash {
		mc.savedWorkerMu.Unlock()
		return
	}
	generation := mc.beginSavedWorkerSyncLocked()
	mc.savedWorkerMu.Unlock()
	mc.lookupSavedWorkerState(hash, generation)
}

// beginSavedWorkerSyncLocked publishes an incomplete cache generation before
// its store lookup starts. Callers must hold savedWorkerMu and must have
// already installed the registered worker identity for this generation.
func (mc *MinerConn) beginSavedWorkerSyncLocked() uint64 {
	mc.savedWorkerSyncGen++
	mc.savedWorkerSyncing = true
	mc.savedWorkerTracked = false
	mc.savedWorkerBestDiff = 0
	return mc.savedWorkerSyncGen
}

func (mc *MinerConn) lookupSavedWorkerState(hash string, generation uint64) {
	if mc.savedWorkerStore == nil {
		mc.completeSavedWorkerSync(hash, generation, 0, false, false)
		return
	}
	best, ok, err := mc.savedWorkerStore.BestDifficultyForHash(hash)
	if err != nil {
		logger.Warn("saved worker best difficulty lookup failed", "error", err, "hash", hash)
		mc.completeSavedWorkerSync(hash, generation, 0, false, false)
		return
	}
	mc.completeSavedWorkerSync(hash, generation, best, ok, true)
}

// completeSavedWorkerSync applies a lookup only to the exact identity
// generation that started it. The generation check matters when a connection
// reauthorizes A -> B -> A while the first A lookup is still in flight.
func (mc *MinerConn) completeSavedWorkerSync(hash string, generation uint64, best float64, tracked, apply bool) bool {
	mc.savedWorkerMu.Lock()
	defer mc.savedWorkerMu.Unlock()
	if mc.registeredWorkerHash != hash || mc.savedWorkerSyncGen != generation || !mc.savedWorkerSyncing {
		return false
	}
	if apply {
		if tracked && mc.savedWorkerBestDiff > best {
			best = mc.savedWorkerBestDiff
		}
		mc.savedWorkerBestDiff = best
		mc.savedWorkerTracked = tracked
	}
	mc.savedWorkerSyncing = false
	return true
}

func (mc *MinerConn) maybeUpdateSavedWorkerBestDiff(diff float64) {
	if mc == nil {
		return
	}
	mc.savedWorkerMu.Lock()
	hash := mc.registeredWorkerHash
	mc.savedWorkerMu.Unlock()
	mc.maybeUpdateSavedWorkerBestDiffHash(hash, diff)
}

func (mc *MinerConn) maybeUpdateSavedWorkerBestDiffFor(worker string, diff float64) {
	if mc == nil {
		return
	}
	mc.maybeUpdateSavedWorkerBestDiffHash(workerNameHash(worker), diff)
}

func (mc *MinerConn) maybeUpdateSavedWorkerBestDiffHash(hash string, diff float64) {
	if mc == nil || mc.savedWorkerStore == nil || hash == "" || diff <= 0 {
		return
	}
	mc.savedWorkerMu.Lock()
	isCurrent := mc.registeredWorkerHash == hash
	tracked := mc.savedWorkerTracked
	best := mc.savedWorkerBestDiff
	syncing := isCurrent && mc.savedWorkerSyncing
	mc.savedWorkerMu.Unlock()
	if !isCurrent || syncing {
		var err error
		best, tracked, err = mc.savedWorkerStore.BestDifficultyForHash(hash)
		if err != nil {
			logger.Warn("saved worker best difficulty lookup failed", "error", err, "hash", hash)
			return
		}
	}
	if !tracked || diff <= best {
		return
	}
	if _, err := mc.savedWorkerStore.UpdateSavedWorkerBestDifficulty(hash, diff); err != nil {
		logger.Warn("saved worker best difficulty update failed", "error", err, "hash", hash)
		return
	}
	mc.savedWorkerMu.Lock()
	if mc.registeredWorkerHash == hash && diff > mc.savedWorkerBestDiff {
		mc.savedWorkerBestDiff = diff
	}
	mc.savedWorkerMu.Unlock()
}

func (mc *MinerConn) maybeUpdateSavedWorkerMinuteBestDiff(diff float64, now time.Time) {
	if mc == nil {
		return
	}
	mc.savedWorkerMu.Lock()
	hash := mc.registeredWorkerHash
	mc.savedWorkerMu.Unlock()
	mc.maybeUpdateSavedWorkerMinuteBestDiffHash(hash, diff, now)
}

func (mc *MinerConn) maybeUpdateSavedWorkerMinuteBestDiffFor(worker string, diff float64, now time.Time) {
	if mc == nil {
		return
	}
	mc.maybeUpdateSavedWorkerMinuteBestDiffHash(workerNameHash(worker), diff, now)
}

func (mc *MinerConn) maybeUpdateSavedWorkerMinuteBestDiffHash(hash string, diff float64, now time.Time) {
	if mc == nil || mc.savedWorkerStore == nil || hash == "" || diff <= 0 {
		return
	}
	mc.savedWorkerMu.Lock()
	isCurrent := mc.registeredWorkerHash == hash
	tracked := mc.savedWorkerTracked
	syncing := isCurrent && mc.savedWorkerSyncing
	mc.savedWorkerMu.Unlock()
	if !isCurrent || syncing {
		var err error
		_, tracked, err = mc.savedWorkerStore.BestDifficultyForHash(hash)
		if err != nil {
			logger.Warn("saved worker minute lookup failed", "error", err, "hash", hash)
			return
		}
	}
	if tracked {
		mc.savedWorkerStore.UpdateSavedWorkerMinuteBestDifficulty(hash, diff, now)
	}
}

// singlePayoutScript selects the output script for single-output coinbase
// paths. When pool_fee_percent is 0 (or negative), the full coinbase must go
// to the resolved worker wallet script; if no validated script is available,
// nil is returned so callers can fail fast.
func (mc *MinerConn) singlePayoutScript(job *Job, worker string) []byte {
	if job == nil || len(job.PayoutScript) == 0 {
		return nil
	}
	feePercent, _ := mc.jobPayoutPolicy(job)
	if mc == nil || feePercent > 0 {
		return job.PayoutScript
	}
	_, script, ok := mc.workerWalletDataRef(worker)
	if !ok || len(script) == 0 {
		return nil
	}
	return script
}

// jobPayoutPolicy returns the payout settings captured when a production job
// was built. The fallback preserves compatibility with lightweight Jobs built
// directly by tests and older internal helpers.
func (mc *MinerConn) jobPayoutPolicy(job *Job) (feePercent float64, payoutAddress string) {
	if job != nil && job.PayoutPolicyCaptured {
		return job.PoolFeePercent, job.PayoutAddress
	}
	if mc == nil {
		return 0, ""
	}
	return mc.cfg.PoolFeePercent, mc.cfg.PayoutAddress
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
	feePercent, _ = mc.jobPayoutPolicy(job)
	if feePercent <= 0 {
		return nil, nil, 0, 0, false
	}
	_, script, ok := mc.workerWalletDataRef(worker)
	if !ok || len(script) == 0 {
		return nil, nil, 0, 0, false
	}
	// Beneficiary identity is determined by the decoded scripts committed to
	// the coinbase. Address text is not safe here because Base58 is
	// case-sensitive while other address encodings have different case rules.
	if bytes.Equal(script, job.PayoutScript) {
		return nil, nil, 0, 0, false
	}

	return job.PayoutScript, script, job.CoinbaseValue, feePercent, true
}
