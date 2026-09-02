package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestJobManagerHandleZMQNotification_RawBlockRefreshesJob(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()

	// Build a minimal raw block payload sufficient for parseRawBlockTip().
	// Header (80 bytes) + tx count (1) + coinbase tx version + input count (1) + prevout (36)
	// + script len + scriptSig (first push encodes height=2).
	payload := make([]byte, 0, 140)
	header := make([]byte, 80)
	binary.LittleEndian.PutUint32(header[68:72], uint32(now.Unix()))
	binary.LittleEndian.PutUint32(header[72:76], 0x1d00ffff) // bits
	payload = append(payload, header...)
	payload = append(payload, 0x01)                   // tx count = 1
	payload = append(payload, 0x01, 0x00, 0x00, 0x00) // version
	payload = append(payload, 0x01)                   // input count = 1
	payload = append(payload, make([]byte, 36)...)    // prevout
	payload = append(payload, 0x02)                   // script len = 2
	payload = append(payload, 0x01, 0x02)             // push 1-byte height=2
	tip, err := parseRawBlockTip(payload)
	if err != nil {
		t.Fatalf("parse raw block tip: %v", err)
	}
	bestHash := tip.Hash
	var bestHashCalls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode rpc request: %v", err)
		}
		resp := rpcResponse{ID: req.ID}

		switch req.Method {
		case "getbestblockhash":
			bestHashCalls.Add(1)
			data, _ := json.Marshal(bestHash)
			resp.Result = data
		case "getblockheader":
			header := BlockHeader{Hash: bestHash, Height: 2, Time: now.Unix(), PreviousBlockHash: "", Bits: "1d00ffff", Difficulty: 1}
			data, _ := json.Marshal(header)
			resp.Result = data
		case "getblocktemplate":
			tpl := GetBlockTemplateResult{
				Height:                   3,
				CurTime:                  now.Unix(),
				Bits:                     "1d00ffff",
				Previous:                 bestHash,
				DefaultWitnessCommitment: "00",
				CoinbaseValue:            50 * 1e8,
			}
			data, _ := json.Marshal(tpl)
			resp.Result = data
		default:
			resp.Error = &rpcError{Code: -32601, Message: "method not found"}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)

	rpc := &RPCClient{url: srv.URL, client: srv.Client(), lp: srv.Client()}
	jm := NewJobManager(rpc, Config{ZMQRawBlockAddr: "tcp://127.0.0.1:28332", Extranonce2Size: 4, TemplateExtraNonce2Size: 8}, nil, []byte{0x51}, nil)
	historyCtx, cancelHistory := context.WithCancel(context.Background())
	cancelHistory()
	jm.historyCtx = historyCtx

	if err := jm.handleZMQNotification(context.Background(), "rawblock", payload); err != nil {
		t.Fatalf("handleZMQNotification error: %v", err)
	}

	job := jm.CurrentJob()
	if job == nil {
		t.Fatalf("expected job after rawblock notification")
	}
	if job.Template.Height != 3 {
		t.Fatalf("expected candidate job height 3, got %d", job.Template.Height)
	}
	if jm.blockTipHeight() != 2 {
		t.Fatalf("expected tip height 2, got %d", jm.blockTipHeight())
	}
	if got := bestHashCalls.Load(); got != 0 {
		t.Fatalf("new-parent ZMQ proof made %d getbestblockhash calls, want 0", got)
	}
}
