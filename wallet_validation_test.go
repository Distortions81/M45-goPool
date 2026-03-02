package main

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/txscript"
)

func deterministicPKH() []byte {
	pkh := make([]byte, 20)
	for i := range pkh {
		pkh[i] = byte(i + 1)
	}
	return pkh
}

func deterministicTaprootScript() []byte {
	prog := make([]byte, 32)
	for i := range prog {
		prog[i] = byte(0x40 + i)
	}
	return append([]byte{0x51, 0x20}, prog...)
}

func nestedSegwitAddress(t *testing.T, params *chaincfg.Params, pkh []byte) string {
	t.Helper()
	redeemScript := append([]byte{0x00, 0x14}, pkh...)
	redeemHash := btcutil.Hash160(redeemScript)
	addr, err := btcutil.NewAddressScriptHashFromHash(redeemHash, params)
	if err != nil {
		t.Fatalf("NewAddressScriptHashFromHash(nested): %v", err)
	}
	return addr.EncodeAddress()
}

func newDummyAccountStore(t *testing.T) *AccountStore {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "state", "workers.db")
	db, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("openStateDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	cleanup := setSharedStateDBForTest(db)
	t.Cleanup(cleanup)

	cfg := Config{DataDir: dataDir}
	store, err := NewAccountStore(cfg, false, true)
	if err != nil {
		t.Fatalf("NewAccountStore failed: %v", err)
	}
	return store
}

// TestValidateWorkerWallet_AgreesWithBtcdDecodeAddress checks that
// validateWorkerWallet accepts/rejects the same wallet-style worker strings
// that btcsuite's DecodeAddress does for each supported network.
func TestValidateWorkerWallet_AgreesWithBtcdDecodeAddress(t *testing.T) {
	type testCase struct {
		name    string
		network string
		address string
	}

	var tests []testCase
	for _, netName := range []string{"mainnet", "testnet3", "regtest"} {
		SetChainParams(netName)
		params := ChainParams()
		pkh := deterministicPKH()

		addrPKH, err := btcutil.NewAddressPubKeyHash(pkh, params)
		if err != nil {
			t.Fatalf("NewAddressPubKeyHash(%s): %v", netName, err)
		}
		addrWPKH, err := btcutil.NewAddressWitnessPubKeyHash(pkh, params)
		if err != nil {
			t.Fatalf("NewAddressWitnessPubKeyHash(%s): %v", netName, err)
		}
		taprootAddr := scriptToAddress(deterministicTaprootScript(), params)
		if taprootAddr == "" {
			t.Fatalf("scriptToAddress(taproot, %s) returned empty", netName)
		}

		tests = append(tests,
			testCase{name: netName + " P2PKH valid", network: netName, address: addrPKH.EncodeAddress()},
			testCase{name: netName + " P2SH-P2WPKH nested valid", network: netName, address: nestedSegwitAddress(t, params, pkh)},
			testCase{name: netName + " P2WPKH valid", network: netName, address: addrWPKH.EncodeAddress()},
			testCase{name: netName + " P2TR valid", network: netName, address: taprootAddr},
		)
	}
	tests = append(tests,
		testCase{name: "mainnet invalid checksum", network: "mainnet", address: "1BitcoinEaterAddressDontSendf59kuX"},
		testCase{name: "testnet invalid checksum", network: "testnet3", address: "mkUNMewkQsHKpZMBp7cYjKwdiZxrT9yQVx"},
	)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			SetChainParams(tt.network)

			params := ChainParams()
			_, errBtcd := btcutil.DecodeAddress(tt.address, params)

			store := newDummyAccountStore(t)
			okPool := validateWorkerWallet(nil, store, tt.address)

			if (errBtcd == nil) != okPool {
				t.Fatalf("mismatch: DecodeAddress err=%v, validateWorkerWallet=%v", errBtcd, okPool)
			}
		})
	}
}

// TestScriptForAddress_MatchesBtcdPayToAddrScript ensures that scriptForAddress
// produces the same scriptPubKey bytes as btcd's txscript.PayToAddrScript for
// a variety of address types and networks.
func TestScriptForAddress_MatchesBtcdPayToAddrScript(t *testing.T) {
	nets := []struct {
		name   string
		params *chaincfg.Params
	}{
		{"mainnet", &chaincfg.MainNetParams},
		{"testnet3", &chaincfg.TestNet3Params},
		{"regtest", &chaincfg.RegressionNetParams},
	}

	for _, net := range nets {
		t.Run(net.name, func(t *testing.T) {
			pkh := deterministicPKH()

			// P2PKH
			addrPKH, err := btcutil.NewAddressPubKeyHash(pkh, net.params)
			if err != nil {
				t.Fatalf("NewAddressPubKeyHash: %v", err)
			}
			// Nested SegWit P2SH-P2WPKH
			redeemScript := append([]byte{0x00, 0x14}, pkh...)
			redeemHash := btcutil.Hash160(redeemScript)
			addrNested, err := btcutil.NewAddressScriptHashFromHash(redeemHash, net.params)
			if err != nil {
				t.Fatalf("NewAddressScriptHashFromHash: %v", err)
			}
			// P2WPKH
			addrWPKH, err := btcutil.NewAddressWitnessPubKeyHash(pkh, net.params)
			if err != nil {
				t.Fatalf("NewAddressWitnessPubKeyHash: %v", err)
			}
			// P2TR
			taprootAddrStr := scriptToAddress(deterministicTaprootScript(), net.params)
			if taprootAddrStr == "" {
				t.Fatalf("scriptToAddress(taproot) returned empty")
			}
			addrTR, err := btcutil.DecodeAddress(taprootAddrStr, net.params)
			if err != nil {
				t.Fatalf("DecodeAddress(taproot): %v", err)
			}

			addrs := []btcutil.Address{addrPKH, addrNested, addrWPKH, addrTR}
			for _, a := range addrs {
				addrStr := a.EncodeAddress()
				btcdScript, err := txscript.PayToAddrScript(a)
				if err != nil {
					t.Fatalf("PayToAddrScript(%T) error: %v", a, err)
				}
				poolScript, err := scriptForAddress(addrStr, net.params)
				if err != nil {
					t.Fatalf("scriptForAddress(%s) error: %v", addrStr, err)
				}
				if len(btcdScript) == 0 || len(poolScript) == 0 {
					t.Fatalf("empty script for %s", addrStr)
				}
				if !bytes.Equal(btcdScript, poolScript) {
					t.Fatalf("script mismatch for %s (type %T): btcd=%x pool=%x", addrStr, a, btcdScript, poolScript)
				}
			}
		})
	}
}
