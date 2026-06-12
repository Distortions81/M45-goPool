package main

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"testing"
)

var (
	benchHexNibbleLUT       [256]byte
	benchHexPairLUT         [65536]uint16
	benchHexPairLowerEncLUT [256]uint16
	benchHexQuadLowerEncLUT [65536]uint32
	benchEncodeInputs       [1024][32]byte
)

func parseUint32BEHexBytesSwitch(hexBytes []byte) (uint32, bool) {
	if len(hexBytes) != 8 {
		return 0, false
	}
	var v uint32
	for i := range 8 {
		c := hexBytes[i]
		var nibble byte
		switch {
		case c >= '0' && c <= '9':
			nibble = c - '0'
		case c >= 'a' && c <= 'f':
			nibble = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			nibble = c - 'A' + 10
		default:
			return 0, false
		}
		v = (v << 4) | uint32(nibble)
	}
	return v, true
}

func init() {
	for i := range benchHexNibbleLUT {
		benchHexNibbleLUT[i] = 0xff
	}
	for c := byte('0'); c <= '9'; c++ {
		benchHexNibbleLUT[c] = c - '0'
	}
	for c := byte('a'); c <= 'f'; c++ {
		benchHexNibbleLUT[c] = c - 'a' + 10
	}
	for c := byte('A'); c <= 'F'; c++ {
		benchHexNibbleLUT[c] = c - 'A' + 10
	}

	for i := range benchHexPairLUT {
		benchHexPairLUT[i] = 0x100
	}
	for hi := 0; hi < 256; hi++ {
		h := benchHexNibbleLUT[hi]
		if h == 0xff {
			continue
		}
		for lo := 0; lo < 256; lo++ {
			l := benchHexNibbleLUT[lo]
			if l == 0xff {
				continue
			}
			benchHexPairLUT[(hi<<8)|lo] = uint16((h << 4) | l)
		}
	}

	for i := 0; i < 256; i++ {
		b := byte(i)
		hi := hexLowerDigits[b>>4]
		lo := hexLowerDigits[b&0x0f]
		benchHexPairLowerEncLUT[i] = uint16(hi)<<8 | uint16(lo)
	}

	for i := 0; i < 65536; i++ {
		hi := byte(i >> 8)
		lo := byte(i)
		hh := hexLowerDigits[hi>>4]
		hl := hexLowerDigits[hi&0x0f]
		lh := hexLowerDigits[lo>>4]
		ll := hexLowerDigits[lo&0x0f]
		benchHexQuadLowerEncLUT[i] = uint32(hh)<<24 | uint32(hl)<<16 | uint32(lh)<<8 | uint32(ll)
	}

	// Pre-generate inputs so the compiler can't constant-fold most of the work
	// (which can happen if only a single byte changes each iteration).
	var x uint32 = 0x9e3779b9
	for i := range benchEncodeInputs {
		for j := range benchEncodeInputs[i] {
			x ^= x << 13
			x ^= x >> 17
			x ^= x << 5
			benchEncodeInputs[i][j] = byte(x)
		}
	}
}

func decodeHexToFixedBytesBytesSingleLUT(dst []byte, src []byte) bool {
	if len(src) != len(dst)*2 {
		return false
	}
	for i := range dst {
		hi := benchHexNibbleLUT[src[i*2]]
		lo := benchHexNibbleLUT[src[i*2+1]]
		if hi == 0xff || lo == 0xff {
			return false
		}
		dst[i] = (hi << 4) | lo
	}
	return true
}

func decodeHexToFixedBytesBytesPairLUT(dst []byte, src []byte) bool {
	if len(src) != len(dst)*2 {
		return false
	}
	for i := range dst {
		v := benchHexPairLUT[int(src[i*2])<<8|int(src[i*2+1])]
		if v > 0xff {
			return false
		}
		dst[i] = byte(v)
	}
	return true
}

func benchNaiveHexNibble(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	default:
		return 0, false
	}
}

func encode32ToHex64Lower_Naive(dst *[64]byte, src *[32]byte) {
	for i, j := 0, 0; i < 32; i, j = i+1, j+2 {
		hi := src[i] >> 4
		if hi < 10 {
			dst[j] = '0' + hi
		} else {
			dst[j] = 'a' + hi - 10
		}

		lo := src[i] & 0x0f
		if lo < 10 {
			dst[j+1] = '0' + lo
		} else {
			dst[j+1] = 'a' + lo - 10
		}
	}
}

