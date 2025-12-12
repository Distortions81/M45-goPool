package main

import (
	"sync"

	"github.com/btcsuite/btcd/chaincfg"
)

var (
	chainParamsMu sync.RWMutex
	chainParams   *chaincfg.Params = &chaincfg.MainNetParams
)

// SetChainParams selects the active Bitcoin network parameters used for local
// address validation. It should be called once during startup, after CLI
// flags / config are resolved. Unknown names default to mainnet.
func SetChainParams(network string) {
	chainParamsMu.Lock()
	defer chainParamsMu.Unlock()

	switch network {
	case "mainnet", "", "bitcoin":
		chainParams = &chaincfg.MainNetParams
	case "testnet", "testnet3":
		chainParams = &chaincfg.TestNet3Params
	case "regtest", "regressiontest":
		chainParams = &chaincfg.RegressionNetParams
	default:
		chainParams = &chaincfg.MainNetParams
	}
}

// ChainParams returns the currently selected network parameters. Call
// SetChainParams during startup to ensure this reflects the actual network.
func ChainParams() *chaincfg.Params {
	chainParamsMu.RLock()
	defer chainParamsMu.RUnlock()
	return chainParams
}
