package main

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/chaincfg"
)

const fastEmptyMedianTimeWindow = 11

// activateFastEmptyJob publishes valid coinbase-only work for the direct child
// of the newly connected raw block. Every prerequisite is checked locally; an
// uncertain case is skipped so the ordinary full-GBT race remains authoritative.
func (jm *JobManager) activateFastEmptyJob(tip ZMQBlockTip, triggeredAt time.Time) (*Job, bool, error) {
	if jm == nil || tip.Hash == "" || tip.previousHash == "" || tip.Height < 0 || tip.Time.IsZero() {
		return nil, false, nil
	}
	if triggeredAt.IsZero() {
		triggeredAt = time.Now()
	}

	jm.applyMu.Lock()
	defer jm.applyMu.Unlock()

	jm.mu.RLock()
	previousJob := jm.curJob
	jm.mu.RUnlock()
	if previousJob == nil || previousJob.FastEmpty {
		return nil, false, nil
	}
	// The raw block must be the exact candidate height and direct child of the
	// full template currently being mined. This excludes reorgs, skipped ZMQ
	// events, and startup without an authoritative base template.
	if previousJob.Template.Height != tip.Height ||
		previousJob.Template.Previous != tip.previousHash ||
		previousJob.Template.Bits != tip.Bits {
		return nil, false, nil
	}

	nextHeight := tip.Height + 1
	if !fastEmptyCanReuseTipBits(nextHeight) {
		return nil, false, nil
	}
	medianTime, ok := jm.fastEmptyMedianTime(tip)
	if !ok {
		return nil, false, nil
	}

	curTime := medianTime + 1
	coreNow := previousJob.Template.CurTime
	if coreNow > 0 && !previousJob.CreatedAt.IsZero() {
		elapsed := time.Since(previousJob.CreatedAt)
		if elapsed > 0 {
			coreNow += int64(elapsed / time.Second)
		}
	}
	if coreNow > curTime {
		curTime = coreNow
	}
	if curTime <= medianTime || curTime > math.MaxUint32 || nextHeight > math.MaxInt32 {
		return nil, false, nil
	}

	target, err := targetFromBits(tip.Bits)
	if err != nil || target.Sign() <= 0 || target.BitLen() > 256 {
		return nil, false, nil
	}

	tpl := previousJob.Template
	tpl.Previous = tip.Hash
	tpl.Height = nextHeight
	tpl.Bits = tip.Bits
	tpl.Target = fmt.Sprintf("%064x", target)
	tpl.CurTime = curTime
	tpl.Mintime = medianTime + 1
	tpl.CoinbaseValue = blockchain.CalcBlockSubsidy(int32(nextHeight), ChainParams())
	tpl.DefaultWitnessCommitment = ""
	tpl.LongPollID = ""
	tpl.Transactions = nil

	job, err := jm.buildFastEmptyJobLocked(tpl)
	if err != nil {
		return nil, false, err
	}
	job.Clean = true
	job.FastEmpty = true
	job.fastEmptyTriggeredAt = triggeredAt

	jm.mu.Lock()
	// applyMu prevents a template apply from changing curJob between the direct
	// successor checks above and this publication.
	jm.curJob = job
	jm.mu.Unlock()

	logger.Info("new fast empty job", "height", tpl.Height, "job_id", job.JobID, "bits", tpl.Bits, "parent", tpl.Previous)
	jm.broadcastJob(job)
	jm.recordJobSuccess(job)
	return job, true, nil
}

// fastEmptyCanReuseTipBits identifies heights where the newly connected
// block's nBits is also the required nBits for its child. Retarget boundaries
// and test-style reduced-difficulty rules require more chain context and are
// intentionally left to Core's full template.
func fastEmptyCanReuseTipBits(nextHeight int64) bool {
	if nextHeight <= 0 {
		return false
	}
	params := ChainParams()
	if params == nil {
		return false
	}
	// Mainnet has deterministic same-bits intervals and regtest is useful for
	// end-to-end validation. Signet requires additional block-signing data in
	// the coinbase, while public test networks have special minimum-difficulty
	// rules, so neither may use this shortcut.
	if params.Name != chaincfg.MainNetParams.Name && params.Name != chaincfg.RegressionNetParams.Name {
		return false
	}
	if window := int64(params.MinerConfirmationWindow); window > 0 && nextHeight%window == 0 {
		return false
	}
	if params.PoWNoRetargeting {
		return true
	}
	if params.ReduceMinDifficulty {
		return false
	}
	if params.TargetTimePerBlock <= 0 {
		return false
	}
	interval := int64(params.TargetTimespan / params.TargetTimePerBlock)
	return interval > 0 && nextHeight%interval != 0
}

// fastEmptyMedianTime calculates the exact median-time-past for a candidate
// extending tip. The cached timestamps must end at tip's direct parent.
func (jm *JobManager) fastEmptyMedianTime(tip ZMQBlockTip) (int64, bool) {
	jm.zmqPayloadMu.RLock()
	historyTip := jm.consensusHistoryTip
	historyHeight := jm.consensusHistoryHeight
	history := append([]time.Time(nil), jm.consensusHistoryTimes...)
	jm.zmqPayloadMu.RUnlock()

	if historyTip != tip.previousHash || historyHeight != tip.Height-1 {
		return 0, false
	}
	priorNeeded := fastEmptyMedianTimeWindow - 1
	if tip.Height < int64(priorNeeded) {
		priorNeeded = int(tip.Height)
	}
	if priorNeeded < 0 || len(history) < priorNeeded {
		return 0, false
	}

	times := make([]int64, 0, priorNeeded+1)
	for _, ts := range history[len(history)-priorNeeded:] {
		if ts.IsZero() {
			return 0, false
		}
		times = append(times, ts.Unix())
	}
	times = append(times, tip.Time.Unix())
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	return times[len(times)/2], true
}
