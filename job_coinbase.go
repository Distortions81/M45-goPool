package main

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strings"
)

// coinbasePayoutOutput describes a single non-witness-commitment output in a
// coinbase transaction.
type coinbasePayoutOutput struct {
	Script []byte
	Value  int64
}

type coinbasePayoutPlan struct {
	TotalValue               int64
	RemainderScript          []byte
	FeeSlices                []coinbaseFeeSlice
	RequireRemainderPositive bool
}

// coinbaseFeeSlice describes a percentage-based deduction from the total block
// reward. The computed fee amount is optionally sub-split into additional
// outputs (e.g. a donation from the pool fee), with the remainder going to the
// slice's Script.
type coinbaseFeeSlice struct {
	Script    []byte
	Percent   float64
	SubSlices []coinbaseFeeSubSlice
}

// coinbaseFeeSubSlice describes a percentage-based split of a fee slice amount.
// The percentage is applied to the parent fee slice amount (not the total).
type coinbaseFeeSubSlice struct {
	Script  []byte
	Percent float64
}

type coinbasePayoutBreakdown struct {
	TotalValue     int64
	RemainderValue int64
	FeeSlices      []coinbaseFeeSliceBreakdown
}

type coinbaseFeeSliceBreakdown struct {
	FeeTotal    int64
	ParentValue int64
	SubValues   []int64
}

const maxCoinbasePayoutOutputs = 32

func validateCoinbasePayoutOutputs(outputs []coinbasePayoutOutput) error {
	if len(outputs) == 0 {
		return fmt.Errorf("at least one payout output is required")
	}
	if len(outputs) > maxCoinbasePayoutOutputs {
		return fmt.Errorf("too many payout outputs: %d > %d", len(outputs), maxCoinbasePayoutOutputs)
	}
	for i, o := range outputs {
		if len(o.Script) == 0 {
			return fmt.Errorf("payout output %d script required", i)
		}
		if o.Value < 0 {
			return fmt.Errorf("payout output %d value cannot be negative", i)
		}
	}
	return nil
}

func computeCoinbasePayouts(plan coinbasePayoutPlan) ([]coinbasePayoutOutput, *coinbasePayoutBreakdown, error) {
	if plan.TotalValue <= 0 {
		return nil, nil, fmt.Errorf("total coinbase value must be positive")
	}
	if len(plan.RemainderScript) == 0 {
		return nil, nil, fmt.Errorf("remainder payout script required")
	}
	remaining := plan.TotalValue
	payouts := make([]coinbasePayoutOutput, 0, 1+len(plan.FeeSlices))

	breakdown := &coinbasePayoutBreakdown{
		TotalValue: plan.TotalValue,
		FeeSlices:  make([]coinbaseFeeSliceBreakdown, 0, len(plan.FeeSlices)),
	}

	for i, fee := range plan.FeeSlices {
		if len(fee.Script) == 0 {
			return nil, nil, fmt.Errorf("fee slice %d script required", i)
		}

		feePct := fee.Percent
		if feePct < 0 {
			feePct = 0
		}
		if feePct > 99.99 {
			feePct = 99.99
		}

		feeTotal := int64(math.Round(float64(plan.TotalValue) * feePct / 100.0))
		if feeTotal < 0 {
			feeTotal = 0
		}
		if feeTotal > remaining {
			feeTotal = remaining
		}
		remaining -= feeTotal

		feeRemaining := feeTotal
		subValues := make([]int64, 0, len(fee.SubSlices))
		subPayouts := make([]coinbasePayoutOutput, 0, len(fee.SubSlices))
		for j, sub := range fee.SubSlices {
			if len(sub.Script) == 0 {
				return nil, nil, fmt.Errorf("fee slice %d subslice %d script required", i, j)
			}
			subPct := sub.Percent
			if subPct < 0 {
				subPct = 0
			}
			if subPct > 100 {
				subPct = 100
			}

			subAmt := int64(math.Round(float64(feeTotal) * subPct / 100.0))
			if subAmt < 0 {
				subAmt = 0
			}
			if subAmt > feeRemaining {
				subAmt = feeRemaining
			}
			feeRemaining -= subAmt

			subValues = append(subValues, subAmt)
			subPayouts = append(subPayouts, coinbasePayoutOutput{Script: sub.Script, Value: subAmt})
		}

		// The builder emits fee slice outputs first (then subslices). Final
		// on-wire ordering is handled by buildCoinbaseOutputs.
		payouts = append(payouts, coinbasePayoutOutput{Script: fee.Script, Value: feeRemaining})
		payouts = append(payouts, subPayouts...)

		breakdown.FeeSlices = append(breakdown.FeeSlices, coinbaseFeeSliceBreakdown{
			FeeTotal:    feeTotal,
			ParentValue: feeRemaining,
			SubValues:   subValues,
		})
	}

	breakdown.RemainderValue = remaining
	if plan.RequireRemainderPositive && remaining <= 0 {
		return nil, nil, fmt.Errorf("remainder payout must be positive after applying fees")
	}
	payouts = append(payouts, coinbasePayoutOutput{Script: plan.RemainderScript, Value: remaining})

	if err := validateCoinbasePayoutOutputs(payouts); err != nil {
		return nil, nil, err
	}
	return payouts, breakdown, nil
}

