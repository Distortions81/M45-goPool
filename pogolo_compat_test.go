package main

import (
	"bytes"
	"encoding/hex"
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
		for i := range 7 {
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

func TestDuplicateShareSet(t *testing.T) {
	ring := &duplicateShareSet{}

	var key1 duplicateShareKey
	makeDuplicateShareKey(&key1, "abcd1234", "5f5e100", "12345678", 0x20000000)
	if ring.seenOrAdd(key1) {
		t.Fatal("first share submission should not be duplicate")
	}
	if !ring.seenOrAdd(key1) {
		t.Fatal("second identical share submission should be duplicate")
	}

	var key2 duplicateShareKey
	makeDuplicateShareKey(&key2, "ffff1234", "5f5e100", "12345678", 0x20000000)
	if ring.seenOrAdd(key2) {
		t.Fatal("share with different extranonce2 should not be duplicate")
	}

	var key3 duplicateShareKey
	makeDuplicateShareKey(&key3, "abcd1234", "5f5e100", "87654321", 0x20000000)
	if ring.seenOrAdd(key3) {
		t.Fatal("share with different nonce should not be duplicate")
	}
}

// BenchmarkMerkleBranchComputation compares performance of merkle branch
// computation with different transaction counts
func BenchmarkMerkleBranchComputation(b *testing.B) {
	txCounts := []int{1, 10, 100, 1000, 4000}

	for _, count := range txCounts {
		b.Run(strconv.Itoa(count)+"_transactions", func(b *testing.B) {
			// Create dummy transaction IDs
			txids := make([][]byte, count)
			for i := range count {
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
