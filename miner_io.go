package main

import (
	"encoding/json"
	"io"
	"strconv"
	"time"
)

func (mc *MinerConn) writeJSON(v any) error {
	b, err := fastJSONMarshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return mc.writeBytes(b)
}

func (mc *MinerConn) writeBytes(b []byte) error {
	mc.writeMu.Lock()
	defer mc.writeMu.Unlock()

	return mc.writeBytesLocked(b)
}

func (mc *MinerConn) writeBytesLocked(b []byte) error {
	if err := mc.conn.SetWriteDeadline(time.Now().Add(stratumWriteTimeout)); err != nil {
		return err
	}
	logNetMessage("send", b)
	for len(b) > 0 {
		n, err := mc.conn.Write(b)
		if n > 0 {
			b = b[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrUnexpectedEOF
		}
	}
	return nil
}

func (mc *MinerConn) writeResponse(resp StratumResponse) {
	if err := mc.writeJSON(resp); err != nil {
		logger.Error("write error", "remote", mc.id, "error", err)
	}
}

var (
	cannedPongSuffix       = []byte(`,"result":"pong","error":null}`)
	cannedEmptySliceSuffix = []byte(`,"result":[],"error":null}`)
	cannedTrueSuffix       = []byte(`,"result":true,"error":null}`)
	cannedSubscribePrefix  = []byte(`,"result":[[["mining.set_difficulty","1"],["mining.notify","1"]],`)
	cannedSubscribeSuffix  = []byte(`],"error":null}`)
)

func (mc *MinerConn) writePongResponse(id any) {
	if mc.cfg.StratumFastEncodeEnabled {
		mc.sendCannedResponse("pong", id, cannedPongSuffix)
		return
	}
	mc.writeResponse(StratumResponse{
		ID:     id,
		Result: "pong",
		Error:  nil,
	})
}

func (mc *MinerConn) writeEmptySliceResponse(id any) {
	if mc.cfg.StratumFastEncodeEnabled {
		mc.sendCannedResponse("empty slice", id, cannedEmptySliceSuffix)
		return
	}
	mc.writeResponse(StratumResponse{
		ID:     id,
		Result: []any{},
		Error:  nil,
	})
}

func (mc *MinerConn) writeTrueResponse(id any) {
	if mc.cfg.StratumFastEncodeEnabled {
		mc.sendCannedResponse("true", id, cannedTrueSuffix)
		return
	}
	mc.writeResponse(StratumResponse{
		ID:     id,
		Result: true,
		Error:  nil,
	})
}

func (mc *MinerConn) writeSubscribeResponse(id any, extranonce1Hex string, extranonce2Size int) {
	if mc.cfg.StratumFastEncodeEnabled {
		if err := mc.writeCannedSubscribeResponse(id, extranonce1Hex, extranonce2Size); err != nil {
			logger.Error("write canned response", "remote", mc.id, "label", "subscribe", "error", err)
		}
		return
	}

	mc.writeResponse(StratumResponse{
		ID: id,
		Result: []any{
			[][]any{
				{"mining.set_difficulty", "1"},
				{"mining.notify", "1"},
			},
			extranonce1Hex,
			extranonce2Size,
		},
		Error: nil,
	})
}

func (mc *MinerConn) sendCannedResponse(label string, id any, suffix []byte) {
	if err := mc.writeCannedResponse(id, suffix); err != nil {
		logger.Error("write canned response", "remote", mc.id, "label", label, "error", err)
	}
}

func (mc *MinerConn) writeCannedResponse(id any, suffix []byte) error {
	mc.writeMu.Lock()
	defer mc.writeMu.Unlock()

	buf := mc.writeScratch[:0]
	buf = append(buf, `{"id":`...)
	var err error
	buf, err = appendJSONValue(buf, id)
	if err != nil {
		return err
	}
	buf = append(buf, suffix...)
	buf = append(buf, '\n')

	// Persist the (possibly grown) scratch for reuse.
	mc.writeScratch = buf[:0]
	return mc.writeBytesLocked(buf)
}

func (mc *MinerConn) writeCannedSubscribeResponse(id any, extranonce1Hex string, extranonce2Size int) error {
	mc.writeMu.Lock()
	defer mc.writeMu.Unlock()

	buf := mc.writeScratch[:0]
	buf, err := appendSubscribeResponseBytes(buf, id, extranonce1Hex, extranonce2Size)
	if err != nil {
		return err
	}
	buf = append(buf, '\n')

	mc.writeScratch = buf[:0]
	return mc.writeBytesLocked(buf)
}

func buildSubscribeResponseBytes(id any, extranonce1Hex string, extranonce2Size int) ([]byte, error) {
	buf := make([]byte, 0, 128+len(extranonce1Hex))
	buf, err := appendSubscribeResponseBytes(buf, id, extranonce1Hex, extranonce2Size)
	if err != nil {
		return nil, err
	}
	buf = append(buf, '\n')
	return buf, nil
}

func appendSubscribeResponseBytes(buf []byte, id any, extranonce1Hex string, extranonce2Size int) ([]byte, error) {
	// {"id":<id>,"result":[[["mining.set_difficulty","1"],["mining.notify","1"]],"<ex1>",<en2Size>],"error":null}
	buf = append(buf, `{"id":`...)
	var err error
	buf, err = appendJSONValue(buf, id)
	if err != nil {
		return nil, err
	}
	buf = append(buf, cannedSubscribePrefix...)
	buf = strconv.AppendQuote(buf, extranonce1Hex)
	buf = append(buf, ',')
	buf = strconv.AppendInt(buf, int64(extranonce2Size), 10)
	buf = append(buf, cannedSubscribeSuffix...)
	return buf, nil
}

func appendJSONValue(buf []byte, value any) ([]byte, error) {
	switch v := value.(type) {
	case nil:
		return append(buf, "null"...), nil
	case string:
		return strconv.AppendQuote(buf, v), nil
	case bool:
		if v {
			return append(buf, "true"...), nil
		}
		return append(buf, "false"...), nil
	case json.Number:
		return append(buf, v...), nil
	case float64:
		return strconv.AppendFloat(buf, v, 'g', -1, 64), nil
	case float32:
		return strconv.AppendFloat(buf, float64(v), 'g', -1, 32), nil
	case int:
		return strconv.AppendInt(buf, int64(v), 10), nil
	case int8:
		return strconv.AppendInt(buf, int64(v), 10), nil
	case int16:
		return strconv.AppendInt(buf, int64(v), 10), nil
	case int32:
		return strconv.AppendInt(buf, int64(v), 10), nil
	case int64:
		return strconv.AppendInt(buf, v, 10), nil
	case uint:
		return strconv.AppendUint(buf, uint64(v), 10), nil
	case uint8:
		return strconv.AppendUint(buf, uint64(v), 10), nil
	case uint16:
		return strconv.AppendUint(buf, uint64(v), 10), nil
	case uint32:
		return strconv.AppendUint(buf, uint64(v), 10), nil
	case uint64:
		return strconv.AppendUint(buf, v, 10), nil
	default:
		b, err := fastJSONMarshal(value)
		if err != nil {
			return buf, err
		}
		return append(buf, b...), nil
	}
}
