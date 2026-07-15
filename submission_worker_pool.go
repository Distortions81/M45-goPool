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

func ensureSubmissionWorkerPool() *submissionWorkerPool {
	submissionWorkerOnce.Do(func() {
		workers := runtime.NumCPU()
		if workers <= 0 {
			workers = 1
		}
		submissionWorkers = newSubmissionWorkerPool(workers)
	})
	return submissionWorkers
}

type submissionTask struct {
	mc                  *MinerConn
	reqID               any
	job                 *Job
	jobID               string
	workerName          string
	extranonce2         string
	extranonce2Len      uint16
	extranonce2Bytes    [32]byte
	extranonce2Large    []byte
	ntime               string
	ntimeVal            uint32
	nonce               string
	nonceVal            uint32
	versionHex          string
	useVersion          uint32
	alternateVersionHex string
	alternateUseVersion uint32
	hasAlternateVersion bool
	blockRescueVersions [3]uint32
	blockRescueExtra    []uint32
	blockRescueCount    int
	notifiedCoinbase    notifiedCoinbaseParts
	hasNotifiedCoinbase bool
	scriptTime          int64
	assignedDifficulty  float64
	policyReject        submitPolicyReject
	banPolicy           *bannedSubmitPolicy
	receivedAt          time.Time
}

func (t *submissionTask) blockRescueVersion(index int) (uint32, bool) {
	if t == nil || index < 0 || index >= t.blockRescueCount {
		return 0, false
	}
	if index < len(t.blockRescueVersions) {
		return t.blockRescueVersions[index], true
	}
	extraIndex := index - len(t.blockRescueVersions)
	if extraIndex < 0 || extraIndex >= len(t.blockRescueExtra) {
		return 0, false
	}
	return t.blockRescueExtra[extraIndex], true
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

type submitPolicyReject struct {
	reason  submitRejectReason
	errCode int
	errMsg  string
}

type bannedSubmitPolicy struct {
	until  time.Time
	reason string
	err    []any
}

type submissionWorkerPool struct {
	tasks chan submissionTask

	// submitMu keeps sends and channel closure mutually exclusive. A read lock
	// is held across each send so a task accepted before shutdown is always
	// present in the channel before drainAndClose closes it. Workers do not need
	// this lock, so a full queue can continue draining while shutdown waits for
	// blocked senders to finish.
	submitMu  sync.RWMutex
	closed    bool
	closeOnce sync.Once
	workerWg  sync.WaitGroup
}

func newSubmissionWorkerPool(workerCount int) *submissionWorkerPool {
	if workerCount <= 0 {
		workerCount = 1
	}
	queueDepth := max(workerCount*submissionWorkerQueueMultiplier, submissionWorkerQueueMinDepth)
	pool := &submissionWorkerPool{
		tasks: make(chan submissionTask, queueDepth),
	}
	pool.workerWg.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		go pool.worker(i)
	}
	return pool
}

// submit queues task unless shutdown has already closed the pool. Holding the
// read lock through the send prevents a concurrent close from racing the send.
func (p *submissionWorkerPool) submit(task submissionTask) bool {
	if p == nil || p.tasks == nil {
		return false
	}
	p.submitMu.RLock()
	defer p.submitMu.RUnlock()
	if p.closed {
		return false
	}
	p.tasks <- task
	return true
}

// drainAndClose stops new submissions and waits until every task accepted by
// the pool has finished processing. It is safe to call more than once.
func (p *submissionWorkerPool) drainAndClose() {
	if p == nil {
		return
	}
	p.closeOnce.Do(func() {
		p.submitMu.Lock()
		p.closed = true
		if p.tasks != nil {
			close(p.tasks)
		}
		p.submitMu.Unlock()
	})
	p.workerWg.Wait()
}

func (p *submissionWorkerPool) worker(id int) {
	defer p.workerWg.Done()
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
