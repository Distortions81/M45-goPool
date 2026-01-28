package main

import (
	"encoding/hex"
	"fmt"
)

func serializeDualCoinbaseTx(height int64, extranonce1, extranonce2 []byte, templateExtraNonce2Size int, poolScript []byte, workerScript []byte, totalValue int64, feePercent float64, witnessCommitment string, coinbaseFlags string, coinbaseMsg string, scriptTime int64) ([]byte, []byte, error) {
	var flagsBytes []byte
	if coinbaseFlags != "" {
		b, err := hex.DecodeString(coinbaseFlags)
		if err != nil {
			return nil, nil, fmt.Errorf("decode coinbase flags: %w", err)
		}
		flagsBytes = b
	}
	var commitmentScript []byte
	if witnessCommitment != "" {
		b, err := hex.DecodeString(witnessCommitment)
		if err != nil {
			return nil, nil, fmt.Errorf("decode witness commitment: %w", err)
		}
		commitmentScript = b
	}
	return serializeDualCoinbaseTxPredecoded(height, extranonce1, extranonce2, templateExtraNonce2Size, poolScript, workerScript, totalValue, feePercent, commitmentScript, flagsBytes, coinbaseMsg, scriptTime)
}

func serializeTripleCoinbaseTx(height int64, extranonce1, extranonce2 []byte, templateExtraNonce2Size int, poolScript []byte, donationScript []byte, workerScript []byte, totalValue int64, poolFeePercent float64, donationFeePercent float64, witnessCommitment string, coinbaseFlags string, coinbaseMsg string, scriptTime int64) ([]byte, []byte, error) {
	var flagsBytes []byte
	if coinbaseFlags != "" {
		b, err := hex.DecodeString(coinbaseFlags)
		if err != nil {
			return nil, nil, fmt.Errorf("decode coinbase flags: %w", err)
		}
		flagsBytes = b
	}
	var commitmentScript []byte
	if witnessCommitment != "" {
		b, err := hex.DecodeString(witnessCommitment)
		if err != nil {
			return nil, nil, fmt.Errorf("decode witness commitment: %w", err)
		}
		commitmentScript = b
	}
	return serializeTripleCoinbaseTxPredecoded(height, extranonce1, extranonce2, templateExtraNonce2Size, poolScript, donationScript, workerScript, totalValue, poolFeePercent, donationFeePercent, commitmentScript, flagsBytes, coinbaseMsg, scriptTime)
}
