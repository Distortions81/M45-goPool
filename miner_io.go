package main

import (
	"encoding/json"
	"io"
	"strconv"
	"strings"
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

func (mc *MinerConn) sendClientShowMessage(message string) {
	if mc == nil || mc.conn == nil {
		return
	}
	if mc.protocolStateSnapshot().sv2 != nil {
		return
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return
	}
	if len(message) > 512 {
		message = message[:512]
	}
	msg := StratumMessage{
		ID:     nil,
		Method: "client.show_message",
		Params: []any{message},
	}
	worker := mc.currentWorker()
	fields := []any{"remote", mc.id, "message", message}
	if worker != "" {
		fields = append(fields, "worker", worker)
	}
	switch {
	case strings.HasPrefix(message, "Banned:"):
		logger.Warn("sending client.show_message", fields...)
	case strings.HasPrefix(message, "Warning:"):
		logger.Warn("sending client.show_message", fields...)
	default:
		logger.Info("sending client.show_message", fields...)
	}
	if err := mc.writeJSON(msg); err != nil {
		errFields := append([]any{}, fields...)
		errFields = append(errFields, "error", err)
		logger.Warn("client.show_message write error", errFields...)
	}
}

var (
	cannedPongSuffix       = []byte(`,"result":"pong","error":null}`)
	cannedEmptySliceSuffix = []byte(`,"result":[],"error":null}`)
	cannedTrueSuffix       = []byte(`,"result":true,"error":null}`)
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

func (mc *MinerConn) writePongResponseRawID(idRaw []byte) {
	if mc.cfg.StratumFastEncodeEnabled {
		mc.sendCannedResponseRawID("pong", idRaw, cannedPongSuffix)
		return
	}
	idVal, _, ok := parseJSONValue(idRaw, 0)
	if !ok {
		return
	}
	mc.writePongResponse(idVal)
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

func (mc *MinerConn) writeEmptySliceResponseRawID(idRaw []byte) {
	if mc.cfg.StratumFastEncodeEnabled {
		mc.sendCannedResponseRawID("empty slice", idRaw, cannedEmptySliceSuffix)
		return
	}
	idVal, _, ok := parseJSONValue(idRaw, 0)
	if !ok {
		return
	}
	mc.writeEmptySliceResponse(idVal)
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

func (mc *MinerConn) writeTrueResponseRawID(idRaw []byte) {
	if mc.cfg.StratumFastEncodeEnabled {
		mc.sendCannedResponseRawID("true", idRaw, cannedTrueSuffix)
		return
	}
	idVal, _, ok := parseJSONValue(idRaw, 0)
	if !ok {
		return
	}
	mc.writeTrueResponse(idVal)
}

func (mc *MinerConn) writeSubscribeResponse(id any, extranonce1Hex string, extranonce2Size int, subID string) {
	if mc.cfg.StratumFastEncodeEnabled {
		if err := mc.writeCannedSubscribeResponse(id, extranonce1Hex, extranonce2Size, subID); err != nil {
			logger.Error("write canned response", "remote", mc.id, "label", "subscribe", "error", err)
		}
		return
	}

	if strings.TrimSpace(subID) == "" {
		subID = "1"
	}
	subs := subscribeMethodTuples(subID, mc.cfg.CKPoolEmulate)
	mc.writeResponse(StratumResponse{
		ID: id,
		Result: []any{
			subs,
			extranonce1Hex,
			extranonce2Size,
		},
		Error: nil,
	})
}

func (mc *MinerConn) writeSubscribeResponseRawID(idRaw []byte, extranonce1Hex string, extranonce2Size int, subID string) {
	if mc.cfg.StratumFastEncodeEnabled {
		if err := mc.writeCannedSubscribeResponseRawID(idRaw, extranonce1Hex, extranonce2Size, subID); err != nil {
			logger.Error("write canned response", "remote", mc.id, "label", "subscribe", "error", err)
		}
		return
	}
	idVal, _, ok := parseJSONValue(idRaw, 0)
	if !ok {
		return
	}
	mc.writeSubscribeResponse(idVal, extranonce1Hex, extranonce2Size, subID)
}

func (mc *MinerConn) sendCannedResponse(label string, id any, suffix []byte) {
	if err := mc.writeCannedResponse(id, suffix); err != nil {
		logger.Error("write canned response", "remote", mc.id, "label", label, "error", err)
	}
}

func (mc *MinerConn) sendCannedResponseRawID(label string, idRaw []byte, suffix []byte) {
	if err := mc.writeCannedResponseRawID(idRaw, suffix); err != nil {
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

func (mc *MinerConn) writeCannedResponseRawID(idRaw []byte, suffix []byte) error {
	mc.writeMu.Lock()
	defer mc.writeMu.Unlock()

	buf := mc.writeScratch[:0]
	buf = append(buf, `{"id":`...)
	buf = append(buf, idRaw...)
	buf = append(buf, suffix...)
	buf = append(buf, '\n')

	mc.writeScratch = buf[:0]
	return mc.writeBytesLocked(buf)
}

func (mc *MinerConn) writeCannedSubscribeResponse(id any, extranonce1Hex string, extranonce2Size int, subID string) error {
	mc.writeMu.Lock()
	defer mc.writeMu.Unlock()

	buf := mc.writeScratch[:0]
	buf, err := appendSubscribeResponseBytes(buf, id, extranonce1Hex, extranonce2Size, subID, mc.cfg.CKPoolEmulate)
	if err != nil {
		return err
	}
	buf = append(buf, '\n')

	mc.writeScratch = buf[:0]
	return mc.writeBytesLocked(buf)
}

func (mc *MinerConn) writeCannedSubscribeResponseRawID(idRaw []byte, extranonce1Hex string, extranonce2Size int, subID string) error {
	mc.writeMu.Lock()
	defer mc.writeMu.Unlock()

	// subID is a logical identifier used by miners to correlate subscriptions.
	// Don't use strings.TrimSpace here; it is unnecessary overhead in a hot path.
	if subID == "" {
		subID = "1"
	}
	subIDDigits := isASCIIAll(subID, isASCIIDigit)
	ex1Hex := isASCIIAll(extranonce1Hex, isASCIIHexDigit)
	ckpoolEmulate := mc.cfg.CKPoolEmulate

	buf := mc.writeScratch[:0]
	buf = append(buf, `{"id":`...)
	buf = append(buf, idRaw...)
	buf = append(buf, `,"result":[[[`...)
	if ckpoolEmulate {
		buf = append(buf, `"mining.notify",`...)
		if subIDDigits {
			buf = append(buf, '"')
			buf = append(buf, subID...)
			buf = append(buf, '"')
		} else {
			buf = strconv.AppendQuote(buf, subID)
		}
	} else {
		buf = append(buf, `"mining.set_difficulty",`...)
		if subIDDigits {
			buf = append(buf, '"')
			buf = append(buf, subID...)
			buf = append(buf, '"')
		} else {
			buf = strconv.AppendQuote(buf, subID)
		}
		buf = append(buf, `],["mining.notify",`...)
		if subIDDigits {
			buf = append(buf, '"')
			buf = append(buf, subID...)
			buf = append(buf, '"')
		} else {
			buf = strconv.AppendQuote(buf, subID)
		}
		buf = append(buf, `],["mining.set_extranonce",`...)
		if subIDDigits {
			buf = append(buf, '"')
			buf = append(buf, subID...)
			buf = append(buf, '"')
		} else {
			buf = strconv.AppendQuote(buf, subID)
		}
		buf = append(buf, `],["mining.set_version_mask",`...)
		if subIDDigits {
			buf = append(buf, '"')
			buf = append(buf, subID...)
			buf = append(buf, '"')
		} else {
			buf = strconv.AppendQuote(buf, subID)
		}
	}
	buf = append(buf, `]],`...)
	if ex1Hex {
		buf = append(buf, '"')
		buf = append(buf, extranonce1Hex...)
		buf = append(buf, '"')
	} else {
		buf = strconv.AppendQuote(buf, extranonce1Hex)
	}
	buf = append(buf, ',')
	buf = strconv.AppendInt(buf, int64(extranonce2Size), 10)
	buf = append(buf, cannedSubscribeSuffix...)
	buf = append(buf, '\n')

	mc.writeScratch = buf[:0]
	return mc.writeBytesLocked(buf)
}

func buildSubscribeResponseBytes(id any, extranonce1Hex string, extranonce2Size int) ([]byte, error) {
	return buildSubscribeResponseBytesWithMode(id, extranonce1Hex, extranonce2Size, true)
}

func buildSubscribeResponseBytesWithMode(id any, extranonce1Hex string, extranonce2Size int, ckpoolEmulate bool) ([]byte, error) {
	// The subscribe response JSON is fairly large (multiple method strings).
	// Use a capacity that avoids buffer growth in typical cases.
	buf := make([]byte, 0, 256+len(extranonce1Hex))
	buf, err := appendSubscribeResponseBytes(buf, id, extranonce1Hex, extranonce2Size, "1", ckpoolEmulate)
	if err != nil {
		return nil, err
	}
	buf = append(buf, '\n')
	return buf, nil
}

func appendSubscribeResponseBytes(buf []byte, id any, extranonce1Hex string, extranonce2Size int, subID string, ckpoolEmulate bool) ([]byte, error) {
	// subscribe result is configurable:
	// - CKPool emulate: [[["mining.notify","<subid>"]],"<ex1>",<en2Size>]
	// - Extended:       [[["mining.set_difficulty","<subid>"],["mining.notify","<subid>"],["mining.set_extranonce","<subid>"],["mining.set_version_mask","<subid>"]],"<ex1>",<en2Size>]
	// Avoid TrimSpace: subID is generated by us, and if it ever arrives blank we
	// just use the canonical "1".
	if subID == "" {
		subID = "1"
	}
	subIDDigits := isASCIIAll(subID, isASCIIDigit)
	ex1Hex := isASCIIAll(extranonce1Hex, isASCIIHexDigit)

	buf = append(buf, `{"id":`...)
	var err error
	buf, err = appendJSONValue(buf, id)
	if err != nil {
		return nil, err
	}
	buf = append(buf, `,"result":[[[`...)
	if ckpoolEmulate {
		buf = append(buf, `"mining.notify",`...)
		if subIDDigits {
			buf = append(buf, '"')
			buf = append(buf, subID...)
			buf = append(buf, '"')
		} else {
			buf = strconv.AppendQuote(buf, subID)
		}
	} else {
		buf = append(buf, `"mining.set_difficulty",`...)
		if subIDDigits {
			buf = append(buf, '"')
			buf = append(buf, subID...)
			buf = append(buf, '"')
		} else {
			buf = strconv.AppendQuote(buf, subID)
		}
		buf = append(buf, `],["mining.notify",`...)
		if subIDDigits {
			buf = append(buf, '"')
			buf = append(buf, subID...)
			buf = append(buf, '"')
		} else {
			buf = strconv.AppendQuote(buf, subID)
		}
		buf = append(buf, `],["mining.set_extranonce",`...)
		if subIDDigits {
			buf = append(buf, '"')
			buf = append(buf, subID...)
			buf = append(buf, '"')
		} else {
			buf = strconv.AppendQuote(buf, subID)
		}
		buf = append(buf, `],["mining.set_version_mask",`...)
		if subIDDigits {
			buf = append(buf, '"')
			buf = append(buf, subID...)
			buf = append(buf, '"')
		} else {
			buf = strconv.AppendQuote(buf, subID)
		}
	}
	buf = append(buf, `]],`...)
	if ex1Hex {
		buf = append(buf, '"')
		buf = append(buf, extranonce1Hex...)
		buf = append(buf, '"')
	} else {
		buf = strconv.AppendQuote(buf, extranonce1Hex)
	}
	buf = append(buf, ',')
	buf = strconv.AppendInt(buf, int64(extranonce2Size), 10)
	buf = append(buf, cannedSubscribeSuffix...)
	return buf, nil
}

func subscribeMethodTuples(subID string, ckpoolEmulate bool) [][]any {
	if ckpoolEmulate {
		return [][]any{
			{"mining.notify", subID},
		}
	}
	return [][]any{
		{"mining.set_difficulty", subID},
		{"mining.notify", subID},
		{"mining.set_extranonce", subID},
		{"mining.set_version_mask", subID},
	}
}

func isASCIIAll(s string, fn func(byte) bool) bool {
	for i := 0; i < len(s); i++ {
		if !fn(s[i]) {
			return false
		}
	}
	return true
}

func isASCIIDigit(b byte) bool { return b >= '0' && b <= '9' }

func isASCIIHexDigit(b byte) bool {
	return (b >= '0' && b <= '9') ||
		(b >= 'a' && b <= 'f') ||
		(b >= 'A' && b <= 'F')
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
