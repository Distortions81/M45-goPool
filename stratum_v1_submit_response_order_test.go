package main

import (
	"bytes"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

type recordConn struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *recordConn) Read(b []byte) (int, error)  { return 0, nil }
func (c *recordConn) Close() error                { return nil }
func (c *recordConn) LocalAddr() net.Addr         { return &net.IPAddr{} }
func (c *recordConn) RemoteAddr() net.Addr        { return &net.IPAddr{} }
func (c *recordConn) SetDeadline(time.Time) error { return nil }
func (c *recordConn) SetReadDeadline(time.Time) error {
	return nil
}
func (c *recordConn) SetWriteDeadline(time.Time) error {
	return nil
}

func (c *recordConn) Write(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(b)
}

func (c *recordConn) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

func TestMiningSubmitRespondsBeforeNotifyOnVardiffMove(t *testing.T) {
	workerName, workerWallet, workerScript := generateTestWorker(t)
	job := &Job{
		JobID: "submit-order-job",
		Template: GetBlockTemplateResult{
			Height:        101,
			CurTime:       1700000000,
			Mintime:       1700000000,
			Bits:          "1d00ffff",
			Previous:      "0000000000000000000000000000000000000000000000000000000000000000",
			CoinbaseValue: 50 * 1e8,
		},
		Extranonce2Size:         4,
		TemplateExtraNonce2Size: 8,
		PayoutScript:            []byte{0x51}, // OP_TRUE; structure-only
		CoinbaseMsg:             "goPool-submit-order-test",
		CoinbaseValue:           50 * 1e8,
		PrevHash:                "0000000000000000000000000000000000000000000000000000000000000000",
	}

	conn := &recordConn{}
	mc := &MinerConn{
		id:      "submit-order-miner",
		cfg:     Config{PoolFeePercent: 0},
		conn:    conn,
		vardiff: defaultVarDiff,
		stratumV1: minerConnStratumV1State{
			extranonce1: []byte{0x01, 0x02, 0x03, 0x04},
			authorized:  true,
			subscribed:  true,
		},
		stats: MinerStats{
			Worker:       workerName,
			WorkerSHA256: workerNameHash(workerName),
		},
		// Keep stats updates synchronous in this test.
		statsUpdates: nil,
		activeJobs:   make(map[string]*Job, 1),
		jobDifficulty: map[string]float64{
			job.JobID: 1,
		},
		maxRecentJobs: 1,
	}
	atomicStoreFloat64(&mc.difficulty, 1)
	mc.shareTarget.Store(targetFromDifficulty(1))
	mc.initialEMAWindowDone.Store(true)
	mc.lastDiffChange.Store(time.Now().Add(-2 * mc.vardiff.AdjustmentWindow).UnixNano())
	mc.setWorkerWallet(workerName, workerWallet, workerScript)

	// Prime stats so VarDiff can move difficulty, which also triggers writes
	// of mining.set_difficulty and mining.notify.
	mc.statsMu.Lock()
	mc.stats.WindowStart = time.Now().Add(-time.Minute)
	mc.stats.WindowAccepted = 10
	mc.stats.WindowSubmissions = 10
	mc.statsMu.Unlock()
	mc.rollingHashrateValue = 1e10 // enough to trigger an upward move (and power-of-two quantization)

	task := submissionTask{
		mc:               mc,
		reqID:            1,
		job:              job,
		jobID:            job.JobID,
		workerName:       workerName,
		extranonce2:      "00000000",
		extranonce2Large: []byte{0, 0, 0, 0},
		ntime:            "6553f100",
		ntimeVal:         0x6553f100,
		nonce:            "00000001",
		nonceVal:         0x00000001,
		useVersion:       1,
		scriptTime:       job.Template.CurTime,
		receivedAt:       time.Now(),
	}
	ctx := shareContext{
		hashHex:   strings.Repeat("0", 64),
		shareDiff: 1,
		isBlock:   false,
	}

	mc.lockDifficulty = false
	mc.processSoloShare(task, ctx)

	out := conn.String()
	lines := strings.Split(out, "\n")
	var nonEmpty []string
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			nonEmpty = append(nonEmpty, l)
		}
	}
	if len(nonEmpty) == 0 {
		t.Fatalf("expected output, got none")
	}
	if !strings.Contains(nonEmpty[0], `"result":true`) {
		t.Fatalf("expected first message to be submit response, got: %q", nonEmpty[0])
	}
	// Sanity: ensure we actually emitted some follow-up traffic (difficulty and/or notify).
	combined := strings.Join(nonEmpty[1:], "\n")
	if !strings.Contains(combined, `"method":"mining.set_difficulty"`) && !strings.Contains(combined, `"method":"mining.notify"`) {
		t.Fatalf("expected follow-up set_difficulty/notify after response, got: %q", combined)
	}
}
