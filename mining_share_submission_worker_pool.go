package main

import (
	"runtime"
	"sync"
	"time"
)

const (
	// submissionWorkerQueueMultiplier determines how much backlog we allow
	// per worker goroutine.
	submissionWorkerQueueMultiplier = 32
	// submissionWorkerQueueMinDepth ensures the queue can hold at least this
	// many tasks regardless of CPU count.
	submissionWorkerQueueMinDepth = 128
)

var (
	submissionWorkers    *submissionWorkerPool
	submissionWorkerOnce sync.Once
)

func ensureSubmissionWorkerPool() {
	submissionWorkerOnce.Do(func() {
		workers := runtime.NumCPU()
		if workers <= 0 {
			workers = 1
		}
		submissionWorkers = newSubmissionWorkerPool(workers)
	})
}

type submissionTask struct {
	mc               *MinerConn
	submitHooks      miningShareSubmitHooks
	reqID            any
	job              *Job
	jobID            string
	workerName       string
	extranonce2      string
	extranonce2Len   uint16
	extranonce2Bytes [32]byte
	extranonce2Large []byte
	ntime            string
	ntimeVal         uint32
	nonce            string
	nonceVal         uint32
	versionHex       string
	useVersion       uint32
	scriptTime       int64
	policyReject     sharePolicyReject
	receivedAt       time.Time
}

func (t *submissionTask) extranonce2Decoded() []byte {
	if t == nil {
		return nil
	}
	if t.extranonce2Large != nil {
		return t.extranonce2Large
	}
	n := int(t.extranonce2Len)
	if n <= 0 {
		return nil
	}
	if n > len(t.extranonce2Bytes) {
		n = len(t.extranonce2Bytes)
	}
	return t.extranonce2Bytes[:n]
}

func (t *submissionTask) hooksOrDefault(mc *MinerConn) miningShareSubmitHooks {
	if t != nil && t.submitHooks != nil {
		return t.submitHooks
	}
	return newStratumV1MiningShareSubmitHooks(mc)
}

type sharePolicyReject struct {
	reason  shareRejectReason
	errCode int
	errMsg  string
}

type submissionWorkerPool struct {
	tasks chan submissionTask
}

func newSubmissionWorkerPool(workerCount int) *submissionWorkerPool {
	if workerCount <= 0 {
		workerCount = 1
	}
	queueDepth := max(workerCount*submissionWorkerQueueMultiplier, submissionWorkerQueueMinDepth)
	pool := &submissionWorkerPool{
		tasks: make(chan submissionTask, queueDepth),
	}
	for i := 0; i < workerCount; i++ {
		go pool.worker(i)
	}
	return pool
}

func (p *submissionWorkerPool) submit(task submissionTask) {
	p.tasks <- task
}

func (p *submissionWorkerPool) worker(id int) {
	for task := range p.tasks {
		func(t submissionTask) {
			defer func() {
				if r := recover(); r != nil {
					logger.Error("submission worker panic", "worker", id, "error", r)
				}
			}()
			t.mc.processSubmissionTask(t)
		}(task)
	}
}
