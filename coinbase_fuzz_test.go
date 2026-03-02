package main

import (
	"bytes"
	"sort"
	"testing"

	"github.com/btcsuite/btcd/chaincfg"
)

// FuzzCoinbasePayoutSerializationRoundTrip stress-tests the full payout path:
// generated btcd addresses -> scripts -> coinbase serialization -> tx decode.
func FuzzCoinbasePayoutSerializationRoundTrip(f *testing.F) {
	f.Add([]byte{0x00})
	f.Add([]byte{0x01, 0x20, 0x40, 0x80})
	f.Add([]byte("goPool-fuzz-coinbase-payouts"))

	f.Fuzz(func(t *testing.T, seed []byte) {
		if len(seed) == 0 {
			seed = []byte{0x00}
		}

		var params *chaincfg.Params
		switch seed[0] % 3 {
		case 0:
			params = &chaincfg.MainNetParams
		case 1:
			params = &chaincfg.TestNet3Params
		default:
			params = &chaincfg.RegressionNetParams
		}

		height := int64(100000 + int(seed[0]))
		ex1 := []byte{0x01, 0x02, 0x03, 0x04}
		ex2 := []byte{0xaa, 0xbb, 0xcc, 0xdd}
		templateExtra := 8

		count := 1 + int(seed[len(seed)-1]%8) // 1-8 payouts
		payouts := make([]coinbasePayoutOutput, 0, count)
		for i := 0; i < count; i++ {
			addrType := fuzzDigest(seed, byte(i), 0x31)
			addr := fuzzGeneratedAddress(t, params, seed, i, int(addrType[0]%5))

			script, err := scriptForAddress(addr, params)
			if err != nil {
				t.Fatalf("scriptForAddress(%s): %v", addr, err)
			}

			valSeed := fuzzDigest(seed, byte(i), 0x32)
			value := int64(1 + (int64(valSeed[0]) << 2) + int64(i%3)) // keep non-zero, with ties.
			payouts = append(payouts, coinbasePayoutOutput{
				Script: script,
				Value:  value,
			})
		}

		var commitment []byte
		if seed[0]&1 == 1 {
			// OP_RETURN(0x6a) + push-36(0x24) + aa21a9ed + 32-byte commitment.
			commitment = append([]byte{0x6a, 0x24, 0xaa, 0x21, 0xa9, 0xed}, bytes.Repeat([]byte{0x00}, 32)...)
		}

		raw, txid, err := serializeCoinbaseTxPayoutsPredecoded(
			height,
			ex1,
			ex2,
			templateExtra,
			payouts,
			commitment,
			nil,
			"fuzz-payouts",
			0,
		)
		if err != nil {
			t.Fatalf("serializeCoinbaseTxPayoutsPredecoded: %v", err)
		}
		if len(txid) != 32 {
			t.Fatalf("txid len mismatch: got %d", len(txid))
		}

		outs := parseCoinbaseOutputs(t, raw)
		wantCount := len(payouts)
		if len(commitment) > 0 {
			wantCount++
		}
		if len(outs) != wantCount {
			t.Fatalf("output count mismatch: got %d want %d", len(outs), wantCount)
		}

		start := 0
		if len(commitment) > 0 {
			if outs[0].Value != 0 || !bytes.Equal(outs[0].PkScript, commitment) {
				t.Fatalf("commitment output mismatch")
			}
			start = 1
		}

		expected := append([]coinbasePayoutOutput(nil), payouts...)
		sort.SliceStable(expected, func(i, j int) bool {
			return expected[i].Value > expected[j].Value
		})

		var gotTotal int64
		for i, want := range expected {
			got := outs[start+i]
			if got.Value != want.Value {
				t.Fatalf("value mismatch idx=%d got=%d want=%d", i, got.Value, want.Value)
			}
			if !bytes.Equal(got.PkScript, want.Script) {
				t.Fatalf("script mismatch idx=%d got=%x want=%x", i, got.PkScript, want.Script)
			}
			gotTotal += got.Value

			// Ensure script<->address conversion still holds on serialized outputs.
			addr := scriptToAddress(got.PkScript, params)
			if addr == "" {
				t.Fatalf("scriptToAddress returned empty for idx=%d script=%x", i, got.PkScript)
			}
			rtScript, err := scriptForAddress(addr, params)
			if err != nil {
				t.Fatalf("round-trip scriptForAddress failed for idx=%d addr=%s: %v", i, addr, err)
			}
			if !bytes.Equal(rtScript, got.PkScript) {
				t.Fatalf("round-trip script mismatch idx=%d addr=%s", i, addr)
			}
		}

		var wantTotal int64
		for _, o := range payouts {
			wantTotal += o.Value
		}
		if gotTotal != wantTotal {
			t.Fatalf("payout total mismatch: got=%d want=%d", gotTotal, wantTotal)
		}
	})
}
