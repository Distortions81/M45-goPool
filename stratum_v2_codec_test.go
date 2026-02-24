package main

import (
	"bytes"
	"testing"
)

func TestStratumV2FrameHeaderRoundTrip(t *testing.T) {
	in := stratumV2Frame{
		ExtensionType: stratumV2CoreExtensionType | stratumV2ChannelMsgBit,
		MsgType:       stratumV2MsgTypeSubmitSharesStandard,
		Payload:       []byte{1, 2, 3, 4, 5},
	}
	enc, err := encodeStratumV2Frame(in)
	if err != nil {
		t.Fatalf("encodeStratumV2Frame: %v", err)
	}
	if len(enc) != stratumV2FrameHeaderLen+5 {
		t.Fatalf("encoded len=%d", len(enc))
	}
	got, err := decodeStratumV2Frame(enc)
	if err != nil {
		t.Fatalf("decodeStratumV2Frame: %v", err)
	}
	if got.ExtensionType != in.ExtensionType || got.MsgType != in.MsgType || !bytes.Equal(got.Payload, in.Payload) {
		t.Fatalf("frame roundtrip mismatch: got=%#v want=%#v", got, in)
	}
}

func TestStratumV2FrameDecodeRejectsLengthMismatch(t *testing.T) {
	b := []byte{
		0x00, 0x80, // extension_type LE (channel bit set)
		0x1a,             // msg_type
		0x02, 0x00, 0x00, // payload len = 2
		0x01, // only one payload byte
	}
	if _, err := decodeStratumV2Frame(b); err == nil {
		t.Fatalf("expected payload length mismatch error")
	}
}

func TestStratumV2SubmitSharesStandardWireCodecRoundTrip(t *testing.T) {
	in := stratumV2WireSubmitSharesStandard{
		ChannelID:      10,
		SequenceNumber: 11,
		JobID:          12,
		Nonce:          13,
		NTime:          14,
		Version:        15,
	}
	enc, err := encodeStratumV2SubmitSharesStandardFrame(in)
	if err != nil {
		t.Fatalf("encodeStratumV2SubmitSharesStandardFrame: %v", err)
	}
	dec, err := decodeStratumV2SubmitWireFrame(enc)
	if err != nil {
		t.Fatalf("decodeStratumV2SubmitWireFrame: %v", err)
	}
	got, ok := dec.(stratumV2WireSubmitSharesStandard)
	if !ok {
		t.Fatalf("decoded type=%T", dec)
	}
	if got != in {
		t.Fatalf("roundtrip mismatch: got=%#v want=%#v", got, in)
	}
}

func TestStratumV2OpenStandardMiningChannelWireCodecRoundTrip(t *testing.T) {
	in := stratumV2WireOpenStandardMiningChannel{
		RequestID:       42,
		UserIdentity:    "user.worker",
		NominalHashRate: 123.5,
		MaxTarget:       [32]byte{0xaa, 0xbb, 0xcc},
	}
	enc, err := encodeStratumV2OpenStandardMiningChannelFrame(in)
	if err != nil {
		t.Fatalf("encodeStratumV2OpenStandardMiningChannelFrame: %v", err)
	}
	dec, err := decodeStratumV2MiningWireFrame(enc)
	if err != nil {
		t.Fatalf("decodeStratumV2MiningWireFrame: %v", err)
	}
	got, ok := dec.(stratumV2WireOpenStandardMiningChannel)
	if !ok {
		t.Fatalf("decoded type=%T", dec)
	}
	if got.RequestID != in.RequestID || got.UserIdentity != in.UserIdentity || got.NominalHashRate != in.NominalHashRate || got.MaxTarget != in.MaxTarget {
		t.Fatalf("roundtrip mismatch: got=%#v want=%#v", got, in)
	}
}

func TestStratumV2OpenStandardMiningChannelSuccessWireCodecRoundTrip(t *testing.T) {
	in := stratumV2WireOpenStandardMiningChannelSuccess{
		RequestID:        3,
		ChannelID:        7,
		Target:           [32]byte{1, 2, 3},
		ExtranoncePrefix: []byte{0xaa, 0xbb},
		GroupChannelID:   9,
	}
	enc, err := encodeStratumV2OpenStandardMiningChannelSuccessFrame(in)
	if err != nil {
		t.Fatalf("encodeStratumV2OpenStandardMiningChannelSuccessFrame: %v", err)
	}
	dec, err := decodeStratumV2MiningWireFrame(enc)
	if err != nil {
		t.Fatalf("decodeStratumV2MiningWireFrame: %v", err)
	}
	got, ok := dec.(stratumV2WireOpenStandardMiningChannelSuccess)
	if !ok {
		t.Fatalf("decoded type=%T", dec)
	}
	if got.RequestID != in.RequestID || got.ChannelID != in.ChannelID || got.Target != in.Target || got.GroupChannelID != in.GroupChannelID || !bytes.Equal(got.ExtranoncePrefix, in.ExtranoncePrefix) {
		t.Fatalf("roundtrip mismatch: got=%#v want=%#v", got, in)
	}
}

