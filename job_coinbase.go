package main

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
)

func serializeCoinbaseTx(height int64, extranonce1, extranonce2 []byte, templateExtraNonce2Size int, payoutScript []byte, coinbaseValue int64, witnessCommitment string, coinbaseFlags string, coinbaseMsg string, scriptTime int64) ([]byte, []byte, error) {
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
	return serializeCoinbaseTxPredecoded(height, extranonce1, extranonce2, templateExtraNonce2Size, payoutScript, coinbaseValue, commitmentScript, flagsBytes, coinbaseMsg, scriptTime)
}

// serializeCoinbaseTxPredecoded is the hot-path variant that reuses
// pre-decoded flags/commitment bytes.
func serializeCoinbaseTxPredecoded(height int64, extranonce1, extranonce2 []byte, templateExtraNonce2Size int, payoutScript []byte, coinbaseValue int64, commitmentScript []byte, flagsBytes []byte, coinbaseMsg string, scriptTime int64) ([]byte, []byte, error) {
	if len(payoutScript) == 0 {
		return nil, nil, fmt.Errorf("payout script required")
	}

	padLen := templateExtraNonce2Size - len(extranonce2)
	if padLen < 0 {
		padLen = 0
	}
	placeholderLen := len(extranonce1) + len(extranonce2) + padLen
	extraNoncePlaceholder := bytes.Repeat([]byte{0x00}, placeholderLen)

	scriptSigPart1 := bytes.Join([][]byte{
		serializeNumberScript(height),
		flagsBytes,                        // coinbaseaux.flags from bitcoind
		serializeNumberScript(scriptTime), // stable per job
		{byte(len(extraNoncePlaceholder))},
	}, nil)
	msg := normalizeCoinbaseMessage(coinbaseMsg)
	scriptSigPart2 := serializeStringScript(msg)
	scriptSigLen := len(scriptSigPart1) + padLen + len(extranonce1) + len(extranonce2) + len(scriptSigPart2)

	var vin bytes.Buffer
	writeVarInt(&vin, 1)
	vin.Write(bytes.Repeat([]byte{0x00}, 32))
	writeUint32LE(&vin, 0xffffffff)
	writeVarInt(&vin, uint64(scriptSigLen))
	vin.Write(scriptSigPart1)
	if padLen > 0 {
		vin.Write(bytes.Repeat([]byte{0x00}, padLen))
	}
	vin.Write(extranonce1)
	vin.Write(extranonce2)
	vin.Write(scriptSigPart2)
	writeUint32LE(&vin, 0) // sequence

	var outputs bytes.Buffer
	outputCount := uint64(1)
	if len(commitmentScript) > 0 {
		outputCount++
	}
	writeVarInt(&outputs, outputCount)
	if len(commitmentScript) > 0 {
		writeUint64LE(&outputs, 0)
		writeVarInt(&outputs, uint64(len(commitmentScript)))
		outputs.Write(commitmentScript)
	}
	writeUint64LE(&outputs, uint64(coinbaseValue))
	writeVarInt(&outputs, uint64(len(payoutScript)))
	outputs.Write(payoutScript)

	var tx bytes.Buffer
	writeUint32LE(&tx, 1) // version
	tx.Write(vin.Bytes())
	tx.Write(outputs.Bytes())
	writeUint32LE(&tx, 0) // locktime

	txid := doubleSHA256(tx.Bytes())
	return tx.Bytes(), txid, nil
}

// serializeDualCoinbaseTx builds a coinbase transaction that splits the block
// reward between a pool-fee output and a worker output. It mirrors
// serializeCoinbaseTx but takes separate scripts and a fee percentage.
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

