package main

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
)

func decodeHexToFixedBytes(dst []byte, src string) error {
	if len(src) != len(dst)*2 {
		return fmt.Errorf("expected %d hex characters, got %d", len(dst)*2, len(src))
	}
	for i := range dst {
		hi := src[i*2]
		lo := src[i*2+1]
		var hn, ln byte
		switch {
		case hi >= '0' && hi <= '9':
			hn = hi - '0'
		case hi >= 'a' && hi <= 'f':
			hn = hi - 'a' + 10
		case hi >= 'A' && hi <= 'F':
			hn = hi - 'A' + 10
		default:
			return fmt.Errorf("invalid hex digit %q in %q", hi, src)
		}
		switch {
		case lo >= '0' && lo <= '9':
			ln = lo - '0'
		case lo >= 'a' && lo <= 'f':
			ln = lo - 'a' + 10
		case lo >= 'A' && lo <= 'F':
			ln = lo - 'A' + 10
		default:
			return fmt.Errorf("invalid hex digit %q in %q", lo, src)
		}
		dst[i] = (hn << 4) | ln
	}
	return nil
}

func parseUint32BEHex(hexStr string) (uint32, error) {
	if len(hexStr) != 8 {
		return 0, fmt.Errorf("expected 8 hex characters, got %d", len(hexStr))
	}
	var v uint32
	for i := range 8 {
		c := hexStr[i]
		var nibble byte
		switch {
		case c >= '0' && c <= '9':
			nibble = c - '0'
		case c >= 'a' && c <= 'f':
			nibble = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			nibble = c - 'A' + 10
		default:
			return 0, fmt.Errorf("invalid hex digit %q in %q", c, hexStr)
		}
		v = (v << 4) | uint32(nibble)
	}
	return v, nil
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
