package main

import (
	"context"
	"encoding/binary"
	"sync/atomic"
)

func (jm *JobManager) CurrentJob() *Job {
	jm.mu.RLock()
	defer jm.mu.RUnlock()
	return jm.curJob
}

func (jm *JobManager) Ready() bool {
	jm.mu.RLock()
	defer jm.mu.RUnlock()
	return jm.curJob != nil
}

func (jm *JobManager) NextExtranonce1() []byte {
	id := atomic.AddUint32(&jm.extraID, 1)
	var buf [4]byte // Use fixed-size array instead of slice allocation
	binary.BigEndian.PutUint32(buf[:], id)
	return buf[:]
}

func (jm *JobManager) Subscribe() chan *Job {
	ch := make(chan *Job, jobSubscriberBuffer)
	jm.subsMu.Lock()
	jm.subs[ch] = struct{}{}
	jm.subsMu.Unlock()

	return ch
}

func (jm *JobManager) Unsubscribe(ch chan *Job) {
	jm.subsMu.Lock()
	delete(jm.subs, ch)
	close(ch)
	jm.subsMu.Unlock()
}

func (jm *JobManager) ActiveMiners() int {
	jm.subsMu.Lock()
	defer jm.subsMu.Unlock()
	return len(jm.subs)
}

func (jm *JobManager) broadcastJob(job *Job) {
	// Queue the job for async distribution instead of blocking here
	select {
	case jm.notifyQueue <- job:
		// Successfully queued for async processing
	default:
		// Queue is full, fall back to synchronous broadcast
		logger.Warn("notification queue full, falling back to sync broadcast")
		jm.broadcastJobSync(job)
	}
}

// sendJobNonBlocking attempts to deliver the latest job to a subscriber channel
// without blocking. If the channel is full, it drops one pending job and retries
// so the subscriber converges to the newest template.
func sendJobNonBlocking(ch chan *Job, job *Job) (dropped bool) {
	select {
	case ch <- job:
		return false
	default:
	}

	// Channel full: drop one stale job, then retry once.
	select {
	case <-ch:
		dropped = true
	default:
	}
	select {
	case ch <- job:
	default:
		dropped = true
	}
	return dropped
}

// broadcastJobSync performs synchronous job notification (fallback only)
func (jm *JobManager) broadcastJobSync(job *Job) {
	jm.subsMu.Lock()
	dropped := 0
	subscribers := len(jm.subs)
	for ch := range jm.subs {
		if sendJobNonBlocking(ch, job) {
			dropped++
		}
	}
	jm.subsMu.Unlock()

	if dropped > 0 {
		logger.Warn("job broadcast dropped stale updates (sync)", "subscribers", subscribers, "dropped", dropped)
	}
}

// notificationWorker processes job notifications asynchronously
func (jm *JobManager) notificationWorker(ctx context.Context, workerID int) {
	defer jm.notifyWg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case job, ok := <-jm.notifyQueue:
			if !ok {
				return
			}

			// Keep lock held during sends so Unsubscribe can't close channels
			// concurrently. Sends are non-blocking (drop/replace semantics).
			jm.subsMu.Lock()
			dropped := 0
			subscribers := len(jm.subs)
			for ch := range jm.subs {
				if sendJobNonBlocking(ch, job) {
					dropped++
				}
			}
			jm.subsMu.Unlock()

			if dropped > 0 {
				logger.Warn("job broadcast dropped stale updates", "worker", workerID, "subscribers", subscribers, "dropped", dropped)
			}
		}
	}
}
