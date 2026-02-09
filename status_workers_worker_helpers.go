package main

import (
	"strings"
	"sync/atomic"
	"time"
)

func (s *StatusServer) setWorkerCurrentJobCoinbase(data *WorkerStatusData, job *Job, wv WorkerView) {
	if data == nil || job == nil {
		return
	}
	data.CurrentJobID = strings.TrimSpace(job.JobID)
	data.CurrentJobHeight = job.Template.Height
	data.CurrentJobPrevHash = strings.TrimSpace(job.PrevHash)

	// Only show current-job coinbase data when we can derive it from the live
	// miner connection state for this worker.
	if mc := s.connectionForWorkerView(wv); mc != nil {
		data.CurrentJobCoinbase = mc.buildCurrentJobCoinbaseDetail(job)
	}
}

func (s *StatusServer) connectionForWorkerView(wv WorkerView) *MinerConn {
	if s == nil || s.workerRegistry == nil {
		return nil
	}
	if wv.ConnectionSeq > 0 {
		if mc := s.workerRegistry.connectionBySeq(wv.ConnectionSeq); mc != nil {
			return mc
		}
	}
	hash := strings.TrimSpace(wv.WorkerSHA256)
	if hash == "" {
		return nil
	}
	conns := s.workerRegistry.getConnectionsByHash(hash)
	if len(conns) == 0 {
		return nil
	}
	var best *MinerConn
	var bestSeq uint64
	for _, mc := range conns {
		if mc == nil {
			continue
		}
		seq := atomic.LoadUint64(&mc.connectionSeq)
		if seq >= bestSeq {
			best = mc
			bestSeq = seq
		}
	}
	return best
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
