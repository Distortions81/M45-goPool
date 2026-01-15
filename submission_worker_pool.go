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
	reqID            interface{}
	job              *Job
	jobID            string
	workerName       string
	extranonce2      string
	extranonce2Bytes []byte
	ntime            string
	nonce            string
	versionHex       string
	useVersion       uint32
	scriptTime       int64
	policyReject     submitPolicyReject
	receivedAt       time.Time
}

type submitPolicyReject struct {
	reason  submitRejectReason
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
	queueDepth := workerCount * submissionWorkerQueueMultiplier
	if queueDepth < submissionWorkerQueueMinDepth {
		queueDepth = submissionWorkerQueueMinDepth
	}
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
