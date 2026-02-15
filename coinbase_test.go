package main

import (
	"bytes"
	"encoding/hex"
	"math"
	"testing"

	"github.com/btcsuite/btcd/wire"
)

// TestSerializeCoinbaseTx_SingleOutputStructure uses btcd's wire.MsgTx to
// decode the serialized coinbase and verify basic structure and fields.
func TestSerializeCoinbaseTx_SingleOutputStructure(t *testing.T) {
	height := int64(100)
	ex1 := []byte{0x01, 0x02, 0x03, 0x04}
	ex2 := []byte{0xaa, 0xbb, 0xcc, 0xdd}
	templateExtra := len(ex1) + len(ex2)
	payoutScript := []byte{0x51} // OP_TRUE (simple, non-standard but fine for structure)
	coinbaseValue := int64(50 * 1e8)
	witnessCommitment := ""
	coinbaseFlags := ""
	coinbaseMsg := "goPool-test"
	scriptTime := int64(0)

	raw, txid, err := serializeCoinbaseTx(height, ex1, ex2, templateExtra, payoutScript, coinbaseValue, witnessCommitment, coinbaseFlags, coinbaseMsg, scriptTime)
	if err != nil {
		t.Fatalf("serializeCoinbaseTx error: %v", err)
	}
	if len(txid) != 32 {
		t.Fatalf("expected 32-byte txid, got %d", len(txid))
	}

	var tx wire.MsgTx
	if err := tx.Deserialize(bytes.NewReader(raw)); err != nil {
		t.Fatalf("btcd MsgTx deserialize error: %v", err)
	}

	if tx.Version != 1 {
		t.Fatalf("expected version 1, got %d", tx.Version)
	}
	if len(tx.TxIn) != 1 {
		t.Fatalf("expected 1 input, got %d", len(tx.TxIn))
	}
	if len(tx.TxOut) != 1 {
		t.Fatalf("expected 1 output, got %d", len(tx.TxOut))
	}
	if tx.TxOut[0].Value != coinbaseValue {
		t.Fatalf("expected output value %d, got %d", coinbaseValue, tx.TxOut[0].Value)
	}
	if !bytes.Equal(tx.TxOut[0].PkScript, payoutScript) {
		t.Fatalf("payout script mismatch: got %x, want %x", tx.TxOut[0].PkScript, payoutScript)
	}
	if len(tx.TxIn[0].SignatureScript) == 0 || len(tx.TxIn[0].SignatureScript) > 100 {
		t.Fatalf("coinbase scriptSig length out of bounds: %d", len(tx.TxIn[0].SignatureScript))
	}
}

// TestSerializeDualCoinbaseTx_DualOutputs verifies that serializeDualCoinbaseTx
// produces a coinbase with correct fee split and scripts, using btcd's parser.
func TestSerializeDualCoinbaseTx_DualOutputs(t *testing.T) {
	height := int64(200)
	ex1 := []byte{0x11, 0x22, 0x33, 0x44}
	ex2 := []byte{0xde, 0xad, 0xbe, 0xef}
	templateExtra := len(ex1) + len(ex2)

	poolScript := []byte{0x51}   // OP_TRUE
	workerScript := []byte{0x52} // OP_2
	totalValue := int64(50 * 1e8)
	feePercent := 2.0
	witnessCommitment := ""
	coinbaseFlags := ""
	coinbaseMsg := "goPool-dual"
	scriptTime := int64(0)

	raw, txid, err := serializeDualCoinbaseTx(height, ex1, ex2, templateExtra, poolScript, workerScript, totalValue, feePercent, witnessCommitment, coinbaseFlags, coinbaseMsg, scriptTime)
	if err != nil {
		t.Fatalf("serializeDualCoinbaseTx error: %v", err)
	}
	if len(txid) != 32 {
		t.Fatalf("expected 32-byte txid, got %d", len(txid))
	}

	var tx wire.MsgTx
	if err := tx.Deserialize(bytes.NewReader(raw)); err != nil {
		t.Fatalf("btcd MsgTx deserialize error: %v", err)
	}

	if len(tx.TxIn) != 1 {
		t.Fatalf("expected 1 input, got %d", len(tx.TxIn))
	}
	if len(tx.TxOut) != 2 {
		t.Fatalf("expected 2 outputs (pool + worker), got %d", len(tx.TxOut))
	}

	// Recompute expected split using the same logic as serializeDualCoinbaseTx.
	poolFee := max(int64(math.Round(float64(totalValue)*feePercent/100.0)), 0)
	if poolFee > totalValue {
		poolFee = totalValue
	}
	workerValue := totalValue - poolFee
	if workerValue <= 0 {
		t.Fatalf("computed workerValue <= 0")
	}

	var (
		gotPoolValue   *int64
		gotWorkerValue *int64
	)
	for _, o := range tx.TxOut {
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
		t.Fatalf("pool output not found by script")
	}
	if gotWorkerValue == nil {
		t.Fatalf("worker output not found by script")
	}
	if *gotPoolValue != poolFee {
		t.Fatalf("pool output value mismatch: got %d, want %d", *gotPoolValue, poolFee)
	}
	if *gotWorkerValue != workerValue {
		t.Fatalf("worker output value mismatch: got %d, want %d", *gotWorkerValue, workerValue)
	}

	if len(tx.TxIn[0].SignatureScript) == 0 || len(tx.TxIn[0].SignatureScript) > 100 {
		t.Fatalf("coinbase scriptSig length out of bounds: %d", len(tx.TxIn[0].SignatureScript))
	}
}