func TestStratumV2SetTargetWireCodecRoundTrip(t *testing.T) {
	in := stratumV2WireSetTarget{
		ChannelID:     33,
		MaximumTarget: [32]byte{0xde, 0xad, 0xbe, 0xef},
	}
	enc, err := encodeStratumV2SetTargetFrame(in)
	if err != nil {
		t.Fatalf("encodeStratumV2SetTargetFrame: %v", err)
	}
	dec, err := decodeStratumV2MiningWireFrame(enc)
	if err != nil {
		t.Fatalf("decodeStratumV2MiningWireFrame: %v", err)
	}
	got, ok := dec.(stratumV2WireSetTarget)
	if !ok {
		t.Fatalf("decoded type=%T", dec)
	}
	if got != in {
		t.Fatalf("roundtrip mismatch: got=%#v want=%#v", got, in)
	}
}

func TestStratumV2SubmitSharesExtendedWireCodecRoundTrip(t *testing.T) {
	in := stratumV2WireSubmitSharesExtended{
		ChannelID:      100,
		SequenceNumber: 101,
		JobID:          102,
		Nonce:          103,
		NTime:          104,
		Version:        105,
		Extranonce:     []byte{0xaa, 0xbb, 0xcc},
	}
	enc, err := encodeStratumV2SubmitSharesExtendedFrame(in)
	if err != nil {
		t.Fatalf("encodeStratumV2SubmitSharesExtendedFrame: %v", err)
	}
	dec, err := decodeStratumV2SubmitWireFrame(enc)
	if err != nil {
		t.Fatalf("decodeStratumV2SubmitWireFrame: %v", err)
	}
	got, ok := dec.(stratumV2WireSubmitSharesExtended)
	if !ok {
		t.Fatalf("decoded type=%T", dec)
	}
	if got.ChannelID != in.ChannelID || got.SequenceNumber != in.SequenceNumber || got.JobID != in.JobID || got.Nonce != in.Nonce || got.NTime != in.NTime || got.Version != in.Version || !bytes.Equal(got.Extranonce, in.Extranonce) {
		t.Fatalf("roundtrip mismatch: got=%#v want=%#v", got, in)
	}
}

func TestStratumV2SubmitSharesSuccessWireCodecRoundTrip(t *testing.T) {
	in := stratumV2WireSubmitSharesSuccess{
		ChannelID:               1,
		LastSequenceNumber:      200,
		NewSubmitsAcceptedCount: 7,
		NewSharesSum:            123456789,
	}
	enc, err := encodeStratumV2SubmitSharesSuccessFrame(in)
	if err != nil {
		t.Fatalf("encodeStratumV2SubmitSharesSuccessFrame: %v", err)
	}
	dec, err := decodeStratumV2SubmitWireFrame(enc)
	if err != nil {
		t.Fatalf("decodeStratumV2SubmitWireFrame: %v", err)
	}
	got, ok := dec.(stratumV2WireSubmitSharesSuccess)
	if !ok {
		t.Fatalf("decoded type=%T", dec)
	}
	if got != in {
		t.Fatalf("roundtrip mismatch: got=%#v want=%#v", got, in)
	}
}

func TestStratumV2SubmitSharesErrorWireCodecRoundTrip(t *testing.T) {
	in := stratumV2WireSubmitSharesError{
		ChannelID:      2,
		SequenceNumber: 300,
		ErrorCode:      "difficulty-too-low",
	}
	enc, err := encodeStratumV2SubmitSharesErrorFrame(in)
	if err != nil {
		t.Fatalf("encodeStratumV2SubmitSharesErrorFrame: %v", err)
	}
	dec, err := decodeStratumV2SubmitWireFrame(enc)
	if err != nil {
		t.Fatalf("decodeStratumV2SubmitWireFrame: %v", err)
	}
	got, ok := dec.(stratumV2WireSubmitSharesError)
	if !ok {
		t.Fatalf("decoded type=%T", dec)
	}
	if got != in {
		t.Fatalf("roundtrip mismatch: got=%#v want=%#v", got, in)
	}
}

func TestStratumV2SubmitSharesExtendedRejectsOversizedExtranonce(t *testing.T) {
	in := stratumV2WireSubmitSharesExtended{Extranonce: bytes.Repeat([]byte{1}, 33)}
	if _, err := encodeStratumV2SubmitSharesExtendedFrame(in); err == nil {
		t.Fatalf("expected oversized extranonce error")
	}
}

func TestStratumV2SubmitWireFrameRejectsNonChannelFrame(t *testing.T) {
	frame, err := encodeStratumV2Frame(stratumV2Frame{
		ExtensionType: stratumV2CoreExtensionType,
		MsgType:       stratumV2MsgTypeSubmitSharesStandard,
		Payload:       make([]byte, 24),
	})
	if err != nil {
		t.Fatalf("encodeStratumV2Frame: %v", err)
	}
	if _, err := decodeStratumV2SubmitWireFrame(frame); err == nil {
		t.Fatalf("expected non-channel submit frame error")
	}
}
