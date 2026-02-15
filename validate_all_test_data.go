//go:build ignore

// Validates ALL test addresses used in the test suite against BTCD
// Run with: go run validate_all_test_data.go

package main

import (
	"fmt"
	"os"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
)

func main() {
	params := &chaincfg.MainNetParams

	// All addresses used in test files
	testAddresses := []struct {
		file    string
		address string
		valid   bool // Expected validity
	}{
		// From cross_impl_sanity_test.go
		{"cross_impl_sanity_test.go", "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa", true},
		{"cross_impl_sanity_test.go", "3Ai1JZ8pdJb2ksieUV8FsxSNVJCpoPi8W6", true},
		{"cross_impl_sanity_test.go", "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4", true},
		{"cross_impl_sanity_test.go", "bc1p5cyxnuxmeuwuvkwfem96lqzszd02n6xdcjrs20cac6yqjjwudpxqkedrcr", true},
		{"cross_impl_sanity_test.go", "1HLoD9E4SDFFPDiYfNYnkBLQ85Y51J3Zb1", true},
		{"cross_impl_sanity_test.go", "3EktnHQD7RiAE6uzMj2ZifT9YgRrkSgzQX", true},
		{"cross_impl_sanity_test.go", "bc1qrp33g0q5c5txsp9arysrx4k6zdkfs4nce4xj0gdcccefvpysxf3qccfmv3", true},

		// BTCD-generated taproot addresses
		{"cross_impl_sanity_test.go", "bc1ps597k6l9nflzcewxl7gmmduem5qfg7s0e4zcrf5vdj23gdf7clps9a9z2y", true},
		{"cross_impl_sanity_test.go", "bc1prnymkn0fg6lnf5m63flsfcynhj8uyqe32htrlfp0nlhfgdf3vyyq25t76j", true},
		{"cross_impl_sanity_test.go", "bc1pm875mewx3zl40hy68kmttdu4nr4r0z8e8u8re3tzpqhfya30srrqwz53zs", true},
		{"cross_impl_sanity_test.go", "bc1pj4my707xemu3avysg2tmp06eje56spc22cj6fx0yvknqh7pjvt2q649ctp", true},
		{"cross_impl_sanity_test.go", "bc1ppnmuhyp7atfquynclw96w64nzwlp20ga0akd7ard75286d40ed4qx37wal", true},
		{"cross_impl_sanity_test.go", "1Fsppk7v3icgwunnShNREk1DGYAKTs3yz5", true},
		{"cross_impl_sanity_test.go", "1tUgjBV7M1Lw8xSUgYjMcGwJpuJMaMocR", true},
		{"cross_impl_sanity_test.go", "bc1qqd4ggl3csy6fxxfqllhukdns3h5nheh4qd5wrq", true},

		// From coinbase_wallet_types_test.go
		{"coinbase_wallet_types_test.go", "bc1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vqzk5jj0", true},

		// From taproot_test.go
		{"taproot_test.go", "bc1pqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqsyjer9e", true},

		// From address_roundtrip_test.go
		{"address_roundtrip_test.go", "bc1p5cyxnuxmeuwuvkwfem96lqzszd02n6xdcjrs20cac6yqjjwudpxqkedrcr", true},
		{"address_roundtrip_test.go", "bc1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vqzk5jj0", true},

		// Invalid addresses that should fail
		{"cross_impl_sanity_test.go", "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNb", false}, // Wrong checksum
		{"cross_impl_sanity_test.go", "", false},                                   // Empty

		// Testnet addresses
		{"cross_impl_sanity_test.go [testnet]", "mipcBbFg9gMiCh81Kj8tqqdgoZub1ZJRfn", true},
		{"cross_impl_sanity_test.go [testnet]", "tb1qw508d6qejxtdg4y5r3zarvary0c5xw7kxpjzsx", true},
	}

	fmt.Println("# Validating All Test Addresses Against BTCD")
	fmt.Println()

	failures := 0
	successes := 0

	for _, tc := range testAddresses {
		// Determine network params
		testParams := params
		if tc.file == "cross_impl_sanity_test.go [testnet]" {
			testParams = &chaincfg.TestNet3Params
		}

		addr, err := btcutil.DecodeAddress(tc.address, testParams)

		if tc.valid {
			if err != nil {
				fmt.Printf("❌ FAIL: %s\n", tc.file)
				fmt.Printf("   Address: %s\n", tc.address)
				fmt.Printf("   Expected: VALID\n")
				fmt.Printf("   Got: INVALID - %v\n", err)
				fmt.Println()
				failures++
			} else {
				// Verify round-trip
				roundTrip := addr.EncodeAddress()
				if roundTrip != tc.address {
					fmt.Printf("⚠️  WARN: %s\n", tc.file)
					fmt.Printf("   Address: %s\n", tc.address)
					fmt.Printf("   Round-trip mismatch: %s\n", roundTrip)
					fmt.Println()
					failures++
				} else {
					fmt.Printf("✅ PASS: %s - %s\n", tc.file, tc.address)
					successes++
				}
			}
		} else {
			if err == nil {
				fmt.Printf("❌ FAIL: %s\n", tc.file)
				fmt.Printf("   Address: %s\n", tc.address)
				fmt.Printf("   Expected: INVALID\n")
				fmt.Printf("   Got: VALID\n")
				fmt.Println()
				failures++
			} else {
				fmt.Printf("✅ PASS: %s - %s (correctly rejected)\n", tc.file, tc.address)
				successes++
			}
		}
	}

	fmt.Println()
	fmt.Printf("Results: %d passed, %d failed\n", successes, failures)

	if failures > 0 {
		fmt.Println()
		fmt.Println("⚠️  VALIDATION FAILED - Some test addresses are invalid!")
		fmt.Println("   Please regenerate test addresses using: go run generate_test_addresses.go")
		os.Exit(1)
	}

	fmt.Println()
	fmt.Println("✅ All test addresses validated successfully!")
}