// TestSerializeCoinbaseTx_WithWitnessCommitment verifies that when a witness
// commitment script is provided, it appears as the first zero-value output,
// matching how bitcoind/GBT expect coinbase witness commitments to be placed.
func TestSerializeCoinbaseTx_WithWitnessCommitment(t *testing.T) {
	height := int64(300)
	ex1 := []byte{0x01, 0x02, 0x03, 0x04}
	ex2 := []byte{0xaa, 0xbb, 0xcc, 0xdd}
	templateExtra := len(ex1) + len(ex2)
	payoutScript := []byte{0x51} // OP_TRUE
	coinbaseValue := int64(50 * 1e8)
	// Minimal OP_RETURN witness commitment-like script:
	// OP_RETURN 0x24 aa21a9ed <32 bytes zero>
	witnessCommitment := "6a24aa21a9ed" + "0000000000000000000000000000000000000000000000000000000000000000"
	coinbaseFlags := ""
	coinbaseMsg := "goPool-witness"
	scriptTime := int64(0)

	raw, _, err := serializeCoinbaseTx(height, ex1, ex2, templateExtra, payoutScript, coinbaseValue, witnessCommitment, coinbaseFlags, coinbaseMsg, scriptTime)
	if err != nil {
		t.Fatalf("serializeCoinbaseTx error: %v", err)
	}

	var tx wire.MsgTx
	if err := tx.Deserialize(bytes.NewReader(raw)); err != nil {
		t.Fatalf("btcd MsgTx deserialize error: %v", err)
	}
	if len(tx.TxOut) != 2 {
		t.Fatalf("expected 2 outputs (commitment + payout), got %d", len(tx.TxOut))
	}
	if tx.TxOut[0].Value != 0 {
		t.Fatalf("expected witness commitment output value 0, got %d", tx.TxOut[0].Value)
	}
	// Decode the expected script and compare.
	expectedScript, err := hex.DecodeString(witnessCommitment)
	if err != nil {
		t.Fatalf("decode witnessCommitment hex: %v", err)
	}
	if !bytes.Equal(tx.TxOut[0].PkScript, expectedScript) {
		t.Fatalf("witness commitment script mismatch: got %x, want %x", tx.TxOut[0].PkScript, expectedScript)
	}
	if tx.TxOut[1].Value != coinbaseValue {
		t.Fatalf("expected payout output value %d, got %d", coinbaseValue, tx.TxOut[1].Value)
	}
	if !bytes.Equal(tx.TxOut[1].PkScript, payoutScript) {
		t.Fatalf("payout script mismatch: got %x, want %x", tx.TxOut[1].PkScript, payoutScript)
	}
}

// TestNormalizeCoinbaseMessage verifies that the coinbase message normalization
// properly handles trimming, prefix/suffix removal, and re-adding slashes.
func TestNormalizeCoinbaseMessage(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: "/nodeStratum",
		},
		{
			name:     "only spaces",
			input:    "   ",
			expected: "/nodeStratum",
		},
		{
			name:     "simple message",
			input:    "goPool",
			expected: "/goPool",
		},
		{
			name:     "message with spaces",
			input:    "  goPool  ",
			expected: "/goPool",
		},
		{
			name:     "message with existing prefix",
			input:    "/goPool",
			expected: "/goPool",
		},
		{
			name:     "message with existing suffix",
			input:    "goPool/",
			expected: "/goPool",
		},
		{
			name:     "message with both prefix and suffix",
			input:    "/goPool/",
			expected: "/goPool",
		},
		{
			name:     "message with spaces and slashes",
			input:    "  /goPool/  ",
			expected: "/goPool",
		},
		{
			name:     "message with multiple slashes",
			input:    "//goPool//",
			expected: "//goPool/",
		},
		{
			name:     "complex message",
			input:    "  /my-pool-v2/  ",
			expected: "/my-pool-v2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := normalizeCoinbaseMessage(tt.input)
			if result != tt.expected {
				t.Errorf("normalizeCoinbaseMessage(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestClampCoinbaseMessage(t *testing.T) {
	height := int64(500)
	scriptTime := int64(600)
	message := "goPool-super-long-tag-1234567890abcdef"
	fixedLen, err := coinbaseScriptSigFixedLen(height, scriptTime, "", 4, 8)
	if err != nil {
		t.Fatalf("coinbaseScriptSigFixedLen error: %v", err)
	}
	limit := fixedLen + 4
	trimmed, truncated, err := clampCoinbaseMessage(message, limit, height, scriptTime, "", 4, 8)
	if err != nil {
		t.Fatalf("clampCoinbaseMessage error: %v", err)
	}
	if !truncated {
		t.Fatalf("expected message to be truncated")
	}
	normalized := normalizeCoinbaseMessage(trimmed)
	scriptSigLen := fixedLen + len(serializeStringScript(normalized))
	if scriptSigLen > limit {
		t.Fatalf("scriptSig length %d exceeds limit %d", scriptSigLen, limit)
	}
}
