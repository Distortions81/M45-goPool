package main

import (
	"context"
	"testing"
	"time"

	"github.com/remeh/sizedwaitgroup"
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

func TestShardedFanoutPreservesPerSubscriberOrder(t *testing.T) {
	jm := NewJobManager(nil, Config{}, nil, nil, nil)
	const subscribers = jobFanoutSubscribersPerShard + 32
	channels := make([]chan *Job, 0, subscribers)
	for range subscribers {
		channels = append(channels, jm.Subscribe())
	}
	if got := jobFanoutShardCount(subscribers); got < 2 {
		t.Fatalf("fanout shard count = %d, want at least 2", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	jm.notifyWg = sizedwaitgroup.New(1)
	jm.notifyWg.Add()
	go jm.notificationWorker(ctx, 0)

	first := &Job{JobID: "first", Generation: 1}
	second := &Job{JobID: "second", Generation: 2}
	jm.broadcastJob(first)
	jm.broadcastJob(second)
	for i, ch := range channels {
		select {
		case got := <-ch:
			if got != first {
				t.Fatalf("subscriber %d first job = %v, want first", i, got)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("subscriber %d timed out waiting for first job", i)
		}
		select {
		case got := <-ch:
			if got != second {
				t.Fatalf("subscriber %d second job = %v, want second", i, got)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("subscriber %d timed out waiting for second job", i)
		}
	}
	cancel()
	jm.notifyWg.Wait()
}
