package main

import (
	"encoding/binary"
	"fmt"
	"math"
)

const (
	stratumV2FrameHeaderLen     = 6
	stratumV2CoreExtensionType  = uint16(0x0000)
	stratumV2ChannelMsgBit      = uint16(0x8000)
	stratumV2MaxFramePayloadLen = 0xFFFFFF

	stratumV2MsgTypeSubmitSharesStandard             = uint8(0x1a)
	stratumV2MsgTypeSubmitSharesExtended             = uint8(0x1b)
	stratumV2MsgTypeSubmitSharesSuccess              = uint8(0x1c)
	stratumV2MsgTypeSubmitSharesError                = uint8(0x1d)
	stratumV2MsgTypeOpenStandardMiningChannel        = uint8(0x10)
	stratumV2MsgTypeOpenStandardMiningChannelSuccess = uint8(0x11)
	stratumV2MsgTypeOpenExtendedMiningChannel        = uint8(0x13)
	stratumV2MsgTypeOpenExtendedMiningChannelSuccess = uint8(0x14)
	stratumV2MsgTypeNewMiningJob                     = uint8(0x15)
	stratumV2MsgTypeSetExtranoncePrefix              = uint8(0x19)
	stratumV2MsgTypeSetTarget                        = uint8(0x21)
)

type stratumV2Frame struct {
	ExtensionType uint16
	MsgType       uint8
	Payload       []byte
}

func (f stratumV2Frame) isChannelMessage() bool {
	return f.ExtensionType&stratumV2ChannelMsgBit != 0
}

func (f stratumV2Frame) baseExtensionType() uint16 {
	return f.ExtensionType &^ stratumV2ChannelMsgBit
}

func encodeStratumV2Frame(f stratumV2Frame) ([]byte, error) {
	if len(f.Payload) > stratumV2MaxFramePayloadLen {
		return nil, fmt.Errorf("sv2 payload too large: %d", len(f.Payload))
	}
	out := make([]byte, stratumV2FrameHeaderLen+len(f.Payload))
	binary.LittleEndian.PutUint16(out[0:2], f.ExtensionType)
	out[2] = f.MsgType
	putUint24LE(out[3:6], uint32(len(f.Payload)))
	copy(out[6:], f.Payload)
	return out, nil
}

func decodeStratumV2Frame(b []byte) (stratumV2Frame, error) {
	if len(b) < stratumV2FrameHeaderLen {
		return stratumV2Frame{}, fmt.Errorf("sv2 frame too short: %d", len(b))
	}
	payloadLen := int(readUint24LE(b[3:6]))
	if len(b)-stratumV2FrameHeaderLen != payloadLen {
		return stratumV2Frame{}, fmt.Errorf("sv2 frame payload length mismatch: header=%d actual=%d", payloadLen, len(b)-stratumV2FrameHeaderLen)
	}
	payload := make([]byte, payloadLen)
	copy(payload, b[stratumV2FrameHeaderLen:])
	return stratumV2Frame{
		ExtensionType: binary.LittleEndian.Uint16(b[0:2]),
		MsgType:       b[2],
		Payload:       payload,
	}, nil
}

func putUint24LE(dst []byte, v uint32) {
	if len(dst) < 3 {
		return
	}
	dst[0] = byte(v)
	dst[1] = byte(v >> 8)
	dst[2] = byte(v >> 16)
}

func readUint24LE(src []byte) uint32 {
	if len(src) < 3 {
		return 0
	}
	return uint32(src[0]) | uint32(src[1])<<8 | uint32(src[2])<<16
}

func putStr0_255(dst []byte, s string) ([]byte, error) {
	if len(s) > 255 {
		return dst, fmt.Errorf("STR0_255 too long: %d", len(s))
	}
	dst = append(dst, byte(len(s)))
	dst = append(dst, s...)
	return dst, nil
}