// serializeDualCoinbaseTxPredecoded is the hot-path variant that reuses
// pre-decoded flags/commitment bytes.
func serializeDualCoinbaseTxPredecoded(height int64, extranonce1, extranonce2 []byte, templateExtraNonce2Size int, poolScript []byte, workerScript []byte, totalValue int64, feePercent float64, commitmentScript []byte, flagsBytes []byte, coinbaseMsg string, scriptTime int64) ([]byte, []byte, error) {
	if len(poolScript) == 0 || len(workerScript) == 0 {
		return nil, nil, fmt.Errorf("both pool and worker payout scripts are required")
	}
	if totalValue <= 0 {
		return nil, nil, fmt.Errorf("total coinbase value must be positive")
	}

	padLen := templateExtraNonce2Size - len(extranonce2)
	if padLen < 0 {
		padLen = 0
	}
	placeholderLen := len(extranonce1) + len(extranonce2) + padLen
	extraNoncePlaceholder := bytes.Repeat([]byte{0x00}, placeholderLen)

	scriptSigPart1 := bytes.Join([][]byte{
		serializeNumberScript(height),
		flagsBytes,
		serializeNumberScript(scriptTime),
		{byte(len(extraNoncePlaceholder))},
	}, nil)
	msg := normalizeCoinbaseMessage(coinbaseMsg)
	scriptSigPart2 := serializeStringScript(msg)
	scriptSigLen := len(scriptSigPart1) + padLen + len(extranonce1) + len(extranonce2) + len(scriptSigPart2)

	var vin bytes.Buffer
	writeVarInt(&vin, 1)
	vin.Write(bytes.Repeat([]byte{0x00}, 32))
	writeUint32LE(&vin, 0xffffffff)
	writeVarInt(&vin, uint64(scriptSigLen))
	vin.Write(scriptSigPart1)
	if padLen > 0 {
		vin.Write(bytes.Repeat([]byte{0x00}, padLen))
	}
	vin.Write(extranonce1)
	vin.Write(extranonce2)
	vin.Write(scriptSigPart2)
	writeUint32LE(&vin, 0)

	// Split total value into pool fee and worker payout.
	if feePercent < 0 {
		feePercent = 0
	}
	if feePercent > 99.99 {
		feePercent = 99.99
	}
	poolFee := int64(math.Round(float64(totalValue) * feePercent / 100.0))
	if poolFee < 0 {
		poolFee = 0
	}
	if poolFee > totalValue {
		poolFee = totalValue
	}
	workerValue := totalValue - poolFee
	if workerValue <= 0 {
		return nil, nil, fmt.Errorf("worker payout must be positive after applying pool fee")
	}

	var outputs bytes.Buffer
	outputCount := uint64(2) // pool + worker
	if len(commitmentScript) > 0 {
		outputCount++
	}
	writeVarInt(&outputs, outputCount)
	if len(commitmentScript) > 0 {
		writeUint64LE(&outputs, 0)
		writeVarInt(&outputs, uint64(len(commitmentScript)))
		outputs.Write(commitmentScript)
	}
	// Pool fee output.
	writeUint64LE(&outputs, uint64(poolFee))
	writeVarInt(&outputs, uint64(len(poolScript)))
	outputs.Write(poolScript)
	// Worker payout output.
	writeUint64LE(&outputs, uint64(workerValue))
	writeVarInt(&outputs, uint64(len(workerScript)))
	outputs.Write(workerScript)

	var tx bytes.Buffer
	writeUint32LE(&tx, 1)
	tx.Write(vin.Bytes())
	tx.Write(outputs.Bytes())
	writeUint32LE(&tx, 0)

	txid := doubleSHA256(tx.Bytes())
	return tx.Bytes(), txid, nil
}

// serializeTripleCoinbaseTx builds a coinbase transaction that splits the block
// reward between a pool-fee output, a donation output, and a worker output.
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

