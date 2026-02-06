package main

import (
	"encoding/hex"
	"strings"
	"time"
)

func (s *StatusServer) setWorkerCurrentJobCoinbase(data *WorkerStatusData, job *Job, wv WorkerView) {
	if data == nil || job == nil {
		return
	}
	data.CurrentJobID = strings.TrimSpace(job.JobID)
	data.CurrentJobHeight = job.Template.Height
	data.CurrentJobPrevHash = strings.TrimSpace(job.PrevHash)

	coinbase, ok := buildWorkerCoinbaseForJob(job, wv, s.Config())
	if ok {
		data.CurrentJobCoinbase = coinbase
	}
}

func buildWorkerCoinbaseForJob(job *Job, wv WorkerView, cfg Config) (*ShareDetail, bool) {
	if job == nil || job.CoinbaseValue <= 0 {
		return nil, false
	}
	if len(job.PayoutScript) == 0 {
		return nil, false
	}

	extranonce1 := make([]byte, coinbaseExtranonce1Size)
	scriptTime := job.ScriptTime

	var workerScript []byte
	if ws := strings.TrimSpace(wv.WalletScript); ws != "" {
		if b, err := hex.DecodeString(ws); err == nil && len(b) > 0 {
			workerScript = b
		}
	}

	var (
		coinb1 string
		coinb2 string
		err    error
	)
	workerAddr := strings.TrimSpace(wv.WalletAddress)
	poolAddr := strings.TrimSpace(cfg.PayoutAddress)
	useDual := cfg.PoolFeePercent > 0 && len(workerScript) > 0 && !strings.EqualFold(workerAddr, poolAddr)
	if useDual {
		if job.OperatorDonationPercent > 0 && len(job.DonationScript) > 0 {
			coinb1, coinb2, err = buildTriplePayoutCoinbaseParts(
				job.Template.Height,
				extranonce1,
				job.Extranonce2Size,
				job.TemplateExtraNonce2Size,
				job.PayoutScript,
				job.DonationScript,
				workerScript,
				job.CoinbaseValue,
				cfg.PoolFeePercent,
				job.OperatorDonationPercent,
				job.WitnessCommitment,
				job.Template.CoinbaseAux.Flags,
				job.CoinbaseMsg,
				scriptTime,
			)
		} else {
			coinb1, coinb2, err = buildDualPayoutCoinbaseParts(
				job.Template.Height,
				extranonce1,
				job.Extranonce2Size,
				job.TemplateExtraNonce2Size,
				job.PayoutScript,
				workerScript,
				job.CoinbaseValue,
				cfg.PoolFeePercent,
				job.WitnessCommitment,
				job.Template.CoinbaseAux.Flags,
				job.CoinbaseMsg,
				scriptTime,
			)
		}
	}
	if coinb1 == "" || coinb2 == "" || err != nil {
		coinb1, coinb2, err = buildCoinbaseParts(
			job.Template.Height,
			extranonce1,
			job.Extranonce2Size,
			job.TemplateExtraNonce2Size,
			job.PayoutScript,
			job.CoinbaseValue,
			job.WitnessCommitment,
			job.Template.CoinbaseAux.Flags,
			job.CoinbaseMsg,
			scriptTime,
		)
	}
	if err != nil || coinb1 == "" || coinb2 == "" {
		return nil, false
	}

	extranonce2Size := job.Extranonce2Size
	if extranonce2Size < 0 {
		extranonce2Size = 0
	}
	extranonce2 := make([]byte, extranonce2Size)
	coinbaseHex := coinb1 + hex.EncodeToString(extranonce1) + hex.EncodeToString(extranonce2) + coinb2

	detail := &ShareDetail{Coinbase: coinbaseHex}
	detail.DecodeCoinbaseFields()
	return detail, true
}

// cacheWorkerPage stores a rendered worker-status HTML payload in the
// workerPageCache, enforcing the workerPageCacheLimit and pruning expired
// entries when necessary.
func (s *StatusServer) cacheWorkerPage(key string, now time.Time, payload []byte) {
	entry := cachedWorkerPage{
		payload:   append([]byte(nil), payload...),
		updatedAt: now,
		expiresAt: now.Add(overviewRefreshInterval),
	}
	s.workerPageMu.Lock()
	defer s.workerPageMu.Unlock()
	if s.workerPageCache == nil {
		s.workerPageCache = make(map[string]cachedWorkerPage)
	}
	// Enforce a simple size bound: evict expired entries first, then
	// fall back to removing an arbitrary entry if still at capacity.
	if workerPageCacheLimit > 0 && len(s.workerPageCache) >= workerPageCacheLimit {
		for k, v := range s.workerPageCache {
			if now.After(v.expiresAt) {
				delete(s.workerPageCache, k)
				break
			}
		}
		if len(s.workerPageCache) >= workerPageCacheLimit {
			for k := range s.workerPageCache {
				delete(s.workerPageCache, k)
				break
			}
		}
	}
	s.workerPageCache[key] = entry
}