func readStr0_255(payload []byte, off int) (string, int, error) {
	if off >= len(payload) {
		return "", off, fmt.Errorf("STR0_255 missing length")
	}
	n := int(payload[off])
	off++
	if off+n > len(payload) {
		return "", off, fmt.Errorf("STR0_255 length %d exceeds payload", n)
	}
	return string(payload[off : off+n]), off + n, nil
}

func putB0_32(dst []byte, b []byte) ([]byte, error) {
	if len(b) > 32 {
		return dst, fmt.Errorf("B0_32 too long: %d", len(b))
	}
	dst = append(dst, byte(len(b)))
	dst = append(dst, b...)
	return dst, nil
}

func readB0_32(payload []byte, off int) ([]byte, int, error) {
	if off >= len(payload) {
		return nil, off, fmt.Errorf("B0_32 missing length")
	}
	n := int(payload[off])
	off++
	if n > 32 {
		return nil, off, fmt.Errorf("B0_32 length out of range: %d", n)
	}
	if off+n > len(payload) {
		return nil, off, fmt.Errorf("B0_32 length %d exceeds payload", n)
	}
	out := append([]byte(nil), payload[off:off+n]...)
	return out, off + n, nil
}

func encodeOptionU32(dst []byte, has bool, v uint32) []byte {
	if !has {
		return append(dst, 0)
	}
	var tmp [4]byte
	dst = append(dst, 1)
	binary.LittleEndian.PutUint32(tmp[:], v)
	return append(dst, tmp[:]...)
}

func decodeOptionU32(payload []byte, off int) (has bool, v uint32, next int, err error) {
	if off >= len(payload) {
		return false, 0, off, fmt.Errorf("OPTION[u32] missing tag")
	}
	tag := payload[off]
	off++
	switch tag {
	case 0:
		return false, 0, off, nil
	case 1:
		if off+4 > len(payload) {
			return false, 0, off, fmt.Errorf("OPTION[u32] missing value")
		}
		return true, binary.LittleEndian.Uint32(payload[off : off+4]), off + 4, nil
	default:
		return false, 0, off, fmt.Errorf("OPTION[u32] invalid tag: %d", tag)
	}
}

func encodeStratumV2OpenStandardMiningChannelFrame(msg stratumV2WireOpenStandardMiningChannel) ([]byte, error) {
	payload := make([]byte, 0, 4+1+len(msg.UserIdentity)+4+32)
	var tmp4 [4]byte
	binary.LittleEndian.PutUint32(tmp4[:], msg.RequestID)
	payload = append(payload, tmp4[:]...)
	var err error
	payload, err = putStr0_255(payload, msg.UserIdentity)
	if err != nil {
		return nil, err
	}
	binary.LittleEndian.PutUint32(tmp4[:], math.Float32bits(msg.NominalHashRate))
	payload = append(payload, tmp4[:]...)
	payload = append(payload, msg.MaxTarget[:]...)
	return encodeStratumV2Frame(stratumV2Frame{
		ExtensionType: stratumV2CoreExtensionType,
		MsgType:       stratumV2MsgTypeOpenStandardMiningChannel,
		Payload:       payload,
	})
}

func encodeStratumV2NewMiningJobFrame(msg stratumV2WireNewMiningJob) ([]byte, error) {
	payload := make([]byte, 0, 4+4+1+4+4+32)
	var tmp4 [4]byte
	binary.LittleEndian.PutUint32(tmp4[:], msg.ChannelID)
	payload = append(payload, tmp4[:]...)
	binary.LittleEndian.PutUint32(tmp4[:], msg.JobID)
	payload = append(payload, tmp4[:]...)
	payload = encodeOptionU32(payload, msg.HasMinNTime, msg.MinNTime)
	binary.LittleEndian.PutUint32(tmp4[:], msg.Version)
	payload = append(payload, tmp4[:]...)
	payload = append(payload, msg.MerkleRoot[:]...)
	return encodeStratumV2Frame(stratumV2Frame{
		ExtensionType: stratumV2CoreExtensionType,
		MsgType:       stratumV2MsgTypeNewMiningJob,
		Payload:       payload,
	})
}

