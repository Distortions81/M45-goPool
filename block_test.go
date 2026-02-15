package main

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math"
	"testing"

	"github.com/btcsuite/btcd/wire"
)

// merkleRootFromBtcdTxs mirrors the standard merkle tree computation over a
// slice of btcd MsgTx values, using the same doubleSHA256 used in the pool.
func merkleRootFromBtcdTxs(txs []*wire.MsgTx) []byte {
	if len(txs) == 0 {
		return nil
	}
	// Collect txids as big-endian hashes.
	layer := make([][]byte, len(txs))
	for i, tx := range txs {
		h := tx.TxHash()
		layer[i] = h.CloneBytes()
	}
	for len(layer) > 1 {
		if len(layer)%2 == 1 {
			layer = append(layer, layer[len(layer)-1])
		}
		next := make([][]byte, 0, len(layer)/2)
		for i := 0; i < len(layer); i += 2 {
			joined := append(append([]byte{}, layer[i]...), layer[i+1]...)
			next = append(next, doubleSHA256(joined))
		}
		layer = next
	}
	return layer[0]
}

// TestBuildBlock_ParsesWithBtcdAndHasValidMerkle builds a minimal block using
// buildBlock and then parses it with btcd's wire.MsgBlock, checking header
// fields and merkle root.
func TestBuildBlock_ParsesWithBtcdAndHasValidMerkle(t *testing.T) {
	job := &Job{
		JobID: "test-job",
		Template: GetBlockTemplateResult{
			Height:        101,
			CurTime:       1700000000,
			Mintime:       0,
			Bits:          "1d00ffff",
			Previous:      "0000000000000000000000000000000000000000000000000000000000000000",
			CoinbaseValue: 50 * 1e8,
		},
		Extranonce2Size:         4,
		TemplateExtraNonce2Size: 8,
		PayoutScript:            []byte{0x51}, // OP_TRUE for structure test
		WitnessCommitment:       "",
		CoinbaseMsg:             "goPool-blocktest",
		ScriptTime:              0,
		Transactions:            nil,
		MerkleBranches:          nil,
	}

	ex1 := []byte{0x01, 0x02, 0x03, 0x04}
	ex2 := []byte{0xaa, 0xbb, 0xcc, 0xdd}
	ntimeHex := fmt.Sprintf("%08x", job.Template.CurTime)
	nonceHex := "00000001"
	version := int32(1)

	blockHex, headerHash, header, merkleRoot, err := buildBlock(job, ex1, ex2, ntimeHex, nonceHex, version)
	if err != nil {
		t.Fatalf("buildBlock error: %v", err)
	}
	if len(blockHex) == 0 || len(headerHash) != 32 || len(header) != 80 || len(merkleRoot) != 32 {
		t.Fatalf("unexpected block artifacts: len(blockHex)=%d len(headerHash)=%d len(header)=%d len(merkleRoot)=%d",
			len(blockHex), len(headerHash), len(header), len(merkleRoot))
	}

	raw, err := hex.DecodeString(blockHex)
	if err != nil {
		t.Fatalf("decode blockHex: %v", err)
	}

	var msgBlock wire.MsgBlock
	if err := msgBlock.Deserialize(bytes.NewReader(raw)); err != nil {
		t.Fatalf("btcd MsgBlock deserialize error: %v", err)
	}

	if msgBlock.Header.Version != version {
		t.Fatalf("header version mismatch: got %d, want %d", msgBlock.Header.Version, version)
	}
	if len(msgBlock.Transactions) != 1 {
		t.Fatalf("expected 1 transaction (coinbase only), got %d", len(msgBlock.Transactions))
	}

	// Recompute merkle root from btcd's view of the transactions and ensure
	// it matches the header merkle root.
	rootFromTxs := merkleRootFromBtcdTxs(msgBlock.Transactions)
	if rootFromTxs == nil || !bytes.Equal(rootFromTxs, msgBlock.Header.MerkleRoot.CloneBytes()) {
		t.Fatalf("merkle root mismatch: header=%x computed=%x", msgBlock.Header.MerkleRoot.CloneBytes(), rootFromTxs)
	}

	// Re-serialize the block with btcd and ensure it matches our original
	// bytes exactly. This confirms our block layout (header + tx count +
	// tx ordering) matches btcd's wire encoding.
	var buf bytes.Buffer
	if err := msgBlock.Serialize(&buf); err != nil {
		t.Fatalf("btcd MsgBlock serialize error: %v", err)
	}
	roundTrip := buf.Bytes()
	if !bytes.Equal(raw, roundTrip) {
		t.Fatalf("block serialization mismatch between pool and btcd (len orig=%d len btcd=%d)", len(raw), len(roundTrip))
	}
}

