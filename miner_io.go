package main

import (
	"io"
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

func (mc *MinerConn) writePongResponse(id any) {
	mc.writeResponse(StratumResponse{
		ID:     id,
		Result: "pong",
		Error:  nil,
	})
}

func (mc *MinerConn) writeEmptySliceResponse(id any) {
	mc.writeResponse(StratumResponse{
		ID:     id,
		Result: []any{},
		Error:  nil,
	})
}

func (mc *MinerConn) writeTrueResponse(id any) {
	mc.writeResponse(StratumResponse{
		ID:     id,
		Result: true,
		Error:  nil,
	})
}

func (mc *MinerConn) writeSubscribeResponse(id any, extranonce1Hex string, extranonce2Size int, subID string) {
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
