package main

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
)

func mustDecodeHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hex decode failed: %v", err)
	}
	return b
}

func assertCoinbaseEncodesAndDecodesWithBtcd(t *testing.T, raw []byte, txid []byte) {
	t.Helper()

	if got, want := chainhash.DoubleHashB(raw), txid; !bytes.Equal(got, want) {
		t.Fatalf("txid mismatch vs btcd chainhash.DoubleHashB: got %x want %x", got, want)
	}

	var tx wire.MsgTx
	if err := tx.Deserialize(bytes.NewReader(raw)); err != nil {
		t.Fatalf("btcd MsgTx deserialize: %v", err)
	}

	h := tx.TxHash()
	if got, want := h[:], txid; !bytes.Equal(got, want) {
		t.Fatalf("txid mismatch vs btcd MsgTx.TxHash: got %x want %x", got, want)
	}

	var buf bytes.Buffer
	if err := tx.Serialize(&buf); err != nil {
		t.Fatalf("btcd MsgTx serialize: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), raw) {
		t.Fatalf("btcd serialize roundtrip differs from original bytes")
	}
}

func TestCoinbase_EncodeDecode_AgainstBtcd(t *testing.T) {
	height := int64(123)
	ex1 := []byte{0x01, 0x02, 0x03, 0x04}
	ex2 := []byte{0xaa, 0xbb, 0xcc, 0xdd}
	templateExtraNonce2Size := 8 // force padding path
	witnessCommitment := "6a24aa21a9ed" + "0000000000000000000000000000000000000000000000000000000000000000"
	coinbaseFlags := "deadbeef"
	coinbaseMsg := "btcd-compat"
	scriptTime := int64(0)

	t.Run("single", func(t *testing.T) {
		payoutScript := []byte{0x51} // OP_TRUE
		coinbaseValue := int64(50 * 1e8)
		raw, txid, err := serializeCoinbaseTx(height, ex1, ex2, templateExtraNonce2Size, payoutScript, coinbaseValue, witnessCommitment, coinbaseFlags, coinbaseMsg, scriptTime)
		if err != nil {
			t.Fatalf("serializeCoinbaseTx: %v", err)
		}
		assertCoinbaseEncodesAndDecodesWithBtcd(t, raw, txid)
	})

	t.Run("dual", func(t *testing.T) {
		poolScript := []byte{0x51}
		workerScript := []byte{0x52}
		totalValue := int64(50 * 1e8)
		feePercent := 2.0
		raw, txid, err := serializeDualCoinbaseTx(height, ex1, ex2, templateExtraNonce2Size, poolScript, workerScript, totalValue, feePercent, witnessCommitment, coinbaseFlags, coinbaseMsg, scriptTime)
		if err != nil {
			t.Fatalf("serializeDualCoinbaseTx: %v", err)
		}
		assertCoinbaseEncodesAndDecodesWithBtcd(t, raw, txid)
	})

	t.Run("triple", func(t *testing.T) {
		poolScript := []byte{0x51}
		donationScript := []byte{0x53}
		workerScript := []byte{0x52}
		totalValue := int64(50 * 1e8)
		poolFeePercent := 2.0
		donationFeePercent := 12.5
		raw, txid, err := serializeTripleCoinbaseTx(height, ex1, ex2, templateExtraNonce2Size, poolScript, donationScript, workerScript, totalValue, poolFeePercent, donationFeePercent, witnessCommitment, coinbaseFlags, coinbaseMsg, scriptTime)
		if err != nil {
			t.Fatalf("serializeTripleCoinbaseTx: %v", err)
		}
		assertCoinbaseEncodesAndDecodesWithBtcd(t, raw, txid)
	})

	t.Run("multi-output hot-path", func(t *testing.T) {
		flagsBytes := mustDecodeHex(t, coinbaseFlags)
		commitmentScript := mustDecodeHex(t, witnessCommitment)
		payouts := []coinbasePayoutOutput{
			{Script: []byte{0x51}, Value: 1},
			{Script: []byte{0x52}, Value: 2},
			{Script: []byte{0x53}, Value: 3},
		}
		raw, txid, err := serializeCoinbaseTxPayoutsPredecoded(height, ex1, ex2, templateExtraNonce2Size, payouts, commitmentScript, flagsBytes, coinbaseMsg, scriptTime)
		if err != nil {
			t.Fatalf("serializeCoinbaseTxPayoutsPredecoded: %v", err)
		}
		assertCoinbaseEncodesAndDecodesWithBtcd(t, raw, txid)
	})
}

func TestCoinbaseParts_ReconstructsExactTx_Btcd(t *testing.T) {
	height := int64(456)
	ex1 := []byte{0x01, 0x02, 0x03, 0x04}
	ex2 := []byte{0xaa, 0xbb, 0xcc, 0xdd}
	extranonce2Size := len(ex2)
	templateExtraNonce2Size := 8 // force padding path
	witnessCommitment := "6a24aa21a9ed" + "0000000000000000000000000000000000000000000000000000000000000000"
	coinbaseFlags := "deadbeef"
	coinbaseMsg := "btcd-coinb-parts"
	scriptTime := int64(0)

	payouts := []coinbasePayoutOutput{
		{Script: []byte{0x51}, Value: 1},
		{Script: []byte{0x52}, Value: 2},
		{Script: []byte{0x53}, Value: 3},
	}

	coinb1, coinb2, err := buildCoinbasePartsPayouts(height, ex1, extranonce2Size, templateExtraNonce2Size, payouts, witnessCommitment, coinbaseFlags, coinbaseMsg, scriptTime)
	if err != nil {
		t.Fatalf("buildCoinbasePartsPayouts: %v", err)
	}

	rawFromParts := mustDecodeHex(t, coinb1+hex.EncodeToString(ex1)+hex.EncodeToString(ex2)+coinb2)

	flagsBytes := mustDecodeHex(t, coinbaseFlags)
	commitmentScript := mustDecodeHex(t, witnessCommitment)
	rawDirect, txidDirect, err := serializeCoinbaseTxPayoutsPredecoded(height, ex1, ex2, templateExtraNonce2Size, payouts, commitmentScript, flagsBytes, coinbaseMsg, scriptTime)
	if err != nil {
		t.Fatalf("serializeCoinbaseTxPayoutsPredecoded: %v", err)
	}

	if !bytes.Equal(rawFromParts, rawDirect) {
		t.Fatalf("coinb1/coinb2 reconstruction mismatch vs direct serialization")
	}
	assertCoinbaseEncodesAndDecodesWithBtcd(t, rawFromParts, txidDirect)
}

func TestCoinbaseParts_Lengths_SparkMinerCompatibility(t *testing.T) {
	height := int64(900000)
	ex1 := []byte{0x01, 0x02, 0x03, 0x04}
	extranonce2Size := 4
	templateExtraNonce2Size := 4
	witnessCommitment := "6a24aa21a9ed" + "0000000000000000000000000000000000000000000000000000000000000000"
	coinbaseFlags := "deadbeef"
	coinbaseMsg := "m45tiny-goPool"
	scriptTime := int64(0x699ef208)

	// Realistic P2WPKH output scripts (22 bytes each).
	poolScript := mustDecodeHex(t, "0014e083a5a0a55cd19df9a19481fe362dbdc39fb43f")
	workerScript := mustDecodeHex(t, "0014ea30ffab0c33f0fd99511da4175f3eb7f3f24e45")
	donationScript := mustDecodeHex(t, "0014331a0a25095d90d338e93dab611d8f8c3584fa57")

	t.Run("single", func(t *testing.T) {
		coinb1, coinb2, err := buildCoinbaseParts(height, ex1, extranonce2Size, templateExtraNonce2Size, workerScript, 50*1e8, witnessCommitment, coinbaseFlags, coinbaseMsg, scriptTime)
		if err != nil {
			t.Fatalf("buildCoinbaseParts: %v", err)
		}
		t.Logf("single coinb1_hex=%d coinb2_hex=%d coinb2_bytes=%d", len(coinb1), len(coinb2), len(coinb2)/2)
		if len(coinb2)%2 != 0 {
			t.Fatalf("coinb2 hex length must be even, got %d", len(coinb2))
		}
	})

	t.Run("dual", func(t *testing.T) {
		coinb1, coinb2, err := buildDualPayoutCoinbaseParts(height, ex1, extranonce2Size, templateExtraNonce2Size, poolScript, workerScript, 50*1e8, 2.0, witnessCommitment, coinbaseFlags, coinbaseMsg, scriptTime)
		if err != nil {
			t.Fatalf("buildDualPayoutCoinbaseParts: %v", err)
		}
		t.Logf("dual coinb1_hex=%d coinb2_hex=%d coinb2_bytes=%d", len(coinb1), len(coinb2), len(coinb2)/2)
		if len(coinb2)%2 != 0 {
			t.Fatalf("coinb2 hex length must be even, got %d", len(coinb2))
		}
		if len(coinb2) <= 255 {
			t.Fatalf("expected dual-payout coinb2 to exceed SparkMiner coinBase2 buffer (255 hex chars), got %d", len(coinb2))
		}
	})

	t.Run("triple", func(t *testing.T) {
		coinb1, coinb2, err := buildTriplePayoutCoinbaseParts(height, ex1, extranonce2Size, templateExtraNonce2Size, poolScript, donationScript, workerScript, 50*1e8, 2.0, 12.5, witnessCommitment, coinbaseFlags, coinbaseMsg, scriptTime)
		if err != nil {
			t.Fatalf("buildTriplePayoutCoinbaseParts: %v", err)
		}
		t.Logf("triple coinb1_hex=%d coinb2_hex=%d coinb2_bytes=%d", len(coinb1), len(coinb2), len(coinb2)/2)
		if len(coinb2)%2 != 0 {
			t.Fatalf("coinb2 hex length must be even, got %d", len(coinb2))
		}
		if len(coinb2) <= 255 {
			t.Fatalf("expected triple-payout coinb2 to exceed SparkMiner coinBase2 buffer (255 hex chars), got %d", len(coinb2))
		}
	})
}
