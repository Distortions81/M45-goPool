package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestHandleAuthorize_AllowsBeforeSubscribe(t *testing.T) {
	conn := &writeRecorderConn{}
	mc := &MinerConn{
		id:           "auth-before-subscribe",
		ctx:          context.Background(),
		conn:         conn,
		cfg:          Config{ConnectionTimeout: time.Hour},
		lastActivity: time.Now(),
	}

	worker := "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa.worker"
	mc.handleAuthorizeID(1, worker, "")

	if !mc.authorized {
		t.Fatalf("expected authorized=true")
	}
	if mc.subscribed {
		t.Fatalf("expected subscribed=false")
	}
	if mc.listenerOn {
		t.Fatalf("expected listenerOn=false before subscribe")
	}
	if conn.closed {
		t.Fatalf("expected connection to remain open")
	}

	out := conn.String()
	if !strings.Contains(out, "\"result\":true") || !strings.Contains(out, "\"error\":null") {
		t.Fatalf("expected true authorize response, got: %q", out)
	}
}

func TestJobSubscriptionStartsOnlyWhenMinerReady(t *testing.T) {
	cfg := defaultConfig()
	jm := NewJobManager(nil, cfg, nil, nil, nil)
	mc := NewMinerConn(context.Background(), nopConn{}, jm, nil, cfg, nil, nil, nil, nil, nil, false)
	t.Cleanup(mc.cleanup)

	if got := jm.ActiveMiners(); got != 0 {
		t.Fatalf("new connection job subscribers = %d, want 0", got)
	}
	if mc.startJobListener() {
		t.Fatal("job listener started before subscribe and authorize")
	}

	mc.subscribed = true
	if mc.startJobListener() {
		t.Fatal("job listener started before authorize")
	}
	if got := jm.ActiveMiners(); got != 0 {
		t.Fatalf("subscribe-only job subscribers = %d, want 0", got)
	}

	mc.authorized = true
	if !mc.startJobListener() {
		t.Fatal("job listener did not start after subscribe and authorize")
	}
	if !mc.startJobListener() {
		t.Fatal("repeated job listener start did not report active")
	}
	if got := jm.ActiveMiners(); got != 1 {
		t.Fatalf("ready connection job subscribers = %d, want 1", got)
	}

	mc.cleanup()
	if got := jm.ActiveMiners(); got != 0 {
		t.Fatalf("cleaned connection job subscribers = %d, want 0", got)
	}
}

func TestMinerConn_MiningAuthAlias(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()

	mc := &MinerConn{
		id:           "mining.auth-alias",
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

	worker := "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa.worker"
	_, err := io.WriteString(client, `{"id":1,"method":"mining.auth","params":[`+strconvQuote(worker)+`,""]}`+"\n")
	if err != nil {
		t.Fatalf("write request: %v", err)
	}

	br := bufio.NewReader(client)
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	var resp StratumResponse
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		t.Fatalf("unmarshal response: %v; line=%q", err, line)
	}
	if resp.ID == nil {
		t.Fatalf("expected id in response")
	}
	if resp.Result != true || resp.Error != nil {
		t.Fatalf("expected result=true error=nil, got result=%#v error=%#v", resp.Result, resp.Error)
	}

	_ = client.Close()
	_ = server.Close()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatalf("miner conn did not exit")
	}
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
