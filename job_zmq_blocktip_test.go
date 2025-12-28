package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestJobManagerRefreshFromTemplate_UpdatesBlockTip_WithZMQEnabled(t *testing.T) {
	bestHash := "0000000000000000000000000000000000000000000000000000000000000001"
	t0 := time.Unix(1_700_000_000, 0).UTC()
	t1 := t0.Add(10 * time.Minute)
	t2 := t1.Add(10 * time.Minute)
	t3 := t2.Add(10 * time.Minute)

	headers := map[string]BlockHeader{
		bestHash: {Hash: bestHash, Height: 103, Time: t3.Unix(), PreviousBlockHash: "h2", Bits: "1d00ffff", Difficulty: 1},
		"h2":     {Hash: "h2", Height: 102, Time: t2.Unix(), PreviousBlockHash: "h1", Bits: "1d00ffff", Difficulty: 1},
		"h1":     {Hash: "h1", Height: 101, Time: t1.Unix(), PreviousBlockHash: "h0", Bits: "1d00ffff", Difficulty: 1},
		"h0":     {Hash: "h0", Height: 100, Time: t0.Unix(), PreviousBlockHash: "", Bits: "1d00ffff", Difficulty: 1},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode rpc request: %v", err)
		}
		resp := rpcResponse{ID: req.ID}

		switch req.Method {
		case "getbestblockhash":
			data, _ := json.Marshal(bestHash)
			resp.Result = data
		case "getblockheader":
			params, _ := req.Params.([]interface{})
			if len(params) < 1 {
				resp.Error = &rpcError{Code: -32602, Message: "missing hash param"}
				break
			}
			hash, _ := params[0].(string)
			header, ok := headers[hash]
			if !ok {
				resp.Error = &rpcError{Code: -5, Message: "block not found"}
				break
			}
			data, _ := json.Marshal(header)
			resp.Result = data
		default:
			resp.Error = &rpcError{Code: -32601, Message: "method not found"}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)

	rpc := &RPCClient{url: srv.URL, client: srv.Client(), lp: srv.Client()}
	jm := NewJobManager(rpc, Config{ZMQBlockAddr: "tcp://127.0.0.1:28332", Extranonce2Size: 4, TemplateExtraNonce2Size: 8}, []byte{0x51}, nil)

	tpl := GetBlockTemplateResult{
		Height:                   103,
		CurTime:                  t3.Unix(),
		Bits:                     "1d00ffff",
		Previous:                 bestHash,
		DefaultWitnessCommitment: "00",
		CoinbaseValue:            50 * 1e8,
	}

	if err := jm.refreshFromTemplate(context.Background(), tpl); err != nil {
		t.Fatalf("refreshFromTemplate error: %v", err)
	}

	jm.zmqPayloadMu.RLock()
	gotTip := jm.zmqPayload.BlockTip
	gotTimes := append([]time.Time(nil), jm.zmqPayload.RecentBlockTimes...)
	gotActive := jm.zmqPayload.BlockTimerActive
	jm.zmqPayloadMu.RUnlock()

	if gotTip.Height != 103 {
		t.Fatalf("unexpected tip height: got %d, want 103", gotTip.Height)
	}
	if gotTip.Hash != bestHash {
		t.Fatalf("unexpected tip hash: got %q, want %q", gotTip.Hash, bestHash)
	}
	if !gotTip.Time.Equal(t3) {
		t.Fatalf("unexpected tip time: got %s, want %s", gotTip.Time, t3)
	}
	if !gotActive {
		t.Fatalf("expected block timer active")
	}
	if len(gotTimes) != 4 {
		t.Fatalf("unexpected recent times length: got %d, want 4", len(gotTimes))
	}
	want := []time.Time{t0, t1, t2, t3}
	for i := range want {
		if !gotTimes[i].Equal(want[i]) {
			t.Fatalf("unexpected recent time[%d]: got %s, want %s", i, gotTimes[i], want[i])
		}
	}
}