func decodeHex64To32Bytes_Naive(dst *[32]byte, src *[64]byte) bool {
	for i, j := 0, 0; i < 32; i, j = i+1, j+2 {
		hi, ok := benchNaiveHexNibble(src[j])
		if !ok {
			return false
		}
		lo, ok := benchNaiveHexNibble(src[j+1])
		if !ok {
			return false
		}
		dst[i] = (hi << 4) | lo
	}
	return true
}

func decodeHex64To32Bytes_UnrolledLUT(dst *[32]byte, src *[64]byte) bool {
	v0 := hexPairByteLUT[int(src[0])<<8|int(src[1])]
	if v0 > 0xff {
		return false
	}
	dst[0] = byte(v0)
	v1 := hexPairByteLUT[int(src[2])<<8|int(src[3])]
	if v1 > 0xff {
		return false
	}
	dst[1] = byte(v1)
	v2 := hexPairByteLUT[int(src[4])<<8|int(src[5])]
	if v2 > 0xff {
		return false
	}
	dst[2] = byte(v2)
	v3 := hexPairByteLUT[int(src[6])<<8|int(src[7])]
	if v3 > 0xff {
		return false
	}
	dst[3] = byte(v3)
	v4 := hexPairByteLUT[int(src[8])<<8|int(src[9])]
	if v4 > 0xff {
		return false
	}
	dst[4] = byte(v4)
	v5 := hexPairByteLUT[int(src[10])<<8|int(src[11])]
	if v5 > 0xff {
		return false
	}
	dst[5] = byte(v5)
	v6 := hexPairByteLUT[int(src[12])<<8|int(src[13])]
	if v6 > 0xff {
		return false
	}
	dst[6] = byte(v6)
	v7 := hexPairByteLUT[int(src[14])<<8|int(src[15])]
	if v7 > 0xff {
		return false
	}
	dst[7] = byte(v7)
	v8 := hexPairByteLUT[int(src[16])<<8|int(src[17])]
	if v8 > 0xff {
		return false
	}
	dst[8] = byte(v8)
	v9 := hexPairByteLUT[int(src[18])<<8|int(src[19])]
	if v9 > 0xff {
		return false
	}
	dst[9] = byte(v9)
	v10 := hexPairByteLUT[int(src[20])<<8|int(src[21])]
	if v10 > 0xff {
		return false
	}
	dst[10] = byte(v10)
	v11 := hexPairByteLUT[int(src[22])<<8|int(src[23])]
	if v11 > 0xff {
		return false
	}
	dst[11] = byte(v11)
	v12 := hexPairByteLUT[int(src[24])<<8|int(src[25])]
	if v12 > 0xff {
		return false
	}
	dst[12] = byte(v12)
	v13 := hexPairByteLUT[int(src[26])<<8|int(src[27])]
	if v13 > 0xff {
		return false
	}
	dst[13] = byte(v13)
	v14 := hexPairByteLUT[int(src[28])<<8|int(src[29])]
	if v14 > 0xff {
		return false
	}
	dst[14] = byte(v14)
	v15 := hexPairByteLUT[int(src[30])<<8|int(src[31])]
	if v15 > 0xff {
		return false
	}
	dst[15] = byte(v15)
	v16 := hexPairByteLUT[int(src[32])<<8|int(src[33])]
	if v16 > 0xff {
		return false
	}
	dst[16] = byte(v16)
	v17 := hexPairByteLUT[int(src[34])<<8|int(src[35])]
	if v17 > 0xff {
		return false
	}
	dst[17] = byte(v17)
	v18 := hexPairByteLUT[int(src[36])<<8|int(src[37])]
	if v18 > 0xff {
		return false
	}
	dst[18] = byte(v18)
	v19 := hexPairByteLUT[int(src[38])<<8|int(src[39])]
	if v19 > 0xff {
		return false
	}
	dst[19] = byte(v19)
	v20 := hexPairByteLUT[int(src[40])<<8|int(src[41])]
	if v20 > 0xff {
		return false
	}
	dst[20] = byte(v20)
	v21 := hexPairByteLUT[int(src[42])<<8|int(src[43])]
	if v21 > 0xff {
		return false
	}
	dst[21] = byte(v21)
	v22 := hexPairByteLUT[int(src[44])<<8|int(src[45])]
	if v22 > 0xff {
		return false
	}
	dst[22] = byte(v22)
	v23 := hexPairByteLUT[int(src[46])<<8|int(src[47])]
	if v23 > 0xff {
		return false
	}
	dst[23] = byte(v23)
	v24 := hexPairByteLUT[int(src[48])<<8|int(src[49])]
	if v24 > 0xff {
		return false
	}
	dst[24] = byte(v24)
	v25 := hexPairByteLUT[int(src[50])<<8|int(src[51])]
	if v25 > 0xff {
		return false
	}
	dst[25] = byte(v25)
	v26 := hexPairByteLUT[int(src[52])<<8|int(src[53])]
	if v26 > 0xff {
		return false
	}
	dst[26] = byte(v26)
	v27 := hexPairByteLUT[int(src[54])<<8|int(src[55])]
	if v27 > 0xff {
		return false
	}
	dst[27] = byte(v27)
	v28 := hexPairByteLUT[int(src[56])<<8|int(src[57])]
	if v28 > 0xff {
		return false
	}
	dst[28] = byte(v28)
	v29 := hexPairByteLUT[int(src[58])<<8|int(src[59])]
	if v29 > 0xff {
		return false
	}
	dst[29] = byte(v29)
	v30 := hexPairByteLUT[int(src[60])<<8|int(src[61])]
	if v30 > 0xff {
		return false
	}
	dst[30] = byte(v30)
	v31 := hexPairByteLUT[int(src[62])<<8|int(src[63])]
	if v31 > 0xff {
		return false
	}
	dst[31] = byte(v31)
	return true
}

