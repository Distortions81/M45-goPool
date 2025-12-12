package main

import (
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/btcsuite/btcd/chaincfg"
)

// scriptForAddress performs local validation of a Bitcoin address for the given
// network and returns the corresponding scriptPubKey. It supports base58
// (P2PKH/P2SH) and bech32/bech32m segwit destinations.
func scriptForAddress(addr string, params *chaincfg.Params) ([]byte, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" || params == nil {
		return nil, errors.New("empty address")
	}

	// Try bech32/segwit first.
	if strings.Contains(addr, "1") {
		hrp := strings.ToLower(strings.SplitN(addr, "1", 2)[0])
		if hrp == strings.ToLower(params.Bech32HRPSegwit) {
			return scriptForSegwitAddress(addr, params)
		}
	}

	// Fallback to base58 P2PKH/P2SH.
	return scriptForBase58Address(addr, params)
}

// ----- Base58 + Base58Check decoding -----

const b58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

var b58Indexes [256]int8

func init() {
	for i := range b58Indexes {
		b58Indexes[i] = -1
	}
	for i := 0; i < len(b58Alphabet); i++ {
		b58Indexes[b58Alphabet[i]] = int8(i)
	}
}

func base58Decode(s string) ([]byte, error) {
	if s == "" {
		return nil, errors.New("empty base58 string")
	}
	num := big.NewInt(0)
	radix := big.NewInt(58)
	for i := 0; i < len(s); i++ {
		ch := s[i]
		idx := b58Indexes[ch]
		if idx == -1 {
			return nil, fmt.Errorf("invalid base58 character %q", ch)
		}
		num.Mul(num, radix)
		num.Add(num, big.NewInt(int64(idx)))
	}
	// Convert big.Int to bytes.
	decoded := num.Bytes()
	// Add leading zeroes for each leading '1'.
	nLeading := 0
	for i := 0; i < len(s) && s[i] == '1'; i++ {
		nLeading++
	}
	if nLeading > 0 {
		decoded = append(make([]byte, nLeading), decoded...)
	}
	return decoded, nil
}

func base58CheckDecode(s string) (version byte, payload []byte, err error) {
	raw, err := base58Decode(s)
	if err != nil {
		return 0, nil, err
	}
	if len(raw) < 4 {
		return 0, nil, errors.New("base58check data too short")
	}
	data := raw[:len(raw)-4]
	checksum := raw[len(raw)-4:]
	h := doubleSHA256(data)
	if len(h) < 4 || !equalBytes(checksum, h[:4]) {
		return 0, nil, errors.New("base58check checksum mismatch")
	}
	if len(data) < 1 {
		return 0, nil, errors.New("base58check missing version byte")
	}
	return data[0], data[1:], nil
}

func scriptForBase58Address(addr string, params *chaincfg.Params) ([]byte, error) {
	ver, payload, err := base58CheckDecode(addr)
	if err != nil {
		return nil, fmt.Errorf("decode base58: %w", err)
	}
	switch ver {
	case params.PubKeyHashAddrID:
		// OP_DUP OP_HASH160 <20> <pubKeyHash> OP_EQUALVERIFY OP_CHECKSIG
		if len(payload) != 20 {
			return nil, fmt.Errorf("p2pkh payload length %d != 20", len(payload))
		}
		out := make([]byte, 0, 25)
		out = append(out, 0x76, 0xa9, 0x14)
		out = append(out, payload...)
		out = append(out, 0x88, 0xac)
		return out, nil
	case params.ScriptHashAddrID:
		// OP_HASH160 <20> <scriptHash> OP_EQUAL
		if len(payload) != 20 {
			return nil, fmt.Errorf("p2sh payload length %d != 20", len(payload))
		}
		out := make([]byte, 0, 23)
		out = append(out, 0xa9, 0x14)
		out = append(out, payload...)
		out = append(out, 0x87)
		return out, nil
	default:
		return nil, fmt.Errorf("unknown base58 version 0x%02x for this network", ver)
	}
}

// ----- Bech32 / Segwit decoding -----

const (
	bech32Charset = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"
)

var bech32CharsetRev [128]int8

func init() {
	for i := range bech32CharsetRev {
		bech32CharsetRev[i] = -1
	}
	for i := 0; i < len(bech32Charset); i++ {
		bech32CharsetRev[bech32Charset[i]] = int8(i)
	}
}

