package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type submitResultSequenceRPC struct {
	mu          sync.Mutex
	results     []any
	submitCalls atomic.Int32
	headerCalls atomic.Int32
}

func (r *submitResultSequenceRPC) callCtx(_ context.Context, method string, _ any, out any) error {
	switch method {
	case "submitblock":
		call := int(r.submitCalls.Add(1)) - 1
		r.mu.Lock()
		defer r.mu.Unlock()
		if call >= len(r.results) {
			return errors.New("unexpected extra submitblock call")
		}
		if dst, ok := out.(*any); ok {
			*dst = r.results[call]
		}
		return nil
	case "getblockheader":
		r.headerCalls.Add(1)
		return errors.New("side-chain block is not in the active chain")
	default:
		return errors.New("unexpected RPC method")
	}
}

func TestSubmitBlockDuplicateCompletesWithoutActiveChainConfirmation(t *testing.T) {
	rpc := &submitResultSequenceRPC{results: []any{"duplicate"}}
	mc := &MinerConn{id: "duplicate-side-chain", rpc: rpc}

	var result any
	if err := mc.submitBlockWithFastRetry(&Job{}, "worker", strings.Repeat("a", 64), "deadbeef", &result); err != nil {
		t.Fatalf("duplicate block was not treated as delivered: %v", err)
	}
	if got := rpc.submitCalls.Load(); got != 1 {
		t.Fatalf("submitblock calls = %d, want 1", got)
	}
	if got := rpc.headerCalls.Load(); got != 0 {
		t.Fatalf("getblockheader calls = %d, want 0 for duplicate", got)
	}
}

func TestSubmitBlockInconclusiveResultsStayOnFastRetryPath(t *testing.T) {
	rpc := &submitResultSequenceRPC{results: []any{
		"inconclusive",
		"duplicate-inconclusive",
		nil,
	}}
	mc := &MinerConn{id: "inconclusive-retry", rpc: rpc}

	var result any
	if err := mc.submitBlockWithFastRetry(&Job{}, "worker", strings.Repeat("b", 64), "deadbeef", &result); err != nil {
		t.Fatalf("inconclusive result stopped block delivery: %v", err)
	}
	if got := rpc.submitCalls.Load(); got != 3 {
		t.Fatalf("submitblock calls = %d, want 3", got)
	}
	if got := rpc.headerCalls.Load(); got != 0 {
		t.Fatalf("getblockheader calls = %d, want 0 on the 100ms retry path", got)
	}
}

func TestSubmitBlockEmptyResultIsNotAccepted(t *testing.T) {
	rpc := &submitResultSequenceRPC{results: []any{"", "duplicate"}}
	mc := &MinerConn{id: "empty-result-retry", rpc: rpc}

	var result any
	if err := mc.submitBlockWithFastRetry(&Job{}, "worker", strings.Repeat("c", 64), "deadbeef", &result); err != nil {
		t.Fatalf("empty result did not retry to a conclusive result: %v", err)
	}
	if got := rpc.submitCalls.Load(); got != 2 {
		t.Fatalf("submitblock calls = %d, want 2", got)
	}
}

func TestClassifySubmitBlockResult(t *testing.T) {
	tests := []struct {
		name        string
		result      any
		disposition submitBlockResultDisposition
		wantErr     bool
	}{
		{name: "accepted", result: nil, disposition: submitBlockResultAccepted},
		{name: "duplicate", result: "duplicate", disposition: submitBlockResultAccepted},
		{name: "inconclusive", result: "inconclusive", disposition: submitBlockResultRetryable, wantErr: true},
		{name: "duplicate inconclusive", result: "duplicate-inconclusive", disposition: submitBlockResultRetryable, wantErr: true},
		{name: "empty", result: "", disposition: submitBlockResultRetryable, wantErr: true},
		{name: "validation rejection", result: "bad-cb-amount", disposition: submitBlockResultRejected, wantErr: true},
		{name: "unexpected type", result: true, disposition: submitBlockResultRejected, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := test.result
			got, err := classifySubmitBlockResult(&result)
			if got != test.disposition {
				t.Fatalf("disposition = %v, want %v", got, test.disposition)
			}
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr=%v", err, test.wantErr)
			}
		})
	}
}
