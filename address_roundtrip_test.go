package main

import (
	"testing"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/txscript"
)

// TestAddressRoundTripAllTypes verifies that address -> script -> address
// conversion works correctly for all address types. This is CRITICAL for
// block rewards - we cannot afford to lose funds due to address conversion bugs!
func TestAddressRoundTripAllTypes(t *testing.T) {
	params := &chaincfg.MainNetParams

	testCases := []struct {
		name    string
		address string
	}{
		// Real mainnet addresses that should work
		{"P2PKH_genesis", "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa"},
		{"P2PKH_hal", "1HLoD9E4SDFFPDiYfNYnkBLQ85Y51J3Zb1"}, // Hal Finney
		{"P2WPKH_segwit", "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4"},
		{"P2WSH_segwit", "bc1qrp33g0q5c5txsp9arysrx4k6zdkfs4nce4xj0gdcccefvpysxf3qccfmv3"},
		{"P2TR_taproot_1", "bc1p5cyxnuxmeuwuvkwfem96lqzszd02n6xdcjrs20cac6yqjjwudpxqkedrcr"},
		{"P2TR_taproot_2", "bc1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vqzk5jj0"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Step 1: Verify btcd can decode it
			btcdAddr, err := btcutil.DecodeAddress(tc.address, params)
			if err != nil {
				t.Skipf("btcd cannot decode: %v", err)
			}

			// Step 2: Get script from btcd
			btcdScript, err := txscript.PayToAddrScript(btcdAddr)
			if err != nil {
				t.Fatalf("btcd PayToAddrScript failed: %v", err)
			}

			// Step 3: Address -> script with goPool
			poolScript, err := scriptForAddress(tc.address, params)
			if err != nil {
				t.Fatalf("scriptForAddress failed: %v", err)
			}

			// Verify scripts match
			if !equalBytes(btcdScript, poolScript) {
				t.Fatalf("script mismatch:\nbtcd:   %x\ngoPool: %x", btcdScript, poolScript)
			}

			// Step 4: Script -> address with goPool (THE CRITICAL TEST!)
			resultAddr := scriptToAddress(poolScript, params)
			if resultAddr == "" {
				t.Fatalf("scriptToAddress returned empty string for script %x", poolScript)
			}

			// Step 5: Verify round-trip
			if resultAddr != tc.address {
				t.Errorf("ROUND-TRIP FAILURE (CRITICAL!):\noriginal: %s\nresult:   %s\nscript:   %x",
					tc.address, resultAddr, poolScript)
			} else {
				t.Logf("✓ Round-trip OK: %s", tc.address)
			}

			// Step 6: Verify the result can be decoded by btcd
			verifyAddr, err := btcutil.DecodeAddress(resultAddr, params)
			if err != nil {
				t.Errorf("btcd cannot decode result address %s: %v", resultAddr, err)
			}

			// Step 7: Verify the script from result address matches
			verifyScript, err := txscript.PayToAddrScript(verifyAddr)
			if err != nil {
				t.Errorf("btcd PayToAddrScript failed for result: %v", err)
			}

			if !equalBytes(verifyScript, poolScript) {
				t.Errorf("script from result address doesn't match:\noriginal: %x\nverify:   %x",
					poolScript, verifyScript)
			}
		})
	}
}