func decodeStratumV2NewMiningJobPayload(payload []byte) (stratumV2WireNewMiningJob, error) {
	if len(payload) < 45 {
		return stratumV2WireNewMiningJob{}, fmt.Errorf("newminingjob payload too short: %d", len(payload))
	}
	out := stratumV2WireNewMiningJob{
		ChannelID: binary.LittleEndian.Uint32(payload[0:4]),
		JobID:     binary.LittleEndian.Uint32(payload[4:8]),
	}
	var err error
	off := 8
	out.HasMinNTime, out.MinNTime, off, err = decodeOptionU32(payload, off)
	if err != nil {
		return stratumV2WireNewMiningJob{}, err
	}
	if off+4+32 != len(payload) {
		return stratumV2WireNewMiningJob{}, fmt.Errorf("newminingjob payload len=%d invalid tail", len(payload))
	}
	out.Version = binary.LittleEndian.Uint32(payload[off : off+4])
	off += 4
	copy(out.MerkleRoot[:], payload[off:off+32])
	return out, nil
}

func encodeStratumV2SetExtranoncePrefixFrame(msg stratumV2WireSetExtranoncePrefix) ([]byte, error) {
	payload := make([]byte, 0, 4+1+len(msg.ExtranoncePrefix))
	var tmp4 [4]byte
	binary.LittleEndian.PutUint32(tmp4[:], msg.ChannelID)
	payload = append(payload, tmp4[:]...)
	var err error
	payload, err = putB0_32(payload, msg.ExtranoncePrefix)
	if err != nil {
		return nil, err
	}
	return encodeStratumV2Frame(stratumV2Frame{
		ExtensionType: stratumV2CoreExtensionType | stratumV2ChannelMsgBit,
		MsgType:       stratumV2MsgTypeSetExtranoncePrefix,
		Payload:       payload,
	})
}

func decodeStratumV2SetExtranoncePrefixPayload(payload []byte) (stratumV2WireSetExtranoncePrefix, error) {
	if len(payload) < 5 {
		return stratumV2WireSetExtranoncePrefix{}, fmt.Errorf("setextranonceprefix payload too short: %d", len(payload))
	}
	out := stratumV2WireSetExtranoncePrefix{
		ChannelID: binary.LittleEndian.Uint32(payload[0:4]),
	}
	var err error
	out.ExtranoncePrefix, _, err = readB0_32(payload, 4)
	if err != nil {
		return stratumV2WireSetExtranoncePrefix{}, err
	}
	if len(payload) != 5+len(out.ExtranoncePrefix) {
		return stratumV2WireSetExtranoncePrefix{}, fmt.Errorf("setextranonceprefix payload len=%d invalid tail", len(payload))
	}
	return out, nil
}

func decodeStratumV2OpenStandardMiningChannelPayload(payload []byte) (stratumV2WireOpenStandardMiningChannel, error) {
	if len(payload) < 41 {
		return stratumV2WireOpenStandardMiningChannel{}, fmt.Errorf("openstandardminingchannel payload too short: %d", len(payload))
	}
	out := stratumV2WireOpenStandardMiningChannel{
		RequestID: binary.LittleEndian.Uint32(payload[0:4]),
	}
	var err error
	off := 4
	out.UserIdentity, off, err = readStr0_255(payload, off)
	if err != nil {
		return stratumV2WireOpenStandardMiningChannel{}, err
	}
	if off+4+32 != len(payload) {
		return stratumV2WireOpenStandardMiningChannel{}, fmt.Errorf("openstandardminingchannel payload len=%d invalid tail", len(payload))
	}
	out.NominalHashRate = math.Float32frombits(binary.LittleEndian.Uint32(payload[off : off+4]))
	off += 4
	copy(out.MaxTarget[:], payload[off:off+32])
	return out, nil
}