func bech32Polymod(values []byte) uint32 {
	const (
		g0 = 0x3b6a57b2
		g1 = 0x26508e6d
		g2 = 0x1ea119fa
		g3 = 0x3d4233dd
		g4 = 0x2a1462b3
	)
	chk := uint32(1)
	for _, v := range values {
		b := chk >> 25
		chk = (chk&0x1ffffff)<<5 ^ uint32(v)
		if b&1 != 0 {
			chk ^= g0
		}
		if b&2 != 0 {
			chk ^= g1
		}
		if b&4 != 0 {
			chk ^= g2
		}
		if b&8 != 0 {
			chk ^= g3
		}
		if b&16 != 0 {
			chk ^= g4
		}
	}
	return chk
}

func bech32HrpExpand(hrp string) []byte {
	hrpBytes := []byte(strings.ToLower(hrp))
	out := make([]byte, 0, len(hrpBytes)*2+1)
	for _, b := range hrpBytes {
		out = append(out, b>>5)
	}
	out = append(out, 0)
	for _, b := range hrpBytes {
		out = append(out, b&31)
	}
	return out
}

func bech32VerifyChecksum(hrp string, data []byte) bool {
	values := append(bech32HrpExpand(hrp), data...)
	return bech32Polymod(values) == 1
}

func bech32Decode(s string) (hrp string, data []byte, err error) {
	if len(s) < 8 || len(s) > 90 {
		return "", nil, errors.New("bech32 string length out of range")
	}
	s = strings.ToLower(s)
	pos := strings.LastIndexByte(s, '1')
	if pos < 1 || pos+7 > len(s) {
		return "", nil, errors.New("invalid bech32 separator position")
	}
	hrp = s[:pos]
	dataPart := s[pos+1:]
	data = make([]byte, len(dataPart))
	for i := 0; i < len(dataPart); i++ {
		c := dataPart[i]
		if c < 33 || c >= 128 {
			return "", nil, errors.New("invalid bech32 character range")
		}
		v := bech32CharsetRev[c]
		if v == -1 {
			return "", nil, fmt.Errorf("invalid bech32 character %q", c)
		}
		data[i] = byte(v)
	}
	if !bech32VerifyChecksum(hrp, data) {
		return "", nil, errors.New("invalid bech32 checksum")
	}
	return hrp, data[:len(data)-6], nil
}

// convertBits converts data between bit groups, used for segwit programs.
func convertBits(data []byte, fromBits, toBits uint, pad bool) ([]byte, error) {
	var ret []byte
	var acc uint
	var bits uint
	maxv := uint((1 << toBits) - 1)
	for _, value := range data {
		if value>>fromBits != 0 {
			return nil, errors.New("invalid value for convertBits")
		}
		acc = (acc << fromBits) | uint(value)
		bits += fromBits
		for bits >= toBits {
			bits -= toBits
			ret = append(ret, byte((acc>>bits)&maxv))
		}
	}
	if pad {
		if bits > 0 {
			ret = append(ret, byte((acc<<(toBits-bits))&maxv))
		}
	} else if bits >= fromBits || ((acc<<(toBits-bits))&maxv) != 0 {
		return nil, errors.New("invalid padding in convertBits")
	}
	return ret, nil
}

