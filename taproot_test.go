package main

import (
	"bytes"
	"testing"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/txscript"
)

// TestTaprootAddressValidation verifies that goPool correctly validates
// taproot addresses (witness version 1) using bech32m encoding as specified
// in BIP 350.
func TestTaprootAddressValidation(t *testing.T) {
	testCases := []struct {
		name    string
		address string
		network *chaincfg.Params
		valid   bool
	}{
		{
			name:    "mainnet_taproot_valid",
			address: "bc1p5cyxnuxmeuwuvkwfem96lqzszd02n6xdcjrs20cac6yqjjwudpxqkedrcr",
			network: &chaincfg.MainNetParams,
			valid:   true,
		},
		{
			name:    "mainnet_taproot_valid_2",
			address: "bc1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vqzk5jj0",
			network: &chaincfg.MainNetParams,
			valid:   true,
		},
		{
			name:    "testnet_taproot_valid",
			address: "tb1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vq47zagq",
			network: &chaincfg.TestNet3Params,
			valid:   true,
		},
		{
			name:    "mainnet_segwit_v0_valid",
			address: "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
			network: &chaincfg.MainNetParams,
			valid:   true,
		},
		{
			name:    "mainnet_taproot_invalid_checksum",
			address: "bc1p5cyxnuxmeuwuvkwfem96lqzszd02n6xdcjrs20cac6yqjjwudpxqkedrxx",
			network: &chaincfg.MainNetParams,
			valid:   false,
		},
		{
			name:    "testnet_on_mainnet",
			address: "tb1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vq47zagq",
			network: &chaincfg.MainNetParams,
			valid:   false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Test with btcd's DecodeAddress
			addr, errBtcd := btcutil.DecodeAddress(tc.address, tc.network)

			// Test with goPool's scriptForAddress
			script, errPool := scriptForAddress(tc.address, tc.network)

			if tc.valid {
				if errBtcd != nil {
					t.Skipf("btcd doesn't support this address type: %v", errBtcd)
				}
				if errPool != nil {
					t.Errorf("goPool failed to decode valid address: %v", errPool)
				}
				if addr == nil || len(script) == 0 {
					t.Errorf("expected non-nil address and non-empty script")
				}

				// Verify round-trip: address -> script -> address
				if addr != nil && len(script) > 0 {
					roundTrip := scriptToAddress(script, tc.network)
					if roundTrip != tc.address {
						t.Errorf("round-trip mismatch:\noriginal: %s\nround-trip: %s",
							tc.address, roundTrip)
					}
				}
			} else {
				if errBtcd == nil && errPool == nil {
					t.Errorf("expected invalid address but both btcd and goPool accepted it")
				}
			}
		})
	}
}

// TestTaprootScriptGeneration verifies that goPool generates correct
// scriptPubKey for taproot addresses and matches btcd's output.
func TestTaprootScriptGeneration(t *testing.T) {
	// Known taproot address with deterministic 32-byte x-only pubkey
	taprootAddr := "bc1p5cyxnuxmeuwuvkwfem96lqzszd02n6xdcjrs20cac6yqjjwudpxqkedrcr"
	params := &chaincfg.MainNetParams

	// Decode with btcd
	addr, err := btcutil.DecodeAddress(taprootAddr, params)
	if err != nil {
		t.Skipf("btcd doesn't support taproot: %v", err)
	}

	// Generate script with btcd
	btcdScript, err := txscript.PayToAddrScript(addr)
	if err != nil {
		t.Fatalf("btcd PayToAddrScript failed: %v", err)
	}

	// Generate script with goPool
	poolScript, err := scriptForAddress(taprootAddr, params)
	if err != nil {
		t.Fatalf("goPool scriptForAddress failed: %v", err)
	}

	// Compare scripts
	if !bytes.Equal(btcdScript, poolScript) {
		t.Errorf("script mismatch:\nbtcd:   %x\ngoPool: %x",
			btcdScript, poolScript)
	}

	// Verify script structure: OP_1 <32-byte-pubkey>
	if len(poolScript) != 34 {
		t.Errorf("expected 34 bytes for taproot script, got %d", len(poolScript))
	}
	if poolScript[0] != 0x51 { // OP_1
		t.Errorf("expected OP_1 (0x51), got 0x%02x", poolScript[0])
	}
	if poolScript[1] != 0x20 { // 32 bytes
		t.Errorf("expected length 0x20 (32), got 0x%02x", poolScript[1])
	}
}

// TestBech32mChecksum verifies that bech32m checksum validation works
// correctly for taproot addresses.
func TestBech32mChecksum(t *testing.T) {
	testCases := []struct {
		name    string
		address string
		valid   bool
	}{
		{
			name:    "valid_taproot",
			address: "bc1p5cyxnuxmeuwuvkwfem96lqzszd02n6xdcjrs20cac6yqjjwudpxqkedrcr",
			valid:   true,
		},
		{
			name:    "invalid_checksum",
			address: "bc1p5cyxnuxmeuwuvkwfem96lqzszd02n6xdcjrs20cac6yqjjwudpxqkedrxx",
			valid:   false,
		},
		{
			name:    "valid_segwit_v0",
			address: "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
			valid:   true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			hrp, data, err := bech32Decode(tc.address)

			if tc.valid {
				if err != nil {
					t.Errorf("expected valid address, got error: %v", err)
				}
				if hrp == "" || len(data) == 0 {
					t.Errorf("expected non-empty hrp and data")
				}
			} else {
				if err == nil {
					t.Errorf("expected invalid address, but it was accepted")
				}
			}
		})
	}
}