func encodeStratumV2OpenExtendedMiningChannelFrame(msg stratumV2WireOpenExtendedMiningChannel) ([]byte, error) {
	payload := make([]byte, 0, 4+1+len(msg.UserIdentity)+4+32+2)
	var tmp4 [4]byte
	binary.LittleEndian.PutUint32(tmp4[:], msg.RequestID)
	payload = append(payload, tmp4[:]...)
	var err error
	payload, err = putStr0_255(payload, msg.UserIdentity)
	if err != nil {
		return nil, err
	}
	binary.LittleEndian.PutUint32(tmp4[:], math.Float32bits(msg.NominalHashRate))
	payload = append(payload, tmp4[:]...)
	payload = append(payload, msg.MaxTarget[:]...)
	var tmp2 [2]byte
	binary.LittleEndian.PutUint16(tmp2[:], msg.MinExtranonceSize)
	payload = append(payload, tmp2[:]...)
	return encodeStratumV2Frame(stratumV2Frame{
		ExtensionType: stratumV2CoreExtensionType,
		MsgType:       stratumV2MsgTypeOpenExtendedMiningChannel,
		Payload:       payload,
	})
}

func decodeStratumV2OpenExtendedMiningChannelPayload(payload []byte) (stratumV2WireOpenExtendedMiningChannel, error) {
	if len(payload) < 43 {
		return stratumV2WireOpenExtendedMiningChannel{}, fmt.Errorf("openextendedminingchannel payload too short: %d", len(payload))
	}
	base, err := decodeStratumV2OpenStandardMiningChannelPayload(payload[:len(payload)-2])
	if err != nil {
		return stratumV2WireOpenExtendedMiningChannel{}, err
	}
	return stratumV2WireOpenExtendedMiningChannel{
		stratumV2WireOpenStandardMiningChannel: base,
		MinExtranonceSize:                      binary.LittleEndian.Uint16(payload[len(payload)-2:]),
	}, nil
}

func encodeStratumV2OpenStandardMiningChannelSuccessFrame(msg stratumV2WireOpenStandardMiningChannelSuccess) ([]byte, error) {
	payload := make([]byte, 0, 4+4+32+1+len(msg.ExtranoncePrefix)+4)
	var tmp4 [4]byte
	binary.LittleEndian.PutUint32(tmp4[:], msg.RequestID)
	payload = append(payload, tmp4[:]...)
	binary.LittleEndian.PutUint32(tmp4[:], msg.ChannelID)
	payload = append(payload, tmp4[:]...)
	payload = append(payload, msg.Target[:]...)
	var err error
	payload, err = putB0_32(payload, msg.ExtranoncePrefix)
	if err != nil {
		return nil, err
	}
	binary.LittleEndian.PutUint32(tmp4[:], msg.GroupChannelID)
	payload = append(payload, tmp4[:]...)
	return encodeStratumV2Frame(stratumV2Frame{
		ExtensionType: stratumV2CoreExtensionType,
		MsgType:       stratumV2MsgTypeOpenStandardMiningChannelSuccess,
		Payload:       payload,
	})
}

func decodeStratumV2OpenStandardMiningChannelSuccessPayload(payload []byte) (stratumV2WireOpenStandardMiningChannelSuccess, error) {
	if len(payload) < 45 {
		return stratumV2WireOpenStandardMiningChannelSuccess{}, fmt.Errorf("openstandardminingchannel.success payload too short: %d", len(payload))
	}
	out := stratumV2WireOpenStandardMiningChannelSuccess{
		RequestID: binary.LittleEndian.Uint32(payload[0:4]),
		ChannelID: binary.LittleEndian.Uint32(payload[4:8]),
	}
	copy(out.Target[:], payload[8:40])
	var err error
	off := 40
	out.ExtranoncePrefix, off, err = readB0_32(payload, off)
	if err != nil {
		return stratumV2WireOpenStandardMiningChannelSuccess{}, err
	}
	if off+4 != len(payload) {
		return stratumV2WireOpenStandardMiningChannelSuccess{}, fmt.Errorf("openstandardminingchannel.success payload len=%d invalid tail", len(payload))
	}
	out.GroupChannelID = binary.LittleEndian.Uint32(payload[off : off+4])
	return out, nil
}

