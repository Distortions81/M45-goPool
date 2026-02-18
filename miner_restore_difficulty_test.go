package main

import (
	"strings"
	"testing"
	"time"
)

func TestAuthorizeRestoresHigherRememberedDifficulty(t *testing.T) {
	conn := &writeRecorderConn{}

	worker := "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4.rig1"
	workerHash := workerNameHash(worker)
	if workerHash == "" {
		t.Fatalf("expected non-empty worker hash")
	}

	globalDifficultyCache.setWorker(workerHash, 2048, time.Now().UTC())

	mc := &MinerConn{
		id:                   "restore-diff",
		conn:                 conn,
		cfg:                  Config{MinDifficulty: 1.0, MaxDifficulty: 0, EnforceSuggestedDifficultyLimits: true},
		subscribed:           true,
		listenerOn:           true,
		initialWorkScheduled: true, // avoid async sendInitialWork()
		statsUpdates:         make(chan statsUpdate),
	}

	mc.handleAuthorizeID(1, worker, "x")

	if got := mc.currentDifficulty(); got < 2048 {
		t.Fatalf("expected restored difficulty >= 2048, got %v", got)
	}
	out := conn.String()
	if !strings.Contains(out, "\"method\":\"mining.set_difficulty\"") {
		t.Fatalf("expected set_difficulty message, got: %q", out)
	}
}
