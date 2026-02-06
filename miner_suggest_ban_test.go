package main

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

type writeRecorderConn struct {
	buf    bytes.Buffer
	closed bool
}

func (c *writeRecorderConn) Read([]byte) (int, error)         { return 0, nil }
func (c *writeRecorderConn) LocalAddr() net.Addr              { return &net.IPAddr{} }
func (c *writeRecorderConn) RemoteAddr() net.Addr             { return &net.IPAddr{} }
func (c *writeRecorderConn) SetDeadline(time.Time) error      { return nil }
func (c *writeRecorderConn) SetReadDeadline(time.Time) error  { return nil }
func (c *writeRecorderConn) SetWriteDeadline(time.Time) error { return nil }

func (c *writeRecorderConn) Write(b []byte) (int, error) { return c.buf.Write(b) }

func (c *writeRecorderConn) Close() error {
	c.closed = true
	return nil
}

func (c *writeRecorderConn) String() string { return c.buf.String() }

func TestSuggestDifficultyOutOfRangeBansAndDisconnects(t *testing.T) {
	conn := &writeRecorderConn{}
	mc := &MinerConn{
		id:           "suggest-ban-miner",
		cfg:          Config{MinDifficulty: 1.0, MaxDifficulty: 2.0},
		conn:         conn,
		writer:       bufio.NewWriterSize(conn, 4096),
		statsUpdates: make(chan statsUpdate),
	}

	req := &StratumRequest{
		ID:     1,
		Method: "mining.suggest_difficulty",
		Params: []interface{}{0.5},
	}
	mc.suggestDifficulty(req)

	if !conn.closed {
		t.Fatalf("expected miner connection to be closed")
	}

	until, reason, _ := mc.banDetails()
	if reason == "" || !strings.Contains(reason, "outside pool limits") {
		t.Fatalf("expected ban reason to mention pool limits, got: %q", reason)
	}
	if until.Before(time.Now().Add(55 * time.Minute)) {
		t.Fatalf("expected ~1h ban, got until=%s", until)
	}

	out := conn.String()
	if !strings.Contains(out, "\"error\"") || !strings.Contains(out, "\"banned\"") {
		t.Fatalf("expected banned error response, got: %q", out)
	}
}

func TestSuggestDifficultyInRangeDoesNotBan(t *testing.T) {
	conn := &writeRecorderConn{}
	mc := &MinerConn{
		id:           "suggest-ok-miner",
		cfg:          Config{MinDifficulty: 1.0, MaxDifficulty: 2.0},
		conn:         conn,
		writer:       bufio.NewWriterSize(conn, 4096),
		statsUpdates: make(chan statsUpdate),
	}

	req := &StratumRequest{
		ID:     1,
		Method: "mining.suggest_difficulty",
		Params: []interface{}{1.5},
	}
	mc.suggestDifficulty(req)

	if conn.closed {
		t.Fatalf("did not expect miner connection to be closed")
	}
	until, _, _ := mc.banDetails()
	if !until.IsZero() {
		t.Fatalf("did not expect ban, got until=%s", until)
	}
}

func TestSuggestDifficultyZeroIsIgnoredAndDoesNotBan(t *testing.T) {
	conn := &writeRecorderConn{}
	mc := &MinerConn{
		id:           "suggest-zero-miner",
		cfg:          Config{MinDifficulty: 1.0, MaxDifficulty: 2.0},
		conn:         conn,
		writer:       bufio.NewWriterSize(conn, 4096),
		statsUpdates: make(chan statsUpdate),
	}

	req := &StratumRequest{
		ID:     1,
		Method: "mining.suggest_difficulty",
		Params: []interface{}{0.0},
	}
	mc.suggestDifficulty(req)

	if conn.closed {
		t.Fatalf("did not expect miner connection to be closed")
	}
	until, _, _ := mc.banDetails()
	if !until.IsZero() {
		t.Fatalf("did not expect ban, got until=%s", until)
	}
	out := conn.String()
	if !strings.Contains(out, "\"result\":true") {
		t.Fatalf("expected result=true response, got: %q", out)
	}
	if strings.Contains(out, "\"error\"") && strings.Contains(out, "invalid params") {
		t.Fatalf("expected no invalid params error, got: %q", out)
	}
}

