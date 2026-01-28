package main

import (
	"errors"

	"github.com/btcsuite/btcd/btcutil/bech32"
)

func bech32Decode(s string) (hrp string, data []byte, err error) {
	hrp, data, version, err := bech32.DecodeGeneric(s)
	if err != nil {
		return "", nil, err
	}
	if len(data) == 0 {
		return "", nil, errors.New("empty segwit data")
	}
	witnessVer := data[0]
	if witnessVer == 0 && version != bech32.Version0 {
		return "", nil, errors.New("witness version 0 must use bech32 encoding")
	}
	if witnessVer >= 1 && witnessVer <= 16 && version != bech32.VersionM {
		return "", nil, errors.New("witness version 1+ must use bech32m encoding")
	}
	return hrp, data, nil
}
