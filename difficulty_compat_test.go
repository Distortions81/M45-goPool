package main

import (
	"encoding/hex"
	"math/big"
	"strconv"
	"testing"

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/chaincfg"
)

// TestDifficultyFromHashCompatWithBtcdWork verifies that goPool's
// difficultyFromHash helper is consistent with btcd's getdifficulty ratio
// (as implemented by getDifficultyRatio) for synthetic hashes that sit
// exactly on the target boundary for a given bits value.
func TestDifficultyFromHashCompatWithBtcdWork(t *testing.T) {
	testCases := []struct {
		name string
		bits string // compact representation (8 hex chars)
	}{
		{name: "difficulty_1", bits: "1d00ffff"},
		{name: "higher_difficulty", bits: "1b0404cb"}, // ~16307 diff
		{name: "lower_difficulty", bits: "1e00ffff"},  // easier than diff1
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			bitsBytes, err := hex.DecodeString(tc.bits)
			if err != nil || len(bitsBytes) != 4 {
				t.Fatalf("failed to decode bits %q: %v", tc.bits, err)
			}
			compact := uint32(bitsBytes[0])<<24 |
				uint32(bitsBytes[1])<<16 |
				uint32(bitsBytes[2])<<8 |
				uint32(bitsBytes[3])

			target := blockchain.CompactToBig(compact)
			if target.Sign() <= 0 {
				t.Fatalf("CompactToBig(%#08x) produced non-positive target", compact)
			}

			// Construct a synthetic "hash" whose LE interpretation matches
			// the target exactly. difficultyFromHash reverses the hash bytes
			// before feeding them to big.Int, so we provide the little-endian
			// form of the target here.
			targetBE := target.FillBytes(make([]byte, 32))
			hash := reverseBytes(targetBE)

			gotDiff := difficultyFromHash(hash)

			// Compute btcd's difficulty ratio as implemented by
			// getDifficultyRatio: max/target where max is derived from
			// PowLimitBits.
			max := blockchain.CompactToBig(chaincfg.MainNetParams.PowLimitBits)
			if max.Sign() <= 0 {
				t.Fatalf("PowLimitBits produced non-positive max target")
			}
			ratio := new(big.Rat).SetFrac(max, target)
			outStr := ratio.FloatString(8)
			expectedDiff, err := strconv.ParseFloat(outStr, 64)
			if err != nil {
				t.Fatalf("ParseFloat difficulty ratio error: %v", err)
			}

			// Allow a tiny relative error to account for integer rounding in
			// CalcWork vs the ideal diff1Target/target formula.
			if expectedDiff == 0 {
				if gotDiff != 0 {
					t.Fatalf("expected zero difficulty, got %.12f", gotDiff)
				}
				return
			}

			diff := gotDiff - expectedDiff
			if diff < 0 {
				diff = -diff
			}
			relErr := diff / expectedDiff
			const maxRelErr = 1e-6
			if relErr > maxRelErr {
				t.Fatalf("difficulty mismatch for bits=%s:\n  goPool=%.12f\n  btcd ~= %.12f\n  relErr=%.12g (max %.12g)",
					tc.bits, gotDiff, expectedDiff, relErr, maxRelErr)
			}
		})
	}
}