// serializeTripleCoinbaseTxPredecoded is the hot-path variant that reuses
// pre-decoded flags/commitment bytes.
func serializeTripleCoinbaseTxPredecoded(height int64, extranonce1, extranonce2 []byte, templateExtraNonce2Size int, poolScript []byte, donationScript []byte, workerScript []byte, totalValue int64, poolFeePercent float64, donationFeePercent float64, commitmentScript []byte, flagsBytes []byte, coinbaseMsg string, scriptTime int64) ([]byte, []byte, error) {
	if len(poolScript) == 0 || len(donationScript) == 0 || len(workerScript) == 0 {
		return nil, nil, fmt.Errorf("pool, donation, and worker payout scripts are all required")
	}
	if totalValue <= 0 {
		return nil, nil, fmt.Errorf("total coinbase value must be positive")
	}

	padLen := templateExtraNonce2Size - len(extranonce2)
	if padLen < 0 {
		padLen = 0
	}
	placeholderLen := len(extranonce1) + len(extranonce2) + padLen
	extraNoncePlaceholder := bytes.Repeat([]byte{0x00}, placeholderLen)

	scriptSigPart1 := bytes.Join([][]byte{
		serializeNumberScript(height),
		flagsBytes,
		serializeNumberScript(scriptTime),
		{byte(len(extraNoncePlaceholder))},
	}, nil)
	msg := normalizeCoinbaseMessage(coinbaseMsg)
	scriptSigPart2 := serializeStringScript(msg)
	scriptSigLen := len(scriptSigPart1) + padLen + len(extranonce1) + len(extranonce2) + len(scriptSigPart2)

	var vin bytes.Buffer
	writeVarInt(&vin, 1)
	vin.Write(bytes.Repeat([]byte{0x00}, 32))
	writeUint32LE(&vin, 0xffffffff)
	writeVarInt(&vin, uint64(scriptSigLen))
	vin.Write(scriptSigPart1)
	if padLen > 0 {
		vin.Write(bytes.Repeat([]byte{0x00}, padLen))
	}
	vin.Write(extranonce1)
	vin.Write(extranonce2)
	vin.Write(scriptSigPart2)
	writeUint32LE(&vin, 0)

	// Split total value: first pool fee, then donation from pool fee, then worker
	if poolFeePercent < 0 {
		poolFeePercent = 0
	}
	if poolFeePercent > 99.99 {
		poolFeePercent = 99.99
	}
	if donationFeePercent < 0 {
		donationFeePercent = 0
	}
	if donationFeePercent > 100 {
		donationFeePercent = 100
	}

	// Calculate pool fee from total value
	totalPoolFee := int64(math.Round(float64(totalValue) * poolFeePercent / 100.0))
	if totalPoolFee < 0 {
		totalPoolFee = 0
	}
	if totalPoolFee > totalValue {
		totalPoolFee = totalValue
	}

	// Calculate donation from pool fee
	donationValue := int64(math.Round(float64(totalPoolFee) * donationFeePercent / 100.0))
	if donationValue < 0 {
		donationValue = 0
	}
	if donationValue > totalPoolFee {
		donationValue = totalPoolFee
	}

	// Remaining pool fee after donation
	poolFee := totalPoolFee - donationValue

	logger.Info("triple coinbase split",
		"total_sats", totalValue,
		"pool_fee_pct", poolFeePercent,
		"total_pool_fee_sats", totalPoolFee,
		"donation_pct", donationFeePercent,
		"donation_sats", donationValue,
		"pool_keeps_sats", poolFee,
		"worker_sats", totalValue-totalPoolFee)

	// Worker gets the rest
	workerValue := totalValue - totalPoolFee
	if workerValue <= 0 {
		return nil, nil, fmt.Errorf("worker payout must be positive after applying pool fee")
	}

	var outputs bytes.Buffer
	outputCount := uint64(3) // pool + donation + worker
	if len(commitmentScript) > 0 {
		outputCount++
	}
	writeVarInt(&outputs, outputCount)
	if len(commitmentScript) > 0 {
		writeUint64LE(&outputs, 0)
		writeVarInt(&outputs, uint64(len(commitmentScript)))
		outputs.Write(commitmentScript)
	}
	// Pool fee output (after donation).
	writeUint64LE(&outputs, uint64(poolFee))
	writeVarInt(&outputs, uint64(len(poolScript)))
	outputs.Write(poolScript)
	// Donation output.
	writeUint64LE(&outputs, uint64(donationValue))
	writeVarInt(&outputs, uint64(len(donationScript)))
	outputs.Write(donationScript)
	// Worker payout output.
	writeUint64LE(&outputs, uint64(workerValue))
	writeVarInt(&outputs, uint64(len(workerScript)))
	outputs.Write(workerScript)

	var tx bytes.Buffer
	writeUint32LE(&tx, 1)
	tx.Write(vin.Bytes())
	tx.Write(outputs.Bytes())
	writeUint32LE(&tx, 0)

	txid := doubleSHA256(tx.Bytes())
	return tx.Bytes(), txid, nil
}

func serializeNumberScript(n int64) []byte {
	if n >= 1 && n <= 16 {
		return []byte{byte(0x50 + n)}
	}
	l := 1
	buf := make([]byte, 9)
	for n > 0x7f {
		buf[l] = byte(n & 0xff)
		l++
		n >>= 8
	}
	buf[0] = byte(l)
	buf[l] = byte(n)
	return buf[:l+1]
}

