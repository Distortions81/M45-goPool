package main

import (
	"strings"
	"testing"
	"time"
)

func TestWarnBeforeInvalidSubmitBanThreshold(t *testing.T) {
	conn := &writeRecorderConn{}
	mc := &MinerConn{
		id:         "warn-before-ban",
		conn:       conn,
		authorized: true,
		cfg: Config{
			BanInvalidSubmissionsAfter:  3,
			BanInvalidSubmissionsWindow: 5 * time.Minute,
		},
		// recordShare falls back to sync updates when statsUpdates is nil.
	}

	now := time.Now()
	req := &StratumRequest{ID: 1, Method: "mining.submit"}
	mc.rejectShareWithBan(req, "worker", rejectInvalidNonce, 20, "invalid nonce", now)
	if strings.Contains(conn.String(), "\"method\":\"client.show_message\"") {
		t.Fatalf("did not expect warning on first invalid")
	}

	mc.rejectShareWithBan(req, "worker", rejectInvalidNonce, 20, "invalid nonce", now.Add(time.Second))
	out := conn.String()
	if !strings.Contains(out, "\"method\":\"client.show_message\"") {
		t.Fatalf("expected warning show_message near ban threshold, got: %q", out)
	}
	if !strings.Contains(out, "Next invalid submission may result in a temporary ban") {
		t.Fatalf("expected warning text, got: %q", out)
	}
}

func TestWarnOnRepeatedDuplicateShares(t *testing.T) {
	conn := &writeRecorderConn{}
	mc := &MinerConn{
		id:         "warn-dup",
		conn:       conn,
		authorized: true,
		cfg:        Config{},
	}

	now := time.Now()
	req := &StratumRequest{ID: 1, Method: "mining.submit"}
	mc.rejectShareWithBan(req, "worker", rejectDuplicateShare, 22, "duplicate share", now)
	mc.rejectShareWithBan(req, "worker", rejectDuplicateShare, 22, "duplicate share", now.Add(1*time.Second))
	mc.rejectShareWithBan(req, "worker", rejectDuplicateShare, 22, "duplicate share", now.Add(2*time.Second))

	out := conn.String()
	if !strings.Contains(out, "\"method\":\"client.show_message\"") {
		t.Fatalf("expected duplicate warning show_message, got: %q", out)
	}
	if !strings.Contains(out, "repeated duplicate shares") {
		t.Fatalf("expected duplicate warning text, got: %q", out)
	}
}
