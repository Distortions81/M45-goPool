package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/txscript"
)

// FuzzScriptForAddress exercises scriptForAddress across randomly generated
// addresses and compares its output to btcsuite's PayToAddrScript. It helps
// catch edge cases in our local address/script handling.
func FuzzScriptForAddress(f *testing.F) {
	// Seed with a few known-good addresses across networks.
	seeds := []struct {
		net  *chaincfg.Params
		addr string
	}{
		{&chaincfg.MainNetParams, "1BitcoinEaterAddressDontSendf59kuE"},
		{&chaincfg.TestNet3Params, "mkUNMewkQsHKpZMBp7cYjKwdiZxrT9yQVr"},
	}
	for i, s := range seeds {
		f.Add(i, s.addr)
	}

	f.Fuzz(func(t *testing.T, netIdx int, addrStr string) {
		// Choose a network based on netIdx.
		var params *chaincfg.Params
		switch netIdx % 3 {
		case 0:
			params = &chaincfg.MainNetParams
		case 1:
			params = &chaincfg.TestNet3Params
		default:
			params = &chaincfg.RegressionNetParams
		}

		addr, err := btcutil.DecodeAddress(addrStr, params)
		if err != nil {
			// For invalid addresses, scriptForAddress should also reject or
			// at least not panic. We only assert that it does not panic.
			_, _ = scriptForAddress(addrStr, params)
			return
		}
		// For valid addresses, scriptForAddress must agree with btcd.
		btcdScript, _ := txscript.PayToAddrScript(addr)
		poolScript, err := scriptForAddress(addrStr, params)
		if err != nil {
			t.Fatalf("scriptForAddress(%s) error: %v", addrStr, err)
		}
		if !bytes.Equal(btcdScript, poolScript) {
			t.Fatalf("script mismatch for %s: btcd=%x pool=%x", addrStr, btcdScript, poolScript)
		}
	})
}

// FuzzValidateWorkerWallet feeds random worker names through
// validateWorkerWallet and checks that, when the base part decodes as a valid
// address for the current network, our helper agrees with btcutil.DecodeAddress.
func FuzzValidateWorkerWallet(f *testing.F) {
	f.Add("1BitcoinEaterAddressDontSendf59kuE")
	f.Add("1BitcoinEaterAddressDontSendf59kuE.worker1")
	f.Add("not-an-address")

	f.Fuzz(func(t *testing.T, worker string) {
		// Use mainnet parameters here; the goal is consistency with
		// btcutil.DecodeAddress under the same network.
		SetChainParams("mainnet")
		params := ChainParams()

		// Extract the base address from the worker name using the same
		// rules as validateWorkerWallet (trim, split on '.', then sanitize).
		raw := strings.TrimSpace(worker)
		if parts := strings.SplitN(raw, ".", 2); len(parts) > 1 {
			raw = parts[0]
		}
		raw = sanitizePayoutAddress(raw)
		if raw == "" {
			_ = validateWorkerWallet(nil, newDummyAccountStore(t), worker)
			return
		}
		addr, err := btcutil.DecodeAddress(raw, params)
		store := newDummyAccountStore(t)
		okPool := validateWorkerWallet(nil, store, worker)

		if err != nil {
			if okPool {
				t.Fatalf("validateWorkerWallet accepted invalid address %q: %v", worker, err)
			}
			return
		}
		if !okPool {
			t.Fatalf("validateWorkerWallet rejected valid address %q (%T)", worker, addr)
		}
	})
}