// TestWitnessVersionEncoding verifies that different witness versions
// use the correct bech32/bech32m encoding.
func TestWitnessVersionEncoding(t *testing.T) {
	params := &chaincfg.MainNetParams

	testCases := []struct {
		name           string
		witnessVersion byte
		program        []byte
		expectedPrefix string
	}{
		{
			name:           "witness_v0_20bytes",
			witnessVersion: 0,
			program:        make([]byte, 20), // P2WPKH
			expectedPrefix: "bc1q",
		},
		{
			name:           "witness_v0_32bytes",
			witnessVersion: 0,
			program:        make([]byte, 32), // P2WSH
			expectedPrefix: "bc1q",
		},
		{
			name:           "witness_v1_32bytes",
			witnessVersion: 1,
			program:        make([]byte, 32), // Taproot
			expectedPrefix: "bc1p",
		},
		{
			name:           "witness_v2_32bytes",
			witnessVersion: 2,
			program:        make([]byte, 32),
			expectedPrefix: "bc1z",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Build script: OP_<version> <len> <program>
			script := make([]byte, 0, 2+len(tc.program))
			if tc.witnessVersion == 0 {
				script = append(script, 0x00)
			} else {
				script = append(script, 0x50+tc.witnessVersion)
			}
			script = append(script, byte(len(tc.program)))
			script = append(script, tc.program...)

			// Convert to address
			addr := scriptToAddress(script, params)
			if addr == "" {
				t.Errorf("scriptToAddress returned empty string")
				return
			}

			// Verify prefix matches expected
			if len(addr) < len(tc.expectedPrefix) || addr[:len(tc.expectedPrefix)] != tc.expectedPrefix {
				t.Errorf("expected prefix %s, got %s", tc.expectedPrefix, addr[:min(len(tc.expectedPrefix), len(addr))])
			}

			// Verify it decodes successfully
			hrp, data, err := bech32Decode(addr)
			if err != nil {
				t.Errorf("failed to decode generated address: %v", err)
			}
			if hrp != params.Bech32HRPSegwit {
				t.Errorf("expected hrp %s, got %s", params.Bech32HRPSegwit, hrp)
			}
			if len(data) == 0 {
				t.Errorf("expected non-empty data")
			}
		})
	}
}

// TestTaprootAddressRoundTrip verifies that taproot addresses can be
// converted to scripts and back without loss.
func TestTaprootAddressRoundTrip(t *testing.T) {
	addresses := []struct {
		addr   string
		params *chaincfg.Params
	}{
		{
			addr:   "bc1p5cyxnuxmeuwuvkwfem96lqzszd02n6xdcjrs20cac6yqjjwudpxqkedrcr",
			params: &chaincfg.MainNetParams,
		},
		{
			addr:   "bc1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vqzk5jj0",
			params: &chaincfg.MainNetParams,
		},
		{
			addr:   "tb1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vq47zagq",
			params: &chaincfg.TestNet3Params,
		},
	}

	for _, test := range addresses {
		t.Run(test.addr, func(t *testing.T) {
			// Address -> Script
			script, err := scriptForAddress(test.addr, test.params)
			if err != nil {
				t.Fatalf("scriptForAddress failed: %v", err)
			}

			// Script -> Address
			roundTrip := scriptToAddress(script, test.params)
			if roundTrip != test.addr {
				t.Errorf("round-trip mismatch:\noriginal:   %s\nround-trip: %s",
					test.addr, roundTrip)
			}

			// Verify script structure
			if len(script) != 34 {
				t.Errorf("expected 34-byte taproot script, got %d bytes", len(script))
			}
			if script[0] != 0x51 {
				t.Errorf("expected OP_1, got 0x%02x", script[0])
			}
			if script[1] != 0x20 {
				t.Errorf("expected 32-byte push, got 0x%02x", script[1])
			}

			t.Logf("✓ Address: %s", test.addr)
			t.Logf("  Script:  %x", script)
		})
	}
}

// TestBIP350TestVectors uses the official BIP 350 test vectors.
func TestBIP350TestVectors(t *testing.T) {
	// Valid bech32m addresses from BIP 350
	validAddresses := []string{
		"bc1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vqzk5jj0",
		"bc1p5cyxnuxmeuwuvkwfem96lqzszd02n6xdcjrs20cac6yqjjwudpxqkedrcr",
	}

	for _, addr := range validAddresses {
		t.Run(addr, func(t *testing.T) {
			_, data, err := bech32Decode(addr)
			if err != nil {
				t.Errorf("failed to decode valid BIP 350 address: %v", err)
			}
			if len(data) == 0 {
				t.Errorf("expected non-empty data")
			}

			// Verify it's witness version 1
			if data[0] != 1 {
				t.Errorf("expected witness version 1, got %d", data[0])
			}
		})
	}

	// Invalid addresses should be rejected
	invalidAddresses := []string{
		"bc1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vqh2y7hd", // wrong checksum
		"bc1p5cyxnuxmeuwuvkwfem96lqzszd02n6xdcjrs20cac6yqjjwudpxqkedrxx", // wrong checksum
	}

	for _, addr := range invalidAddresses {
		t.Run("invalid_"+addr, func(t *testing.T) {
			_, _, err := bech32Decode(addr)
			if err == nil {
				t.Errorf("expected error for invalid address, but it was accepted")
			}
		})
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