// TestDualVsSinglePayoutBlocks simulates block construction for both single-
// payout and dual-payout coinbases and verifies that the resulting blocks are
// structurally valid under btcd and that the coinbase outputs match the
// expected split.
func TestDualVsSinglePayoutBlocks(t *testing.T) {
	job := &Job{
		JobID: "test-job-dual",
		Template: GetBlockTemplateResult{
			Height:        150,
			CurTime:       1700000100,
			Mintime:       0,
			Bits:          "1d00ffff",
			Previous:      "0000000000000000000000000000000000000000000000000000000000000000",
			CoinbaseValue: 50 * 1e8,
		},
		Extranonce2Size:         4,
		TemplateExtraNonce2Size: 8,
		PayoutScript:            []byte{0x51}, // OP_TRUE
		WitnessCommitment:       "",
		CoinbaseMsg:             "goPool-dualtest",
		ScriptTime:              0,
		Transactions:            nil,
		MerkleBranches:          nil,
		CoinbaseValue:           50 * 1e8,
	}

	ex1 := []byte{0x0a, 0x0b, 0x0c, 0x0d}
	ex2 := []byte{0x01, 0x02, 0x03, 0x04}
	ntimeHex := fmt.Sprintf("%08x", job.Template.CurTime)
	nonceHex := "00000002"
	version := int32(1)

	// Single-payout block.
	singleBlockHex, _, _, _, err := buildBlock(job, ex1, ex2, ntimeHex, nonceHex, version)
	if err != nil {
		t.Fatalf("buildBlock (single) error: %v", err)
	}
	singleRaw, err := hex.DecodeString(singleBlockHex)
	if err != nil {
		t.Fatalf("decode singleBlockHex: %v", err)
	}
	var singleBlock wire.MsgBlock
	if err := singleBlock.Deserialize(bytes.NewReader(singleRaw)); err != nil {
		t.Fatalf("btcd MsgBlock (single) deserialize error: %v", err)
	}
	if len(singleBlock.Transactions) != 1 {
		t.Fatalf("single-payout block: expected 1 tx, got %d", len(singleBlock.Transactions))
	}
	if len(singleBlock.Transactions[0].TxOut) != 1 {
		t.Fatalf("single-payout coinbase: expected 1 output, got %d", len(singleBlock.Transactions[0].TxOut))
	}
	if singleBlock.Transactions[0].TxOut[0].Value != job.Template.CoinbaseValue {
		t.Fatalf("single-payout coinbase value mismatch: got %d, want %d", singleBlock.Transactions[0].TxOut[0].Value, job.Template.CoinbaseValue)
	}

	// Dual-payout block: build coinbase using serializeDualCoinbaseTx, then
	// assemble a full block matching the pool's dual-payout path.
	poolScript := job.PayoutScript
	workerScript := []byte{0x52} // OP_2
	totalValue := job.Template.CoinbaseValue
	feePercent := 2.0

	cbTx, cbTxid, err := serializeDualCoinbaseTx(job.Template.Height, ex1, ex2, job.TemplateExtraNonce2Size, poolScript, workerScript, totalValue, feePercent, job.WitnessCommitment, job.Template.CoinbaseAux.Flags, job.CoinbaseMsg, job.ScriptTime)
	if err != nil {
		t.Fatalf("serializeDualCoinbaseTx error: %v", err)
	}
	if len(cbTxid) != 32 {
		t.Fatalf("dual-payout coinbase txid length %d", len(cbTxid))
	}

	merkleRoot := computeMerkleRootFromBranches(cbTxid, job.MerkleBranches)
	header, err := buildBlockHeaderFromHex(version, job.Template.Previous, merkleRoot, ntimeHex, job.Template.Bits, nonceHex)
	if err != nil {
		t.Fatalf("buildBlockHeader (dual) error: %v", err)
	}

	var dualBuf bytes.Buffer
	dualBuf.Write(header)
	writeVarInt(&dualBuf, uint64(1)) // coinbase only
	dualBuf.Write(cbTx)
	dualRaw := dualBuf.Bytes()

	var dualBlock wire.MsgBlock
	if err := dualBlock.Deserialize(bytes.NewReader(dualRaw)); err != nil {
		t.Fatalf("btcd MsgBlock (dual) deserialize error: %v", err)
	}
	if len(dualBlock.Transactions) != 1 {
		t.Fatalf("dual-payout block: expected 1 tx, got %d", len(dualBlock.Transactions))
	}
	coinbase := dualBlock.Transactions[0]
	if len(coinbase.TxOut) != 2 {
		t.Fatalf("dual-payout coinbase: expected 2 outputs, got %d", len(coinbase.TxOut))
	}

	// Verify split matches the same logic used in serializeDualCoinbaseTx.
	poolFee := max(int64(math.Round(float64(totalValue)*feePercent/100.0)), 0)
	if poolFee > totalValue {
		poolFee = totalValue
	}
	workerValue := totalValue - poolFee
	if workerValue <= 0 {
		t.Fatalf("dual-payout worker value <= 0")
	}

	var (
		gotPoolValue   *int64
		gotWorkerValue *int64
	)
	for _, o := range coinbase.TxOut {
		switch {
		case bytes.Equal(o.PkScript, poolScript):
			v := o.Value
			gotPoolValue = &v
		case bytes.Equal(o.PkScript, workerScript):
			v := o.Value
			gotWorkerValue = &v
		}
	}
	if gotPoolValue == nil {
		t.Fatalf("dual-payout pool output not found by script")
	}
	if gotWorkerValue == nil {
		t.Fatalf("dual-payout worker output not found by script")
	}
	if *gotPoolValue != poolFee {
		t.Fatalf("dual-payout pool output value mismatch: got %d, want %d", *gotPoolValue, poolFee)
	}
	if *gotWorkerValue != workerValue {
		t.Fatalf("dual-payout worker output value mismatch: got %d, want %d", *gotWorkerValue, workerValue)
	}
}
