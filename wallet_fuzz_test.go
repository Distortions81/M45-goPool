package main

import (
	"bytes"
	"crypto/sha256"
	"strconv"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
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

func fuzzDigest(seed []byte, tagA, tagB byte) [32]byte {
	h := sha256.New()
	_, _ = h.Write(seed)
	_, _ = h.Write([]byte{tagA, tagB})
	sum := h.Sum(nil)
	var out [32]byte
	copy(out[:], sum[:32])
	return out
}

func fuzzGeneratedAddress(t *testing.T, params *chaincfg.Params, seed []byte, idx int, typ int) string {
	t.Helper()

	keyMaterial := fuzzDigest(seed, byte(idx), 0x01)
	priv, _ := btcec.PrivKeyFromBytes(keyMaterial[:])
	pkh := btcutil.Hash160(priv.PubKey().SerializeCompressed())

	switch typ {
	case 0:
		addr, err := btcutil.NewAddressPubKeyHash(pkh, params)
		if err != nil {
			t.Fatalf("NewAddressPubKeyHash: %v", err)
		}
		return addr.EncodeAddress()
	case 1:
		redeemScript := append([]byte{0x00, 0x14}, pkh...)
		redeemHash := btcutil.Hash160(redeemScript)
		addr, err := btcutil.NewAddressScriptHashFromHash(redeemHash, params)
		if err != nil {
			t.Fatalf("NewAddressScriptHashFromHash: %v", err)
		}
		return addr.EncodeAddress()
	case 2:
		addr, err := btcutil.NewAddressWitnessPubKeyHash(pkh, params)
		if err != nil {
			t.Fatalf("NewAddressWitnessPubKeyHash: %v", err)
		}
		return addr.EncodeAddress()
	case 3:
		witnessScript, err := txscript.NewScriptBuilder().
			AddData(priv.PubKey().SerializeCompressed()).
			AddOp(txscript.OP_CHECKSIG).
			Script()
		if err != nil {
			t.Fatalf("build witness script: %v", err)
		}
		hash := sha256.Sum256(witnessScript)
		addr, err := btcutil.NewAddressWitnessScriptHash(hash[:], params)
		if err != nil {
			t.Fatalf("NewAddressWitnessScriptHash: %v", err)
		}
		return addr.EncodeAddress()
	default:
		outputKey := txscript.ComputeTaprootKeyNoScript(priv.PubKey())
		addr, err := btcutil.NewAddressTaproot(schnorr.SerializePubKey(outputKey), params)
		if err != nil {
			t.Fatalf("NewAddressTaproot: %v", err)
		}
		return addr.EncodeAddress()
	}
}

func mutateAddressForFuzz(addr string, mode int, seed byte) string {
	if addr == "" {
		return addr
	}
	b := []byte(addr)

	findAlpha := func() int {
		start := int(seed) % len(b)
		for off := 0; off < len(b); off++ {
			i := (start + off) % len(b)
			c := b[i]
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
				return i
			}
		}
		return -1
	}

	findNotSep := func() int {
		start := int(seed) % len(b)
		for off := 0; off < len(b); off++ {
			i := (start + off) % len(b)
			if b[i] != '1' {
				return i
			}
		}
		return int(seed) % len(b)
	}

	switch mode {
	case 0:
		return strings.ToUpper(addr)
	case 1:
		return strings.ToLower(addr)
	case 2:
		i := findAlpha()
		if i < 0 {
			return addr
		}
		c := b[i]
		if c >= 'a' && c <= 'z' {
			b[i] = c - ('a' - 'A')
		} else {
			b[i] = c + ('a' - 'A')
		}
		return string(b)
	case 3:
		i := findNotSep()
		// Deliberate typo substitute that is still alphanumeric.
		switch b[i] {
		case '0':
			b[i] = '1'
		case 'O':
			b[i] = 'Q'
		case 'I':
			b[i] = 'J'
		case 'l':
			b[i] = 'm'
		case 'q':
			b[i] = 'p'
		default:
			b[i] = 'x'
		}
		return string(b)
	case 4:
		i := int(seed) % len(b)
		return string(b[:i]) + "0" + string(b[i:])
	default:
		if len(b) <= 6 {
			return addr
		}
		i := int(seed) % len(b)
		return string(b[:i]) + string(b[i+1:])
	}
}