func encodeStratumV2OpenExtendedMiningChannelSuccessFrame(msg stratumV2WireOpenExtendedMiningChannelSuccess) ([]byte, error) {
	payload := make([]byte, 0, 4+4+32+2+1+len(msg.ExtranoncePrefix)+4)
	var tmp4 [4]byte
	binary.LittleEndian.PutUint32(tmp4[:], msg.RequestID)
	payload = append(payload, tmp4[:]...)
	binary.LittleEndian.PutUint32(tmp4[:], msg.ChannelID)
	payload = append(payload, tmp4[:]...)
	payload = append(payload, msg.Target[:]...)
	var tmp2 [2]byte
	binary.LittleEndian.PutUint16(tmp2[:], msg.ExtranonceSize)
	payload = append(payload, tmp2[:]...)
	var err error
	payload, err = putB0_32(payload, msg.ExtranoncePrefix)
	if err != nil {
		return nil, err
	}
	binary.LittleEndian.PutUint32(tmp4[:], msg.GroupChannelID)
	payload = append(payload, tmp4[:]...)
	return encodeStratumV2Frame(stratumV2Frame{
		ExtensionType: stratumV2CoreExtensionType,
		MsgType:       stratumV2MsgTypeOpenExtendedMiningChannelSuccess,
		Payload:       payload,
	})
}

func decodeStratumV2OpenExtendedMiningChannelSuccessPayload(payload []byte) (stratumV2WireOpenExtendedMiningChannelSuccess, error) {
	if len(payload) < 47 {
		return stratumV2WireOpenExtendedMiningChannelSuccess{}, fmt.Errorf("openextendedminingchannel.success payload too short: %d", len(payload))
	}
	out := stratumV2WireOpenExtendedMiningChannelSuccess{
		RequestID: binary.LittleEndian.Uint32(payload[0:4]),
		ChannelID: binary.LittleEndian.Uint32(payload[4:8]),
	}
	copy(out.Target[:], payload[8:40])
	out.ExtranonceSize = binary.LittleEndian.Uint16(payload[40:42])
	var err error
	off := 42
	out.ExtranoncePrefix, off, err = readB0_32(payload, off)
	if err != nil {
		return stratumV2WireOpenExtendedMiningChannelSuccess{}, err
	}
	if off+4 != len(payload) {
		return stratumV2WireOpenExtendedMiningChannelSuccess{}, fmt.Errorf("openextendedminingchannel.success payload len=%d invalid tail", len(payload))
	}
	out.GroupChannelID = binary.LittleEndian.Uint32(payload[off : off+4])
	return out, nil
}

func encodeStratumV2SetTargetFrame(msg stratumV2WireSetTarget) ([]byte, error) {
	payload := make([]byte, 36)
	binary.LittleEndian.PutUint32(payload[0:4], msg.ChannelID)
	copy(payload[4:36], msg.MaximumTarget[:])
	return encodeStratumV2Frame(stratumV2Frame{
		ExtensionType: stratumV2CoreExtensionType | stratumV2ChannelMsgBit,
		MsgType:       stratumV2MsgTypeSetTarget,
		Payload:       payload,
	})
}

func decodeStratumV2SetTargetPayload(payload []byte) (stratumV2WireSetTarget, error) {
	if len(payload) != 36 {
		return stratumV2WireSetTarget{}, fmt.Errorf("settarget payload len=%d want 36", len(payload))
	}
	var out stratumV2WireSetTarget
	out.ChannelID = binary.LittleEndian.Uint32(payload[0:4])
	copy(out.MaximumTarget[:], payload[4:36])
	return out, nil
}

