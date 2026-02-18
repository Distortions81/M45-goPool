package main

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
)

var (
	hexNibbleLUT   [256]byte
	hexPairByteLUT [65536]uint16
	hexPairLowerEncLUT [256]uint16
)

func init() {
	for i := range hexNibbleLUT {
		hexNibbleLUT[i] = 0xff
	}
	for c := byte('0'); c <= '9'; c++ {
		hexNibbleLUT[c] = c - '0'
	}
	for c := byte('a'); c <= 'f'; c++ {
		hexNibbleLUT[c] = c - 'a' + 10
	}
	for c := byte('A'); c <= 'F'; c++ {
		hexNibbleLUT[c] = c - 'A' + 10
	}

	for i := 0; i < 256; i++ {
		b := byte(i)
		hi := hexLowerDigits[b>>4]
		lo := hexLowerDigits[b&0x0f]
		hexPairLowerEncLUT[i] = uint16(hi)<<8 | uint16(lo)
	}

	// 2-byte LUT: maps (hi<<8)|lo => decoded byte, or 0x100 for invalid.
	for i := range hexPairByteLUT {
		hexPairByteLUT[i] = 0x100
	}
	for hi := 0; hi < 256; hi++ {
		h := hexNibbleLUT[hi]
		if h == 0xff {
			continue
		}
		for lo := 0; lo < 256; lo++ {
			l := hexNibbleLUT[lo]
			if l == 0xff {
				continue
			}
			hexPairByteLUT[(hi<<8)|lo] = uint16((h << 4) | l)
		}
	}
}

func decodeHexToFixedBytes(dst []byte, src string) error {
	if len(src) != len(dst)*2 {
		return fmt.Errorf("expected %d hex characters, got %d", len(dst)*2, len(src))
	}
	for i, j := 0, 0; i < len(dst); i, j = i+1, j+2 {
		v := hexPairByteLUT[int(src[j])<<8|int(src[j+1])]
		if v > 0xff {
			return fmt.Errorf("invalid hex digit in %q", src)
		}
		dst[i] = byte(v)
	}
	return nil
}

func decodeHexToFixedBytesBytes(dst []byte, src []byte) error {
	if len(src) != len(dst)*2 {
		return fmt.Errorf("expected %d hex characters, got %d", len(dst)*2, len(src))
	}
	for i, j := 0, 0; i < len(dst); i, j = i+1, j+2 {
		v := hexPairByteLUT[int(src[j])<<8|int(src[j+1])]
		if v > 0xff {
			return fmt.Errorf("invalid hex digit")
		}
		dst[i] = byte(v)
	}
	return nil
}

func encodeBytesToFixedHex(dst []byte, src []byte) error {
	if len(dst) != len(src)*2 {
		return fmt.Errorf("expected %d dst bytes, got %d", len(src)*2, len(dst))
	}
	hex.Encode(dst, src)
	return nil
}

func appendHexBytes(dst []byte, src []byte) []byte {
	n := len(dst)
	dst = append(dst, make([]byte, len(src)*2)...)
	hex.Encode(dst[n:], src)
	return dst
}

func decodeHex8To4(dst *[4]byte, src string) error {
	if len(src) != 8 {
		return fmt.Errorf("expected 8 hex characters, got %d", len(src))
	}
	v0 := hexPairByteLUT[int(src[0])<<8|int(src[1])]
	if v0 > 0xff {
		return fmt.Errorf("invalid hex digit in %q", src)
	}
	v1 := hexPairByteLUT[int(src[2])<<8|int(src[3])]
	if v1 > 0xff {
		return fmt.Errorf("invalid hex digit in %q", src)
	}
	v2 := hexPairByteLUT[int(src[4])<<8|int(src[5])]
	if v2 > 0xff {
		return fmt.Errorf("invalid hex digit in %q", src)
	}
	v3 := hexPairByteLUT[int(src[6])<<8|int(src[7])]
	if v3 > 0xff {
		return fmt.Errorf("invalid hex digit in %q", src)
	}
	dst[0] = byte(v0)
	dst[1] = byte(v1)
	dst[2] = byte(v2)
	dst[3] = byte(v3)
	return nil
}

