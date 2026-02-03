package main

import (
	"sync/atomic"
	"testing"
)

func TestWorkerConnectionRegistryRegisterUnregister(t *testing.T) {
	t.Helper()

	reg := newWorkerConnectionRegistry()

	mc1 := &MinerConn{}
	atomic.StoreUint64(&mc1.connectionSeq, 1)
	prev := reg.register("hashA", "walletA", mc1)
	if prev != nil {
		t.Fatalf("expected no previous connection, got %v", prev)
	}
	if got := reg.connectionBySeq(1); got != mc1 {
		t.Fatalf("expected connection seq 1 to be mc1, got %v", got)
	}
	if conns := reg.getConnectionsByHash("hashA"); len(conns) != 1 || conns[0] != mc1 {
		t.Fatalf("expected hashA to map to mc1, got %+v", conns)
	}
	if conns := reg.getConnectionsByWalletHash("walletA"); len(conns) != 1 || conns[0] != mc1 {
		t.Fatalf("expected walletA to map to mc1, got %+v", conns)
	}

	mc2 := &MinerConn{}
	atomic.StoreUint64(&mc2.connectionSeq, 2)
	prev = reg.register("hashA", "walletA", mc2)
	if prev != mc1 {
		t.Fatalf("expected previous connection to be mc1, got %v", prev)
	}
	if conns := reg.getConnectionsByHash("hashA"); len(conns) != 2 {
		t.Fatalf("expected two connections for hashA, got %d", len(conns))
	}
	if conns := reg.getConnectionsByWalletHash("walletA"); len(conns) != 2 {
		t.Fatalf("expected two connections for walletA, got %d", len(conns))
	}

	reg.unregister("hashA", "walletA", mc2)
	if got := reg.connectionBySeq(2); got != nil {
		t.Fatalf("expected connection seq 2 to be removed, got %v", got)
	}
	if conns := reg.getConnectionsByHash("hashA"); len(conns) != 1 || conns[0] != mc1 {
		t.Fatalf("expected hashA to map to mc1 only, got %+v", conns)
	}
	if conns := reg.getConnectionsByWalletHash("walletA"); len(conns) != 1 || conns[0] != mc1 {
		t.Fatalf("expected walletA to map to mc1 only, got %+v", conns)
	}

	reg.unregister("hashA", "walletA", mc1)
	if got := reg.connectionBySeq(1); got != nil {
		t.Fatalf("expected connection seq 1 to be removed, got %v", got)
	}
	if conns := reg.getConnectionsByHash("hashA"); len(conns) != 0 {
		t.Fatalf("expected hashA to be empty, got %+v", conns)
	}
	if conns := reg.getConnectionsByWalletHash("walletA"); len(conns) != 0 {
		t.Fatalf("expected walletA to be empty, got %+v", conns)
	}
}

func TestWorkerConnectionRegistryConnectionBySeqZero(t *testing.T) {
	t.Helper()

	reg := newWorkerConnectionRegistry()
	if got := reg.connectionBySeq(0); got != nil {
		t.Fatalf("expected nil for seq 0, got %v", got)
	}
}
