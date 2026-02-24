package main

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestDetectSV2TransportMode_PlaintextSetupConnection(t *testing.T) {
	frame := testSV2SetupConnectionFrame(t)
	r := bufio.NewReader(bytes.NewReader(frame))
	got := detectSV2TransportMode(r)
	if got.mode != "plaintext" {
		t.Fatalf("mode=%q want plaintext (cause=%q)", got.mode, got.cause)
	}
}

func TestDetectSV2TransportMode_NoiseLikeBytes(t *testing.T) {
	// Not a valid plaintext SetupConnection header: ext!=0 and msg_type!=0.
	buf := []byte{0x34, 0x12, 0xab, 0x09, 0x00, 0x00, 0xff, 0xee}
	r := bufio.NewReader(bytes.NewReader(buf))
	got := detectSV2TransportMode(r)
	if got.mode != "noise" {
		t.Fatalf("mode=%q want noise (cause=%q)", got.mode, got.cause)
	}
}

func TestNewSV2FrameTransportAuto_NoiseReturnsNotImplemented(t *testing.T) {
	buf := []byte{0x01, 0x00, 0x7f, 0x08, 0x00, 0x00, 0x00}
	r := bufio.NewReader(bytes.NewReader(buf))
	var out bytes.Buffer
	tr, det, err := newSV2FrameTransportAuto(r, &out)
	if err != nil {
		t.Fatalf("unexpected auto transport error: %v", err)
	}
	if det.mode != "noise" {
		t.Fatalf("mode=%q want noise", det.mode)
	}
	if tr == nil || tr.Mode() != "noise" {
		t.Fatalf("expected noise transport, got %#v", tr)
	}
}

func TestSV2NoiseFrameTransport_ModeAndHandshakeState(t *testing.T) {
	var in bytes.Buffer
	var out bytes.Buffer
	tr := newSV2NoiseFrameTransport(&in, &out)
	if tr == nil {
		t.Fatalf("expected transport")
	}
	if got := tr.Mode(); got != "noise" {
		t.Fatalf("Mode()=%q want noise", got)
	}
	if tr.handshake == nil {
		t.Fatalf("expected handshake state")
	}
	if got := tr.handshake.State(); got != sv2NoiseHandshakeInit {
		t.Fatalf("initial handshake state=%q want %q", got, sv2NoiseHandshakeInit)
	}
	if _, err := tr.ReadFrame(); !errors.Is(err, io.EOF) {
		t.Fatalf("ReadFrame err=%v want EOF", err)
	}
	if got := tr.handshake.State(); got != sv2NoiseHandshakeUnsupported {
		t.Fatalf("post-read handshake state=%q want %q", got, sv2NoiseHandshakeUnsupported)
	}
}

func TestSV2NoiseConstants(t *testing.T) {
	if sv2NoiseAct1Len != 64 {
		t.Fatalf("sv2NoiseAct1Len=%d want 64", sv2NoiseAct1Len)
	}
	if sv2NoiseAct2Len != 234 {
		t.Fatalf("sv2NoiseAct2Len=%d want 234", sv2NoiseAct2Len)
	}
	if sv2NoiseEncryptedHeaderLen != 22 {
		t.Fatalf("sv2NoiseEncryptedHeaderLen=%d want 22", sv2NoiseEncryptedHeaderLen)
	}
	hs := sv2NoiseNewHandshakeHash()
	if hs.ck != sv2NoiseNXProtocolHashSHA256 {
		t.Fatalf("protocol ck hash mismatch")
	}
}
