package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

type sv2ProtocolKind int

const (
	sv2ProtocolUnknown sv2ProtocolKind = iota
	sv2ProtocolSV1JSON
	sv2ProtocolPlaintext
	sv2ProtocolEncrypted
)

func detectStratumProtocol(conn net.Conn, reader *bufio.Reader, cfg Config) (sv2ProtocolKind, error) {
	if conn == nil || reader == nil {
		return sv2ProtocolUnknown, fmt.Errorf("sv2 protocol detection requires a live connection")
	}
	deadline := 5 * time.Second
	if cfg.ConnectionTimeout > 0 && cfg.ConnectionTimeout < deadline {
		deadline = cfg.ConnectionTimeout
	}
	if deadline > 0 {
		_ = conn.SetReadDeadline(time.Now().Add(deadline))
		defer func() {
			_ = conn.SetReadDeadline(time.Time{})
		}()
	}
	probe, err := reader.Peek(6)
	if err != nil && !errors.Is(err, bufio.ErrBufferFull) && !errors.Is(err, io.EOF) {
		return sv2ProtocolUnknown, err
	}
	if looksLikeJSONRPCStart(probe) {
		return sv2ProtocolSV1JSON, nil
	}
	if len(probe) >= 3 && probe[0] == 0x00 && probe[1] == 0x00 && probe[2] == sv2MsgSetupConnection {
		if len(probe) == 6 {
			payLen := int(probe[3]) | int(probe[4])<<8 | int(probe[5])<<16
			if payLen > sv2PlaintextFramePayloadMax {
				return sv2ProtocolUnknown, fmt.Errorf("sv2 plaintext setup payload too large: %d", payLen)
			}
		}
		return sv2ProtocolPlaintext, nil
	}
	// Encrypted SV2 cannot be positively identified before the Noise handshake on
	// a shared listener. We keep a bounded fallback for compatibility, but the
	// long-term direction is explicit port separation.
	return sv2ProtocolEncrypted, nil
}

func looksLikeJSONRPCStart(b []byte) bool {
	trimmed := bytes.TrimLeftFunc(b, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	return len(trimmed) > 0 && trimmed[0] == '{'
}