// normalizeCoinbaseMessage trims spaces and ensures the message has '/' prefix and suffix.
// If the message is empty after trimming, returns the default "/nodeStratum/" tag.
func normalizeCoinbaseMessage(msg string) string {
	// Trim spaces
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return "/nodeStratum/"
	}
	// Remove existing '/' prefix and suffix
	msg = strings.TrimPrefix(msg, "/")
	msg = strings.TrimSuffix(msg, "/")
	// Add '/' prefix and suffix
	return "/" + msg + "/"
}

func serializeStringScript(s string) []byte {
	b := []byte(s)
	if len(b) < 253 {
		return append([]byte{byte(len(b))}, b...)
	}
	if len(b) < 0x10000 {
		out := []byte{253, byte(len(b)), byte(len(b) >> 8)}
		return append(out, b...)
	}
	if len(b) < 0x100000000 {
		out := []byte{254, byte(len(b)), byte(len(b) >> 8), byte(len(b) >> 16), byte(len(b) >> 24)}
		return append(out, b...)
	}
	out := []byte{255}
	out = appendVarInt(out, uint64(len(b)))
	return append(out, b...)
}

func coinbaseScriptSigFixedLen(height int64, scriptTime int64, coinbaseFlags string, extranonce2Size int, templateExtraNonce2Size int) (int, error) {
	flagsBytes := []byte{}
	if coinbaseFlags != "" {
		var err error
		flagsBytes, err = hex.DecodeString(coinbaseFlags)
		if err != nil {
			return 0, fmt.Errorf("decode coinbase flags: %w", err)
		}
	}
	if templateExtraNonce2Size < extranonce2Size {
		templateExtraNonce2Size = extranonce2Size
	}
	padLen := templateExtraNonce2Size - extranonce2Size
	if padLen < 0 {
		padLen = 0
	}
	partLen := len(serializeNumberScript(height)) + len(flagsBytes) + len(serializeNumberScript(scriptTime)) + 1
	return partLen + padLen + coinbaseExtranonce1Size + extranonce2Size, nil
}

func clampCoinbaseMessage(message string, limit int, height int64, scriptTime int64, coinbaseFlags string, extranonce2Size int, templateExtraNonce2Size int) (string, bool, error) {
	if limit <= 0 {
		return message, false, nil
	}
	fixedLen, err := coinbaseScriptSigFixedLen(height, scriptTime, coinbaseFlags, extranonce2Size, templateExtraNonce2Size)
	if err != nil {
		return "", false, err
	}
	allowed := limit - fixedLen
	if allowed <= 0 {
		return "", true, nil
	}

	normalized := normalizeCoinbaseMessage(message)
	body := ""
	if len(normalized) > 2 {
		body = normalized[1 : len(normalized)-1]
	}
	if len(serializeStringScript(normalized)) <= allowed {
		return body, false, nil
	}
	for len(body) > 0 {
		body = body[:len(body)-1]
		candidate := "/" + body + "/"
		if len(serializeStringScript(candidate)) <= allowed {
			return body, true, nil
		}
	}
	// Fallback to the default tag if we trimmed everything.
	defaultNormalized := normalizeCoinbaseMessage("")
	if len(serializeStringScript(defaultNormalized)) <= allowed {
		return "", true, nil
	}
	return "", true, nil
}

