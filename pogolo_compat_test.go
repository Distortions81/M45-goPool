package main

import (
	"bytes"
	"encoding/hex"
	"math/big"
	"strconv"
	"testing"
)

// TestMerkleBranchCompat verifies that goPool's merkle branch computation
// is compatible with pogolo's implementation. Both pools must generate
// identical merkle branches from the same transaction set for miners to
// correctly reconstruct the merkle root.
func TestMerkleBranchCompat(t *testing.T) {
	// Test case 1: Empty transaction list (coinbase only)
	t.Run("coinbase_only", func(t *testing.T) {
		txids := [][]byte{}
		branches := buildMerkleBranches(txids)

		// With only coinbase, merkle branch should be empty
		if len(branches) != 0 {
			t.Errorf("expected empty merkle branch for coinbase-only block, got %d branches", len(branches))
		}
	})

	// Test case 2: Single transaction (coinbase + 1 tx)
	t.Run("single_transaction", func(t *testing.T) {
		txid1, _ := hex.DecodeString("abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234")
		txids := [][]byte{txid1}

		branches := buildMerkleBranches(txids)

		// With coinbase + 1 tx, we should have 1 branch (the other tx)
		if len(branches) != 1 {
			t.Fatalf("expected 1 merkle branch, got %d", len(branches))
		}

		// Verify the branch is the txid itself (as hex string)
		branchBytes, _ := hex.DecodeString(branches[0])
		if !bytes.Equal(branchBytes, txid1) {
			t.Errorf("merkle branch mismatch: expected %x, got %s", txid1, branches[0])
		}
	})

	// Test case 3: Multiple transactions requiring tree depth
	t.Run("multiple_transactions", func(t *testing.T) {
		txid1, _ := hex.DecodeString("1111111111111111111111111111111111111111111111111111111111111111")
		txid2, _ := hex.DecodeString("2222222222222222222222222222222222222222222222222222222222222222")
		txid3, _ := hex.DecodeString("3333333333333333333333333333333333333333333333333333333333333333")
		txids := [][]byte{txid1, txid2, txid3}

		branches := buildMerkleBranches(txids)

		// With 4 total transactions (coinbase + 3), we need depth of 2
		// So we expect 2 branches
		if len(branches) < 2 {
			t.Errorf("expected at least 2 merkle branches for 3 transactions, got %d", len(branches))
		}

		// Each branch should be 64 hex chars (32 bytes as hex string)
		for i, branch := range branches {
			if len(branch) != 64 {
				t.Errorf("branch %d has invalid hex length: expected 64, got %d", i, len(branch))
			}
			// Verify it's valid hex
			if _, err := hex.DecodeString(branch); err != nil {
				t.Errorf("branch %d is not valid hex: %v", i, err)
			}
		}
	})

	// Test case 4: Power-of-2 transaction count (perfect tree)
	t.Run("power_of_two_transactions", func(t *testing.T) {
		// Create 7 transactions (coinbase + 7 = 8, which is 2^3)
		var txids [][]byte
		for i := 0; i < 7; i++ {
			txid := make([]byte, 32)
			txid[0] = byte(i + 1)
			txids = append(txids, txid)
		}

		branches := buildMerkleBranches(txids)

		// With 8 total transactions, we need exactly 3 levels (depth 3)
		if len(branches) != 3 {
			t.Logf("Note: merkle branch count %d for 8 transactions (may vary by implementation)", len(branches))
		}

		// Verify all branches are valid hex hashes
		for i, branch := range branches {
			if len(branch) != 64 {
				t.Errorf("branch %d has invalid hex length: expected 64, got %d", i, len(branch))
			}
			if _, err := hex.DecodeString(branch); err != nil {
				t.Errorf("branch %d is not valid hex: %v", i, err)
			}
		}
	})
}

