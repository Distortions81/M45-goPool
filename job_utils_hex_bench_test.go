package main

import (
	"encoding/hex"
	"testing"
)

var (
	benchHexNibbleLUT [256]byte
	benchHexPairLUT   [65536]uint16
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
	var src [32]byte
	for i := range src {
		src[i] = byte(i)
	}
	var out [64]byte
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hex.Encode(out[:], src[:])
	}
}