func encodeStratumV2SubmitSharesStandardFrame(msg stratumV2WireSubmitSharesStandard) ([]byte, error) {
	payload := make([]byte, 24)
	binary.LittleEndian.PutUint32(payload[0:4], msg.ChannelID)
	binary.LittleEndian.PutUint32(payload[4:8], msg.SequenceNumber)
	binary.LittleEndian.PutUint32(payload[8:12], msg.JobID)
	binary.LittleEndian.PutUint32(payload[12:16], msg.Nonce)
	binary.LittleEndian.PutUint32(payload[16:20], msg.NTime)
	binary.LittleEndian.PutUint32(payload[20:24], msg.Version)
	return encodeStratumV2Frame(stratumV2Frame{
		ExtensionType: stratumV2CoreExtensionType | stratumV2ChannelMsgBit,
		MsgType:       stratumV2MsgTypeSubmitSharesStandard,
		Payload:       payload,
	})
}

func decodeStratumV2SubmitSharesStandardPayload(payload []byte) (stratumV2WireSubmitSharesStandard, error) {
	if len(payload) != 24 {
		return stratumV2WireSubmitSharesStandard{}, fmt.Errorf("submitsharesstandard payload len=%d want 24", len(payload))
	}
	return stratumV2WireSubmitSharesStandard{
		ChannelID:      binary.LittleEndian.Uint32(payload[0:4]),
		SequenceNumber: binary.LittleEndian.Uint32(payload[4:8]),
		JobID:          binary.LittleEndian.Uint32(payload[8:12]),
		Nonce:          binary.LittleEndian.Uint32(payload[12:16]),
		NTime:          binary.LittleEndian.Uint32(payload[16:20]),
		Version:        binary.LittleEndian.Uint32(payload[20:24]),
	}, nil
}

func encodeStratumV2SubmitSharesExtendedFrame(msg stratumV2WireSubmitSharesExtended) ([]byte, error) {
	if len(msg.Extranonce) > 32 {
		return nil, fmt.Errorf("submitsharesextended extranonce too large: %d", len(msg.Extranonce))
	}
	payload := make([]byte, 24+1+len(msg.Extranonce))
	binary.LittleEndian.PutUint32(payload[0:4], msg.ChannelID)
	binary.LittleEndian.PutUint32(payload[4:8], msg.SequenceNumber)
	binary.LittleEndian.PutUint32(payload[8:12], msg.JobID)
	binary.LittleEndian.PutUint32(payload[12:16], msg.Nonce)
	binary.LittleEndian.PutUint32(payload[16:20], msg.NTime)
	binary.LittleEndian.PutUint32(payload[20:24], msg.Version)
	payload[24] = byte(len(msg.Extranonce))
	copy(payload[25:], msg.Extranonce)
	return encodeStratumV2Frame(stratumV2Frame{
		ExtensionType: stratumV2CoreExtensionType | stratumV2ChannelMsgBit,
		MsgType:       stratumV2MsgTypeSubmitSharesExtended,
		Payload:       payload,
	})
}

func decodeStratumV2SubmitSharesExtendedPayload(payload []byte) (stratumV2WireSubmitSharesExtended, error) {
	if len(payload) < 25 {
		return stratumV2WireSubmitSharesExtended{}, fmt.Errorf("submitsharesextended payload too short: %d", len(payload))
	}
	n := int(payload[24])
	if n > 32 {
		return stratumV2WireSubmitSharesExtended{}, fmt.Errorf("submitsharesextended extranonce len out of range: %d", n)
	}
	if len(payload) != 25+n {
		return stratumV2WireSubmitSharesExtended{}, fmt.Errorf("submitsharesextended payload len=%d want %d", len(payload), 25+n)
	}
	out := stratumV2WireSubmitSharesExtended{
		ChannelID:      binary.LittleEndian.Uint32(payload[0:4]),
		SequenceNumber: binary.LittleEndian.Uint32(payload[4:8]),
		JobID:          binary.LittleEndian.Uint32(payload[8:12]),
		Nonce:          binary.LittleEndian.Uint32(payload[12:16]),
		NTime:          binary.LittleEndian.Uint32(payload[16:20]),
		Version:        binary.LittleEndian.Uint32(payload[20:24]),
	}
	if n > 0 {
		out.Extranonce = append([]byte(nil), payload[25:25+n]...)
	}
	return out, nil
}

