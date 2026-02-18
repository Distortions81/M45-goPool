package main

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
)

var (
	hexNibbleLUT   [256]byte
	hexPairByteLUT [65536]uint16
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
	for i := range dst {
		v := hexPairByteLUT[int(src[i*2])<<8|int(src[i*2+1])]
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
	for i := range dst {
		v := hexPairByteLUT[int(src[i*2])<<8|int(src[i*2+1])]
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

func uint32ToBEHex(v uint32) string {
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], v)
	return hex.EncodeToString(buf[:])
}

func int32ToBEHex(v int32) string {
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], uint32(v))
	return hex.EncodeToString(buf[:])
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
