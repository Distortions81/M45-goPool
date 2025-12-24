package main

import (
	"context"
	"sync"
	"time"
)

// acceptRateLimiter is a simple token-bucket limiter for new TCP accepts. It
// enforces an average rate (tokens added per second) with a small burst
// capacity so short spikes are allowed without overwhelming the node.
type acceptRateLimiter struct {
	rate   float64   // tokens per second
	burst  float64   // maximum tokens
	tokens float64   // current tokens
	last   time.Time // last refill time
	mu     sync.Mutex
}

func newAcceptRateLimiter(maxPerSecond, burst int) *acceptRateLimiter {
	if maxPerSecond <= 0 {
		return nil
	}
	rate := float64(maxPerSecond)
	burstSize := float64(burst)
	if burstSize <= 0 {
		burstSize = rate
	}
	// By default we allow up to one second's worth of connections to
	// arrive in a short spike; operators can raise/lower burst via
	// config to suit their hardware.
	return &acceptRateLimiter{
		rate:   rate,
		burst:  burstSize,
		tokens: burstSize,
		last:   time.Now(),
	}
}

// updateRate dynamically updates the rate and burst capacity of the limiter.
// This is used to transition from reconnection mode to steady-state mode.
func (l *acceptRateLimiter) updateRate(newRate, newBurst int) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	l.rate = float64(newRate)
	l.burst = float64(newBurst)
	if l.tokens > l.burst {
		l.tokens = l.burst
	}
}

// wait blocks as needed so that no more than "rate" accepts occur on average,
// with up to "burst" accepts allowed in a short spike. It respects ctx
// cancellation so shutdown is not delayed by the limiter.
func (l *acceptRateLimiter) wait(ctx context.Context) bool {
	if l == nil {
		return true
	}

	l.mu.Lock()
	if l.rate <= 0 {
		l.mu.Unlock()
		return true
	}

	now := time.Now()
	if l.last.IsZero() {
		l.last = now
	}
	elapsed := now.Sub(l.last).Seconds()
	if elapsed > 0 {
		l.tokens += elapsed * l.rate
		if l.tokens > l.burst {
			l.tokens = l.burst
		}
		l.last = now
	}

	if l.tokens >= 1 {
		l.tokens -= 1
		l.mu.Unlock()
		return true
	}

	need := 1 - l.tokens
	rate := l.rate
	l.mu.Unlock()

	wait := time.Duration(need / rate * float64(time.Second))
	if wait <= 0 {
		wait = time.Millisecond
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
	}

	l.mu.Lock()
	l.last = time.Now()
	if l.tokens < 1 {
		l.tokens = 0
	} else {
		l.tokens -= 1
	}
	l.mu.Unlock()
	return true
}