func encode32ToHex64LowerUnrolled(dst *[64]byte, src *[32]byte) {
	v0 := hexPairLowerEncLUT[src[0]]
	dst[0] = byte(v0 >> 8)
	dst[1] = byte(v0)
	v1 := hexPairLowerEncLUT[src[1]]
	dst[2] = byte(v1 >> 8)
	dst[3] = byte(v1)
	v2 := hexPairLowerEncLUT[src[2]]
	dst[4] = byte(v2 >> 8)
	dst[5] = byte(v2)
	v3 := hexPairLowerEncLUT[src[3]]
	dst[6] = byte(v3 >> 8)
	dst[7] = byte(v3)
	v4 := hexPairLowerEncLUT[src[4]]
	dst[8] = byte(v4 >> 8)
	dst[9] = byte(v4)
	v5 := hexPairLowerEncLUT[src[5]]
	dst[10] = byte(v5 >> 8)
	dst[11] = byte(v5)
	v6 := hexPairLowerEncLUT[src[6]]
	dst[12] = byte(v6 >> 8)
	dst[13] = byte(v6)
	v7 := hexPairLowerEncLUT[src[7]]
	dst[14] = byte(v7 >> 8)
	dst[15] = byte(v7)
	v8 := hexPairLowerEncLUT[src[8]]
	dst[16] = byte(v8 >> 8)
	dst[17] = byte(v8)
	v9 := hexPairLowerEncLUT[src[9]]
	dst[18] = byte(v9 >> 8)
	dst[19] = byte(v9)
	v10 := hexPairLowerEncLUT[src[10]]
	dst[20] = byte(v10 >> 8)
	dst[21] = byte(v10)
	v11 := hexPairLowerEncLUT[src[11]]
	dst[22] = byte(v11 >> 8)
	dst[23] = byte(v11)
	v12 := hexPairLowerEncLUT[src[12]]
	dst[24] = byte(v12 >> 8)
	dst[25] = byte(v12)
	v13 := hexPairLowerEncLUT[src[13]]
	dst[26] = byte(v13 >> 8)
	dst[27] = byte(v13)
	v14 := hexPairLowerEncLUT[src[14]]
	dst[28] = byte(v14 >> 8)
	dst[29] = byte(v14)
	v15 := hexPairLowerEncLUT[src[15]]
	dst[30] = byte(v15 >> 8)
	dst[31] = byte(v15)
	v16 := hexPairLowerEncLUT[src[16]]
	dst[32] = byte(v16 >> 8)
	dst[33] = byte(v16)
	v17 := hexPairLowerEncLUT[src[17]]
	dst[34] = byte(v17 >> 8)
	dst[35] = byte(v17)
	v18 := hexPairLowerEncLUT[src[18]]
	dst[36] = byte(v18 >> 8)
	dst[37] = byte(v18)
	v19 := hexPairLowerEncLUT[src[19]]
	dst[38] = byte(v19 >> 8)
	dst[39] = byte(v19)
	v20 := hexPairLowerEncLUT[src[20]]
	dst[40] = byte(v20 >> 8)
	dst[41] = byte(v20)
	v21 := hexPairLowerEncLUT[src[21]]
	dst[42] = byte(v21 >> 8)
	dst[43] = byte(v21)
	v22 := hexPairLowerEncLUT[src[22]]
	dst[44] = byte(v22 >> 8)
	dst[45] = byte(v22)
	v23 := hexPairLowerEncLUT[src[23]]
	dst[46] = byte(v23 >> 8)
	dst[47] = byte(v23)
	v24 := hexPairLowerEncLUT[src[24]]
	dst[48] = byte(v24 >> 8)
	dst[49] = byte(v24)
	v25 := hexPairLowerEncLUT[src[25]]
	dst[50] = byte(v25 >> 8)
	dst[51] = byte(v25)
	v26 := hexPairLowerEncLUT[src[26]]
	dst[52] = byte(v26 >> 8)
	dst[53] = byte(v26)
	v27 := hexPairLowerEncLUT[src[27]]
	dst[54] = byte(v27 >> 8)
	dst[55] = byte(v27)
	v28 := hexPairLowerEncLUT[src[28]]
	dst[56] = byte(v28 >> 8)
	dst[57] = byte(v28)
	v29 := hexPairLowerEncLUT[src[29]]
	dst[58] = byte(v29 >> 8)
	dst[59] = byte(v29)
	v30 := hexPairLowerEncLUT[src[30]]
	dst[60] = byte(v30 >> 8)
	dst[61] = byte(v30)
	v31 := hexPairLowerEncLUT[src[31]]
	dst[62] = byte(v31 >> 8)
	dst[63] = byte(v31)
}

func hexEncode32LowerString(src *[32]byte) string {
	var out [64]byte
	encode32ToHex64LowerUnrolled(&out, src)
	return string(out[:])
}

func encode4ToHex8LowerString(src *[4]byte) string {
	var out [8]byte
	v0 := hexPairLowerEncLUT[src[0]]
	out[0] = byte(v0 >> 8)
	out[1] = byte(v0)
	v1 := hexPairLowerEncLUT[src[1]]
	out[2] = byte(v1 >> 8)
	out[3] = byte(v1)
	v2 := hexPairLowerEncLUT[src[2]]
	out[4] = byte(v2 >> 8)
	out[5] = byte(v2)
	v3 := hexPairLowerEncLUT[src[3]]
	out[6] = byte(v3 >> 8)
	out[7] = byte(v3)
	return string(out[:])
}

