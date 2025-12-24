package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"time"
)

func targetFromBits(bits string) (*big.Int, error) {
	b, err := hex.DecodeString(bits)
	if err != nil {
		return nil, fmt.Errorf("decode bits: %w", err)
	}
	if len(b) != 4 {
		return nil, fmt.Errorf("invalid bits length %d", len(b))
	}
	exp := b[0]
	mantissa := new(big.Int).SetBytes(b[1:])
	target := new(big.Int).Lsh(mantissa, 8*uint(exp-3))
	return target, nil
}

var diff1Target = func() *big.Int {
	n, _ := new(big.Int).SetString("00000000FFFF0000000000000000000000000000000000000000000000000000", 16)
	return n
}()

// maxUint256 is the maximum value representable in 256 bits.
var maxUint256 = func() *big.Int {
	n := new(big.Int).Lsh(big.NewInt(1), 256)
	return n.Sub(n, big.NewInt(1))
}()

func targetFromDifficulty(diff float64) *big.Int {
	if diff <= 0 {
		// Lowest difficulty means the largest possible target.
		return new(big.Int).Set(maxUint256)
	}
	diffStr := strconv.FormatFloat(diff, 'g', -1, 64)
	r, ok := new(big.Rat).SetString(diffStr)
	if !ok || r.Sign() <= 0 {
		return new(big.Int).Set(maxUint256)
	}
	target := new(big.Rat).SetInt(diff1Target)
	target.Quo(target, r)
	tgt := new(big.Int).Quo(target.Num(), target.Denom())
	if tgt.Sign() == 0 {
		tgt = big.NewInt(1)
	}
	if tgt.Cmp(maxUint256) > 0 {
		tgt = new(big.Int).Set(maxUint256)
	}
	return tgt
}

// difficultyFromHash converts the block hash to a difficulty value relative to diff=1.
// The hash parameter should be big-endian bytes from SHA256.
func difficultyFromHash(hash []byte) float64 {
	// Bitcoin PoW uses little-endian interpretation
	n := new(big.Int).SetBytes(reverseBytes(hash))
	if n.Sign() == 0 {
		return defaultVarDiff.MaxDiff
	}

	f := new(big.Float).SetPrec(256).SetInt(diff1Target)
	f.Quo(f, new(big.Float).SetInt(n))
	val, _ := f.Float64()
	return val
}

func parseRawBlockTip(payload []byte) (ZMQBlockTip, error) {
	if len(payload) < 80 {
		return ZMQBlockTip{}, fmt.Errorf("block payload too short")
	}
	header := payload[:80]
	bits := binary.LittleEndian.Uint32(header[72:76])
	tipTime := binary.LittleEndian.Uint32(header[68:72])
	height, err := parseCoinbaseHeight(payload)
	if err != nil {
		return ZMQBlockTip{}, err
	}
	hash := blockHashFromHeader(header)
	return ZMQBlockTip{
		Hash:       hash,
		Height:     height,
		Time:       time.Unix(int64(tipTime), 0).UTC(),
		Bits:       fmt.Sprintf("%08x", bits),
		Difficulty: difficultyFromBits(bits),
	}, nil
}

func parseCoinbaseHeight(block []byte) (int64, error) {
	offset := 80
	if offset >= len(block) {
		return 0, fmt.Errorf("missing tx count")
	}
	txCount, n, err := readVarInt(block[offset:])
	if err != nil {
		return 0, err
	}
	offset += n
	if txCount == 0 {
		return 0, fmt.Errorf("zero tx count")
	}
	if len(block) < offset+4 {
		return 0, fmt.Errorf("missing tx version")
	}
	offset += 4
	inCount, n, err := readVarInt(block[offset:])
	if err != nil {
		return 0, err
	}
	offset += n
	if inCount == 0 {
		return 0, fmt.Errorf("zero inputs")
	}
	if len(block) < offset+36 {
		return 0, fmt.Errorf("missing prevout")
	}
	offset += 36
	scriptLen, n, err := readVarInt(block[offset:])
	if err != nil {
		return 0, err
	}
	offset += n
	if len(block) < offset+int(scriptLen) {
		return 0, fmt.Errorf("script too short")
	}
	script := block[offset : offset+int(scriptLen)]
	pushData, err := extractPushData(script)
	if err != nil {
		return 0, err
	}
	return bytesToInt64(pushData), nil
}