func encodeStratumV2SubmitSharesSuccessFrame(msg stratumV2WireSubmitSharesSuccess) ([]byte, error) {
	payload := make([]byte, 20)
	binary.LittleEndian.PutUint32(payload[0:4], msg.ChannelID)
	binary.LittleEndian.PutUint32(payload[4:8], msg.LastSequenceNumber)
	binary.LittleEndian.PutUint32(payload[8:12], msg.NewSubmitsAcceptedCount)
	binary.LittleEndian.PutUint64(payload[12:20], msg.NewSharesSum)
	return encodeStratumV2Frame(stratumV2Frame{
		ExtensionType: stratumV2CoreExtensionType | stratumV2ChannelMsgBit,
		MsgType:       stratumV2MsgTypeSubmitSharesSuccess,
		Payload:       payload,
	})
}

func decodeStratumV2SubmitSharesSuccessPayload(payload []byte) (stratumV2WireSubmitSharesSuccess, error) {
	if len(payload) != 20 {
		return stratumV2WireSubmitSharesSuccess{}, fmt.Errorf("submitshares.success payload len=%d want 20", len(payload))
	}
	return stratumV2WireSubmitSharesSuccess{
		ChannelID:               binary.LittleEndian.Uint32(payload[0:4]),
		LastSequenceNumber:      binary.LittleEndian.Uint32(payload[4:8]),
		NewSubmitsAcceptedCount: binary.LittleEndian.Uint32(payload[8:12]),
		NewSharesSum:            binary.LittleEndian.Uint64(payload[12:20]),
	}, nil
}

func encodeStratumV2SubmitSharesErrorFrame(msg stratumV2WireSubmitSharesError) ([]byte, error) {
	if len(msg.ErrorCode) > 255 {
		return nil, fmt.Errorf("submitshares.error error_code too long: %d", len(msg.ErrorCode))
	}
	payload := make([]byte, 8+1+len(msg.ErrorCode))
	binary.LittleEndian.PutUint32(payload[0:4], msg.ChannelID)
	binary.LittleEndian.PutUint32(payload[4:8], msg.SequenceNumber)
	payload[8] = byte(len(msg.ErrorCode))
	copy(payload[9:], msg.ErrorCode)
	return encodeStratumV2Frame(stratumV2Frame{
		ExtensionType: stratumV2CoreExtensionType | stratumV2ChannelMsgBit,
		MsgType:       stratumV2MsgTypeSubmitSharesError,
		Payload:       payload,
	})
}

func decodeStratumV2SubmitSharesErrorPayload(payload []byte) (stratumV2WireSubmitSharesError, error) {
	if len(payload) < 9 {
		return stratumV2WireSubmitSharesError{}, fmt.Errorf("submitshares.error payload too short: %d", len(payload))
	}
	n := int(payload[8])
	if len(payload) != 9+n {
		return stratumV2WireSubmitSharesError{}, fmt.Errorf("submitshares.error payload len=%d want %d", len(payload), 9+n)
	}
	return stratumV2WireSubmitSharesError{
		ChannelID:      binary.LittleEndian.Uint32(payload[0:4]),
		SequenceNumber: binary.LittleEndian.Uint32(payload[4:8]),
		ErrorCode:      string(payload[9 : 9+n]),
	}, nil
}