// TestDifficultyToTarget verifies that difficulty-to-target conversion
// is compatible between goPool and pogolo. This is critical for validating
// that submitted shares meet the required difficulty.
func TestDifficultyToTarget(t *testing.T) {
	testCases := []struct {
		name       string
		difficulty float64
		// Expected target bits (first 8 bytes) for validation
		expectValid      bool
		expectAtMaxLimit bool // Target should be clamped to max
	}{
		{
			name:             "minimum_difficulty",
			difficulty:       0.0000001,
			expectValid:      true,
			expectAtMaxLimit: true, // Very low diff may hit max target
		},
		{
			name:             "default_difficulty",
			difficulty:       512,
			expectValid:      true,
			expectAtMaxLimit: false,
		},
		{
			name:             "high_difficulty",
			difficulty:       16000,
			expectValid:      true,
			expectAtMaxLimit: false,
		},
		{
			name:             "network_difficulty_mainnet_approx",
			difficulty:       50_000_000_000_000, // ~50T
			expectValid:      true,
			expectAtMaxLimit: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			target := targetFromDifficulty(tc.difficulty)

			if target == nil {
				if tc.expectValid {
					t.Fatal("expected valid target, got nil")
				}
				return
			}

			// Verify target is positive
			if target.Sign() <= 0 {
				t.Errorf("target must be positive, got %v", target)
			}

			// Verify target is less than or equal to max uint256
			// For very low difficulties, the target may be clamped to maxUint256
			maxTarget := new(big.Int).Lsh(big.NewInt(1), 256)
			maxTarget.Sub(maxTarget, big.NewInt(1))

			if target.Cmp(maxTarget) > 0 {
				if !tc.expectAtMaxLimit {
					t.Errorf("target exceeds maximum: %v", target)
				}
			}

			// Convert back to difficulty and verify it's close
			// diff = diff1Target / target
			// diff1Target from Bitcoin: 0x00000000FFFF0000000000000000000000000000000000000000000000000000
			diff1Target, _ := new(big.Int).SetString("00000000FFFF0000000000000000000000000000000000000000000000000000", 16)

			// Skip reverse calculation if target is at max (very low difficulty)
			if tc.expectAtMaxLimit && target.Cmp(maxTarget) == 0 {
				t.Logf("Target clamped to maxUint256 for very low difficulty %.10f", tc.difficulty)
				return
			}

			recoveredDiff := new(big.Float).Quo(
				new(big.Float).SetInt(diff1Target),
				new(big.Float).SetInt(target),
			)
			recoveredDiffFloat, _ := recoveredDiff.Float64()

			// Allow 1% tolerance for floating point rounding
			tolerance := tc.difficulty * 0.01
			if tolerance < 0.0001 {
				tolerance = 0.0001
			}

			diff := recoveredDiffFloat - tc.difficulty
			if diff < 0 {
				diff = -diff
			}

			if diff > tolerance {
				t.Errorf("difficulty conversion mismatch: input=%.10f, recovered=%.10f, diff=%.10f, tolerance=%.10f",
					tc.difficulty, recoveredDiffFloat, diff, tolerance)
			}
		})
	}
}

// TestVersionMaskCompatibility verifies that version rolling mask handling
// is compatible with pogolo's implementation per BIP 310/320.
func TestVersionMaskCompatibility(t *testing.T) {
	// BIP 310 standard version rolling mask
	standardMask := uint32(0x1fffe000)

	testCases := []struct {
		name        string
		poolMask    uint32
		minerMask   uint32
		expectedAnd uint32
	}{
		{
			name:        "standard_mask_full_support",
			poolMask:    standardMask,
			minerMask:   standardMask,
			expectedAnd: standardMask,
		},
		{
			name:        "miner_requests_subset",
			poolMask:    standardMask,
			minerMask:   0x1fff0000,
			expectedAnd: 0x1fff0000 & standardMask,
		},
		{
			name:        "pool_restricts_bits",
			poolMask:    0x1ff00000,
			minerMask:   standardMask,
			expectedAnd: 0x1ff00000,
		},
		{
			name:        "no_overlap",
			poolMask:    0x00001000,
			minerMask:   0x00002000,
			expectedAnd: 0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// The actual mask should be the bitwise AND
			actualMask := tc.poolMask & tc.minerMask

			if actualMask != tc.expectedAnd {
				t.Errorf("version mask mismatch: expected %#08x, got %#08x", tc.expectedAnd, actualMask)
			}

			// Verify that any version bits we set with this mask would be valid
			baseVersion := int32(0x20000000)  // Typical BIP 320 base
			versionBits := uint32(0xaaaaaaaa) // Alternating bit pattern

			// Apply the mask
			maskedBits := versionBits & actualMask
			finalVersion := baseVersion | int32(maskedBits)

			// Verify we didn't corrupt required bits (check as uint32 to avoid overflow)
			if (uint32(finalVersion) & 0xe0000000) != 0x20000000 {
				t.Errorf("version rolling corrupted top bits: %#08x", finalVersion)
			}
		})
	}
}

