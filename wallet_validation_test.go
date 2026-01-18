package main

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/txscript"
)

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
	tests := []struct {
		name       string
		network    string
		address    string
		shouldSkip bool
	}{
		// Mainnet examples.
		{"mainnet P2PKH valid", "mainnet", "1BitcoinEaterAddressDontSendf59kuE", false},
		{"mainnet invalid checksum", "mainnet", "1BitcoinEaterAddressDontSendf59kuX", false},

		// Testnet examples.
		{"testnet P2PKH valid", "testnet3", "mkUNMewkQsHKpZMBp7cYjKwdiZxrT9yQVr", false},
		{"testnet invalid checksum", "testnet3", "mkUNMewkQsHKpZMBp7cYjKwdiZxrT9yQVx", false},

		// Regtest uses the same address format as mainnet/testnet for the
		// underlying network (params are the same as testnet3), but we only
		// care that DecodeAddress + validateWorkerWallet agree.
		{"regtest P2PKH valid", "regtest", "mkUNMewkQsHKpZMBp7cYjKwdiZxrT9yQVr", false},
	}

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
			// Use deterministic 20-byte hashes for PKH/SH.
			pkh := make([]byte, 20)
			sh := make([]byte, 20)
			sh32 := make([]byte, 32)
			for i := 0; i < 20; i++ {
				pkh[i] = byte(i + 1)
				sh[i] = byte(0x80 + i)
				if i < 32 {
					sh32[i] = byte(0x40 + i)
				}
			}

			// P2PKH
			addrPKH, err := btcutil.NewAddressPubKeyHash(pkh, net.params)
			if err != nil {
				t.Fatalf("NewAddressPubKeyHash: %v", err)
			}
			// P2SH
			addrSH, err := btcutil.NewAddressScriptHashFromHash(sh, net.params)
			if err != nil {
				t.Fatalf("NewAddressScriptHashFromHash: %v", err)
			}
			// P2WPKH
			addrWPKH, err := btcutil.NewAddressWitnessPubKeyHash(pkh, net.params)
			if err != nil {
				t.Fatalf("NewAddressWitnessPubKeyHash: %v", err)
			}
			// P2WSH
			addrWSH, err := btcutil.NewAddressWitnessScriptHash(sh32, net.params)
			if err != nil {
				t.Fatalf("NewAddressWitnessScriptHash: %v", err)
			}

			addrs := []btcutil.Address{addrPKH, addrSH, addrWPKH, addrWSH}
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
