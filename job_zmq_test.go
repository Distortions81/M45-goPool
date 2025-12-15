package main

import (
	"sync/atomic"
	"testing"
)

func TestJobManagerZMQReconnects(t *testing.T) {
	jm := NewJobManager(nil, Config{ZMQBlockAddr: "tcp://127.0.0.1:28332"}, nil, nil)

	jm.markZMQHealthy()
	if got := atomic.LoadUint64(&jm.zmqReconnects); got != 1 {
		t.Fatalf("expected 1 reconnect after first healthy mark, got %d", got)
	}

	jm.markZMQHealthy()
	if got := atomic.LoadUint64(&jm.zmqReconnects); got != 1 {
		t.Fatalf("expected reconnect count to stay at 1 while already healthy, got %d", got)
	}

	jm.markZMQUnhealthy("test", nil)
	if got := atomic.LoadUint64(&jm.zmqDisconnects); got != 1 {
		t.Fatalf("expected a single disconnect count after unhealthy, got %d", got)
	}

	jm.markZMQHealthy()
	if got := atomic.LoadUint64(&jm.zmqReconnects); got != 2 {
		t.Fatalf("expected reconnect count to increment after recovering health, got %d", got)
	}
}