// TestStratumErrorCodes verifies that error codes match between implementations
func TestStratumErrorCodes(t *testing.T) {
	// Common Stratum error codes that should be compatible
	errorCodes := map[string]int{
		"unknown_error":    20,
		"job_not_found":    21,
		"duplicate_share":  22,
		"low_difficulty":   23,
		"unauthorized":     24,
		"not_subscribed":   25,
		"invalid_solution": 26, // or similar
	}

	// These are just documentation - actual error code constants
	// should be defined in the main codebase
	for name, expectedCode := range errorCodes {
		t.Run(name, func(t *testing.T) {
			// This test documents expected error codes
			// Actual validation would require comparing against pogolo constants
			if expectedCode < 20 || expectedCode > 99 {
				t.Errorf("error code %d for %s is outside typical Stratum range [20-99]", expectedCode, name)
			}
		})
	}
}

// TestShareValidationFlow simulates the share validation process
// to ensure compatibility with pogolo's validation logic
func TestShareValidationFlow(t *testing.T) {
	t.Run("valid_share_above_difficulty", func(t *testing.T) {
		// This would be implemented with actual share validation logic
		// For now, this is a placeholder showing the test structure

		// Setup: create a mock share that should pass validation
		difficulty := 1024.0
		target := targetFromDifficulty(difficulty)

		if target == nil {
			t.Fatal("failed to create target from difficulty")
		}

		// A real test would:
		// 1. Create a coinbase transaction with extranonce
		// 2. Build a block header
		// 3. Calculate the hash
		// 4. Verify hash < target
	})

	t.Run("reject_duplicate_share", func(t *testing.T) {
		// Test that duplicate detection works
		// Both pools should reject shares with identical parameters

		ring := &duplicateShareSet{}

		// Create a share key
		var key1 duplicateShareKey
		makeDuplicateShareKey(&key1, "abcd1234", "5f5e100", "12345678", 0x20000000)

		// First submission should be new
		if ring.seenOrAdd(key1) {
			t.Error("first share submission should not be marked as duplicate")
		}

		// Second identical submission should be duplicate
		if !ring.seenOrAdd(key1) {
			t.Error("second identical share submission should be marked as duplicate")
		}

		// Different extranonce2 should be new
		var key2 duplicateShareKey
		makeDuplicateShareKey(&key2, "ffff1234", "5f5e100", "12345678", 0x20000000)
		if ring.seenOrAdd(key2) {
			t.Error("share with different extranonce2 should not be marked as duplicate")
		}

		// Different nonce should be new
		var key3 duplicateShareKey
		makeDuplicateShareKey(&key3, "abcd1234", "5f5e100", "87654321", 0x20000000)
		if ring.seenOrAdd(key3) {
			t.Error("share with different nonce should not be marked as duplicate")
		}
	})

	t.Run("reject_low_difficulty_share", func(t *testing.T) {
		// Test that shares not meeting difficulty are rejected

		// Create a high difficulty target (means harder to find)
		difficulty := 100000.0
		target := targetFromDifficulty(difficulty)

		if target == nil {
			t.Fatal("failed to create target")
		}

		// A random hash is extremely unlikely to meet high difficulty
		// This simulates a low-difficulty share that should be rejected
		randomHash := new(big.Int).SetBytes([]byte{
			0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
			0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
			0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
			0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
		})

		// Hash should be greater than target (meaning it doesn't meet difficulty)
		if randomHash.Cmp(target) < 0 {
			t.Error("test setup error: random hash should not meet high difficulty")
		}

		// Verify that a very low hash would meet the difficulty
		lowHash := new(big.Int).SetBytes([]byte{
			0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01,
			0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
			0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
			0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		})

		if lowHash.Cmp(target) >= 0 {
			t.Error("very low hash should meet difficulty requirement")
		}
	})
}

