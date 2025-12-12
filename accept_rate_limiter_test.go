package main

import (
	"context"
	"testing"
	"time"
)

// TestNewAcceptRateLimiterZeroRateReturnsNil verifies that a non-positive
// rate disables the limiter (returns nil).
func TestNewAcceptRateLimiterZeroRateReturnsNil(t *testing.T) {
	if l := newAcceptRateLimiter(0, 0); l != nil {
		t.Fatalf("expected nil limiter when maxPerSecond <= 0, got %#v", l)
	}
}

// TestAcceptRateLimiterRefillOverTime verifies that tokens are refilled based
// on elapsed time and capped at the burst size before a wait call consumes
// one token.
func TestAcceptRateLimiterRefillOverTime(t *testing.T) {
	l := newAcceptRateLimiter(10, 5) // 10 tokens/sec, burst 5
	if l == nil {
		t.Fatalf("expected non-nil limiter")
	}

	// Simulate a state where we ran out of tokens 1 second ago.
	l.mu.Lock()
	l.tokens = 0
	l.last = time.Now().Add(-1 * time.Second)
	l.mu.Unlock()

	start := time.Now()
	if !l.wait(context.Background()) {
		t.Fatalf("expected wait to succeed")
	}
	elapsed := time.Since(start)
	// For this setup, wait should not block because refill should have
	// produced enough tokens to allow an immediate consume.
	if elapsed > 50*time.Millisecond {
		t.Fatalf("wait blocked unexpectedly: elapsed=%s", elapsed)
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.tokens <= 0 {
		t.Fatalf("expected tokens > 0 after refill and consume, got %f", l.tokens)
	}
	if l.tokens >= l.burst {
		t.Fatalf("expected tokens to be below burst after consume, got tokens=%f burst=%f", l.tokens, l.burst)
	}
}

// TestAcceptRateLimiterUpdateRateClampsTokens verifies that updateRate
// adjusts rate/burst and clamps tokens when the new burst is lower.
func TestAcceptRateLimiterUpdateRateClampsTokens(t *testing.T) {
	l := newAcceptRateLimiter(20, 10)
	if l == nil {
		t.Fatalf("expected non-nil limiter")
	}

	l.mu.Lock()
	l.tokens = 10 // full burst under old settings
	l.mu.Unlock()

	// Shrink burst capacity; tokens should be clamped to the new burst.
	l.updateRate(5, 3)

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.rate != 5 {
		t.Fatalf("expected rate=5, got %f", l.rate)
	}
	if l.burst != 3 {
		t.Fatalf("expected burst=3, got %f", l.burst)
	}
	if l.tokens > l.burst {
		t.Fatalf("expected tokens <= burst after update, got tokens=%f burst=%f", l.tokens, l.burst)
	}
}
