package main

import (
	"bytes"
	"math"
	"testing"
)

func filledU256(seed byte) [32]byte {
	var out [32]byte
	for i := range out {
		out[i] = seed + byte(i)
	}
	return out
}

func TestSV2TypesRoundTripCoverage(t *testing.T) {
	t.Run("SetupConnectionSuccess", func(t *testing.T) {
		in := sv2SetupConnectionSuccess{UsedVersion: 2, Flags: 0xAABBCCDD}
		var out sv2SetupConnectionSuccess
		if err := out.decode(in.encode()); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if out != in {
			t.Fatalf("mismatch: got %+v want %+v", out, in)
		}
	})

	t.Run("SetupConnectionError", func(t *testing.T) {
		in := sv2SetupConnectionError{Flags: 7, ErrorCode: "unsupported-protocol"}
		var out sv2SetupConnectionError
		if err := out.decode(in.encode()); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if out != in {
			t.Fatalf("mismatch: got %+v want %+v", out, in)
		}
	})

	t.Run("OpenStandardMiningChannel", func(t *testing.T) {
		in := sv2OpenStandardMiningChannel{
			RequestID:       99,
			UserIdentity:    "worker.001",
			NominalHashRate: 1234.5,
			MaxTarget:       filledU256(0x10),
		}
		var out sv2OpenStandardMiningChannel
		if err := out.decode(in.encode()); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if out.RequestID != in.RequestID || out.UserIdentity != in.UserIdentity || out.MaxTarget != in.MaxTarget {
			t.Fatalf("field mismatch: got %+v want %+v", out, in)
		}
		if math.Abs(float64(out.NominalHashRate-in.NominalHashRate)) > 1e-4 {
			t.Fatalf("hashrate mismatch: got %f want %f", out.NominalHashRate, in.NominalHashRate)
		}
	})

	t.Run("OpenStdMiningChannelSuccess", func(t *testing.T) {
		in := sv2OpenStdMiningChannelSuccess{
			RequestID:        1,
			ChannelID:        2,
			Target:           filledU256(0x11),
			ExtraNoncePrefix: []byte{0x01, 0x02, 0x03},
			GroupChannelID:   3,
		}
		var out sv2OpenStdMiningChannelSuccess
		if err := out.decode(in.encode()); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if out.RequestID != in.RequestID || out.ChannelID != in.ChannelID || out.Target != in.Target || out.GroupChannelID != in.GroupChannelID {
			t.Fatalf("field mismatch: got %+v want %+v", out, in)
		}
		if !bytes.Equal(out.ExtraNoncePrefix, in.ExtraNoncePrefix) {
			t.Fatalf("extranonce prefix mismatch: got %x want %x", out.ExtraNoncePrefix, in.ExtraNoncePrefix)
		}
	})

	t.Run("OpenStdMiningChannelError", func(t *testing.T) {
		in := sv2OpenStdMiningChannelError{RequestID: 7, ErrorCode: "not-authorized"}
		var out sv2OpenStdMiningChannelError
		if err := out.decode(in.encode()); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if out != in {
			t.Fatalf("mismatch: got %+v want %+v", out, in)
		}
	})

	t.Run("OpenExtendedMiningChannel", func(t *testing.T) {
		in := sv2OpenExtendedMiningChannel{
			RequestID:         13,
			UserIdentity:      "wallet.worker",
			NominalHashRate:   9876.25,
			MaxTarget:         filledU256(0x22),
			MinExtranonceSize: 6,
		}
		var out sv2OpenExtendedMiningChannel
		if err := out.decode(in.encode()); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if out.RequestID != in.RequestID || out.UserIdentity != in.UserIdentity || out.MaxTarget != in.MaxTarget || out.MinExtranonceSize != in.MinExtranonceSize {
			t.Fatalf("field mismatch: got %+v want %+v", out, in)
		}
		if math.Abs(float64(out.NominalHashRate-in.NominalHashRate)) > 1e-4 {
			t.Fatalf("hashrate mismatch: got %f want %f", out.NominalHashRate, in.NominalHashRate)
		}
	})

	t.Run("OpenExtMiningChannelSuccess", func(t *testing.T) {
		in := sv2OpenExtMiningChannelSuccess{
			RequestID:        5,
			ChannelID:        6,
			Target:           filledU256(0x33),
			ExtranonceSize:   8,
			ExtraNoncePrefix: []byte{0xaa, 0xbb},
			GroupChannelID:   9,
		}
		var out sv2OpenExtMiningChannelSuccess
		if err := out.decode(in.encode()); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if out.RequestID != in.RequestID || out.ChannelID != in.ChannelID || out.Target != in.Target || out.ExtranonceSize != in.ExtranonceSize || out.GroupChannelID != in.GroupChannelID {
			t.Fatalf("field mismatch: got %+v want %+v", out, in)
		}
		if !bytes.Equal(out.ExtraNoncePrefix, in.ExtraNoncePrefix) {
			t.Fatalf("extranonce prefix mismatch: got %x want %x", out.ExtraNoncePrefix, in.ExtraNoncePrefix)
		}
	})

	t.Run("OpenExtMiningChannelError", func(t *testing.T) {
		in := sv2OpenExtMiningChannelError{RequestID: 77, ErrorCode: "invalid-min-extranonce-size"}
		var out sv2OpenExtMiningChannelError
		if err := out.decode(in.encode()); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if out != in {
			t.Fatalf("mismatch: got %+v want %+v", out, in)
		}
	})

	t.Run("NewExtendedMiningJobWithMinNTime", func(t *testing.T) {
		minNTime := uint32(1700000000)
		in := sv2NewExtendedMiningJob{
			ChannelID:             1,
			JobID:                 2,
			MinNTime:              &minNTime,
			Version:               0x20000000,
			VersionRollingAllowed: true,
			MerklePath:            [][32]byte{filledU256(0x44), filledU256(0x55)},
			CbPrefix:              []byte{0xde, 0xad},
			CbSuffix:              []byte{0xbe, 0xef, 0x01},
		}
		var out sv2NewExtendedMiningJob
		if err := out.decode(in.encode()); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if out.ChannelID != in.ChannelID || out.JobID != in.JobID || out.Version != in.Version || out.VersionRollingAllowed != in.VersionRollingAllowed {
			t.Fatalf("field mismatch: got %+v want %+v", out, in)
		}
		if out.MinNTime == nil || *out.MinNTime != *in.MinNTime {
			t.Fatalf("min_ntime mismatch: got %v want %v", out.MinNTime, in.MinNTime)
		}
		if len(out.MerklePath) != len(in.MerklePath) || out.MerklePath[0] != in.MerklePath[0] || out.MerklePath[1] != in.MerklePath[1] {
			t.Fatalf("merkle path mismatch")
		}
		if !bytes.Equal(out.CbPrefix, in.CbPrefix) || !bytes.Equal(out.CbSuffix, in.CbSuffix) {
			t.Fatalf("coinbase parts mismatch")
		}
	})

	t.Run("NewExtendedMiningJobWithoutMinNTime", func(t *testing.T) {
		in := sv2NewExtendedMiningJob{ChannelID: 7, JobID: 8, Version: 9, VersionRollingAllowed: false}
		var out sv2NewExtendedMiningJob
		if err := out.decode(in.encode()); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if out.MinNTime != nil {
			t.Fatalf("expected nil min_ntime, got %v", *out.MinNTime)
		}
	})

	t.Run("NewMiningJob", func(t *testing.T) {
		minNTime := uint32(1717171717)
		in := sv2NewMiningJob{
			ChannelID:  12,
			JobID:      34,
			MinNTime:   &minNTime,
			Version:    0x20000000,
			MerkleRoot: filledU256(0x5a),
		}
		var out sv2NewMiningJob
		if err := out.decode(in.encode()); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if out.ChannelID != in.ChannelID || out.JobID != in.JobID || out.Version != in.Version || out.MerkleRoot != in.MerkleRoot {
			t.Fatalf("field mismatch: got %+v want %+v", out, in)
		}
		if out.MinNTime == nil || *out.MinNTime != *in.MinNTime {
			t.Fatalf("min_ntime mismatch: got %v want %v", out.MinNTime, in.MinNTime)
		}
	})

	t.Run("SetNewPrevHash", func(t *testing.T) {
		in := sv2SetNewPrevHash{
			ChannelID: 9,
			JobID:     10,
			PrevHash:  filledU256(0x66),
			MinNTime:  1717171717,
			NBits:     0x17020f79,
		}
		var out sv2SetNewPrevHash
		if err := out.decode(in.encode()); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if out != in {
			t.Fatalf("mismatch: got %+v want %+v", out, in)
		}
	})

	t.Run("SetTarget", func(t *testing.T) {
		in := sv2SetTarget{ChannelID: 11, MaximumTarget: filledU256(0x77)}
		var out sv2SetTarget
		if err := out.decode(in.encode()); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if out != in {
			t.Fatalf("mismatch: got %+v want %+v", out, in)
		}
	})

	t.Run("SubmitSharesExtended", func(t *testing.T) {
		in := sv2SubmitSharesExtended{
			ChannelID:      1,
			SequenceNumber: 2,
			JobID:          3,
			Nonce:          4,
			NTime:          5,
			Version:        6,
			Extranonce:     []byte{0x01, 0x02},
		}
		var out sv2SubmitSharesExtended
		if err := out.decode(in.encode()); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if out.ChannelID != in.ChannelID || out.SequenceNumber != in.SequenceNumber || out.JobID != in.JobID || out.Nonce != in.Nonce || out.NTime != in.NTime || out.Version != in.Version {
			t.Fatalf("field mismatch: got %+v want %+v", out, in)
		}
		if !bytes.Equal(out.Extranonce, in.Extranonce) {
			t.Fatalf("extranonce mismatch: got %x want %x", out.Extranonce, in.Extranonce)
		}
	})

	t.Run("SubmitSharesSuccess", func(t *testing.T) {
		in := sv2SubmitSharesSuccess{
			ChannelID:               12,
			LastSequenceNumber:      34,
			NewSubmitsAcceptedCount: 56,
			NewSharesSum:            78,
		}
		var out sv2SubmitSharesSuccess
		if err := out.decode(in.encode()); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if out != in {
			t.Fatalf("mismatch: got %+v want %+v", out, in)
		}
	})

	t.Run("SubmitSharesError", func(t *testing.T) {
		in := sv2SubmitSharesError{ChannelID: 90, SequenceNumber: 91, ErrorCode: "stale-share"}
		var out sv2SubmitSharesError
		if err := out.decode(in.encode()); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if out != in {
			t.Fatalf("mismatch: got %+v want %+v", out, in)
		}
	})
}
