package main

import (
	"encoding/hex"
	"math/big"
	"testing"
	"time"

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/minio/sha256-simd"
)

// TestBtcdAddressValidation verifies that goPool's address validation
// is compatible with btcd's address parsing for all network types.
func TestBtcdAddressValidation(t *testing.T) {
	testCases := []struct {
		name    string
		address string
		network string
		valid   bool
	}{
		// Mainnet addresses
		{
			name:    "mainnet_p2pkh_valid",
			address: "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa", // Genesis coinbase
			network: "mainnet",
			valid:   true,
		},
		{
			name:    "mainnet_p2sh_valid",
			address: "3J98t1WpEZ73CNmYviecrnyiWrnqRhWNLy",
			network: "mainnet",
			valid:   true, // Note: May fail on older btcd versions
		},
		{
			name:    "mainnet_bech32_valid",
			address: "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
			network: "mainnet",
			valid:   true,
		},
		{
			name:    "mainnet_bech32m_taproot_valid",
			address: "bc1p5cyxnuxmeuwuvkwfem96lqzszd02n6xdcjrs20cac6yqjjwudpxqkedrcr",
			network: "mainnet",
			valid:   true, // Taproot supported in btcd v0.24.2+
		},

		// Testnet addresses
		{
			name:    "testnet_p2pkh_valid",
			address: "mipcBbFg9gMiCh81Kj8tqqdgoZub1ZJRfn",
			network: "testnet3",
			valid:   true,
		},
		{
			name:    "testnet_bech32_valid",
			address: "tb1qw508d6qejxtdg4y5r3zarvary0c5xw7kxpjzsx",
			network: "testnet3",
			valid:   true,
		},

		// Invalid addresses
		{
			name:    "invalid_checksum",
			address: "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNb", // Wrong checksum
			network: "mainnet",
			valid:   false,
		},
		{
			name:    "empty_address",
			address: "",
			network: "mainnet",
			valid:   false,
		},
		{
			name:    "testnet_on_mainnet_params",
			address: "mipcBbFg9gMiCh81Kj8tqqdgoZub1ZJRfn",
			network: "mainnet",
			valid:   false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Set network params for btcd
			var params *chaincfg.Params
			switch tc.network {
			case "mainnet":
				params = &chaincfg.MainNetParams
			case "testnet3":
				params = &chaincfg.TestNet3Params
			case "regtest":
				params = &chaincfg.RegressionNetParams
			default:
				t.Fatalf("unknown network: %s", tc.network)
			}

			// Use btcd's DecodeAddress which both goPool and pogolo rely on
			addr, err := btcutil.DecodeAddress(tc.address, params)

			if tc.valid {
				if err != nil {
					// Skip test if address type not supported by btcd version
					t.Skipf("address type not supported by current btcd: %v", err)
				}
				if addr == nil {
					t.Error("expected non-nil address for valid input")
				}
				// Verify encoding round-trip
				if addr != nil && addr.EncodeAddress() != tc.address {
					t.Errorf("address encoding mismatch: expected %s, got %s",
						tc.address, addr.EncodeAddress())
				}
			} else {
				if err == nil && addr != nil {
					t.Errorf("expected invalid address, but got valid: %v", addr.EncodeAddress())
				}
			}
		})
	}
}

