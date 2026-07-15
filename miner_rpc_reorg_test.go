package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

type reorgRetryRPC struct {
	attempts atomic.Int32
}

func (r *reorgRetryRPC) callCtx(_ context.Context, method string, _ any, _ any) error {
	if method != "submitblock" {
		return errors.New("unexpected RPC method")
	}
	if r.attempts.Add(1) == 1 {
		return errors.New("temporary submit transport failure")
	}
	return nil
}

func TestSubmitBlockRetryContinuesAcrossCompetingChainReorg(t *testing.T) {
	rpc := &reorgRetryRPC{}
	submitted := &Job{
		Generation: 10,
		Template: GetBlockTemplateResult{
			Height:   102,
			Previous: "displaced-parent",
		},
	}
	current := &Job{
		Generation: 11,
		Template: GetBlockTemplateResult{
			Height:   102,
			Previous: "competing-parent",
		},
	}
	mc := &MinerConn{
		rpc: rpc,
		jobMgr: &JobManager{
			curJob: current,
		},
	}

	var submitResult any
	if err := mc.submitBlockWithFastRetry(submitted, "worker", "hash", "block", &submitResult); err != nil {
		t.Fatalf("submit retry stopped after reorg: %v", err)
	}
	if got := rpc.attempts.Load(); got != 2 {
		t.Fatalf("submit attempts = %d, want 2", got)
	}
}
