package main

import (
	"bufio"
	"bytes"
	"errors"
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
	_, det, err := newSV2FrameTransportAuto(r, &out)
	if !errors.Is(err, errSV2NoiseHandshakeNotImplemented) {
		t.Fatalf("err=%v want errSV2NoiseHandshakeNotImplemented", err)
	}
	if det.mode != "noise" {
		t.Fatalf("mode=%q want noise", det.mode)
	}
}

