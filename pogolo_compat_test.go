package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"strconv"
	"testing"

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
)

func TestMerkleBranchCompat(t *testing.T) {
	t.Run("coinbase_only", func(t *testing.T) {
		branches := buildMerkleBranches(nil)
		if len(branches) != 0 {
			t.Errorf("expected empty merkle branch for coinbase-only block, got %d branches", len(branches))
		}
	})

	t.Run("internal_byte_order", func(t *testing.T) {
		transactions := makeMerkleTestTransactions(1)
		branches := buildMerkleBranches(transactions)
		if len(branches) != 1 {
			t.Fatalf("expected 1 merkle branch, got %d", len(branches))
		}
		hash := transactions[0].Hash()
		want := hex.EncodeToString(hash[:])
		if branches[0] != want {
			t.Fatalf("branch byte order mismatch: got %s want internal hash %s", branches[0], want)
		}
		if branches[0] == hash.String() {
			t.Fatalf("branch unexpectedly uses display-order txid %s", hash.String())
		}
	})

	for _, count := range []int{1, 2, 3, 7, 10} {
		t.Run("btcd_root_"+strconv.Itoa(count), func(t *testing.T) {
			transactions := makeMerkleTestTransactions(count)
			branches := buildMerkleBranches(transactions)
			coinbase := makeMerkleTestTransactions(1)[0]
			allTransactions := append([]*btcutil.Tx{coinbase}, transactions...)
			tree := blockchain.BuildMerkleTreeStore(allTransactions, false)
			wantRoot := tree[len(tree)-1]

			coinbaseHash := coinbase.Hash()
			gotRoot := computeMerkleRootFromBranches(coinbaseHash[:], branches)
			if gotRoot == nil || !bytes.Equal(gotRoot, wantRoot[:]) {
				t.Fatalf("merkle root mismatch for %d transactions: got %x want %x", count, gotRoot, wantRoot[:])
			}
		})
	}
}

func makeMerkleTestTransactions(count int) []*btcutil.Tx {
	transactions := make([]*btcutil.Tx, count)
	for i := range count {
		var prevHash chainhash.Hash
		binary.LittleEndian.PutUint32(prevHash[:4], uint32(i+1))
		msgTx := wire.NewMsgTx(2)
		msgTx.AddTxIn(wire.NewTxIn(wire.NewOutPoint(&prevHash, uint32(i)), []byte{byte(i + 1)}, nil))
		msgTx.AddTxOut(wire.NewTxOut(int64(1000+i), []byte{0x51}))
		transactions[i] = btcutil.NewTx(msgTx)
	}
	return transactions
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
			transactions := makeMerkleTestTransactions(count)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = buildMerkleBranches(transactions)
			}
		})
	}
}