const hexZeros64 = "0000000000000000000000000000000000000000000000000000000000000000"

func formatBigIntHex64(v *big.Int) string {
	if v == nil || v.Sign() == 0 {
		return hexZeros64
	}
	if v.Sign() < 0 {
		// Targets should never be negative; keep behavior predictable.
		v = new(big.Int).Abs(v)
	}
	// Match %064x's "minimum width" semantics: if the number exceeds 256 bits,
	// return the full (unpadded) hex rather than truncating or panicking.
	if v.BitLen() > 256 {
		return hex.EncodeToString(v.Bytes())
	}
	var buf [32]byte
	v.FillBytes(buf[:])
	return hexEncode32LowerString(&buf)
}

func parseUint32BEHex(hexStr string) (uint32, error) {
	if len(hexStr) != 8 {
		return 0, fmt.Errorf("expected 8 hex characters, got %d", len(hexStr))
	}

	v0 := hexPairByteLUT[int(hexStr[0])<<8|int(hexStr[1])]
	if v0 > 0xff {
		return 0, fmt.Errorf("invalid hex digit in %q", hexStr)
	}
	v1 := hexPairByteLUT[int(hexStr[2])<<8|int(hexStr[3])]
	if v1 > 0xff {
		return 0, fmt.Errorf("invalid hex digit in %q", hexStr)
	}
	v2 := hexPairByteLUT[int(hexStr[4])<<8|int(hexStr[5])]
	if v2 > 0xff {
		return 0, fmt.Errorf("invalid hex digit in %q", hexStr)
	}
	v3 := hexPairByteLUT[int(hexStr[6])<<8|int(hexStr[7])]
	if v3 > 0xff {
		return 0, fmt.Errorf("invalid hex digit in %q", hexStr)
	}
	return uint32(byte(v0))<<24 | uint32(byte(v1))<<16 | uint32(byte(v2))<<8 | uint32(byte(v3)), nil
}

func parseUint32BEHexBytes(hexBytes []byte) (uint32, error) {
	if len(hexBytes) != 8 {
		return 0, fmt.Errorf("expected 8 hex characters, got %d", len(hexBytes))
	}

	v0 := hexPairByteLUT[int(hexBytes[0])<<8|int(hexBytes[1])]
	if v0 > 0xff {
		return 0, fmt.Errorf("invalid hex digit")
	}
	v1 := hexPairByteLUT[int(hexBytes[2])<<8|int(hexBytes[3])]
	if v1 > 0xff {
		return 0, fmt.Errorf("invalid hex digit")
	}
	v2 := hexPairByteLUT[int(hexBytes[4])<<8|int(hexBytes[5])]
	if v2 > 0xff {
		return 0, fmt.Errorf("invalid hex digit")
	}
	v3 := hexPairByteLUT[int(hexBytes[6])<<8|int(hexBytes[7])]
	if v3 > 0xff {
		return 0, fmt.Errorf("invalid hex digit")
	}
	return uint32(byte(v0))<<24 | uint32(byte(v1))<<16 | uint32(byte(v2))<<8 | uint32(byte(v3)), nil
}

const hexLowerDigits = "0123456789abcdef"

func uint32ToHex8Lower(v uint32) string {
	var buf [8]byte
	for i := 7; i >= 0; i-- {
		buf[i] = hexLowerDigits[v&0x0f]
		v >>= 4
	}
	return string(buf[:])
}

func uint32ToBEHex(v uint32) string {
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], v)
	return encode4ToHex8LowerString(&buf)
}

func int32ToBEHex(v int32) string {
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], uint32(v))
	return encode4ToHex8LowerString(&buf)
}

func hexToLEHex(src string) string {
	b, err := hex.DecodeString(src)
	if err != nil || len(b) == 0 {
		return src
	}
	// Treat input as 8 big-endian uint32 words, rewrite each as little-endian,
	// then reverse the full buffer.
	if len(b) != 32 {
		return hex.EncodeToString(reverseBytes(b))
	}
	var buf [32]byte
	copy(buf[:], b)
	for i := range 8 {
		j := i * 4
		v := uint32(buf[j])<<24 | uint32(buf[j+1])<<16 | uint32(buf[j+2])<<8 | uint32(buf[j+3])
		buf[j] = byte(v)
		buf[j+1] = byte(v >> 8)
		buf[j+2] = byte(v >> 16)
		buf[j+3] = byte(v >> 24)
	}
	return hex.EncodeToString(reverseBytes(buf[:]))
}

func versionMutable(mutable []string) bool {
	for _, m := range mutable {
		if strings.HasPrefix(m, "version/") {
			return true
		}
	}
	return false
}
