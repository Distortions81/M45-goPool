package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRefreshNodeSyncInfo_DoesNotPoisonJobFeedWhenCurrentJobExists(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := rpcResponse{Error: &rpcError{Code: -1, Message: "temporary getblockchaininfo failure"}, ID: 1}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)

	rpc := &RPCClient{url: srv.URL, client: srv.Client(), lp: srv.Client()}
	jm := &JobManager{rpc: rpc}
	jm.mu.Lock()
	jm.curJob = &Job{CreatedAt: time.Now()}
	jm.mu.Unlock()

	jm.refreshNodeSyncInfo(context.Background())

	if err := jm.FeedStatus().LastError; err != nil {
		t.Fatalf("expected no job-feed error with current job present, got %v", err)
	}
}

func TestRefreshNodeSyncInfo_RecordsErrorWhenNoCurrentJob(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := rpcResponse{Error: &rpcError{Code: -1, Message: "temporary getblockchaininfo failure"}, ID: 1}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)

	rpc := &RPCClient{url: srv.URL, client: srv.Client(), lp: srv.Client()}
	jm := &JobManager{rpc: rpc}

	jm.refreshNodeSyncInfo(context.Background())

	if err := jm.FeedStatus().LastError; err == nil {
		t.Fatal("expected job-feed error when no current job exists")
	}
}