// buildCoinbaseParts constructs coinb1/coinb2 for the stratum protocol.
// The trailing string in the scriptSig is the pool's coinbase message.
func buildCoinbaseParts(height int64, extranonce1 []byte, extranonce2Size int, templateExtraNonce2Size int, payoutScript []byte, coinbaseValue int64, witnessCommitment string, coinbaseFlags string, coinbaseMsg string, scriptTime int64) (string, string, error) {
	if extranonce2Size <= 0 {
		extranonce2Size = 4
	}
	if templateExtraNonce2Size < extranonce2Size {
		templateExtraNonce2Size = extranonce2Size
	}
	templatePlaceholderLen := len(extranonce1) + templateExtraNonce2Size
	extraNoncePlaceholder := bytes.Repeat([]byte{0x00}, templatePlaceholderLen)
	padLen := templateExtraNonce2Size - extranonce2Size

	var flagsBytes []byte
	if coinbaseFlags != "" {
		var err error
		flagsBytes, err = hex.DecodeString(coinbaseFlags)
		if err != nil {
			return "", "", fmt.Errorf("decode coinbase flags: %w", err)
		}
	}

	scriptSigPart1 := bytes.Join([][]byte{
		serializeNumberScript(height),
		flagsBytes, // coinbaseaux.flags from bitcoind
		serializeNumberScript(scriptTime),
		{byte(len(extraNoncePlaceholder))},
	}, nil)
	msg := normalizeCoinbaseMessage(coinbaseMsg)
	scriptSigPart2 := serializeStringScript(msg)

	// p1: version || input count || prevout || scriptsig length || scriptsig_part1
	var p1 bytes.Buffer
	writeUint32LE(&p1, 1) // tx version
	writeVarInt(&p1, 1)
	p1.Write(bytes.Repeat([]byte{0x00}, 32)) // prev hash
	writeUint32LE(&p1, 0xffffffff)           // prev index
	writeVarInt(&p1, uint64(len(scriptSigPart1)+len(extraNoncePlaceholder)+len(scriptSigPart2)))
	p1.Write(scriptSigPart1)

	// Outputs
	var outputs bytes.Buffer
	outputCount := uint64(1)
	if witnessCommitment != "" {
		outputCount++
	}
	writeVarInt(&outputs, outputCount)
	if witnessCommitment != "" {
		commitmentScript, err := hex.DecodeString(witnessCommitment)
		if err != nil {
			return "", "", fmt.Errorf("decode witness commitment: %w", err)
		}
		writeUint64LE(&outputs, 0)
		writeVarInt(&outputs, uint64(len(commitmentScript)))
		outputs.Write(commitmentScript)
	}
	writeUint64LE(&outputs, uint64(coinbaseValue))
	writeVarInt(&outputs, uint64(len(payoutScript)))
	outputs.Write(payoutScript)

	// p2: scriptSig_part2 || sequence || outputs || locktime
	var p2 bytes.Buffer
	p2.Write(scriptSigPart2)
	writeUint32LE(&p2, 0) // sequence
	p2.Write(outputs.Bytes())
	writeUint32LE(&p2, 0) // locktime

	coinb1 := hex.EncodeToString(p1.Bytes())
	if padLen > 0 {
		coinb1 += strings.Repeat("00", padLen)
	}
	coinb2 := hex.EncodeToString(p2.Bytes())
	return coinb1, coinb2, nil
}