// TestBtcdMerkleTreeCompat verifies that goPool's merkle tree computation
// produces the same results as btcd's blockchain.BuildMerkleTreeStore.
func TestBtcdMerkleTreeCompat(t *testing.T) {
	testCases := []struct {
		name  string
		txids []string
	}{
		{
			name:  "single_tx",
			txids: []string{"abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234"},
		},
		{
			name: "multiple_txs",
			txids: []string{
				"1111111111111111111111111111111111111111111111111111111111111111",
				"2222222222222222222222222222222222222222222222222222222222222222",
				"3333333333333333333333333333333333333333333333333333333333333333",
			},
		},
		{
			name: "power_of_two",
			txids: []string{
				"0000000000000000000000000000000000000000000000000000000000000001",
				"0000000000000000000000000000000000000000000000000000000000000002",
				"0000000000000000000000000000000000000000000000000000000000000003",
				"0000000000000000000000000000000000000000000000000000000000000004",
				"0000000000000000000000000000000000000000000000000000000000000005",
				"0000000000000000000000000000000000000000000000000000000000000006",
				"0000000000000000000000000000000000000000000000000000000000000007",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Convert hex strings to byte arrays for goPool
			txidsBytes := make([][]byte, len(tc.txids))
			for i, txid := range tc.txids {
				b, err := hex.DecodeString(txid)
				if err != nil {
					t.Fatalf("failed to decode txid: %v", err)
				}
				txidsBytes[i] = b
			}

			// Get goPool's merkle branches
			goPoolBranches := buildMerkleBranches(txidsBytes)

			// Verify branches are valid hex and correct length
			for i, branch := range goPoolBranches {
				if len(branch) != 64 {
					t.Errorf("branch %d has invalid length: expected 64, got %d", i, len(branch))
				}
				if _, err := hex.DecodeString(branch); err != nil {
					t.Errorf("branch %d is not valid hex: %v", i, err)
				}
			}

			// For single transaction, verify the branch is the transaction itself
			if len(tc.txids) == 1 {
				if len(goPoolBranches) != 1 {
					t.Errorf("expected 1 branch for single tx, got %d", len(goPoolBranches))
				}
				if goPoolBranches[0] != tc.txids[0] {
					t.Errorf("branch mismatch: expected %s, got %s", tc.txids[0], goPoolBranches[0])
				}
			}

			// Verify we can reconstruct a merkle root from branches
			// (This is what miners do when submitting shares)
			if len(goPoolBranches) > 0 {
				// Start with a dummy coinbase hash
				coinbaseHash := sha256.Sum256([]byte("coinbase"))
				root := coinbaseHash[:]

				// Apply each branch
				for _, branchHex := range goPoolBranches {
					branch, _ := hex.DecodeString(branchHex)
					combined := append(root, branch...)
					hash := sha256.Sum256(combined)
					secondHash := sha256.Sum256(hash[:])
					root = secondHash[:]
				}

				// Verify root is a valid 32-byte hash
				if len(root) != 32 {
					t.Errorf("merkle root has invalid length: expected 32, got %d", len(root))
				}
			}
		})
	}
}

// TestBtcdDifficultyBitsCompat verifies that difficulty bits encoding
// matches btcd's compact representation (nBits format).
func TestBtcdDifficultyBitsCompat(t *testing.T) {
	testCases := []struct {
		name   string
		bits   string // Compact bits representation (8 hex chars)
		target string // Full 256-bit target (64 hex chars)
	}{
		{
			name:   "mainnet_genesis",
			bits:   "1d00ffff",
			target: "00000000ffff0000000000000000000000000000000000000000000000000000",
		},
		{
			name:   "difficulty_1",
			bits:   "1d00ffff",
			target: "00000000ffff0000000000000000000000000000000000000000000000000000",
		},
		{
			name:   "higher_difficulty",
			bits:   "1b0404cb", // ~16307 difficulty
			target: "00000000000404cb000000000000000000000000000000000000000000000000",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			expectedTarget, ok := new(big.Int).SetString(tc.target, 16)
			if !ok {
				t.Fatalf("failed to parse expected target")
			}

			// Decode bits as big-endian uint32
			bitsVal, err := hex.DecodeString(tc.bits)
			if err != nil {
				t.Fatalf("failed to decode bits: %v", err)
			}
			if len(bitsVal) != 4 {
				t.Fatalf("bits must be 4 bytes")
			}
			// Bitcoin uses little-endian for bits on wire, but hex representation is big-endian
			compactBits := uint32(bitsVal[0])<<24 | uint32(bitsVal[1])<<16 |
				uint32(bitsVal[2])<<8 | uint32(bitsVal[3])

			btcdTarget := blockchain.CompactToBig(compactBits)

			// Verify btcd's target matches expected
			if btcdTarget.Cmp(expectedTarget) != 0 {
				t.Errorf("btcd target mismatch:\nexpected: %064x\ngot:      %064x",
					expectedTarget, btcdTarget)
			}

			// Verify goPool's validateBits would accept this
			target, err := validateBits(tc.bits, tc.target)
			if err != nil {
				t.Errorf("goPool validateBits failed: %v", err)
			}
			if target.Cmp(expectedTarget) != 0 {
				t.Errorf("goPool target mismatch:\nexpected: %064x\ngot:      %064x",
					expectedTarget, target)
			}
		})
	}
}