func extractPushData(script []byte) ([]byte, error) {
	if len(script) == 0 {
		return nil, fmt.Errorf("empty script")
	}
	op := script[0]
	switch {
	case op <= 0x4b:
		if len(script) < 1+int(op) {
			return nil, fmt.Errorf("script shorter than push")
		}
		return script[1 : 1+int(op)], nil
	case op == 0x4c:
		if len(script) < 2 {
			return nil, fmt.Errorf("script too short for OP_PUSHDATA1")
		}
		size := int(script[1])
		if len(script) < 2+size {
			return nil, fmt.Errorf("script shorter than push1")
		}
		return script[2 : 2+size], nil
	case op == 0x4d:
		if len(script) < 3 {
			return nil, fmt.Errorf("script too short for OP_PUSHDATA2")
		}
		size := int(binary.LittleEndian.Uint16(script[1:3]))
		if len(script) < 3+size {
			return nil, fmt.Errorf("script shorter than push2")
		}
		return script[3 : 3+size], nil
	case op == 0x4e:
		if len(script) < 5 {
			return nil, fmt.Errorf("script too short for OP_PUSHDATA4")
		}
		size := int(binary.LittleEndian.Uint32(script[1:5]))
		if len(script) < 5+size {
			return nil, fmt.Errorf("script shorter than push4")
		}
		return script[5 : 5+size], nil
	default:
		return nil, fmt.Errorf("unsupported script opcode %02x", op)
	}
}

func bytesToInt64(b []byte) int64 {
	var v int64
	for i := len(b) - 1; i >= 0; i-- {
		v = (v << 8) | int64(b[i])
	}
	return v
}

func blockHashFromHeader(header []byte) string {
	hash := doubleSHA256(header)
	return hex.EncodeToString(reverseBytes(hash))
}

func difficultyFromBits(bits uint32) float64 {
	bitsStr := fmt.Sprintf("%08x", bits)
	target, err := targetFromBits(bitsStr)
	if err != nil || target.Sign() == 0 {
		return 0
	}
	f := new(big.Float).SetPrec(256).SetInt(diff1Target)
	d := new(big.Float).SetPrec(256).SetInt(target)
	f.Quo(f, d)
	val, _ := f.Float64()
	return val
}

func doubleSHA256(b []byte) []byte {
	first := sha256Sum(b)
	second := sha256Sum(first[:])
	return second[:]
}

// doubleSHA256Array returns the double SHA256 hash as a fixed-size array,
// avoiding slice allocation for hot paths.
func doubleSHA256Array(b []byte) [32]byte {
	first := sha256Sum(b)
	return sha256Sum(first[:])
}

func reverseBytes(in []byte) []byte {
	out := append([]byte(nil), in...)
	slices.Reverse(out)
	return out
}

// reverseBytes32 reverses a 32-byte array in-place, avoiding allocation.
// Fully unrolled for hot paths where hashes must be flipped.
func reverseBytes32(b *[32]byte) {
	b[0], b[31] = b[31], b[0]
	b[1], b[30] = b[30], b[1]
	b[2], b[29] = b[29], b[2]
	b[3], b[28] = b[28], b[3]
	b[4], b[27] = b[27], b[4]
	b[5], b[26] = b[26], b[5]
	b[6], b[25] = b[25], b[6]
	b[7], b[24] = b[24], b[7]
	b[8], b[23] = b[23], b[8]
	b[9], b[22] = b[22], b[9]
	b[10], b[21] = b[21], b[10]
	b[11], b[20] = b[20], b[11]
	b[12], b[19] = b[19], b[12]
	b[13], b[18] = b[18], b[13]
	b[14], b[17] = b[17], b[14]
	b[15], b[16] = b[16], b[15]
}

