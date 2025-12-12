package main

import (
	"math"
	"math/big"
	"testing"
)

// TestTargetFromDifficulty_RoundTrip checks that targetFromDifficulty and
// difficultyFromHash roughly invert each other for a range of difficulty
// values. We don't compare against btcd directly here, but we assert stable
// self-consistency, which is what the pool logic relies on.
func TestTargetFromDifficulty_RoundTrip(t *testing.T) {
	diffs := []float64{0.5, 1, 2, 10, 1000, 1e6}
	for _, diff := range diffs {
		target := targetFromDifficulty(diff)
		if target.Sign() <= 0 {
			t.Fatalf("targetFromDifficulty(%v) returned non-positive target", diff)
		}
		// Build a fake SHA256 hash such that the underlying integer used
		// for difficultyFromHash matches the target. difficultyFromHash
		// reverses the bytes before SetBytes, so we reverse here as well.
		nBytes := target.Bytes()
		if len(nBytes) > 32 {
			t.Fatalf("targetFromDifficulty(%v) produced value larger than 32 bytes", diff)
		}
		hash := make([]byte, 32)
		copy(hash, reverseBytes(nBytes))

		round := difficultyFromHash(hash)
		if round <= 0 || math.IsInf(round, 0) || math.IsNaN(round) {
			t.Fatalf("difficultyFromHash produced invalid value %v for diff %v", round, diff)
		}
		ratio := round / diff
		if ratio < 0.25 || ratio > 4 {
			t.Fatalf("round-trip difficulty mismatch: start=%v got=%v ratio=%v", diff, round, ratio)
		}
	}
}

// TestTargetFromDifficulty_Monotonicity ensures that higher difficulty values
// yield lower targets, which is critical for PoW comparison.
func TestTargetFromDifficulty_Monotonicity(t *testing.T) {
	base := targetFromDifficulty(1)
	higher := targetFromDifficulty(2)
	lower := targetFromDifficulty(0.5)

	if higher.Cmp(base) >= 0 {
		t.Fatalf("expected target(diff=2) < target(diff=1); got %v >= %v", higher, base)
	}
	if lower.Cmp(base) <= 0 {
		t.Fatalf("expected target(diff=0.5) > target(diff=1); got %v <= %v", lower, base)
	}
}

// TestDiff1TargetMatchesCompact ensures diff1Target matches the expected
// compact representation used by Bitcoin (0x1d00ffff).
func TestDiff1TargetMatchesCompact(t *testing.T) {
	// Compact representation of Bitcoin's pow limit (difficulty 1).
	const compact uint32 = 0x1d00ffff

	// Convert compact to big.Int using the same algorithm as Bitcoin/ btcd.
	size := compact >> 24
	word := compact & 0x007fffff
	var n big.Int
	if size <= 3 {
		word >>= 8 * (3 - size)
		n.SetInt64(int64(word))
	} else {
		n.SetInt64(int64(word))
		n.Lsh(&n, uint(8*(size-3)))
	}

	if diff1Target.Cmp(&n) != 0 {
		t.Fatalf("diff1Target mismatch: got %x, want %x", diff1Target, &n)
	}
}