func scriptForSegwitAddress(addr string, params *chaincfg.Params) ([]byte, error) {
	hrp, data, err := bech32Decode(addr)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(hrp, params.Bech32HRPSegwit) {
		return nil, fmt.Errorf("wrong bech32 hrp %q for this network", hrp)
	}
	if len(data) == 0 {
		return nil, errors.New("empty segwit data")
	}
	ver := data[0]
	prog, err := convertBits(data[1:], 5, 8, false)
	if err != nil {
		return nil, fmt.Errorf("convertBits: %w", err)
	}
	if len(prog) < 2 || len(prog) > 40 {
		return nil, fmt.Errorf("invalid segwit program length %d", len(prog))
	}
	if ver > 16 {
		return nil, fmt.Errorf("invalid segwit version %d", ver)
	}

	// Build script: OP_<ver> <len> <program>
	script := make([]byte, 0, 2+len(prog))
	if ver == 0 {
		script = append(script, 0x00)
	} else {
		script = append(script, 0x50+ver)
	}
	script = append(script, byte(len(prog)))
	script = append(script, prog...)
	return script, nil
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// base58CheckEncode encodes version+payload using Base58Check with a double
// SHA256 checksum, mirroring Bitcoin's address encoding.
func base58CheckEncode(version byte, payload []byte) (string, error) {
	if payload == nil {
		return "", errors.New("nil payload")
	}
	data := append([]byte{version}, payload...)
	check := doubleSHA256(data)
	if len(check) < 4 {
		return "", errors.New("checksum too short")
	}
	full := append(data, check[:4]...)

	// Convert to big.Int for base58 encoding.
	num := big.NewInt(0)
	for _, b := range full {
		num.Mul(num, big.NewInt(58))
		num.Add(num, big.NewInt(int64(b)))
	}

	// Encode base58.
	var out []byte
	for num.Sign() > 0 {
		mod := new(big.Int)
		num.DivMod(num, big.NewInt(58), mod)
		out = append(out, b58Alphabet[mod.Int64()])
	}
	// Leading zero bytes become leading '1' characters.
	for _, b := range full {
		if b != 0x00 {
			break
		}
		out = append(out, '1')
	}
	// Reverse output.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out), nil
}

// bech32CreateChecksum computes the checksum for bech32 encoding.
func bech32CreateChecksum(hrp string, data []byte) []byte {
	values := append(bech32HrpExpand(hrp), data...)
	polymod := bech32Polymod(append(values, []byte{0, 0, 0, 0, 0, 0}...))
	var checksum [6]byte
	for i := 0; i < 6; i++ {
		checksum[i] = byte((polymod >> uint(5*(5-i))) & 31)
	}
	return checksum[:]
}

// bech32Encode builds a bech32 string from HRP and data.
func bech32Encode(hrp string, data []byte) (string, error) {
	if hrp == "" {
		return "", errors.New("empty hrp")
	}
	checksum := bech32CreateChecksum(hrp, data)
	combined := append(data, checksum...)
	var out strings.Builder
	out.WriteString(strings.ToLower(hrp))
	out.WriteByte('1')
	for _, v := range combined {
		if int(v) >= len(bech32Charset) {
			return "", errors.New("invalid bech32 value")
		}
		out.WriteByte(bech32Charset[v])
	}
	return out.String(), nil
}

// scriptToAddress attempts to derive a human-readable Bitcoin address from a
// standard scriptPubKey for the given network (P2PKH, P2SH, and common
// segwit forms). On failure it returns an empty string.
func scriptToAddress(script []byte, params *chaincfg.Params) string {
	if len(script) == 0 || params == nil {
		return ""
	}

	// P2PKH: OP_DUP OP_HASH160 <20> <hash> OP_EQUALVERIFY OP_CHECKSIG
	if len(script) == 25 &&
		script[0] == 0x76 && script[1] == 0xa9 &&
		script[2] == 0x14 && script[23] == 0x88 && script[24] == 0xac {
		hash := script[3:23]
		if addr, err := base58CheckEncode(params.PubKeyHashAddrID, hash); err == nil {
			return addr
		}
	}

	// P2SH: OP_HASH160 <20> <hash> OP_EQUAL
	if len(script) == 23 &&
		script[0] == 0xa9 && script[1] == 0x14 && script[22] == 0x87 {
		hash := script[2:22]
		if addr, err := base58CheckEncode(params.ScriptHashAddrID, hash); err == nil {
			return addr
		}
	}

	// Segwit: OP_n <program>
	if len(script) >= 4 && script[1] >= 0x02 && script[1] <= 0x28 {
		var ver byte
		switch script[0] {
		case 0x00:
			ver = 0
		default:
			if script[0] >= 0x51 && script[0] <= 0x60 {
				ver = script[0] - 0x50
			} else {
				return ""
			}
		}
		progLen := int(script[1])
		if 2+progLen > len(script) {
			return ""
		}
		prog := script[2 : 2+progLen]
		data, err := convertBits(append([]byte{ver}, prog...), 8, 5, true)
		if err != nil {
			return ""
		}
		addr, err := bech32Encode(params.Bech32HRPSegwit, data)
		if err != nil {
			return ""
		}
		return addr
	}

	return ""
}
