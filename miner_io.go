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
	if mc.closed.Load() {
		return io.ErrClosedPipe
	}
	mc.writeMu.Lock()
	defer mc.writeMu.Unlock()

	// A writer may have failed and closed the session while this caller was
	// waiting for writeMu. Never touch the socket after terminal state has been
	// published.
	if mc.closed.Load() {
		return io.ErrClosedPipe
	}
	err := mc.writeBytesLocked(b)
	if err != nil {
		// Publish terminal state before releasing writeMu so queued writers cannot
		// slip another response or notification onto an ambiguous stream. The
		// caller performs cleanup after writeJSON has released this lock.
		mc.closed.Store(true)
	}
	return err
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

func (mc *MinerConn) writeResponse(resp StratumResponse) bool {
	if resp.ID == nil {
		return true
	}
	if mc.closed.Load() {
		return false
	}
	if err := mc.writeJSON(resp); err != nil {
		logger.Error("write error", "remote", mc.id, "error", err)
		mc.Close("stratum response write failed")
		return false
	}
	return true
}

func (mc *MinerConn) sendClientShowMessage(message string) {
	if mc == nil || mc.conn == nil || mc.closed.Load() {
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
		mc.Close("client.show_message write failed")
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

func (mc *MinerConn) writeTrueResponse(id any) bool {
	return mc.writeResponse(StratumResponse{
		ID:     id,
		Result: true,
		Error:  nil,
	})
}

func (mc *MinerConn) writeSubscribeResponse(id any, extranonce1Hex string, extranonce2Size int, subID string) bool {
	if strings.TrimSpace(subID) == "" {
		subID = "1"
	}
	subs := subscribeMethodTuples(subID, mc.cfg.CKPoolEmulate)
	return mc.writeResponse(StratumResponse{
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