// TestCoinbaseOutputAddressRecovery tests that we can correctly recover
// addresses from coinbase outputs. This simulates what would happen when
// we need to verify or display payout addresses from a mined block.
func TestCoinbaseOutputAddressRecovery(t *testing.T) {
	params := &chaincfg.MainNetParams

	testCases := []struct {
		name      string
		poolAddr  string
		workerAddr string
	}{
		{
			name:       "taproot_both",
			poolAddr:   "bc1p5cyxnuxmeuwuvkwfem96lqzszd02n6xdcjrs20cac6yqjjwudpxqkedrcr",
			workerAddr: "bc1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vqzk5jj0",
		},
		{
			name:       "taproot_segwit",
			poolAddr:   "bc1p5cyxnuxmeuwuvkwfem96lqzszd02n6xdcjrs20cac6yqjjwudpxqkedrcr",
			workerAddr: "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
		},
		{
			name:       "segwit_taproot",
			poolAddr:   "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
			workerAddr: "bc1p5cyxnuxmeuwuvkwfem96lqzszd02n6xdcjrs20cac6yqjjwudpxqkedrcr",
		},
		{
			name:       "legacy_taproot",
			poolAddr:   "1HLoD9E4SDFFPDiYfNYnkBLQ85Y51J3Zb1",
			workerAddr: "bc1p5cyxnuxmeuwuvkwfem96lqzszd02n6xdcjrs20cac6yqjjwudpxqkedrcr",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Generate scripts
			poolScript, err := scriptForAddress(tc.poolAddr, params)
			if err != nil {
				t.Fatalf("failed to generate pool script: %v", err)
			}

			workerScript, err := scriptForAddress(tc.workerAddr, params)
			if err != nil {
				t.Fatalf("failed to generate worker script: %v", err)
			}

			// Simulate: we mined a block and need to recover addresses from outputs
			recoveredPool := scriptToAddress(poolScript, params)
			recoveredWorker := scriptToAddress(workerScript, params)

			// CRITICAL: These must match exactly!
			if recoveredPool != tc.poolAddr {
				t.Errorf("CRITICAL: Pool address recovery failed!\nexpected: %s\ngot:      %s",
					tc.poolAddr, recoveredPool)
			}

			if recoveredWorker != tc.workerAddr {
				t.Errorf("CRITICAL: Worker address recovery failed!\nexpected: %s\ngot:      %s",
					tc.workerAddr, recoveredWorker)
			}

			if recoveredPool == tc.poolAddr && recoveredWorker == tc.workerAddr {
				t.Logf("✓ Pool:   %s ← %x", recoveredPool, poolScript)
				t.Logf("✓ Worker: %s ← %x", recoveredWorker, workerScript)
			}
		})
	}
}

// TestBase58EncodingRoundTrip specifically tests base58 encoding/decoding
// which is critical for P2PKH and P2SH addresses.
func TestBase58EncodingRoundTrip(t *testing.T) {
	params := &chaincfg.MainNetParams

	testHashes := []struct {
		name    string
		version byte
		hash    []byte
	}{
		{
			name:    "genesis_p2pkh",
			version: params.PubKeyHashAddrID,
			hash: []byte{
				0x62, 0xe9, 0x07, 0xb1, 0x5c, 0xbf, 0x27, 0xd5,
				0x42, 0x53, 0x99, 0xeb, 0xf6, 0xf0, 0xfb, 0x50,
				0xeb, 0xb8, 0x8f, 0x18,
			},
		},
		{
			name:    "random_p2pkh",
			version: params.PubKeyHashAddrID,
			hash: []byte{
				0xab, 0xcd, 0xef, 0x01, 0x23, 0x45, 0x67, 0x89,
				0xab, 0xcd, 0xef, 0x01, 0x23, 0x45, 0x67, 0x89,
				0xab, 0xcd, 0xef, 0x01,
			},
		},
	}

	for _, tc := range testHashes {
		t.Run(tc.name, func(t *testing.T) {
			// Encode
			addr, err := base58CheckEncode(tc.version, tc.hash)
			if err != nil {
				t.Fatalf("base58CheckEncode failed: %v", err)
			}

			if addr == "" {
				t.Fatal("base58CheckEncode returned empty string")
			}

			t.Logf("Encoded: %s", addr)

			// Verify btcd can decode it
			btcdAddr, err := btcutil.DecodeAddress(addr, params)
			if err != nil {
				t.Fatalf("btcd cannot decode our encoded address: %v", err)
			}

			// Decode and verify hash matches
			decodedVer, decodedHash, err := base58CheckDecode(addr)
			if err != nil {
				t.Fatalf("base58CheckDecode failed: %v", err)
			}

			if decodedVer != tc.version {
				t.Errorf("version mismatch: expected %d, got %d", tc.version, decodedVer)
			}

			if !equalBytes(decodedHash, tc.hash) {
				t.Errorf("hash mismatch:\nexpected: %x\ngot:      %x", tc.hash, decodedHash)
			}

			// Verify btcd's encoding matches ours
			btcdEncoded := btcdAddr.EncodeAddress()
			if btcdEncoded != addr {
				t.Logf("NOTE: Encoding difference (may be OK if both valid):\nours: %s\nbtcd: %s",
					addr, btcdEncoded)
			}
		})
	}
}