func TestSuggestDifficultyNoParamsIsIgnoredAndDoesNotBan(t *testing.T) {
	conn := &writeRecorderConn{}
	mc := &MinerConn{
		id:           "suggest-noparams-miner",
		cfg:          Config{MinDifficulty: 1.0, MaxDifficulty: 2.0},
		conn:         conn,
		writer:       bufio.NewWriterSize(conn, 4096),
		statsUpdates: make(chan statsUpdate),
	}

	req := &StratumRequest{
		ID:     1,
		Method: "mining.suggest_difficulty",
		Params: nil,
	}
	mc.suggestDifficulty(req)

	if conn.closed {
		t.Fatalf("did not expect miner connection to be closed")
	}
	until, _, _ := mc.banDetails()
	if !until.IsZero() {
		t.Fatalf("did not expect ban, got until=%s", until)
	}
	out := conn.String()
	if !strings.Contains(out, "\"result\":true") {
		t.Fatalf("expected result=true response, got: %q", out)
	}
}

func TestSuggestTargetOutOfRangeBansAndDisconnects(t *testing.T) {
	conn := &writeRecorderConn{}
	mc := &MinerConn{
		id:           "suggest-target-ban-miner",
		cfg:          Config{MinDifficulty: 1.0, MaxDifficulty: 2.0},
		conn:         conn,
		writer:       bufio.NewWriterSize(conn, 4096),
		statsUpdates: make(chan statsUpdate),
	}

	// diff=0.5 is below the configured min=1.0
	target := targetFromDifficulty(0.5)
	req := &StratumRequest{
		ID:     1,
		Method: "mining.suggest_target",
		Params: []interface{}{fmt.Sprintf("%064x", target)},
	}
	mc.suggestTarget(req)

	if !conn.closed {
		t.Fatalf("expected miner connection to be closed")
	}
	until, reason, _ := mc.banDetails()
	if reason == "" || !strings.Contains(reason, "outside pool limits") {
		t.Fatalf("expected ban reason to mention pool limits, got: %q", reason)
	}
	if until.Before(time.Now().Add(55 * time.Minute)) {
		t.Fatalf("expected ~1h ban, got until=%s", until)
	}
}

func TestSuggestTargetNoParamsIsIgnoredAndDoesNotBan(t *testing.T) {
	conn := &writeRecorderConn{}
	mc := &MinerConn{
		id:           "suggest-target-noparams-miner",
		cfg:          Config{MinDifficulty: 1.0, MaxDifficulty: 2.0},
		conn:         conn,
		writer:       bufio.NewWriterSize(conn, 4096),
		statsUpdates: make(chan statsUpdate),
	}

	req := &StratumRequest{
		ID:     1,
		Method: "mining.suggest_target",
		Params: nil,
	}
	mc.suggestTarget(req)

	if conn.closed {
		t.Fatalf("did not expect miner connection to be closed")
	}
	until, _, _ := mc.banDetails()
	if !until.IsZero() {
		t.Fatalf("did not expect ban, got until=%s", until)
	}
	out := conn.String()
	if !strings.Contains(out, "\"result\":true") {
		t.Fatalf("expected result=true response, got: %q", out)
	}
}

func TestSuggestTargetEmptyIsIgnoredAndDoesNotBan(t *testing.T) {
	conn := &writeRecorderConn{}
	mc := &MinerConn{
		id:           "suggest-target-empty-miner",
		cfg:          Config{MinDifficulty: 1.0, MaxDifficulty: 2.0},
		conn:         conn,
		writer:       bufio.NewWriterSize(conn, 4096),
		statsUpdates: make(chan statsUpdate),
	}

	req := &StratumRequest{
		ID:     1,
		Method: "mining.suggest_target",
		Params: []interface{}{""},
	}
	mc.suggestTarget(req)

	if conn.closed {
		t.Fatalf("did not expect miner connection to be closed")
	}
	until, _, _ := mc.banDetails()
	if !until.IsZero() {
		t.Fatalf("did not expect ban, got until=%s", until)
	}
	out := conn.String()
	if !strings.Contains(out, "\"result\":true") {
		t.Fatalf("expected result=true response, got: %q", out)
	}
}