func buildCoinbaseOutputs(commitmentScript []byte, payouts []coinbasePayoutOutput) ([]byte, error) {
	if err := validateCoinbasePayoutOutputs(payouts); err != nil {
		return nil, err
	}

	// Encode payouts from largest to smallest; stable sort preserves tie order.
	orderedPayouts := append([]coinbasePayoutOutput(nil), payouts...)
	sort.SliceStable(orderedPayouts, func(i, j int) bool {
		return orderedPayouts[i].Value > orderedPayouts[j].Value
	})

	var outputs bytes.Buffer
	var total int
	if len(commitmentScript) > 0 {
		total += 8 + 9 + len(commitmentScript)
	}
	for _, o := range orderedPayouts {
		total += 8 + 9 + len(o.Script)
	}
	total += 9 // output count varint upper bound
	outputs.Grow(total)

	outputCount := uint64(len(orderedPayouts))
	if len(commitmentScript) > 0 {
		outputCount++
	}
	writeVarInt(&outputs, outputCount)
	if len(commitmentScript) > 0 {
		writeUint64LE(&outputs, 0)
		writeVarInt(&outputs, uint64(len(commitmentScript)))
		outputs.Write(commitmentScript)
	}
	for _, o := range orderedPayouts {
		writeUint64LE(&outputs, uint64(o.Value))
		writeVarInt(&outputs, uint64(len(o.Script)))
		outputs.Write(o.Script)
	}
	return outputs.Bytes(), nil
}

// serializeCoinbaseTxPayoutsPredecoded builds a coinbase tx with 1..N payout
// outputs plus an optional witness commitment output. Payout outputs are
// encoded largest-to-smallest by value.
func serializeCoinbaseTxPayoutsPredecoded(height int64, extranonce1, extranonce2 []byte, templateExtraNonce2Size int, payouts []coinbasePayoutOutput, commitmentScript []byte, flagsBytes []byte, coinbaseMsg string, scriptTime int64) ([]byte, []byte, error) {
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
	writeUint32LE(&vin, 0)

	outputs, err := buildCoinbaseOutputs(commitmentScript, payouts)
	if err != nil {
		return nil, nil, err
	}

	var tx bytes.Buffer
	writeUint32LE(&tx, 1)
	tx.Write(vin.Bytes())
	tx.Write(outputs)
	writeUint32LE(&tx, 0)

	txid := doubleSHA256(tx.Bytes())
	return tx.Bytes(), txid, nil
}

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
	payouts := []coinbasePayoutOutput{{Script: payoutScript, Value: coinbaseValue}}
	return serializeCoinbaseTxPayoutsPredecoded(height, extranonce1, extranonce2, templateExtraNonce2Size, payouts, commitmentScript, flagsBytes, coinbaseMsg, scriptTime)
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
	plan := coinbasePayoutPlan{
		TotalValue:               totalValue,
		RemainderScript:          workerScript,
		FeeSlices:                []coinbaseFeeSlice{{Script: poolScript, Percent: feePercent}},
		RequireRemainderPositive: true,
	}
	payouts, _, err := computeCoinbasePayouts(plan)
	if err != nil {
		return nil, nil, err
	}
	return serializeCoinbaseTxPayoutsPredecoded(height, extranonce1, extranonce2, templateExtraNonce2Size, payouts, commitmentScript, flagsBytes, coinbaseMsg, scriptTime)
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
	plan := coinbasePayoutPlan{
		TotalValue:               totalValue,
		RemainderScript:          workerScript,
		FeeSlices:                []coinbaseFeeSlice{{Script: poolScript, Percent: poolFeePercent, SubSlices: []coinbaseFeeSubSlice{{Script: donationScript, Percent: donationFeePercent}}}},
		RequireRemainderPositive: true,
	}
	payouts, breakdown, err := computeCoinbasePayouts(plan)
	if err != nil {
		return nil, nil, err
	}

	// Keep the same log payload as before for easier operational debugging.
	// Verify FeeSlices has at least one element before accessing.
	if len(breakdown.FeeSlices) == 0 {
		return nil, nil, fmt.Errorf("breakdown.FeeSlices is empty")
	}
	totalPoolFee := breakdown.FeeSlices[0].FeeTotal
	poolFee := breakdown.FeeSlices[0].ParentValue
	donationValue := int64(0)
	if len(breakdown.FeeSlices[0].SubValues) > 0 {
		donationValue = breakdown.FeeSlices[0].SubValues[0]
	}
	logger.Info("triple coinbase split",
		"total_sats", totalValue,
		"pool_fee_pct", poolFeePercent,
		"total_pool_fee_sats", totalPoolFee,
		"donation_pct", donationFeePercent,
		"donation_sats", donationValue,
		"pool_keeps_sats", poolFee,
		"worker_sats", breakdown.RemainderValue)

	return serializeCoinbaseTxPayoutsPredecoded(height, extranonce1, extranonce2, templateExtraNonce2Size, payouts, commitmentScript, flagsBytes, coinbaseMsg, scriptTime)
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
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return "/nodeStratum/"
	}
	msg = strings.TrimPrefix(msg, "/")
	msg = strings.TrimSuffix(msg, "/")
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

