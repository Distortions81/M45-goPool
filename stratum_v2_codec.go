package main

import (
	"encoding/binary"
	"fmt"
)

const (
	stratumV2FrameHeaderLen     = 6
	stratumV2CoreExtensionType  = uint16(0x0000)
	stratumV2ChannelMsgBit      = uint16(0x8000)
	stratumV2MaxFramePayloadLen = 0xFFFFFF

	stratumV2MsgTypeSubmitSharesStandard = uint8(0x1a)
	stratumV2MsgTypeSubmitSharesExtended = uint8(0x1b)
	stratumV2MsgTypeSubmitSharesSuccess  = uint8(0x1c)
	stratumV2MsgTypeSubmitSharesError    = uint8(0x1d)
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