// buildDualPayoutCoinbaseParts constructs coinbase parts for a dual-payout
// layout where the block reward is split between a pool-fee output and a
// worker output. It mirrors buildCoinbaseParts but takes separate scripts for
// the pool and worker, along with a fee percentage, and is used by
// MinerConn.sendNotifyFor when dual-payout parameters are available.
func buildDualPayoutCoinbaseParts(height int64, extranonce1 []byte, extranonce2Size int, templateExtraNonce2Size int, poolScript []byte, workerScript []byte, totalValue int64, feePercent float64, witnessCommitment string, coinbaseFlags string, coinbaseMsg string, scriptTime int64) (string, string, error) {
	if len(poolScript) == 0 || len(workerScript) == 0 {
		return "", "", fmt.Errorf("both pool and worker payout scripts are required")
	}
	if extranonce2Size <= 0 {
		extranonce2Size = 4
	}
	if templateExtraNonce2Size < extranonce2Size {
		templateExtraNonce2Size = extranonce2Size
	}
	templatePlaceholderLen := len(extranonce1) + templateExtraNonce2Size
	extraNoncePlaceholder := bytes.Repeat([]byte{0x00}, templatePlaceholderLen)
	padLen := templateExtraNonce2Size - extranonce2Size

	var flagsBytes []byte
	if coinbaseFlags != "" {
		var err error
		flagsBytes, err = hex.DecodeString(coinbaseFlags)
		if err != nil {
			return "", "", fmt.Errorf("decode coinbase flags: %w", err)
		}
	}

	scriptSigPart1 := bytes.Join([][]byte{
		serializeNumberScript(height),
		flagsBytes, // coinbaseaux.flags from bitcoind
		serializeNumberScript(scriptTime),
		{byte(len(extraNoncePlaceholder))},
	}, nil)
	msg := normalizeCoinbaseMessage(coinbaseMsg)
	scriptSigPart2 := serializeStringScript(msg)

	// p1: version || input count || prevout || scriptsig length || scriptsig_part1
	var p1 bytes.Buffer
	writeUint32LE(&p1, 1) // tx version
	writeVarInt(&p1, 1)
	p1.Write(bytes.Repeat([]byte{0x00}, 32)) // prev hash
	writeUint32LE(&p1, 0xffffffff)           // prev index
	writeVarInt(&p1, uint64(len(scriptSigPart1)+len(extraNoncePlaceholder)+len(scriptSigPart2)))
	p1.Write(scriptSigPart1)

	// Split total value into pool fee and worker payout.
	if totalValue <= 0 {
		return "", "", fmt.Errorf("total coinbase value must be positive")
	}
	if feePercent < 0 {
		feePercent = 0
	}
	if feePercent > 99.99 {
		feePercent = 99.99
	}
	poolFee := int64(math.Round(float64(totalValue) * feePercent / 100.0))
	if poolFee < 0 {
		poolFee = 0
	}
	if poolFee > totalValue {
		poolFee = totalValue
	}
	workerValue := totalValue - poolFee
	if workerValue <= 0 {
		return "", "", fmt.Errorf("worker payout must be positive after applying pool fee")
	}

	// Outputs: optional witness commitment, then pool-fee output, then worker output.
	var outputs bytes.Buffer
	outputCount := uint64(2) // pool + worker
	if witnessCommitment != "" {
		outputCount++
	}
	writeVarInt(&outputs, outputCount)
	if witnessCommitment != "" {
		commitmentScript, err := hex.DecodeString(witnessCommitment)
		if err != nil {
			return "", "", fmt.Errorf("decode witness commitment: %w", err)
		}
		writeUint64LE(&outputs, 0)
		writeVarInt(&outputs, uint64(len(commitmentScript)))
		outputs.Write(commitmentScript)
	}
	// Pool fee output.
	writeUint64LE(&outputs, uint64(poolFee))
	writeVarInt(&outputs, uint64(len(poolScript)))
	outputs.Write(poolScript)
	// Worker payout output.
	writeUint64LE(&outputs, uint64(workerValue))
	writeVarInt(&outputs, uint64(len(workerScript)))
	outputs.Write(workerScript)

	// p2: scriptSig_part2 || sequence || outputs || locktime
	var p2 bytes.Buffer
	p2.Write(scriptSigPart2)
	writeUint32LE(&p2, 0) // sequence
	p2.Write(outputs.Bytes())
	writeUint32LE(&p2, 0) // locktime

	coinb1 := hex.EncodeToString(p1.Bytes())
	if padLen > 0 {
		coinb1 += strings.Repeat("00", padLen)
	}
	coinb2 := hex.EncodeToString(p2.Bytes())
	return coinb1, coinb2, nil
}

