package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"
)

func TestMinerConn_UnknownMethodWithIDGetsResponse(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()

	mc := &MinerConn{
		id:           "test",
		ctx:          context.Background(),
		conn:         server,
		reader:       bufio.NewReader(server),
		cfg:          Config{ConnectionTimeout: time.Hour},
		lastActivity: time.Now(),
	}

	done := make(chan struct{})
	go func() {
		mc.handle()
		close(done)
	}()

	_, err := io.WriteString(client, `{"id":1,"method":"mining.unknown_method","params":[]}`+"\n")
	if err != nil {
		t.Fatalf("write request: %v", err)
	}

	br := bufio.NewReader(client)
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read response: %v", err)
	}

	var resp map[string]any
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		t.Fatalf("unmarshal response: %v; line=%q", err, line)
	}

	if id, ok := resp["id"].(float64); !ok || id != 1 {
		t.Fatalf("expected id=1, got %#v", resp["id"])
	}

	errVal, ok := resp["error"].([]any)
	if !ok || len(errVal) < 2 {
		t.Fatalf("expected error array, got %#v", resp["error"])
	}
	code, ok := errVal[0].(float64)
	if !ok || int(code) != -32601 {
		t.Fatalf("expected error code -32601, got %#v", errVal[0])
	}
	msg, ok := errVal[1].(string)
	if !ok || msg == "" {
		t.Fatalf("expected non-empty error message, got %#v", errVal[1])
	}

	_ = client.Close()
	_ = server.Close()

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatalf("miner conn did not exit")
	}
}

func TestMinerConn_UnknownMethodWithNullID_NoResponse(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()

	mc := &MinerConn{
		id:           "test",
		ctx:          context.Background(),
		conn:         server,
		reader:       bufio.NewReader(server),
		cfg:          Config{ConnectionTimeout: time.Hour},
		lastActivity: time.Now(),
	}

	done := make(chan struct{})
	go func() {
		mc.handle()
		close(done)
	}()

	_, err := io.WriteString(client, `{"id":null,"method":"mining.unknown_method","params":[]}`+"\n")
	if err != nil {
		t.Fatalf("write request: %v", err)
	}

	// Expect no response (treated as a notification).
	_ = client.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	br := bufio.NewReader(client)
	_, err = br.ReadString('\n')
	if err == nil {
		t.Fatalf("expected no response, but got one")
	}
	if nErr, ok := err.(net.Error); !ok || !nErr.Timeout() {
		t.Fatalf("expected read timeout, got %T: %v", err, err)
	}

	_ = client.Close()
	_ = server.Close()

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatalf("miner conn did not exit")
	}
}

