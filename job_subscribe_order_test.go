package main

import (
	"testing"
	"time"
)

func TestBroadcastJobFullQueueKeepsNewestPendingJob(t *testing.T) {
	jm := &JobManager{
		subs:        make(map[chan *Job]struct{}),
		notifyQueue: make(chan *Job, 2),
	}
	oldest := &Job{JobID: "oldest"}
	older := &Job{JobID: "older"}
	newest := &Job{JobID: "newest"}
	jm.notifyQueue <- oldest
	jm.notifyQueue <- older

	jm.broadcastJob(newest)

	first := <-jm.notifyQueue
	second := <-jm.notifyQueue
	if first != older || second != newest {
		t.Fatalf("queued jobs = [%s, %s], want [older, newest]", first.JobID, second.JobID)
	}
}

func TestMiningJobOrderingUsesGenerationNotHeight(t *testing.T) {
	current := &Job{Generation: 10, Template: GetBlockTemplateResult{Height: 100}}
	reorg := &Job{Generation: 11, Template: GetBlockTemplateResult{Height: 99}}
	delayed := &Job{Generation: 9, Template: GetBlockTemplateResult{Height: 101}}

	if miningJobIsOlder(reorg, current) {
		t.Fatal("newer-generation lower-height reorg job was classified as old")
	}
	if !miningJobIsOlder(delayed, current) {
		t.Fatal("older-generation higher-height delayed job was not classified as old")
	}

	// Lightweight manually-created jobs without generations retain a timestamp
	// fallback for tests and internal compatibility paths.
	now := time.Now()
	if !miningJobIsOlder(&Job{CreatedAt: now.Add(-time.Second)}, &Job{CreatedAt: now}) {
		t.Fatal("timestamp fallback did not classify an older manual job")
	}
}