// FuzzGeneratedWalletAddressBatch stress-tests parsing, script generation,
// and formatting logic using batches of btcd-generated valid wallet addresses.
func FuzzGeneratedWalletAddressBatch(f *testing.F) {
	f.Add([]byte{0x00})
	f.Add([]byte{0x01, 0x7f, 0x02})
	f.Add([]byte("goPool-fuzz-generated-wallets"))

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

		batchSize := 8 + int(seed[len(seed)-1]%16) // 8-23 addresses per input.
		for i := 0; i < batchSize; i++ {
			typeByte := fuzzDigest(seed, byte(i), 0x02)
			addrStr := fuzzGeneratedAddress(t, params, seed, i, int(typeByte[0]%5))

			decoded, err := btcutil.DecodeAddress(addrStr, params)
			if err != nil {
				t.Fatalf("DecodeAddress(%s): %v", addrStr, err)
			}
			btcdScript, err := txscript.PayToAddrScript(decoded)
			if err != nil {
				t.Fatalf("PayToAddrScript(%s): %v", addrStr, err)
			}
			poolScript, err := scriptForAddress(addrStr, params)
			if err != nil {
				t.Fatalf("scriptForAddress(%s): %v", addrStr, err)
			}
			if !bytes.Equal(btcdScript, poolScript) {
				t.Fatalf("script mismatch for %s: btcd=%x pool=%x", addrStr, btcdScript, poolScript)
			}

			roundTrip := scriptToAddress(poolScript, params)
			if roundTrip != addrStr {
				t.Fatalf("round-trip mismatch: in=%s out=%s", addrStr, roundTrip)
			}

			worker := " \t" + addrStr + ".worker-" + strconv.Itoa(i) + "\n"
			base := workerBaseAddress(worker)
			if base != addrStr {
				t.Fatalf("workerBaseAddress mismatch: in=%q base=%q want=%q", worker, base, addrStr)
			}
		}
	})
}

// FuzzGeneratedWalletMutationParity mutates btcd-generated valid addresses with
// typo/case mistakes and requires parser behavior to stay in lock-step with btcd.
func FuzzGeneratedWalletMutationParity(f *testing.F) {
	f.Add([]byte{0x00})
	f.Add([]byte{0x02, 0x8a, 0x10, 0x44})
	f.Add([]byte("goPool-fuzz-wallet-mutations"))

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

		batchSize := 6 + int(seed[len(seed)-1]%12) // 6-17 addresses per input.
		for i := 0; i < batchSize; i++ {
			typeByte := fuzzDigest(seed, byte(i), 0x12)
			addrStr := fuzzGeneratedAddress(t, params, seed, i, int(typeByte[0]%5))

			for m := 0; m < 3; m++ {
				mutation := fuzzDigest(seed, byte(i), byte(0x20+m))
				mutated := mutateAddressForFuzz(addrStr, int(mutation[0]%6), mutation[1])

				btcdAddr, errBtcd := btcutil.DecodeAddress(mutated, params)
				poolScript, errPool := scriptForAddress(mutated, params)
				if (errBtcd == nil) != (errPool == nil) {
					t.Fatalf("decode disagreement for %q: btcdErr=%v poolErr=%v", mutated, errBtcd, errPool)
				}
				if errBtcd != nil {
					continue
				}

				btcdScript, err := txscript.PayToAddrScript(btcdAddr)
				if err != nil {
					t.Fatalf("PayToAddrScript(%q): %v", mutated, err)
				}
				if !bytes.Equal(btcdScript, poolScript) {
					t.Fatalf("script mismatch for %q: btcd=%x pool=%x", mutated, btcdScript, poolScript)
				}

				roundTrip := scriptToAddress(poolScript, params)
				roundScript, err := scriptForAddress(roundTrip, params)
				if err != nil {
					t.Fatalf("round-trip parse failed for %q -> %q: %v", mutated, roundTrip, err)
				}
				if !bytes.Equal(roundScript, poolScript) {
					t.Fatalf("round-trip script mismatch for %q -> %q", mutated, roundTrip)
				}
			}
		}
	})
}
