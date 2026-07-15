package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newLifecycleTestSubmissionPool(queueDepth int, process func(preparedSubmissionTask)) *submissionWorkerPool {
	if queueDepth <= 0 {
		queueDepth = 1
	}
	pool := &submissionWorkerPool{tasks: make(chan preparedSubmissionTask, queueDepth)}
	pool.workerWg.Add(1)
	go func() {
		defer pool.workerWg.Done()
		for task := range pool.tasks {
			process(task)
		}
	}()
	return pool
}

func TestSubmissionWorkerPoolDrainWaitsForQueuedAndInFlightTasks(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var processed atomic.Int32
	pool := newLifecycleTestSubmissionPool(2, func(preparedSubmissionTask) {
		started <- struct{}{}
		<-release
		processed.Add(1)
	})

	if pool.trySubmit(preparedSubmissionTask{}) != submissionAdmissionAccepted ||
		pool.trySubmit(preparedSubmissionTask{}) != submissionAdmissionAccepted {
		t.Fatal("expected both pre-shutdown tasks to be accepted")
	}

	drained := make(chan struct{})
	go func() {
		pool.drainAndClose()
		close(drained)
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start the first task")
	}
	select {
	case <-drained:
		t.Fatal("drain returned while an accepted task was still in flight")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		t.Fatal("drain did not finish after queued tasks completed")
	}
	if got := processed.Load(); got != 2 {
		t.Fatalf("processed tasks = %d, want 2", got)
	}
	if pool.trySubmit(preparedSubmissionTask{}) != submissionAdmissionClosed {
		t.Fatal("closed pool accepted a new task")
	}

	// Repeated shutdown must be harmless and must not close the channel twice.
	pool.drainAndClose()
}

func TestSubmissionWorkerPoolConcurrentSubmitAndDrain(t *testing.T) {
	var processed atomic.Int32
	pool := newLifecycleTestSubmissionPool(4, func(preparedSubmissionTask) {
		processed.Add(1)
	})

	const producers = 128
	start := make(chan struct{})
	var accepted atomic.Int32
	var submitters sync.WaitGroup
	submitters.Add(producers)
	for i := 0; i < producers; i++ {
		go func() {
			defer submitters.Done()
			<-start
			if pool.trySubmit(preparedSubmissionTask{}) == submissionAdmissionAccepted {
				accepted.Add(1)
			}
		}()
	}

	close(start)
	drained := make(chan struct{})
	go func() {
		pool.drainAndClose()
		close(drained)
	}()
	submitters.Wait()
	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent drain did not complete")
	}
	if got, want := processed.Load(), accepted.Load(); got != want {
		t.Fatalf("processed tasks = %d, accepted tasks = %d", got, want)
	}
}

func TestSubmissionWorkerPoolCreatedWorkersExitOnDrain(t *testing.T) {
	pool := newSubmissionWorkerPool(2)
	pool.drainAndClose()
	if pool.trySubmit(preparedSubmissionTask{}) != submissionAdmissionClosed {
		t.Fatal("drained production pool accepted a task")
	}
}