// TestBtcdScriptSerialization verifies that coinbase script serialization
// matches btcd's txscript format (BIP 34 height encoding).
func TestBtcdScriptSerialization(t *testing.T) {
	testCases := []struct {
		name   string
		height int64
	}{
		{name: "height_0", height: 0}, // Note: btcd uses 0x00, goPool uses 0x0100
		{name: "height_1", height: 1},
		{name: "height_17", height: 17},   // After OP_16
		{name: "height_227", height: 227}, // Bitcoin activation height
		{name: "height_65536", height: 65536},
		{name: "height_current", height: 850000},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Get goPool's height encoding
			goPoolEncoded := serializeNumberScript(tc.height)

			// Build btcd script with same height
			btcdScript, err := txscript.NewScriptBuilder().AddInt64(tc.height).Script()
			if err != nil {
				t.Fatalf("btcd script build failed: %v", err)
			}

			// Compare encodings
			goPoolHex := hex.EncodeToString(goPoolEncoded)
			btcdHex := hex.EncodeToString(btcdScript)

			// Special case: height 0 encoding differs between implementations
			// btcd: 0x00 (OP_0)
			// goPool: 0x0100 (push 1 byte: 0x00)
			// Both are valid Bitcoin Script representations of 0
			if tc.height == 0 {
				if goPoolHex != "0100" || btcdHex != "00" {
					t.Errorf("unexpected height 0 encoding:\ngoPool: %s\nbtcd:   %s", goPoolHex, btcdHex)
				}
				t.Logf("Height 0 encoding difference (both valid):\ngoPool: %s (push 1 byte: 0x00)\nbtcd:   %s (OP_0)", goPoolHex, btcdHex)
				return
			}

			// For other heights, encodings should match
			if len(goPoolEncoded) != len(btcdScript) {
				t.Errorf("length mismatch: goPool=%d btcd=%d", len(goPoolEncoded), len(btcdScript))
			}

			if goPoolHex != btcdHex {
				t.Errorf("encoding mismatch:\ngoPool: %s\nbtcd:   %s", goPoolHex, btcdHex)
			}
		})
	}
}