// buildCoinbaseParts constructs coinb1/coinb2 for Stratum notify.
func buildCoinbaseParts(height int64, extranonce1 []byte, extranonce2Size int, templateExtraNonce2Size int, payoutScript []byte, coinbaseValue int64, witnessCommitment string, coinbaseFlags string, coinbaseMsg string, scriptTime int64) (string, string, error) {
	payouts := []coinbasePayoutOutput{{Script: payoutScript, Value: coinbaseValue}}
	return buildCoinbasePartsPayouts(height, extranonce1, extranonce2Size, templateExtraNonce2Size, payouts, witnessCommitment, coinbaseFlags, coinbaseMsg, scriptTime)
}

func buildCoinbasePartsPayouts(height int64, extranonce1 []byte, extranonce2Size int, templateExtraNonce2Size int, payouts []coinbasePayoutOutput, witnessCommitment string, coinbaseFlags string, coinbaseMsg string, scriptTime int64) (string, string, error) {
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
	writeUint32LE(&p1, 1)
	writeVarInt(&p1, 1)
	p1.Write(bytes.Repeat([]byte{0x00}, 32))
	writeUint32LE(&p1, 0xffffffff)
	writeVarInt(&p1, uint64(len(scriptSigPart1)+len(extraNoncePlaceholder)+len(scriptSigPart2)))
	p1.Write(scriptSigPart1)

	// Outputs
	var commitmentScript []byte
	if witnessCommitment != "" {
		b, err := hex.DecodeString(witnessCommitment)
		if err != nil {
			return "", "", fmt.Errorf("decode witness commitment: %w", err)
		}
		commitmentScript = b
	}
	outputs, err := buildCoinbaseOutputs(commitmentScript, payouts)
	if err != nil {
		return "", "", err
	}

	// p2: scriptSig_part2 || sequence || outputs || locktime
	var p2 bytes.Buffer
	p2.Write(scriptSigPart2)
	writeUint32LE(&p2, 0)
	p2.Write(outputs)
	writeUint32LE(&p2, 0)

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
	plan := coinbasePayoutPlan{
		TotalValue:               totalValue,
		RemainderScript:          workerScript,
		FeeSlices:                []coinbaseFeeSlice{{Script: poolScript, Percent: feePercent}},
		RequireRemainderPositive: true,
	}
	payouts, _, err := computeCoinbasePayouts(plan)
	if err != nil {
		return "", "", err
	}
	return buildCoinbasePartsPayouts(height, extranonce1, extranonce2Size, templateExtraNonce2Size, payouts, witnessCommitment, coinbaseFlags, coinbaseMsg, scriptTime)
}

// buildTriplePayoutCoinbaseParts constructs coinbase parts for a triple-payout
// layout where the block reward is split between a pool-fee output, a donation
// output, and a worker output. This is used when both dual-payout parameters
// and donation parameters are available.
func buildTriplePayoutCoinbaseParts(height int64, extranonce1 []byte, extranonce2Size int, templateExtraNonce2Size int, poolScript []byte, donationScript []byte, workerScript []byte, totalValue int64, poolFeePercent float64, donationFeePercent float64, witnessCommitment string, coinbaseFlags string, coinbaseMsg string, scriptTime int64) (string, string, error) {
	if len(poolScript) == 0 || len(donationScript) == 0 || len(workerScript) == 0 {
		return "", "", fmt.Errorf("pool, donation, and worker payout scripts are all required")
	}
	plan := coinbasePayoutPlan{
		TotalValue:               totalValue,
		RemainderScript:          workerScript,
		FeeSlices:                []coinbaseFeeSlice{{Script: poolScript, Percent: poolFeePercent, SubSlices: []coinbaseFeeSubSlice{{Script: donationScript, Percent: donationFeePercent}}}},
		RequireRemainderPositive: true,
	}
	payouts, _, err := computeCoinbasePayouts(plan)
	if err != nil {
		return "", "", err
	}
	return buildCoinbasePartsPayouts(height, extranonce1, extranonce2Size, templateExtraNonce2Size, payouts, witnessCommitment, coinbaseFlags, coinbaseMsg, scriptTime)
}