func readVarInt(raw []byte) (uint64, int, error) {
	if len(raw) == 0 {
		return 0, 0, fmt.Errorf("varint empty")
	}
	switch raw[0] {
	case 0xff:
		if len(raw) < 9 {
			return 0, 0, fmt.Errorf("varint 0xff missing bytes")
		}
		val := binary.LittleEndian.Uint64(raw[1:9])
		return val, 9, nil
	case 0xfe:
		if len(raw) < 5 {
			return 0, 0, fmt.Errorf("varint 0xfe missing bytes")
		}
		val := binary.LittleEndian.Uint32(raw[1:5])
		return uint64(val), 5, nil
	case 0xfd:
		if len(raw) < 3 {
			return 0, 0, fmt.Errorf("varint 0xfd missing bytes")
		}
		val := binary.LittleEndian.Uint16(raw[1:3])
		return uint64(val), 3, nil
	default:
		return uint64(raw[0]), 1, nil
	}
}

// parseMinerID makes a best-effort attempt to split a miner client
// identifier into a name and version. Common formats include:
//
//	"SomeMiner/4.11.0"   -> ("SomeMiner", "4.11.0")
//	"MinerName-variant"  -> ("MinerName-variant", "")
//	"Some Miner 1.2.3"   -> ("Some Miner", "1.2.3")
func parseMinerID(id string) (string, string) {
	s := strings.TrimSpace(id)
	if s == "" {
		return "", ""
	}
	if idx := strings.Index(s, "/"); idx > 0 && idx < len(s)-1 {
		return s[:idx], s[idx+1:]
	}
	parts := strings.Fields(s)
	if len(parts) >= 2 {
		last := parts[len(parts)-1]
		hasDigit := false
		hasDot := false
		for i := 0; i < len(last); i++ {
			if last[i] >= '0' && last[i] <= '9' {
				hasDigit = true
			}
			if last[i] == '.' {
				hasDot = true
			}
		}
		if hasDigit && hasDot {
			return strings.Join(parts[:len(parts)-1], " "), last
		}
	}
	return s, ""
}

func stripWitnessData(raw []byte) ([]byte, bool, error) {
	if len(raw) < 6 {
		return nil, false, fmt.Errorf("tx too short: %d bytes", len(raw))
	}

	idx := 4 // skip version
	hasWitness := len(raw) > idx+1 && raw[idx] == 0x00 && raw[idx+1] != 0x00
	if hasWitness {
		idx += 2
	}

	inputsStart := idx

	vinCount, consumed, err := readVarInt(raw[idx:])
	if err != nil {
		return nil, false, fmt.Errorf("inputs count: %w", err)
	}
	idx += consumed

	for inIdx := uint64(0); inIdx < vinCount; inIdx++ {
		if idx+36 > len(raw) {
			return nil, false, fmt.Errorf("input %d truncated", inIdx)
		}
		idx += 36 // prevout hash + index

		scriptLen, used, err := readVarInt(raw[idx:])
		if err != nil {
			return nil, false, fmt.Errorf("input %d script len: %w", inIdx, err)
		}
		idx += used

		if idx+int(scriptLen)+4 > len(raw) {
			return nil, false, fmt.Errorf("input %d script truncated", inIdx)
		}
		idx += int(scriptLen) + 4 // script + sequence
	}

	voutCount, consumed, err := readVarInt(raw[idx:])
	if err != nil {
		return nil, false, fmt.Errorf("outputs count: %w", err)
	}
	idx += consumed

	for outIdx := uint64(0); outIdx < voutCount; outIdx++ {
		if idx+8 > len(raw) {
			return nil, false, fmt.Errorf("output %d truncated", outIdx)
		}
		idx += 8 // value

		pkLen, used, err := readVarInt(raw[idx:])
		if err != nil {
			return nil, false, fmt.Errorf("output %d script len: %w", outIdx, err)
		}
		idx += used

		if idx+int(pkLen) > len(raw) {
			return nil, false, fmt.Errorf("output %d script truncated", outIdx)
		}
		idx += int(pkLen)
	}

	witnessStart := idx

	if hasWitness {
		for inIdx := uint64(0); inIdx < vinCount; inIdx++ {
			itemCount, used, err := readVarInt(raw[idx:])
			if err != nil {
				return nil, false, fmt.Errorf("input %d witness count: %w", inIdx, err)
			}
			idx += used

			for itemIdx := uint64(0); itemIdx < itemCount; itemIdx++ {
				itemLen, n, err := readVarInt(raw[idx:])
				if err != nil {
					return nil, false, fmt.Errorf("input %d witness %d len: %w", inIdx, itemIdx, err)
				}
				idx += n

				if idx+int(itemLen) > len(raw) {
					return nil, false, fmt.Errorf("input %d witness %d truncated", inIdx, itemIdx)
				}
				idx += int(itemLen)
			}
		}
	}

	if idx+4 > len(raw) {
		return nil, false, fmt.Errorf("locktime truncated")
	}
	locktimeStart := idx
	idx += 4

	if idx != len(raw) {
		return nil, false, fmt.Errorf("unexpected trailing data: %d bytes", len(raw)-idx)
	}

	if !hasWitness {
		return raw, false, nil
	}

	// Rebuild transaction without marker/flag and witness data to compute legacy txid.
	stripped := make([]byte, 0, 4+(witnessStart-inputsStart)+4)
	stripped = append(stripped, raw[:4]...)
	stripped = append(stripped, raw[inputsStart:witnessStart]...)
	stripped = append(stripped, raw[locktimeStart:locktimeStart+4]...)

	return stripped, true, nil
}

