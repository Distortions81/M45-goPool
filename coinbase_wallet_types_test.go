package main

import (
	"bytes"
	"math"
	"testing"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
)

// TestDualCoinbaseWithMixedWalletTypes verifies that dual coinbase (pool + worker)
// works correctly with all combinations of wallet types: P2PKH, P2SH, P2WPKH, P2WSH, and P2TR (taproot).
func TestDualCoinbaseWithMixedWalletTypes(t *testing.T) {
	params := &chaincfg.MainNetParams

	// Define test wallet addresses for each type
	wallets := []struct {
		name     string
		address  string
		addrType string
	}{
		{
			name:     "P2PKH",
			address:  "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa",
			addrType: "P2PKH",
		},
		{
			name:     "P2SH",
			address:  "3Ai1JZ8pdJb2ksieUV8FsxSNVJCpoPi8W6",
			addrType: "P2SH",
		},
		{
			name:     "P2WPKH",
			address:  "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
			addrType: "P2WPKH",
		},
		{
			name:     "P2WSH",
			address:  "bc1qrp33g0q5c5txsp9arysrx4k6zdkfs4nce4xj0gdcccefvpysxf3qccfmv3",
			addrType: "P2WSH",
		},
		{
			name:     "P2TR_Taproot",
			address:  "bc1p5cyxnuxmeuwuvkwfem96lqzszd02n6xdcjrs20cac6yqjjwudpxqkedrcr",
			addrType: "P2TR",
		},
		{
			name:     "P2TR_Taproot_2",
			address:  "bc1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vqzk5jj0",
			addrType: "P2TR",
		},
	}

	// Test all combinations of pool + worker wallet types
	for _, poolWallet := range wallets {
		for _, workerWallet := range wallets {
			testName := poolWallet.name + "_pool_with_" + workerWallet.name + "_worker"
			t.Run(testName, func(t *testing.T) {
				// Generate scripts for both addresses
				poolScript, err := scriptForAddress(poolWallet.address, params)
				if err != nil {
					t.Skipf("Could not generate script for %s: %v", poolWallet.address, err)
				}

				workerScript, err := scriptForAddress(workerWallet.address, params)
				if err != nil {
					t.Skipf("Could not generate script for %s: %v", workerWallet.address, err)
				}

				// Create dual coinbase transaction
				height := int64(800000)
				ex1 := []byte{0x01, 0x02, 0x03, 0x04}
				ex2 := []byte{0xaa, 0xbb, 0xcc, 0xdd}
				templateExtra := len(ex1) + len(ex2)
				totalValue := int64(6_25000000) // 6.25 BTC in satoshis
				feePercent := 2.0
				witnessCommitment := ""
				coinbaseFlags := ""
				coinbaseMsg := "goPool-test"
				scriptTime := int64(0)

				raw, txid, err := serializeDualCoinbaseTx(
					height,
					ex1,
					ex2,
					templateExtra,
					poolScript,
					workerScript,
					totalValue,
					feePercent,
					witnessCommitment,
					coinbaseFlags,
					coinbaseMsg,
					scriptTime,
				)
				if err != nil {
					t.Fatalf("serializeDualCoinbaseTx failed: %v", err)
				}

				if len(txid) != 32 {
					t.Fatalf("expected 32-byte txid, got %d", len(txid))
				}

				// Decode with btcd to verify structure
				var tx wire.MsgTx
				if err := tx.Deserialize(bytes.NewReader(raw)); err != nil {
					t.Fatalf("failed to deserialize coinbase: %v", err)
				}

				// Verify we have exactly 2 outputs (pool + worker)
				if len(tx.TxOut) != 2 {
					t.Fatalf("expected 2 outputs, got %d", len(tx.TxOut))
				}

				// Calculate expected values
				poolFee := int64(math.Round(float64(totalValue) * feePercent / 100.0))
				workerValue := totalValue - poolFee

				// Verify pool output
				var (
					poolOut   *wire.TxOut
					workerOut *wire.TxOut
				)
				if bytes.Equal(poolScript, workerScript) {
					// When pool and worker scripts are identical, outputs are only
					// distinguishable by value. The coinbase encoder orders payouts
					// from largest to smallest.
					if tx.TxOut[0].Value != workerValue || tx.TxOut[1].Value != poolFee {
						t.Errorf("dual outputs mismatch for identical scripts: got [%d,%d], want [%d,%d]", tx.TxOut[0].Value, tx.TxOut[1].Value, workerValue, poolFee)
					}
					if !bytes.Equal(tx.TxOut[0].PkScript, poolScript) || !bytes.Equal(tx.TxOut[1].PkScript, poolScript) {
						t.Errorf("expected both outputs to use the same script")
					}
					poolOut = tx.TxOut[1]
					workerOut = tx.TxOut[0]
				} else {
					for _, o := range tx.TxOut {
						switch {
						case bytes.Equal(o.PkScript, poolScript):
							poolOut = o
						case bytes.Equal(o.PkScript, workerScript):
							workerOut = o
						}
					}
					if poolOut == nil {
						t.Fatalf("pool output not found by script")
					}
					if workerOut == nil {
						t.Fatalf("worker output not found by script")
					}
					if poolOut.Value != poolFee {
						t.Errorf("pool output value mismatch: got %d, want %d", poolOut.Value, poolFee)
					}
					if workerOut.Value != workerValue {
						t.Errorf("worker output value mismatch: got %d, want %d", workerOut.Value, workerValue)
					}
				}

				// Verify total adds up
				totalOut := poolOut.Value + workerOut.Value
				if totalOut != totalValue {
					t.Errorf("total output mismatch: got %d, want %d", totalOut, totalValue)
				}

				// Verify scripts can be decoded back to addresses
				poolAddr := scriptToAddress(poolOut.PkScript, params)
				workerAddr := scriptToAddress(workerOut.PkScript, params)

				if poolAddr != poolWallet.address {
					t.Errorf("pool address round-trip failed:\ngot:  %s\nwant: %s", poolAddr, poolWallet.address)
				}
				if workerAddr != workerWallet.address {
					t.Errorf("worker address round-trip failed:\ngot:  %s\nwant: %s", workerAddr, workerWallet.address)
				}

				t.Logf("✓ Pool (%s): %s -> %d sats", poolWallet.addrType, poolWallet.address, poolFee)
				t.Logf("✓ Worker (%s): %s -> %d sats", workerWallet.addrType, workerWallet.address, workerValue)
			})
		}
	}
}

