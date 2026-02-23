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
	mc.rejectShareWithBan(req, "worker", rejectInvalidNonce, stratumErrCodeInvalidRequest, "invalid nonce", now)
	if strings.Contains(conn.String(), "\"method\":\"client.show_message\"") {
		t.Fatalf("did not expect warning on first invalid")
	}

	mc.rejectShareWithBan(req, "worker", rejectInvalidNonce, stratumErrCodeInvalidRequest, "invalid nonce", now.Add(time.Second))
	out := conn.String()
	if !strings.Contains(out, "\"invalid nonce\"") {
		t.Fatalf("expected invalid nonce error response, got: %q", out)
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
	mc.rejectShareWithBan(req, "worker", rejectDuplicateShare, stratumErrCodeDuplicateShare, "duplicate share", now)
	mc.rejectShareWithBan(req, "worker", rejectDuplicateShare, stratumErrCodeDuplicateShare, "duplicate share", now.Add(1*time.Second))
	mc.rejectShareWithBan(req, "worker", rejectDuplicateShare, stratumErrCodeDuplicateShare, "duplicate share", now.Add(2*time.Second))

	out := conn.String()
	if !strings.Contains(out, "\"duplicate share\"") {
		t.Fatalf("expected duplicate share error response, got: %q", out)
	}
}