// putVarInt encodes v into dst and returns the number of bytes written.
// Using the caller-provided buffer avoids per-call allocations.
func putVarInt(dst *[9]byte, v uint64) int {
	switch {
	case v < 0xfd:
		dst[0] = byte(v)
		return 1
	case v <= 0xffff:
		dst[0] = 0xfd
		dst[1] = byte(v)
		dst[2] = byte(v >> 8)
		return 3
	case v <= 0xffffffff:
		dst[0] = 0xfe
		dst[1] = byte(v)
		dst[2] = byte(v >> 8)
		dst[3] = byte(v >> 16)
		dst[4] = byte(v >> 24)
		return 5
	default:
		dst[0] = 0xff
		dst[1] = byte(v)
		dst[2] = byte(v >> 8)
		dst[3] = byte(v >> 16)
		dst[4] = byte(v >> 24)
		dst[5] = byte(v >> 32)
		dst[6] = byte(v >> 40)
		dst[7] = byte(v >> 48)
		dst[8] = byte(v >> 56)
		return 9
	}
}

// writeVarInt writes v to buf without heap allocation.
func writeVarInt(buf *bytes.Buffer, v uint64) {
	var tmp [9]byte
	n := putVarInt(&tmp, v)
	buf.Write(tmp[:n])
}

// appendVarInt appends the varint encoding of v to dst without allocating.
func appendVarInt(dst []byte, v uint64) []byte {
	var tmp [9]byte
	n := putVarInt(&tmp, v)
	return append(dst, tmp[:n]...)
}

func writeUint32LE(buf *bytes.Buffer, v uint32) {
	var tmp [4]byte
	tmp[0] = byte(v)
	tmp[1] = byte(v >> 8)
	tmp[2] = byte(v >> 16)
	tmp[3] = byte(v >> 24)
	buf.Write(tmp[:])
}

func writeUint64LE(buf *bytes.Buffer, v uint64) {
	var tmp [8]byte
	tmp[0] = byte(v)
	tmp[1] = byte(v >> 8)
	tmp[2] = byte(v >> 16)
	tmp[3] = byte(v >> 24)
	tmp[4] = byte(v >> 32)
	tmp[5] = byte(v >> 40)
	tmp[6] = byte(v >> 48)
	tmp[7] = byte(v >> 56)
	buf.Write(tmp[:])
}

// serializeCoinbaseTx builds a coinbase with segwit commitments per
// docs/protocols/bip-0141.mediawiki and docs/protocols/bip-0143.mediawiki.
// The trailing string in the scriptSig is the pool's coinbase message; if
// empty, a legacy "/nodeStratum/" tag is used for compatibility.