// TestSingleCoinbaseWithAllWalletTypes verifies that single-output coinbase
// works with all wallet types including taproot.
func TestSingleCoinbaseWithAllWalletTypes(t *testing.T) {
	params := &chaincfg.MainNetParams

	wallets := []struct {
		name    string
		address string
	}{
		{"P2PKH", "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa"},
		{"P2SH", "3Ai1JZ8pdJb2ksieUV8FsxSNVJCpoPi8W6"},
		{"P2WPKH", "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4"},
		{"P2WSH", "bc1qrp33g0q5c5txsp9arysrx4k6zdkfs4nce4xj0gdcccefvpysxf3qccfmv3"},
		{"P2TR", "bc1p5cyxnuxmeuwuvkwfem96lqzszd02n6xdcjrs20cac6yqjjwudpxqkedrcr"},
	}

	for _, wallet := range wallets {
		t.Run(wallet.name, func(t *testing.T) {
			script, err := scriptForAddress(wallet.address, params)
			if err != nil {
				t.Skipf("Could not generate script: %v", err)
			}

			height := int64(850000)
			ex1 := []byte{0x01, 0x02, 0x03, 0x04}
			ex2 := []byte{0xaa, 0xbb, 0xcc, 0xdd}
			templateExtra := len(ex1) + len(ex2)
			coinbaseValue := int64(6_25000000)
			witnessCommitment := ""
			coinbaseFlags := ""
			coinbaseMsg := "goPool-single"
			scriptTime := int64(0)

			raw, txid, err := serializeCoinbaseTx(
				height,
				ex1,
				ex2,
				templateExtra,
				script,
				coinbaseValue,
				witnessCommitment,
				coinbaseFlags,
				coinbaseMsg,
				scriptTime,
			)
			if err != nil {
				t.Fatalf("serializeCoinbaseTx failed: %v", err)
			}

			if len(txid) != 32 {
				t.Fatalf("expected 32-byte txid, got %d", len(txid))
			}

			// Decode and verify
			var tx wire.MsgTx
			if err := tx.Deserialize(bytes.NewReader(raw)); err != nil {
				t.Fatalf("failed to deserialize: %v", err)
			}

			if len(tx.TxOut) != 1 {
				t.Fatalf("expected 1 output, got %d", len(tx.TxOut))
			}

			if tx.TxOut[0].Value != coinbaseValue {
				t.Errorf("output value mismatch: got %d, want %d", tx.TxOut[0].Value, coinbaseValue)
			}

			if !bytes.Equal(tx.TxOut[0].PkScript, script) {
				t.Errorf("script mismatch")
			}

			// Verify round-trip
			addr := scriptToAddress(tx.TxOut[0].PkScript, params)
			if addr != wallet.address {
				t.Errorf("address round-trip failed:\ngot:  %s\nwant: %s", addr, wallet.address)
			}

			t.Logf("✓ %s: %s -> %d sats", wallet.name, wallet.address, coinbaseValue)
		})
	}
}