func TestHex32FixedBenchmarkHelpersAgree(t *testing.T) {
	src := benchEncodeInputs[17]
	var fastHex, naiveHex [64]byte
	encode32ToHex64LowerUnrolled(&fastHex, &src)
	encode32ToHex64Lower_Naive(&naiveHex, &src)
	if fastHex != naiveHex {
		t.Fatalf("encode mismatch: fast=%q naive=%q", string(fastHex[:]), string(naiveHex[:]))
	}

	var fastBin, naiveBin [32]byte
	if !decodeHex64To32Bytes_UnrolledLUT(&fastBin, &fastHex) {
		t.Fatal("fast decode failed")
	}
	if !decodeHex64To32Bytes_Naive(&naiveBin, &fastHex) {
		t.Fatal("naive decode failed")
	}
	if fastBin != src {
		t.Fatalf("fast decode mismatch: got=%x want=%x", fastBin, src)
	}
	if naiveBin != src {
		t.Fatalf("naive decode mismatch: got=%x want=%x", naiveBin, src)
	}
}

func BenchmarkDecodeHexToFixedBytesBytes_32_SingleLUT(b *testing.B) {
	src := []byte("00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	var out [32]byte
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !decodeHexToFixedBytesBytesSingleLUT(out[:], src) {
			b.Fatal("decode failed")
		}
	}
}

func BenchmarkDecodeHexToFixedBytesBytes_32_SingleLUT_Upper(b *testing.B) {
	src := []byte("00112233445566778899AABBCCDDEEFF00112233445566778899AABBCCDDEEFF")
	var out [32]byte
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !decodeHexToFixedBytesBytesSingleLUT(out[:], src) {
			b.Fatal("decode failed")
		}
	}
}

func BenchmarkDecodeHexToFixedBytesBytes_32_PairLUT(b *testing.B) {
	src := []byte("00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	var out [32]byte
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !decodeHexToFixedBytesBytesPairLUT(out[:], src) {
			b.Fatal("decode failed")
		}
	}
}

func BenchmarkDecodeHexToFixedBytesBytes_32_PairLUT_Upper(b *testing.B) {
	src := []byte("00112233445566778899AABBCCDDEEFF00112233445566778899AABBCCDDEEFF")
	var out [32]byte
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !decodeHexToFixedBytesBytesPairLUT(out[:], src) {
			b.Fatal("decode failed")
		}
	}
}

