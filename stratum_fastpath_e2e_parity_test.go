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

type fastpathParityRunConfig struct {
	fastDecode bool
	fastEncode bool
	setup      func(mc *MinerConn)
}

func runMinerConnSingleRequestJSON(t *testing.T, cfg fastpathParityRunConfig, reqLine string) any {
	t.Helper()

	server, client := net.Pipe()
	defer client.Close()

	mc := &MinerConn{
		id:           "fastpath-parity",
		ctx:          context.Background(),
		conn:         server,
		reader:       bufio.NewReader(server),
		jobMgr:       &JobManager{},
		cfg:          Config{ConnectionTimeout: time.Hour, StratumFastDecodeEnabled: cfg.fastDecode, StratumFastEncodeEnabled: cfg.fastEncode},
		lastActivity: time.Now(),
	}
	if cfg.setup != nil {
		cfg.setup(mc)
	}

	done := make(chan struct{})
	go func() {
		mc.handle()
		close(done)
	}()

	if _, err := io.WriteString(client, reqLine+"\n"); err != nil {
		t.Fatalf("write request: %v", err)
	}

	br := bufio.NewReader(client)
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read response: %v", err)
	}

	var out any
	if err := json.Unmarshal([]byte(line), &out); err != nil {
		t.Fatalf("unmarshal response: %v; line=%q", err, line)
	}

	_ = client.Close()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatalf("miner conn did not exit")
	}
	return out
}

func assertFastpathParityMatrix(t *testing.T, reqLine string, setup func(mc *MinerConn)) {
	t.Helper()

	base := runMinerConnSingleRequestJSON(t, fastpathParityRunConfig{
		fastDecode: false,
		fastEncode: false,
		setup:      setup,
	}, reqLine)

	variants := []fastpathParityRunConfig{
		{fastDecode: true, fastEncode: false, setup: setup},
		{fastDecode: false, fastEncode: true, setup: setup},
		{fastDecode: true, fastEncode: true, setup: setup},
	}
	for _, v := range variants {
		got := runMinerConnSingleRequestJSON(t, v, reqLine)
		if !jsonDeepEqual(base, got) {
			t.Fatalf("fastpath parity mismatch decode=%v encode=%v\nbase=%#v\ngot=%#v", v.fastDecode, v.fastEncode, base, got)
		}
	}
}

func jsonDeepEqual(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}

func TestStratumFastpathEndToEndParity_Ping(t *testing.T) {
	assertFastpathParityMatrix(t, `{"id":7,"method":"mining.ping","params":[]}`, nil)
}

func TestStratumFastpathEndToEndParity_Authorize(t *testing.T) {
	req := `{"id":1,"method":"mining.authorize","params":["1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa.worker",""]}`
	assertFastpathParityMatrix(t, req, nil)
}

func TestStratumFastpathEndToEndParity_AuthorizeEscapedFallback(t *testing.T) {
	req := `{"id":"abc","method":"mining.authorize","params":["1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa.worker\u0031","p\"w"]}`
	assertFastpathParityMatrix(t, req, nil)
}

func TestStratumFastpathEndToEndParity_Subscribe(t *testing.T) {
	req := `{"id":"sub-1","method":"mining.subscribe","params":["cgminer/4.11.1","resume-fixed-session"]}`
	assertFastpathParityMatrix(t, req, func(mc *MinerConn) {
		mc.extranonce1Hex = "01020304"
		mc.extranonce1 = []byte{1, 2, 3, 4}
		mc.cfg.Extranonce2Size = 4
	})
}

func TestStratumFastpathEndToEndParity_UnknownMethod(t *testing.T) {
	req := `{"id":"abc-1","method":"mining.unknown_ext","params":[]}`
	assertFastpathParityMatrix(t, req, nil)
}

func TestStratumFastpathEndToEndParity_InvalidJSONParseError(t *testing.T) {
	// Keep top-level id/method sniffable, but break the params array so the
	// authorize fast-path cannot extract both string params and must fall back.
	req := `{"id":1,"method":"mining.authorize","params":["worker","x`
	assertFastpathParityMatrix(t, req, nil)
}

func TestStratumFastpathEndToEndParity_SubmitInvalidParams(t *testing.T) {
	req := `{"id":1,"method":"mining.submit","params":["worker"]}`
	assertFastpathParityMatrix(t, req, nil)
}

func TestStratumFastpathEndToEndParity_SubmitEscapedFallbackInvalidParams(t *testing.T) {
	req := `{"id":1,"method":"mining.submit","params":["worker\u0031"]}`
	assertFastpathParityMatrix(t, req, nil)
}