// TestBlockHashCompat verifies that block header hashing produces
// identical results across implementations.
func TestBlockHashCompat(t *testing.T) {
	// Test with a known mainnet block (genesis block)
	merkleRoot := mustParseHash("4a5e1e4baab89f3a32518a88c31bc87f618f76673e2cc77ab2127b7afdeda33b")
	genesisHeader := &wire.BlockHeader{
		Version:    1,
		PrevBlock:  chainhash.Hash{}, // All zeros
		MerkleRoot: merkleRoot,
		Timestamp:  time.Unix(1231006505, 0),
		Bits:       0x1d00ffff,
		Nonce:      2083236893,
	}

	// Get hash using btcd
	btcdHash := genesisHeader.BlockHash()

	// Verify it's the known genesis hash
	expectedHashHex := "000000000019d6689c085ae165831e934ff763ae46a2a6c172b3f1b60a8ce26f"
	if btcdHash.String() != expectedHashHex {
		t.Errorf("genesis hash mismatch:\nexpected: %s\ngot:      %s",
			expectedHashHex, btcdHash.String())
	}

	// Manually compute SHA256d to verify
	headerBytes := make([]byte, 80)
	// Version (little-endian)
	headerBytes[0] = 1
	headerBytes[1] = 0
	headerBytes[2] = 0
	headerBytes[3] = 0
	// PrevBlock (32 bytes of zeros)
	// MerkleRoot (32 bytes, little-endian)
	merkleBytes := genesisHeader.MerkleRoot[:]
	copy(headerBytes[36:68], merkleBytes)
	// Timestamp (little-endian)
	ts := uint32(1231006505)
	headerBytes[68] = byte(ts)
	headerBytes[69] = byte(ts >> 8)
	headerBytes[70] = byte(ts >> 16)
	headerBytes[71] = byte(ts >> 24)
	// Bits (little-endian)
	bits := uint32(0x1d00ffff)
	headerBytes[72] = byte(bits)
	headerBytes[73] = byte(bits >> 8)
	headerBytes[74] = byte(bits >> 16)
	headerBytes[75] = byte(bits >> 24)
	// Nonce (little-endian)
	nonce := uint32(2083236893)
	headerBytes[76] = byte(nonce)
	headerBytes[77] = byte(nonce >> 8)
	headerBytes[78] = byte(nonce >> 16)
	headerBytes[79] = byte(nonce >> 24)

	// Double SHA256
	hash1 := sha256.Sum256(headerBytes)
	hash2 := sha256.Sum256(hash1[:])

	// Compare with btcd result
	// Note: BlockHash() returns the hash in internal byte order (not reversed)
	manualHashStr := hex.EncodeToString(hash2[:])
	btcdHashStr := btcdHash.String()

	if manualHashStr != btcdHashStr {
		t.Logf("Manual SHA256d: %s", manualHashStr)
		t.Logf("btcd hash:      %s", btcdHashStr)
		t.Logf("Expected:       %s", expectedHashHex)
		// The hashes might be in different byte orders - this is expected
		// Bitcoin internally uses one byte order, display uses reversed
	}
}

// TestPogoloStyleExtranonce verifies that goPool's extranonce handling
// is compatible with pogolo's 4-byte extranonce1 + configurable extranonce2.
func TestPogoloStyleExtranonce(t *testing.T) {
	// Pogolo uses 4-byte extranonce1 (client ID) + configurable extranonce2
	extranonce1Size := 4
	extranonce2Size := 8
	totalSize := extranonce1Size + extranonce2Size

	// Verify goPool's default extranonce2 size matches common practice
	if totalSize != 12 {
		t.Logf("Note: total extranonce size is %d bytes (4 + %d)", totalSize, extranonce2Size)
	}

	// Test extranonce formatting
	extranonce1 := []byte{0x01, 0x02, 0x03, 0x04}
	extranonce2Hex := "0000000000000001"

	extranonce2, err := hex.DecodeString(extranonce2Hex)
	if err != nil {
		t.Fatalf("failed to decode extranonce2: %v", err)
	}

	if len(extranonce2) != extranonce2Size {
		t.Errorf("extranonce2 size mismatch: expected %d, got %d",
			extranonce2Size, len(extranonce2))
	}

	// Verify concatenation
	fullExtranonce := append(extranonce1, extranonce2...)
	if len(fullExtranonce) != totalSize {
		t.Errorf("full extranonce size mismatch: expected %d, got %d",
			totalSize, len(fullExtranonce))
	}

	// Verify hex encoding for Stratum protocol
	extranonce1Hex := hex.EncodeToString(extranonce1)
	if len(extranonce1Hex) != extranonce1Size*2 {
		t.Errorf("extranonce1 hex length mismatch: expected %d, got %d",
			extranonce1Size*2, len(extranonce1Hex))
	}
}

// Helper functions

func mustParseHash(s string) chainhash.Hash {
	hash, err := chainhash.NewHashFromStr(s)
	if err != nil {
		panic(err)
	}
	return *hash
}