func BenchmarkDecodeHexToFixedBytesBytes_32_Std(b *testing.B) {
	src := []byte("00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	var out [32]byte
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := hex.Decode(out[:], src); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecodeHexToFixedBytesBytes_32_Std_Upper(b *testing.B) {
	src := []byte("00112233445566778899AABBCCDDEEFF00112233445566778899AABBCCDDEEFF")
	var out [32]byte
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := hex.Decode(out[:], src); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseUint32BEHexBytes_LUT(b *testing.B) {
	src := []byte("1d00ffff")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := parseUint32BEHexBytes(src); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseUint32BEHexBytes_Switch(b *testing.B) {
	src := []byte("1d00ffff")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := parseUint32BEHexBytesSwitch(src); !ok {
			b.Fatal("parse failed")
		}
	}
}

func BenchmarkParseUint32BEHexBytes_LUT_Upper(b *testing.B) {
	src := []byte("1D00FFFF")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := parseUint32BEHexBytes(src); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseUint32BEHexBytes_Switch_Upper(b *testing.B) {
	src := []byte("1D00FFFF")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := parseUint32BEHexBytesSwitch(src); !ok {
			b.Fatal("parse failed")
		}
	}
}

func BenchmarkEncodeBytesToFixedHex_32_Std(b *testing.B) {
	var out [64]byte
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		src := &benchEncodeInputs[i&1023]
		hex.Encode(out[:], src[:])
	}
	benchHexEncodeSink = out
}

var benchHexEncodeSink [64]byte
var benchHexDecodeSink [32]byte
var benchHexEncodeStringSink string

func BenchmarkHex32Fixed_BinaryToHex_UnrolledLUT(b *testing.B) {
	var out [64]byte
	b.ReportAllocs()
	b.SetBytes(32)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		src := &benchEncodeInputs[i&1023]
		encode32ToHex64LowerUnrolled(&out, src)
	}
	benchHexEncodeSink = out
}

func BenchmarkHex32Fixed_BinaryToHex_Naive(b *testing.B) {
	var out [64]byte
	b.ReportAllocs()
	b.SetBytes(32)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		src := &benchEncodeInputs[i&1023]
		encode32ToHex64Lower_Naive(&out, src)
	}
	benchHexEncodeSink = out
}

func BenchmarkHex32Fixed_HexToBinary_UnrolledLUT(b *testing.B) {
	var src [64]byte
	copy(src[:], "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	var out [32]byte
	b.ReportAllocs()
	b.SetBytes(32)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !decodeHex64To32Bytes_UnrolledLUT(&out, &src) {
			b.Fatal("decode failed")
		}
	}
	benchHexDecodeSink = out
}

func BenchmarkHex32Fixed_HexToBinary_Naive(b *testing.B) {
	var src [64]byte
	copy(src[:], "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	var out [32]byte
	b.ReportAllocs()
	b.SetBytes(32)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !decodeHex64To32Bytes_Naive(&out, &src) {
			b.Fatal("decode failed")
		}
	}
	benchHexDecodeSink = out
}

func encode32ToHex64Lower_LUTLoop(dst *[64]byte, src *[32]byte) {
	for i, j := 0, 0; i < 32; i, j = i+1, j+2 {
		v := benchHexPairLowerEncLUT[src[i]]
		dst[j] = byte(v >> 8)
		dst[j+1] = byte(v)
	}
}

