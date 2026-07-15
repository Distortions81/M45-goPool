package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEnsureTemplateFreshAllowsBestChainRegressionOnReorg(t *testing.T) {
	const bestHash = "0000000000000000000000000000000000000000000000000000000000000002"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		result, _ := json.Marshal(bestHash)
		_ = json.NewEncoder(w).Encode(rpcResponse{ID: req.ID, Result: result})
	}))
	t.Cleanup(srv.Close)

	rpc := &RPCClient{url: srv.URL, client: srv.Client(), lp: srv.Client()}
	jm := &JobManager{
		rpc: rpc,
		curJob: &Job{Template: GetBlockTemplateResult{
			Height:   102,
			CurTime:  1_700_000_100,
			Previous: "old-best-hash",
		}},
	}
	reorgTemplate := GetBlockTemplateResult{
		Height:   101,
		CurTime:  1_700_000_000,
		Previous: bestHash,
	}

	if err := jm.ensureTemplateFresh(context.Background(), reorgTemplate); err != nil {
		t.Fatalf("best-chain reorg template rejected: %v", err)
	}
}