// TestScriptCompatibilityWithBtcd verifies that our scriptForAddress generates
// the same scripts as btcd for all wallet types.
func TestScriptCompatibilityWithBtcd(t *testing.T) {
	params := &chaincfg.MainNetParams

	addresses := []string{
		"1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa",                             // P2PKH
		"3Ai1JZ8pdJb2ksieUV8FsxSNVJCpoPi8W6",                             // P2SH
		"bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",                     // P2WPKH
		"bc1qrp33g0q5c5txsp9arysrx4k6zdkfs4nce4xj0gdcccefvpysxf3qccfmv3", // P2WSH
		"bc1p5cyxnuxmeuwuvkwfem96lqzszd02n6xdcjrs20cac6yqjjwudpxqkedrcr", // P2TR (taproot)
		"bc1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vqzk5jj0", // P2TR (taproot)
	}

	for _, addrStr := range addresses {
		t.Run(addrStr, func(t *testing.T) {
			// Decode with btcd
			btcdAddr, err := btcutil.DecodeAddress(addrStr, params)
			if err != nil {
				t.Skipf("btcd doesn't support this address: %v", err)
			}

			// Generate script with btcd
			btcdScript, err := txscript.PayToAddrScript(btcdAddr)
			if err != nil {
				t.Fatalf("btcd PayToAddrScript failed: %v", err)
			}

			// Generate script with goPool
			poolScript, err := scriptForAddress(addrStr, params)
			if err != nil {
				t.Fatalf("goPool scriptForAddress failed: %v", err)
			}

			// Compare
			if !bytes.Equal(btcdScript, poolScript) {
				t.Errorf("script mismatch for %s:\nbtcd:   %x\ngoPool: %x",
					addrStr, btcdScript, poolScript)
			}

			t.Logf("✓ %s matches btcd", addrStr)
		})
	}
}