func encode32ToHex64Lower_Unrolled(dst *[64]byte, src *[32]byte) {
	v0 := benchHexPairLowerEncLUT[src[0]]
	dst[0] = byte(v0 >> 8)
	dst[1] = byte(v0)
	v1 := benchHexPairLowerEncLUT[src[1]]
	dst[2] = byte(v1 >> 8)
	dst[3] = byte(v1)
	v2 := benchHexPairLowerEncLUT[src[2]]
	dst[4] = byte(v2 >> 8)
	dst[5] = byte(v2)
	v3 := benchHexPairLowerEncLUT[src[3]]
	dst[6] = byte(v3 >> 8)
	dst[7] = byte(v3)
	v4 := benchHexPairLowerEncLUT[src[4]]
	dst[8] = byte(v4 >> 8)
	dst[9] = byte(v4)
	v5 := benchHexPairLowerEncLUT[src[5]]
	dst[10] = byte(v5 >> 8)
	dst[11] = byte(v5)
	v6 := benchHexPairLowerEncLUT[src[6]]
	dst[12] = byte(v6 >> 8)
	dst[13] = byte(v6)
	v7 := benchHexPairLowerEncLUT[src[7]]
	dst[14] = byte(v7 >> 8)
	dst[15] = byte(v7)
	v8 := benchHexPairLowerEncLUT[src[8]]
	dst[16] = byte(v8 >> 8)
	dst[17] = byte(v8)
	v9 := benchHexPairLowerEncLUT[src[9]]
	dst[18] = byte(v9 >> 8)
	dst[19] = byte(v9)
	v10 := benchHexPairLowerEncLUT[src[10]]
	dst[20] = byte(v10 >> 8)
	dst[21] = byte(v10)
	v11 := benchHexPairLowerEncLUT[src[11]]
	dst[22] = byte(v11 >> 8)
	dst[23] = byte(v11)
	v12 := benchHexPairLowerEncLUT[src[12]]
	dst[24] = byte(v12 >> 8)
	dst[25] = byte(v12)
	v13 := benchHexPairLowerEncLUT[src[13]]
	dst[26] = byte(v13 >> 8)
	dst[27] = byte(v13)
	v14 := benchHexPairLowerEncLUT[src[14]]
	dst[28] = byte(v14 >> 8)
	dst[29] = byte(v14)
	v15 := benchHexPairLowerEncLUT[src[15]]
	dst[30] = byte(v15 >> 8)
	dst[31] = byte(v15)
	v16 := benchHexPairLowerEncLUT[src[16]]
	dst[32] = byte(v16 >> 8)
	dst[33] = byte(v16)
	v17 := benchHexPairLowerEncLUT[src[17]]
	dst[34] = byte(v17 >> 8)
	dst[35] = byte(v17)
	v18 := benchHexPairLowerEncLUT[src[18]]
	dst[36] = byte(v18 >> 8)
	dst[37] = byte(v18)
	v19 := benchHexPairLowerEncLUT[src[19]]
	dst[38] = byte(v19 >> 8)
	dst[39] = byte(v19)
	v20 := benchHexPairLowerEncLUT[src[20]]
	dst[40] = byte(v20 >> 8)
	dst[41] = byte(v20)
	v21 := benchHexPairLowerEncLUT[src[21]]
	dst[42] = byte(v21 >> 8)
	dst[43] = byte(v21)
	v22 := benchHexPairLowerEncLUT[src[22]]
	dst[44] = byte(v22 >> 8)
	dst[45] = byte(v22)
	v23 := benchHexPairLowerEncLUT[src[23]]
	dst[46] = byte(v23 >> 8)
	dst[47] = byte(v23)
	v24 := benchHexPairLowerEncLUT[src[24]]
	dst[48] = byte(v24 >> 8)
	dst[49] = byte(v24)
	v25 := benchHexPairLowerEncLUT[src[25]]
	dst[50] = byte(v25 >> 8)
	dst[51] = byte(v25)
	v26 := benchHexPairLowerEncLUT[src[26]]
	dst[52] = byte(v26 >> 8)
	dst[53] = byte(v26)
	v27 := benchHexPairLowerEncLUT[src[27]]
	dst[54] = byte(v27 >> 8)
	dst[55] = byte(v27)
	v28 := benchHexPairLowerEncLUT[src[28]]
	dst[56] = byte(v28 >> 8)
	dst[57] = byte(v28)
	v29 := benchHexPairLowerEncLUT[src[29]]
	dst[58] = byte(v29 >> 8)
	dst[59] = byte(v29)
	v30 := benchHexPairLowerEncLUT[src[30]]
	dst[60] = byte(v30 >> 8)
	dst[61] = byte(v30)
	v31 := benchHexPairLowerEncLUT[src[31]]
	dst[62] = byte(v31 >> 8)
	dst[63] = byte(v31)
}

func encode32ToHex64Lower_2ByteLUTLoop(dst *[64]byte, src *[32]byte) {
	for i, j := 0, 0; i < 32; i, j = i+2, j+4 {
		k := int(src[i])<<8 | int(src[i+1])
		v := benchHexQuadLowerEncLUT[k]
		dst[j] = byte(v >> 24)
		dst[j+1] = byte(v >> 16)
		dst[j+2] = byte(v >> 8)
		dst[j+3] = byte(v)
	}
}

