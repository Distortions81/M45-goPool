package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"sync"
)

const sv2PlaintextFramePayloadMax = 64*1024 - 6

const sv2ExtensionTypeNone uint16 = 0x0000
const sv2ExtensionTypeCompat0800 uint16 = 0x0080

var sv2UnknownExtensionTypesSeen sync.Map

func sv2IsSupportedExtensionType(ext uint16) bool {
	switch ext {
	case sv2ExtensionTypeNone, sv2ExtensionTypeCompat0800:
		return true
	default:
		return false
	}
}

func sv2ObserveExtensionType(ext uint16, transport string) {
	if sv2IsSupportedExtensionType(ext) {
		return
	}
	if _, loaded := sv2UnknownExtensionTypesSeen.LoadOrStore(ext, struct{}{}); loaded {
		return
	}
	logger.Warn("sv2 frame extension type not recognized; accepting for compatibility",
		"component", "stratum",
		"kind", "sv2",
		"transport", transport,
		"extension_type", fmt.Sprintf("0x%04x", ext),
		"todo", "verify extension semantics against sv2 extension registry",
	)
}

// sv2ReadFrame reads a 6-byte SV2 frame header and payload.
// Header: extension_type[u16 LE] + msg_type[u8] + payload_length[u24 LE]
func sv2ReadFrame(r io.Reader) (msgType byte, payload []byte, err error) {
	var hdr [6]byte
	if _, err = io.ReadFull(r, hdr[:]); err != nil {
		return
	}
	ext := binary.LittleEndian.Uint16(hdr[0:2])
	sv2ObserveExtensionType(ext, "plaintext")
	msgType = hdr[2]
	payLen := uint32(hdr[3]) | uint32(hdr[4])<<8 | uint32(hdr[5])<<16
	if payLen > sv2PlaintextFramePayloadMax {
		return 0, nil, fmt.Errorf("sv2 frame payload too large: %d", payLen)
	}
	if payLen > 0 {
		payload = make([]byte, payLen)
		_, err = io.ReadFull(r, payload)
	}
	return
}

// sv2WriteFrame writes an SV2 frame with extension_type=0.
func sv2WriteFrame(w io.Writer, msgType byte, payload []byte) error {
	payLen := len(payload)
	if payLen > sv2PlaintextFramePayloadMax {
		return fmt.Errorf("sv2 frame payload too large: %d", payLen)
	}
	hdr := [6]byte{
		0, 0, // extension_type = 0
		msgType,
		byte(payLen), byte(payLen >> 8), byte(payLen >> 16),
	}
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(payload) > 0 {
		_, err := w.Write(payload)
		return err
	}
	return nil
}

// --- Read helpers ---

func sv2ReadU8(b []byte, off *int) (uint8, error) {
	if *off+1 > len(b) {
		return 0, io.ErrUnexpectedEOF
	}
	v := b[*off]
	*off++
	return v, nil
}

func sv2ReadU16(b []byte, off *int) (uint16, error) {
	if *off+2 > len(b) {
		return 0, io.ErrUnexpectedEOF
	}
	v := binary.LittleEndian.Uint16(b[*off:])
	*off += 2
	return v, nil
}

func sv2ReadU32(b []byte, off *int) (uint32, error) {
	if *off+4 > len(b) {
		return 0, io.ErrUnexpectedEOF
	}
	v := binary.LittleEndian.Uint32(b[*off:])
	*off += 4
	return v, nil
}

func sv2ReadU64(b []byte, off *int) (uint64, error) {
	if *off+8 > len(b) {
		return 0, io.ErrUnexpectedEOF
	}
	v := binary.LittleEndian.Uint64(b[*off:])
	*off += 8
	return v, nil
}

func sv2ReadU256(b []byte, off *int) ([32]byte, error) {
	if *off+32 > len(b) {
		return [32]byte{}, io.ErrUnexpectedEOF
	}
	var v [32]byte
	copy(v[:], b[*off:*off+32])
	*off += 32
	return v, nil
}

func sv2ReadBool(b []byte, off *int) (bool, error) {
	v, err := sv2ReadU8(b, off)
	return v != 0, err
}