// TestTaprootInDualCoinbase specifically tests that taproot addresses work
// correctly in dual coinbase mode (the most complex scenario).
func TestTaprootInDualCoinbase(t *testing.T) {
	params := &chaincfg.MainNetParams

	testCases := []struct {
		name        string
		poolAddr    string
		workerAddr  string
		description string
	}{
		{
			name:        "taproot_pool_legacy_worker",
			poolAddr:    "bc1p5cyxnuxmeuwuvkwfem96lqzszd02n6xdcjrs20cac6yqjjwudpxqkedrcr",
			workerAddr:  "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa",
			description: "Taproot pool receiving fees, legacy P2PKH worker",
		},
		{
			name:        "legacy_pool_taproot_worker",
			poolAddr:    "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa",
			workerAddr:  "bc1p5cyxnuxmeuwuvkwfem96lqzszd02n6xdcjrs20cac6yqjjwudpxqkedrcr",
			description: "Legacy pool, taproot worker getting reward",
		},
		{
			name:        "both_taproot",
			poolAddr:    "bc1p5cyxnuxmeuwuvkwfem96lqzszd02n6xdcjrs20cac6yqjjwudpxqkedrcr",
			workerAddr:  "bc1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vqzk5jj0",
			description: "Both pool and worker using taproot",
		},
		{
			name:        "taproot_pool_segwit_worker",
			poolAddr:    "bc1p5cyxnuxmeuwuvkwfem96lqzszd02n6xdcjrs20cac6yqjjwudpxqkedrcr",
			workerAddr:  "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
			description: "Taproot pool, segwit v0 worker",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			poolScript, err := scriptForAddress(tc.poolAddr, params)
			if err != nil {
				t.Fatalf("failed to generate pool script: %v", err)
			}

			workerScript, err := scriptForAddress(tc.workerAddr, params)
			if err != nil {
				t.Fatalf("failed to generate worker script: %v", err)
			}

			// Create dual coinbase
			height := int64(900000)
			ex1 := []byte{0x01, 0x02, 0x03, 0x04}
			ex2 := []byte{0xaa, 0xbb, 0xcc, 0xdd}
			templateExtra := len(ex1) + len(ex2)
			totalValue := int64(6_25000000)
			feePercent := 2.5
			witnessCommitment := ""
			coinbaseFlags := ""
			coinbaseMsg := "goPool-taproot"
			scriptTime := int64(0)

			raw, _, err := serializeDualCoinbaseTx(
				height,
				ex1,
				ex2,
				templateExtra,
				poolScript,
				workerScript,
				totalValue,
				feePercent,
				witnessCommitment,
				coinbaseFlags,
				coinbaseMsg,
				scriptTime,
			)
			if err != nil {
				t.Fatalf("serializeDualCoinbaseTx failed: %v", err)
			}

			// Decode and verify
			var tx wire.MsgTx
			if err := tx.Deserialize(bytes.NewReader(raw)); err != nil {
				t.Fatalf("failed to deserialize: %v", err)
			}

			if len(tx.TxOut) != 2 {
				t.Fatalf("expected 2 outputs, got %d", len(tx.TxOut))
			}

			var (
				poolOut   *wire.TxOut
				workerOut *wire.TxOut
			)
			for _, o := range tx.TxOut {
				switch {
				case bytes.Equal(o.PkScript, poolScript):
					poolOut = o
				case bytes.Equal(o.PkScript, workerScript):
					workerOut = o
				}
			}
			if poolOut == nil {
				t.Fatalf("pool output not found by script")
			}
			if workerOut == nil {
				t.Fatalf("worker output not found by script")
			}

			// Verify addresses match
			poolAddrOut := scriptToAddress(poolOut.PkScript, params)
			workerAddrOut := scriptToAddress(workerOut.PkScript, params)

			if poolAddrOut != tc.poolAddr {
				t.Errorf("pool address mismatch:\ngot:  %s\nwant: %s", poolAddrOut, tc.poolAddr)
			}
			if workerAddrOut != tc.workerAddr {
				t.Errorf("worker address mismatch:\ngot:  %s\nwant: %s", workerAddrOut, tc.workerAddr)
			}

			t.Logf("✓ %s", tc.description)
			t.Logf("  Pool:   %s (%d sats)", tc.poolAddr, poolOut.Value)
			t.Logf("  Worker: %s (%d sats)", tc.workerAddr, workerOut.Value)
		})
	}
}
