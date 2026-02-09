package main

import (
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil"
)

func generateTestWallet(t *testing.T) (address string, script []byte) {
	t.Helper()
	priv, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatalf("generate private key: %v", err)
	}
	pkh := btcutil.Hash160(priv.PubKey().SerializeCompressed())
	addr, err := btcutil.NewAddressWitnessPubKeyHash(pkh, ChainParams())
	if err != nil {
		t.Fatalf("generate witness address: %v", err)
	}
	addrStr := addr.EncodeAddress()
	pkScript, err := scriptForAddress(addrStr, ChainParams())
	if err != nil {
		t.Fatalf("scriptForAddress(%s): %v", addrStr, err)
	}
	return addrStr, pkScript
}

func generateTestWorker(t *testing.T) (workerName string, walletAddr string, walletScript []byte) {
	t.Helper()
	walletAddr, walletScript = generateTestWallet(t)
	return walletAddr + ".worker", walletAddr, walletScript
}

func generateBenchmarkWorker() (workerName string, walletAddr string, walletScript []byte) {
	priv, err := btcec.NewPrivateKey()
	if err != nil {
		panic(err)
	}
	pkh := btcutil.Hash160(priv.PubKey().SerializeCompressed())
	addr, err := btcutil.NewAddressWitnessPubKeyHash(pkh, ChainParams())
	if err != nil {
		panic(err)
	}
	walletAddr = addr.EncodeAddress()
	walletScript, err = scriptForAddress(walletAddr, ChainParams())
	if err != nil {
		panic(err)
	}
	return walletAddr + ".worker", walletAddr, walletScript
}
