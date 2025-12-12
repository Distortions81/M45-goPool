package main

import (
	"bytes"
	"testing"

	"github.com/btcsuite/btcd/wire"
)

// equalHashBytes reports whether a and b are equal, allowing for either
// big-endian or little-endian order. This matches the tolerance used in
// validateTransactions for txid/wtxid comparisons.
func equalHashBytes(a, b []byte) bool {
	if bytes.Equal(a, b) {
		return true
	}
	return bytes.Equal(reverseBytes(a), b)
}

// TestStripWitnessDataCompatWithBtcd_SegWit builds a synthetic SegWit
// transaction, runs it through stripWitnessData, and verifies that the txid
// and wtxid derived from our helper match btcd's TxHash/WitnessHash.
func TestStripWitnessDataCompatWithBtcd_SegWit(t *testing.T) {
	tx := wire.NewMsgTx(2) // version 2 (SegWit-capable)

	// One input spending a fake outpoint.
	prevHash := [32]byte{}
	prevHash[0] = 0x01
	outPoint := wire.OutPoint{
		Hash:  prevHash,
		Index: 0,
	}
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: outPoint,
		SignatureScript:  []byte{0x51}, // OP_TRUE
		Sequence:         0xffffffff,
	})

	// One output paying a trivial script.
	tx.AddTxOut(&wire.TxOut{
		Value:    1000,
		PkScript: []byte{0x51}, // OP_TRUE
	})

	// Add witness stack for the input so the serializer emits marker/flag.
	tx.TxIn[0].Witness = wire.TxWitness{
		[]byte{0x01, 0x02},
		[]byte{0x03, 0x04, 0x05},
	}

	var buf bytes.Buffer
	if err := tx.Serialize(&buf); err != nil {
		t.Fatalf("btcd MsgTx serialize error: %v", err)
	}
	raw := buf.Bytes()
	if len(raw) == 0 {
		t.Fatalf("expected non-empty serialized tx")
	}

	base, hasWitness, err := stripWitnessData(raw)
	if err != nil {
		t.Fatalf("stripWitnessData error: %v", err)
	}
	if !hasWitness {
		t.Fatalf("expected hasWitness=true for SegWit tx")
	}
	if len(base) >= len(raw) {
		t.Fatalf("expected stripped tx to be smaller than full tx, got base=%d raw=%d", len(base), len(raw))
	}

	// Our txid calculation: double SHA256 of the stripped (no-witness) bytes.
	txidBytes := doubleSHA256(base)
	// Our wtxid calculation: double SHA256 of the full serialization.
	wtxidBytes := doubleSHA256(raw)

	btcdTxid := tx.TxHash()
	btcdWtxid := tx.WitnessHash()

	if !equalHashBytes(txidBytes, btcdTxid[:]) {
		t.Fatalf("txid mismatch between stripWitnessData and btcd:\n goPool=%x\n btcd=%x", txidBytes, btcdTxid[:])
	}
	if !equalHashBytes(wtxidBytes, btcdWtxid[:]) {
		t.Fatalf("wtxid mismatch between stripWitnessData and btcd:\n goPool=%x\n btcd=%x", wtxidBytes, btcdWtxid[:])
	}
}

// TestStripWitnessDataCompatWithBtcd_NonSegWit builds a non-SegWit transaction
// (no witness data), runs it through stripWitnessData, and verifies that both
// our txid and wtxid calculations match btcd's view.
func TestStripWitnessDataCompatWithBtcd_NonSegWit(t *testing.T) {
	tx := wire.NewMsgTx(1) // legacy version

	// One input spending a fake outpoint.
	prevHash := [32]byte{}
	prevHash[0] = 0x02
	outPoint := wire.OutPoint{
		Hash:  prevHash,
		Index: 1,
	}
	tx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: outPoint,
		SignatureScript:  []byte{0x51}, // OP_TRUE
		Sequence:         0xffffffff,
	})

	// One output paying a trivial script.
	tx.AddTxOut(&wire.TxOut{
		Value:    500,
		PkScript: []byte{0x51}, // OP_TRUE
	})

	var buf bytes.Buffer
	if err := tx.Serialize(&buf); err != nil {
		t.Fatalf("btcd MsgTx serialize error: %v", err)
	}
	raw := buf.Bytes()
	if len(raw) == 0 {
		t.Fatalf("expected non-empty serialized tx")
	}

	base, hasWitness, err := stripWitnessData(raw)
	if err != nil {
		t.Fatalf("stripWitnessData error: %v", err)
	}
	if hasWitness {
		t.Fatalf("expected hasWitness=false for non-SegWit tx")
	}
	if !bytes.Equal(base, raw) {
		t.Fatalf("expected stripped tx to equal raw serialization for non-SegWit tx")
	}

	txidBytes := doubleSHA256(base)
	wtxidBytes := doubleSHA256(raw)

	btcdTxid := tx.TxHash()
	btcdWtxid := tx.WitnessHash()

	if !equalHashBytes(txidBytes, btcdTxid[:]) {
		t.Fatalf("txid mismatch for non-SegWit tx:\n goPool=%x\n btcd=%x", txidBytes, btcdTxid[:])
	}
	if !equalHashBytes(wtxidBytes, btcdWtxid[:]) {
		t.Fatalf("wtxid mismatch for non-SegWit tx:\n goPool=%x\n btcd=%x", wtxidBytes, btcdWtxid[:])
	}
}