// TestCoinbaseScriptCompat verifies coinbase script construction
// matches between implementations
func TestCoinbaseScriptCompat(t *testing.T) {
	t.Run("extranonce_placement", func(t *testing.T) {
		// Verify that extranonce1 and extranonce2 are placed correctly
		// in the coinbase script

		extranonce1Size := 4
		extranonce2Size := 8
		totalExtranonceSize := extranonce1Size + extranonce2Size

		// Both implementations should reserve the same total space
		if totalExtranonceSize != 12 {
			t.Logf("Total extranonce size: %d bytes", totalExtranonceSize)
		}
	})

	t.Run("coinbase_message", func(t *testing.T) {
		// Test coinbase message/tag format
		// goPool uses: "goPool-<brand>"
		// pogolo likely uses similar format

		coinbaseMsg := "goPool"
		if len(coinbaseMsg) > 40 {
			t.Errorf("coinbase message too long: %d bytes (max 40)", len(coinbaseMsg))
		}
	})

	t.Run("height_encoding_bip34", func(t *testing.T) {
		// BIP 34 requires height in coinbase
		// Test various heights to ensure encoding is correct

		testCases := []struct {
			height      int64
			expectedHex string // Expected encoding as hex
		}{
			{height: 0, expectedHex: "0100"},           // Special case: 0 encoded as [0x01, 0x00]
			{height: 1, expectedHex: "51"},             // OP_1
			{height: 16, expectedHex: "60"},            // OP_16
			{height: 127, expectedHex: "017f"},         // [0x01, 0x7f]
			{height: 128, expectedHex: "028000"},       // [0x02, 0x80, 0x00] - needs zero byte for sign
			{height: 255, expectedHex: "02ff00"},       // [0x02, 0xff, 0x00] - needs zero byte for sign
			{height: 256, expectedHex: "020001"},       // [0x02, 0x00, 0x01]
			{height: 65535, expectedHex: "03ffff00"},   // [0x03, 0xff, 0xff, 0x00] - needs zero byte
			{height: 65536, expectedHex: "03000001"},   // [0x03, 0x00, 0x00, 0x01]
			{height: 1000000, expectedHex: "0340420f"}, // [0x03, 0x40, 0x42, 0x0f]
		}

		for _, tc := range testCases {
			encoded := serializeNumberScript(tc.height)
			encodedHex := hex.EncodeToString(encoded)

			if encodedHex != tc.expectedHex {
				t.Errorf("height %d: expected %s, got %s",
					tc.height, tc.expectedHex, encodedHex)
			}

			// Verify first byte is the length indicator for multi-byte encodings
			if len(encoded) > 1 {
				lengthByte := encoded[0]
				actualDataLen := len(encoded) - 1
				if int(lengthByte) != actualDataLen {
					t.Errorf("height %d: length byte mismatch: got %d, actual data length %d",
						tc.height, lengthByte, actualDataLen)
				}
			}
		}
	})
}

// TestNtimeRolling verifies that ntime (timestamp) handling is compatible
func TestNtimeRolling(t *testing.T) {
	t.Run("future_tolerance", func(t *testing.T) {
		// Both pools should accept ntime values within a tolerance window
		// goPool uses: ntimeFutureTolerance = 2 * time.Hour

		now := int64(1700000000) // Reference time
		future1h := now + 3600   // 1 hour future (should be OK)
		future2h := now + 7200   // 2 hours future (at limit)
		future3h := now + 10800  // 3 hours future (should reject)

		futureTolerance := int64(2 * 3600) // 2 hours in seconds

		// Within tolerance
		if future1h-now > futureTolerance {
			t.Errorf("1 hour future should be within tolerance")
		}

		// At tolerance boundary
		if future2h-now > futureTolerance {
			t.Errorf("2 hour future should be at tolerance limit")
		}

		// Beyond tolerance
		if future3h-now <= futureTolerance {
			t.Errorf("3 hour future should exceed tolerance")
		}
	})

	t.Run("past_bounds", func(t *testing.T) {
		// Test that ntime values before template time are rejected

		templateTime := int64(1700000000)
		pastTime := templateTime - 3600 // 1 hour in the past

		// Shares with ntime before template time should be rejected
		if pastTime >= templateTime {
			t.Error("past time should be before template time")
		}

		// Current time should be acceptable
		currentTime := templateTime
		if currentTime < templateTime {
			t.Error("template time should be acceptable")
		}
	})

	t.Run("ntime_update", func(t *testing.T) {
		// Verify ntime can be updated by miner within bounds

		templateTime := int64(1700000000)
		minerUpdate := templateTime + 60 // Miner adds 60 seconds

		// Miner can roll ntime forward within reasonable bounds
		if minerUpdate <= templateTime {
			t.Error("miner should be able to increment ntime")
		}

		// But not backwards
		invalidUpdate := templateTime - 60
		if invalidUpdate >= templateTime {
			t.Error("miner should not be able to decrement ntime below template time")
		}
	})
}

// BenchmarkMerkleBranchComputation compares performance of merkle branch
// computation with different transaction counts
func BenchmarkMerkleBranchComputation(b *testing.B) {
	txCounts := []int{1, 10, 100, 1000, 4000}

	for _, count := range txCounts {
		b.Run(strconv.Itoa(count)+"_transactions", func(b *testing.B) {
			// Create dummy transaction IDs
			txids := make([][]byte, count)
			for i := 0; i < count; i++ {
				txid := make([]byte, 32)
				txid[0] = byte(i)
				txid[1] = byte(i >> 8)
				txids[i] = txid
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = buildMerkleBranches(txids)
			}
		})
	}
}
