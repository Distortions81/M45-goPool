package main

import (
	"math"
	"testing"
)

// FuzzDifficultyRoundTrip exercises the difficulty <-> target conversion
// over a wide range of difficulty values, checking basic invariants:
//   - targetFromDifficulty(diff) is positive and bounded
//   - difficultyFromHash(target) is finite and non-zero
//   - the round-trip ratio stays within a loose, but deterministic bound.
func FuzzDifficultyRoundTrip(f *testing.F) {
	// Seed with representative difficulties.
	seeds := []float64{
		0.0001, 0.001, 0.01, 0.1,
		0.5, 1, 2, 10, 100, 1e3, 1e6, 1e9,
	}
	for _, d := range seeds {
		f.Add(d)
	}

	f.Fuzz(func(t *testing.T, diff float64) {
		// Ignore non-positive and pathological inputs; production code
		// never passes these through targetFromDifficulty.
		if diff <= 0 || math.IsNaN(diff) || math.IsInf(diff, 0) {
			return
		}

		target := targetFromDifficulty(diff)
		if target.Sign() <= 0 {
			t.Fatalf("targetFromDifficulty(%v) <= 0", diff)
		}
		if target.Cmp(maxUint256) > 0 {
			t.Fatalf("targetFromDifficulty(%v) exceeded maxUint256", diff)
		}

		// Build a 32-byte hash that encodes the same integer as target
		// for difficultyFromHash to consume.
		nBytes := target.Bytes()
		if len(nBytes) > 32 {
			t.Fatalf("targetFromDifficulty(%v) produced %d bytes", diff, len(nBytes))
		}
		hash := make([]byte, 32)
		copy(hash, reverseBytes(nBytes))

		round := difficultyFromHash(hash)
		if round <= 0 || math.IsNaN(round) || math.IsInf(round, 0) {
			t.Fatalf("difficultyFromHash returned invalid value %v for diff %v", round, diff)
		}

		// The exact ratio will vary due to integer rounding; keep a
		// generous, but deterministic bound.
		ratio := round / diff
		if ratio < 0.1 || ratio > 10 {
			t.Fatalf("round-trip difficulty mismatch: start=%v got=%v ratio=%v", diff, round, ratio)
		}
	})
}

// FuzzTargetFromDifficultyMonotone checks that targetFromDifficulty is
// strictly monotone in the sense that larger difficulty never yields a
// larger target, and smaller difficulty never yields a smaller target.
func FuzzTargetFromDifficultyMonotone(f *testing.F) {
	f.Add(0.5, 1.0)
	f.Add(1.0, 2.0)
	f.Add(100.0, 1000.0)

	f.Fuzz(func(t *testing.T, a, b float64) {
		if a <= 0 || b <= 0 {
			return
		}
		ta := targetFromDifficulty(a)
		tb := targetFromDifficulty(b)
		if ta.Sign() <= 0 || tb.Sign() <= 0 {
			t.Fatalf("non-positive targets: ta=%v tb=%v", ta, tb)
		}
		switch {
		case a < b:
			if tb.Cmp(ta) >= 0 {
				t.Fatalf("expected target(%v) < target(%v); got %v >= %v", b, a, tb, ta)
			}
		case a > b:
			if tb.Cmp(ta) <= 0 {
				t.Fatalf("expected target(%v) > target(%v); got %v <= %v", b, a, tb, ta)
			}
		default:
			// a == b: targets should match exactly.
			if ta.Cmp(tb) != 0 {
				t.Fatalf("target(%v) != target(%v): %v vs %v", a, b, ta, tb)
			}
		}
	})
}
