package main

import (
	"encoding/json"
	"strconv"
	"time"
)

func (mc *MinerConn) writeJSON(v interface{}) error {
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

	if err := mc.conn.SetWriteDeadline(time.Now().Add(stratumWriteTimeout)); err != nil {
		return err
	}
	logNetMessage("send", b)
	if _, err := mc.writer.Write(b); err != nil {
		return err
	}
	return mc.writer.Flush()
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
)

func (mc *MinerConn) writePongResponse(id interface{}) {
	mc.sendCannedResponse("pong", id, cannedPongSuffix)
}

func (mc *MinerConn) writeEmptySliceResponse(id interface{}) {
	mc.sendCannedResponse("empty slice", id, cannedEmptySliceSuffix)
}

func (mc *MinerConn) writeTrueResponse(id interface{}) {
	mc.sendCannedResponse("true", id, cannedTrueSuffix)
}

func (mc *MinerConn) sendCannedResponse(label string, id interface{}, suffix []byte) {
	if err := mc.writeCannedResponse(id, suffix); err != nil {
		logger.Error("write canned response", "remote", mc.id, "label", label, "error", err)
	}
}

func (mc *MinerConn) writeCannedResponse(id interface{}, suffix []byte) error {
	buf := make([]byte, 0, 64)
	buf = append(buf, `{"id":`...)
	var err error
	buf, err = appendJSONValue(buf, id)
	if err != nil {
		return err
	}
	buf = append(buf, suffix...)
	buf = append(buf, '\n')
	return mc.writeBytes(buf)
}

func appendJSONValue(buf []byte, value interface{}) ([]byte, error) {
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

func buildStringRequest(id interface{}, method string, params []string) StratumRequest {
	iface := make([]interface{}, len(params))
	for i, p := range params {
		iface[i] = p
	}
	return StratumRequest{
		ID:     id,
		Method: method,
		Params: iface,
	}
}