func decodeStratumV2SubmitWireFrame(b []byte) (any, error) {
	frame, err := decodeStratumV2Frame(b)
	if err != nil {
		return nil, err
	}
	if frame.baseExtensionType() != stratumV2CoreExtensionType {
		return nil, fmt.Errorf("unsupported sv2 extension_type: %#04x", frame.baseExtensionType())
	}
	if !frame.isChannelMessage() {
		return nil, fmt.Errorf("submit message missing channel_msg bit")
	}
	switch frame.MsgType {
	case stratumV2MsgTypeSubmitSharesStandard:
		return decodeStratumV2SubmitSharesStandardPayload(frame.Payload)
	case stratumV2MsgTypeSubmitSharesExtended:
		return decodeStratumV2SubmitSharesExtendedPayload(frame.Payload)
	case stratumV2MsgTypeSubmitSharesSuccess:
		return decodeStratumV2SubmitSharesSuccessPayload(frame.Payload)
	case stratumV2MsgTypeSubmitSharesError:
		return decodeStratumV2SubmitSharesErrorPayload(frame.Payload)
	default:
		return nil, fmt.Errorf("unsupported sv2 submit msg_type: %#02x", frame.MsgType)
	}
}

func decodeStratumV2MiningWireFrame(b []byte) (any, error) {
	frame, err := decodeStratumV2Frame(b)
	if err != nil {
		return nil, err
	}
	if frame.baseExtensionType() != stratumV2CoreExtensionType {
		return nil, fmt.Errorf("unsupported sv2 extension_type: %#04x", frame.baseExtensionType())
	}
	switch frame.MsgType {
	case stratumV2MsgTypeOpenStandardMiningChannel:
		if frame.isChannelMessage() {
			return nil, fmt.Errorf("openstandardminingchannel must not set channel_msg bit")
		}
		return decodeStratumV2OpenStandardMiningChannelPayload(frame.Payload)
	case stratumV2MsgTypeOpenStandardMiningChannelSuccess:
		if frame.isChannelMessage() {
			return nil, fmt.Errorf("openstandardminingchannel.success must not set channel_msg bit")
		}
		return decodeStratumV2OpenStandardMiningChannelSuccessPayload(frame.Payload)
	case stratumV2MsgTypeOpenExtendedMiningChannel:
		if frame.isChannelMessage() {
			return nil, fmt.Errorf("openextendedminingchannel must not set channel_msg bit")
		}
		return decodeStratumV2OpenExtendedMiningChannelPayload(frame.Payload)
	case stratumV2MsgTypeOpenExtendedMiningChannelSuccess:
		if frame.isChannelMessage() {
			return nil, fmt.Errorf("openextendedminingchannel.success must not set channel_msg bit")
		}
		return decodeStratumV2OpenExtendedMiningChannelSuccessPayload(frame.Payload)
	case stratumV2MsgTypeSetTarget:
		if !frame.isChannelMessage() {
			return nil, fmt.Errorf("settarget must set channel_msg bit")
		}
		return decodeStratumV2SetTargetPayload(frame.Payload)
	case stratumV2MsgTypeSetExtranoncePrefix:
		if !frame.isChannelMessage() {
			return nil, fmt.Errorf("setextranonceprefix must set channel_msg bit")
		}
		return decodeStratumV2SetExtranoncePrefixPayload(frame.Payload)
	case stratumV2MsgTypeNewMiningJob:
		if frame.isChannelMessage() {
			return nil, fmt.Errorf("newminingjob must not set channel_msg bit")
		}
		return decodeStratumV2NewMiningJobPayload(frame.Payload)
	case stratumV2MsgTypeSubmitSharesStandard, stratumV2MsgTypeSubmitSharesExtended, stratumV2MsgTypeSubmitSharesSuccess, stratumV2MsgTypeSubmitSharesError:
		return decodeStratumV2SubmitWireFrame(b)
	default:
		return nil, fmt.Errorf("unsupported sv2 mining msg_type: %#02x", frame.MsgType)
	}
}
