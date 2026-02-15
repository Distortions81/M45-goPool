package main

import (
	"bufio"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHandleAuthorizeRejectsPersistedWorkerBan(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state", "workers.db")
	db, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("openStateDB: %v", err)
	}
	defer db.Close()
	cleanup := setSharedStateDBForTest(db)
	defer cleanup()

	accounting, err := NewAccountStore(Config{DataDir: dir}, false, false)
	if err != nil {
		t.Fatalf("NewAccountStore: %v", err)
	}

	worker := "wallet.worker"
	accounting.MarkBan(worker, time.Now().Add(time.Hour), "stratum message rate limit")

	conn := &writeRecorderConn{}
	mc := &MinerConn{
		id:           "banned-miner",
		cfg:          Config{},
		conn:         conn,
		writer:       bufio.NewWriterSize(conn, 4096),
		accounting:   accounting,
		subscribed:   true,
		statsUpdates: make(chan statsUpdate),
	}

	req := &StratumRequest{
		ID:     1,
		Method: "mining.authorize",
		Params: []any{worker, "x"},
	}
	mc.handleAuthorize(req)

	if !conn.closed {
		t.Fatalf("expected banned miner connection to be closed")
	}
	if mc.authorized {
		t.Fatalf("expected banned miner authorization to fail")
	}
	out := conn.String()
	if !strings.Contains(out, "\"banned\"") {
		t.Fatalf("expected banned authorize response, got: %q", out)
	}
}