func sv2ReadOptU32(b []byte, off *int) (*uint32, error) {
	present, err := sv2ReadBool(b, off)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil
	}
	v, err := sv2ReadU32(b, off)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func sv2ReadF32(b []byte, off *int) (float32, error) {
	bits, err := sv2ReadU32(b, off)
	if err != nil {
		return 0, err
	}
	return math.Float32frombits(bits), nil
}

func sv2ReadStr(b []byte, off *int) (string, error) {
	length, err := sv2ReadU8(b, off)
	if err != nil {
		return "", err
	}
	if *off+int(length) > len(b) {
		return "", io.ErrUnexpectedEOF
	}
	s := string(b[*off : *off+int(length)])
	*off += int(length)
	return s, nil
}

func sv2ReadBytes(b []byte, off *int) ([]byte, error) {
	length, err := sv2ReadU8(b, off)
	if err != nil {
		return nil, err
	}
	if *off+int(length) > len(b) {
		return nil, io.ErrUnexpectedEOF
	}
	v := make([]byte, length)
	copy(v, b[*off:*off+int(length)])
	*off += int(length)
	return v, nil
}

func sv2ReadU256Seq(b []byte, off *int) ([][32]byte, error) {
	count, err := sv2ReadU8(b, off)
	if err != nil {
		return nil, err
	}
	result := make([][32]byte, count)
	for i := range int(count) {
		result[i], err = sv2ReadU256(b, off)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

// sv2ReadBytesB0_64K reads bytes with a u16 LE length prefix (for cbprefix/cbsuffix in NewMiningJob).
func sv2ReadBytesB0_64K(b []byte, off *int) ([]byte, error) {
	if *off+2 > len(b) {
		return nil, io.ErrUnexpectedEOF
	}
	length := binary.LittleEndian.Uint16(b[*off:])
	*off += 2
	if *off+int(length) > len(b) {
		return nil, io.ErrUnexpectedEOF
	}
	v := make([]byte, length)
	copy(v, b[*off:*off+int(length)])
	*off += int(length)
	return v, nil
}

// --- Append helpers ---

func sv2AppendU8(b []byte, v uint8) []byte {
	return append(b, v)
}

func sv2AppendU16(b []byte, v uint16) []byte {
	var buf [2]byte
	binary.LittleEndian.PutUint16(buf[:], v)
	return append(b, buf[:]...)
}

func sv2AppendU32(b []byte, v uint32) []byte {
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], v)
	return append(b, buf[:]...)
}

func sv2AppendU64(b []byte, v uint64) []byte {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], v)
	return append(b, buf[:]...)
}

func sv2AppendU256(b []byte, v [32]byte) []byte {
	return append(b, v[:]...)
}

func sv2AppendBool(b []byte, v bool) []byte {
	if v {
		return append(b, 1)
	}
	return append(b, 0)
}

func sv2AppendOptU32(b []byte, v *uint32) []byte {
	if v == nil {
		return sv2AppendBool(b, false)
	}
	b = sv2AppendBool(b, true)
	return sv2AppendU32(b, *v)
}

func sv2AppendF32(b []byte, v float32) []byte {
	return sv2AppendU32(b, math.Float32bits(v))
}

func sv2AppendStr(b []byte, s string) []byte {
	if len(s) > 255 {
		s = s[:255]
	}
	b = append(b, byte(len(s)))
	return append(b, s...)
}

func sv2AppendBytes(b []byte, v []byte) []byte {
	if len(v) > 255 {
		v = v[:255]
	}
	b = append(b, byte(len(v)))
	return append(b, v...)
}

func sv2AppendBytesB0_64K(b []byte, v []byte) []byte {
	l := len(v)
	if l > 65535 {
		l = 65535
		v = v[:l]
	}
	b = sv2AppendU16(b, uint16(l))
	return append(b, v...)
}

func sv2AppendU256Seq(b []byte, vs [][32]byte) []byte {
	count := len(vs)
	if count > 255 {
		count = 255
	}
	b = append(b, byte(count))
	for _, v := range vs[:count] {
		b = sv2AppendU256(b, v)
	}
	return b
}