func BenchmarkEncode32ToHex64Lower_LUTLoop(b *testing.B) {
	var out [64]byte
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		src := &benchEncodeInputs[i&1023]
		encode32ToHex64Lower_LUTLoop(&out, src)
	}
	benchHexEncodeSink = out
}

func BenchmarkEncode32ToHex64Lower_2ByteLUTLoop(b *testing.B) {
	var out [64]byte
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		src := &benchEncodeInputs[i&1023]
		encode32ToHex64Lower_2ByteLUTLoop(&out, src)
	}
	benchHexEncodeSink = out
}

func BenchmarkEncode32ToHex64Lower_Unrolled(b *testing.B) {
	var out [64]byte
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		src := &benchEncodeInputs[i&1023]
		encode32ToHex64Lower_Unrolled(&out, src)
	}
	benchHexEncodeSink = out
}

func BenchmarkEncodeToString_32_Std(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	var s string
	for i := 0; i < b.N; i++ {
		src := &benchEncodeInputs[i&1023]
		s = hex.EncodeToString(src[:])
	}
	benchHexEncodeStringSink = s
}

func BenchmarkEncodeToString_32_StdStackBuf(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	var s string
	var out [64]byte
	for i := 0; i < b.N; i++ {
		src := &benchEncodeInputs[i&1023]
		hex.Encode(out[:], src[:])
		s = string(out[:])
	}
	benchHexEncodeStringSink = s
}

func BenchmarkEncodeToString_32_Unrolled(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	var s string
	var out [64]byte
	for i := 0; i < b.N; i++ {
		src := &benchEncodeInputs[i&1023]
		encode32ToHex64Lower_Unrolled(&out, src)
		s = string(out[:])
	}
	benchHexEncodeStringSink = s
}

func BenchmarkDecodeHexToFixedBytesBytes_32_PoolPairLUT(b *testing.B) {
	src := []byte("00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	var out [32]byte
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := decodeHexToFixedBytesBytes(out[:], src); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecodeHex8To4_String_PairLUT(b *testing.B) {
	src := "deadbeef"
	var out [4]byte
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := decodeHex8To4(&out, src); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecodeHexToFixedBytesBytes_4_PoolPairLUT(b *testing.B) {
	src := []byte("deadbeef")
	var out [4]byte
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := decodeHexToFixedBytesBytes(out[:], src); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecodeHexToFixedBytesBytes_4_Std(b *testing.B) {
	src := []byte("deadbeef")
	var out [4]byte
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := hex.Decode(out[:], src); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkFormatBigInt_064x_Sprintf(b *testing.B) {
	targetBytes := [32]byte{
		0x12, 0x34, 0x56, 0x78,
		0x9a, 0xbc, 0xde, 0xf0,
		0x10, 0x32, 0x54, 0x76,
		0x98, 0xba, 0xdc, 0xfe,
		0x01, 0x23, 0x45, 0x67,
		0x89, 0xab, 0xcd, 0xef,
		0xff, 0xee, 0xdd, 0xcc,
		0xbb, 0xaa, 0x99, 0x88,
	}
	target := new(big.Int).SetBytes(targetBytes[:])

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = fmt.Sprintf("%064x", target)
	}
}

func BenchmarkFormatBigInt_32_FillBytesHexEncodeToString(b *testing.B) {
	targetBytes := [32]byte{
		0x12, 0x34, 0x56, 0x78,
		0x9a, 0xbc, 0xde, 0xf0,
		0x10, 0x32, 0x54, 0x76,
		0x98, 0xba, 0xdc, 0xfe,
		0x01, 0x23, 0x45, 0x67,
		0x89, 0xab, 0xcd, 0xef,
		0xff, 0xee, 0xdd, 0xcc,
		0xbb, 0xaa, 0x99, 0x88,
	}
	target := new(big.Int).SetBytes(targetBytes[:])
	var buf [32]byte

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		target.FillBytes(buf[:])
		_ = hex.EncodeToString(buf[:])
	}
}