// buildTriplePayoutCoinbaseParts constructs coinbase parts for a triple-payout
// layout where the block reward is split between a pool-fee output, a donation
// output, and a worker output. This is used when both dual-payout parameters
// and donation parameters are available.
func buildTriplePayoutCoinbaseParts(height int64, extranonce1 []byte, extranonce2Size int, templateExtraNonce2Size int, poolScript []byte, donationScript []byte, workerScript []byte, totalValue int64, poolFeePercent float64, donationFeePercent float64, witnessCommitment string, coinbaseFlags string, coinbaseMsg string, scriptTime int64) (string, string, error) {
	if len(poolScript) == 0 || len(donationScript) == 0 || len(workerScript) == 0 {
		return "", "", fmt.Errorf("pool, donation, and worker payout scripts are all required")
	}
	if extranonce2Size <= 0 {
		extranonce2Size = 4
	}
	if templateExtraNonce2Size < extranonce2Size {
		templateExtraNonce2Size = extranonce2Size
	}
	templatePlaceholderLen := len(extranonce1) + templateExtraNonce2Size
	extraNoncePlaceholder := bytes.Repeat([]byte{0x00}, templatePlaceholderLen)
	padLen := templateExtraNonce2Size - extranonce2Size

	var flagsBytes []byte
	if coinbaseFlags != "" {
		var err error
		flagsBytes, err = hex.DecodeString(coinbaseFlags)
		if err != nil {
			return "", "", fmt.Errorf("decode coinbase flags: %w", err)
		}
	}

	scriptSigPart1 := bytes.Join([][]byte{
		serializeNumberScript(height),
		flagsBytes, // coinbaseaux.flags from bitcoind
		serializeNumberScript(scriptTime),
		{byte(len(extraNoncePlaceholder))},
	}, nil)
	msg := normalizeCoinbaseMessage(coinbaseMsg)
	scriptSigPart2 := serializeStringScript(msg)

	// p1: version || input count || prevout || scriptsig length || scriptsig_part1
	var p1 bytes.Buffer
	writeUint32LE(&p1, 1) // tx version
	writeVarInt(&p1, 1)
	p1.Write(bytes.Repeat([]byte{0x00}, 32)) // prev hash
	writeUint32LE(&p1, 0xffffffff)           // prev index
	writeVarInt(&p1, uint64(len(scriptSigPart1)+len(extraNoncePlaceholder)+len(scriptSigPart2)))
	p1.Write(scriptSigPart1)

	// Split total value: first pool fee, then donation from pool fee, then worker
	if totalValue <= 0 {
		return "", "", fmt.Errorf("total coinbase value must be positive")
	}
	if poolFeePercent < 0 {
		poolFeePercent = 0
	}
	if poolFeePercent > 99.99 {
		poolFeePercent = 99.99
	}
	if donationFeePercent < 0 {
		donationFeePercent = 0
	}
	if donationFeePercent > 100 {
		donationFeePercent = 100
	}

	// Calculate pool fee from total value
	totalPoolFee := int64(math.Round(float64(totalValue) * poolFeePercent / 100.0))
	if totalPoolFee < 0 {
		totalPoolFee = 0
	}
	if totalPoolFee > totalValue {
		totalPoolFee = totalValue
	}

	// Calculate donation from pool fee
	donationValue := int64(math.Round(float64(totalPoolFee) * donationFeePercent / 100.0))
	if donationValue < 0 {
		donationValue = 0
	}
	if donationValue > totalPoolFee {
		donationValue = totalPoolFee
	}

	// Remaining pool fee after donation
	poolFee := totalPoolFee - donationValue

	// Worker gets the rest
	workerValue := totalValue - totalPoolFee
	if workerValue <= 0 {
		return "", "", fmt.Errorf("worker payout must be positive after applying pool fee")
	}

	// Outputs: optional witness commitment, then pool-fee output, then donation output, then worker output.
	var outputs bytes.Buffer
	outputCount := uint64(3) // pool + donation + worker
	if witnessCommitment != "" {
		outputCount++
	}
	writeVarInt(&outputs, outputCount)
	if witnessCommitment != "" {
		commitmentScript, err := hex.DecodeString(witnessCommitment)
		if err != nil {
			return "", "", fmt.Errorf("decode witness commitment: %w", err)
		}
		writeUint64LE(&outputs, 0)
		writeVarInt(&outputs, uint64(len(commitmentScript)))
		outputs.Write(commitmentScript)
	}
	// Pool fee output (after donation).
	writeUint64LE(&outputs, uint64(poolFee))
	writeVarInt(&outputs, uint64(len(poolScript)))
	outputs.Write(poolScript)
	// Donation output.
	writeUint64LE(&outputs, uint64(donationValue))
	writeVarInt(&outputs, uint64(len(donationScript)))
	outputs.Write(donationScript)
	// Worker payout output.
	writeUint64LE(&outputs, uint64(workerValue))
	writeVarInt(&outputs, uint64(len(workerScript)))
	outputs.Write(workerScript)

	// p2: scriptSig_part2 || sequence || outputs || locktime
	var p2 bytes.Buffer
	p2.Write(scriptSigPart2)
	writeUint32LE(&p2, 0) // sequence
	p2.Write(outputs.Bytes())
	writeUint32LE(&p2, 0) // locktime

	coinb1 := hex.EncodeToString(p1.Bytes())
	if padLen > 0 {
		coinb1 += strings.Repeat("00", padLen)
	}
	coinb2 := hex.EncodeToString(p2.Bytes())
	return coinb1, coinb2, nil
}
