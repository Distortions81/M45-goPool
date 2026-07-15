package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func auxRPCTemplate(prev string, height int64) GetBlockTemplateResult {
	return GetBlockTemplateResult{
		Bits:                     "1d00ffff",
		Target:                   "00000000ffff0000000000000000000000000000000000000000000000000000",
		CurTime:                  1_700_000_000,
		Mintime:                  1_700_000_000,
		Height:                   height,
		Version:                  0x20000000,
		Previous:                 prev,
		CoinbaseValue:            50 * 1e8,
		DefaultWitnessCommitment: "00",
		LongPollID:               "cursor",
	}
}

func writeRPCResult(t *testing.T, w http.ResponseWriter, id int, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Errorf("marshal RPC result: %v", err)
		http.Error(w, "marshal RPC result", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(rpcResponse{Result: raw, ID: id}); err != nil {
		t.Errorf("write RPC result: %v", err)
	}
}

func closeTestChannel(ch chan struct{}) {
	select {
	case <-ch:
	default:
		close(ch)
	}
}

func TestTemplateVerificationTimeoutReleasesRefreshLocks(t *testing.T) {
	const (
		oldPrev = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		newPrev = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	tpl := auxRPCTemplate(newPrev, 101)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode RPC request: %v", err)
			return
		}
		switch req.Method {
		case "getblocktemplate":
			writeRPCResult(t, w, req.ID, tpl)
		case "getbestblockhash":
			http.Error(w, "node unavailable", http.StatusServiceUnavailable)
		default:
			t.Errorf("unexpected RPC method %q", req.Method)
			http.Error(w, "unexpected method", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(server.Close)

	cfg := defaultConfig()
	cfg.RPCURL = server.URL
	rpc := NewRPCClient(cfg, nil)
	jm := NewJobManager(rpc, cfg, nil, []byte{0x51}, nil)
	jm.refreshRPCTimeout = 150 * time.Millisecond
	oldJob := &Job{Generation: 1, Template: auxRPCTemplate(oldPrev, 100), CreatedAt: time.Now()}
	jm.mu.Lock()
	jm.curJob = oldJob
	jm.mu.Unlock()
	jm.recordJobSuccess(oldJob)
	lastSuccess := jm.FeedStatus().LastSuccess

	start := time.Now()
	err := jm.refreshJobCtxForce(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("refresh error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("verification timeout took %v", elapsed)
	}
	if got := jm.CurrentJob(); got != oldJob {
		t.Fatalf("failed verification replaced current job: got=%p want=%p", got, oldJob)
	}
	status := jm.FeedStatus()
	if status.LastError == nil {
		t.Fatal("verification failure did not publish feed error")
	}
	if !status.LastSuccess.Equal(lastSuccess) {
		t.Fatalf("last success changed across failed verification: got=%v want=%v", status.LastSuccess, lastSuccess)
	}

	for name, lock := range map[string]*syncMutexForTest{
		"refreshMu": {lock: jm.refreshMu.Lock, unlock: jm.refreshMu.Unlock},
		"applyMu":   {lock: jm.applyMu.Lock, unlock: jm.applyMu.Unlock},
	} {
		done := make(chan struct{})
		go func(l *syncMutexForTest) {
			l.lock()
			l.unlock()
			close(done)
		}(lock)
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatalf("%s remained locked after verification timeout", name)
		}
	}
}

func TestRefreshDeadlineIncludesApplyLockWait(t *testing.T) {
	jm := NewJobManager(nil, defaultConfig(), nil, []byte{0x51}, nil)
	jm.applyMu.Lock()
	defer jm.applyMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := jm.refreshFromTemplate(ctx, auxRPCTemplate(
		"abababababababababababababababababababababababababababababababab", 101,
	))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("refresh error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("apply-lock wait ignored refresh deadline: %v", elapsed)
	}
}

func TestUnchangedTemplateCannotMaskAdvancedBestHash(t *testing.T) {
	const (
		oldPrev = "1212121212121212121212121212121212121212121212121212121212121212"
		newPrev = "3434343434343434343434343434343434343434343434343434343434343434"
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode RPC request: %v", err)
			return
		}
		if req.Method != "getbestblockhash" {
			t.Errorf("unexpected RPC method %q", req.Method)
			return
		}
		writeRPCResult(t, w, req.ID, newPrev)
	}))
	t.Cleanup(server.Close)

	cfg := defaultConfig()
	cfg.RPCURL = server.URL
	jm := NewJobManager(NewRPCClient(cfg, nil), cfg, nil, []byte{0x51}, nil)
	currentTemplate := auxRPCTemplate(oldPrev, 100)
	currentTemplate.LongPollID = "cursor-old"
	current := &Job{Generation: 1, Template: currentTemplate, CreatedAt: time.Now()}
	jm.mu.Lock()
	jm.curJob = current
	jm.longPollID = currentTemplate.LongPollID
	jm.mu.Unlock()
	jm.recordJobSuccess(current)
	lastSuccess := jm.FeedStatus().LastSuccess

	unchanged := currentTemplate
	unchanged.CurTime++
	unchanged.LongPollID = "cursor-stale"
	err := jm.refreshFromTemplate(context.Background(), unchanged)
	if !errors.Is(err, errStaleTemplate) {
		t.Fatalf("refresh error = %v, want errStaleTemplate", err)
	}
	job, cursor := jm.currentJobAndLongPollID()
	if job != current || cursor != currentTemplate.LongPollID {
		t.Fatalf("stale heartbeat changed work: job=%p cursor=%q", job, cursor)
	}
	status := jm.FeedStatus()
	if status.LastError == nil || !status.LastSuccess.Equal(lastSuccess) {
		t.Fatalf("stale heartbeat incorrectly recovered feed health: %+v", status)
	}
}

type syncMutexForTest struct {
	lock   func()
	unlock func()
}

func TestRefreshPublishesBeforeBlockHistoryRPC(t *testing.T) {
	const tipHash = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	tpl := auxRPCTemplate(tipHash, 200)
	historyStarted := make(chan struct{})
	releaseHistory := make(chan struct{})
	defer closeTestChannel(releaseHistory)
	var bestCalls atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode RPC request: %v", err)
			return
		}
		switch req.Method {
		case "getbestblockhash":
			if bestCalls.Add(1) == 2 {
				close(historyStarted)
				<-releaseHistory
			}
			writeRPCResult(t, w, req.ID, tipHash)
		case "getblockheader":
			writeRPCResult(t, w, req.ID, BlockHeader{Hash: tipHash, Height: 199, Time: 1_700_000_000, Bits: tpl.Bits})
		default:
			t.Errorf("unexpected RPC method %q", req.Method)
			http.Error(w, "unexpected method", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(server.Close)

	cfg := defaultConfig()
	cfg.RPCURL = server.URL
	jm := NewJobManager(NewRPCClient(cfg, nil), cfg, nil, []byte{0x51}, nil)
	jm.historyRPCTimeout = 2 * time.Second

	if err := jm.refreshFromTemplate(context.Background(), tpl); err != nil {
		t.Fatalf("refreshFromTemplate: %v", err)
	}
	select {
	case <-historyStarted:
	case <-time.After(time.Second):
		t.Fatal("history RPC did not start")
	}

	select {
	case job := <-jm.notifyQueue:
		if job == nil || job.Template.Previous != tipHash {
			t.Fatalf("published job = %#v", job)
		}
	default:
		t.Fatal("new job was not published before history completed")
	}
	if status := jm.FeedStatus(); status.LastError != nil || status.LastSuccess.IsZero() {
		t.Fatalf("published job did not mark feed successful: %+v", status)
	}

	applyDone := make(chan struct{})
	go func() {
		jm.applyMu.Lock()
		jm.applyMu.Unlock()
		close(applyDone)
	}()
	select {
	case <-applyDone:
	case <-time.After(time.Second):
		t.Fatal("history RPC retained applyMu")
	}

	closeTestChannel(releaseHistory)
	waitForHistoryWorker(t, jm)
}

func TestBlockHistoryCannotOverwriteNewerReorg(t *testing.T) {
	const (
		hashA = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
		hashB = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	)
	aStarted := make(chan struct{})
	bStarted := make(chan struct{})
	releaseA := make(chan struct{})
	releaseB := make(chan struct{})
	defer func() {
		closeTestChannel(releaseA)
		closeTestChannel(releaseB)
	}()
	var bestCalls atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode RPC request: %v", err)
			return
		}
		switch req.Method {
		case "getbestblockhash":
			switch bestCalls.Add(1) {
			case 1:
				close(aStarted)
				<-releaseA
				writeRPCResult(t, w, req.ID, hashA)
			default:
				close(bStarted)
				<-releaseB
				writeRPCResult(t, w, req.ID, hashB)
			}
		case "getblockheader":
			params, _ := req.Params.([]any)
			hash, _ := params[0].(string)
			height := int64(300)
			if hash == hashB {
				height = 299 // A lower-height reorg is still the newer generation.
			}
			writeRPCResult(t, w, req.ID, BlockHeader{Hash: hash, Height: height, Time: 1_700_000_000, Bits: "1d00ffff"})
		default:
			t.Errorf("unexpected RPC method %q", req.Method)
			http.Error(w, "unexpected method", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(server.Close)

	cfg := defaultConfig()
	cfg.RPCURL = server.URL
	jm := NewJobManager(NewRPCClient(cfg, nil), cfg, nil, []byte{0x51}, nil)
	jm.historyRPCTimeout = 2 * time.Second
	jobA := &Job{Generation: 1, Template: auxRPCTemplate(hashA, 301)}
	jobB := &Job{Generation: 2, Template: auxRPCTemplate(hashB, 300)}
	jm.mu.Lock()
	jm.curJob = jobA
	jm.mu.Unlock()
	jm.storeBlockHistory(ZMQBlockTip{Hash: "sentinel"}, nil)
	jm.scheduleBlockHistoryRefresh(jobA)
	select {
	case <-aStarted:
	case <-time.After(time.Second):
		t.Fatal("branch A history did not start")
	}

	jm.mu.Lock()
	jm.curJob = jobB
	jm.mu.Unlock()
	jm.scheduleBlockHistoryRefresh(jobB)
	closeTestChannel(releaseA)
	select {
	case <-bStarted:
	case <-time.After(time.Second):
		t.Fatal("branch B history did not start")
	}
	if got := jm.payloadStatus().BlockTip.Hash; got != "sentinel" {
		t.Fatalf("superseded branch A overwrote history: got %q", got)
	}

	closeTestChannel(releaseB)
	waitForHistoryWorker(t, jm)
	if got := jm.payloadStatus().BlockTip; got.Hash != hashB || got.Height != 299 {
		t.Fatalf("newer reorg history was not committed: %+v", got)
	}
}

func TestBlockHistorySurvivesSameParentJobChurn(t *testing.T) {
	const hash = "f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0"
	started := make(chan struct{})
	release := make(chan struct{})
	defer closeTestChannel(release)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode RPC request: %v", err)
			return
		}
		switch req.Method {
		case "getbestblockhash":
			closeTestChannel(started)
			<-release
			writeRPCResult(t, w, req.ID, hash)
		case "getblockheader":
			writeRPCResult(t, w, req.ID, BlockHeader{Hash: hash, Height: 400, Time: 1_700_000_000, Bits: "1d00ffff"})
		default:
			t.Errorf("unexpected RPC method %q", req.Method)
			http.Error(w, "unexpected method", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(server.Close)

	cfg := defaultConfig()
	cfg.RPCURL = server.URL
	jm := NewJobManager(NewRPCClient(cfg, nil), cfg, nil, []byte{0x51}, nil)
	jm.historyRPCTimeout = 2 * time.Second
	jobA := &Job{Generation: 1, Template: auxRPCTemplate(hash, 401)}
	jm.mu.Lock()
	jm.curJob = jobA
	jm.mu.Unlock()
	jm.scheduleBlockHistoryRefresh(jobA)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("history RPC did not start")
	}

	jm.mu.Lock()
	jm.curJob = &Job{Generation: 2, Template: auxRPCTemplate(hash, 401)}
	jm.mu.Unlock()
	closeTestChannel(release)
	waitForHistoryWorker(t, jm)
	if got := jm.payloadStatus().BlockTip; got.Hash != hash || got.Height != 400 {
		t.Fatalf("same-parent job churn discarded valid history: %+v", got)
	}
}

func TestBlockHistoryCannotOverwriteNewerRawBlockTip(t *testing.T) {
	const (
		hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	jm := NewJobManager(nil, Config{}, nil, nil, nil)
	jobA := &Job{Generation: 1, Template: auxRPCTemplate(hashA, 301)}
	jm.mu.Lock()
	jm.curJob = jobA
	jm.mu.Unlock()

	req := blockHistoryRefreshRequest{generation: jobA.Generation, prevHash: hashA}
	newerTip := ZMQBlockTip{Hash: hashB, Height: 301, Time: time.Unix(1_700_000_100, 0)}
	jm.recordBlockTip(newerTip)
	if jm.commitBlockHistoryIfCurrent(req, ZMQBlockTip{Hash: hashA, Height: 300}, nil) {
		t.Fatal("older history committed over a newer raw-block notification")
	}
	if got := jm.payloadStatus().BlockTip; got.Hash != hashB || got.Height != newerTip.Height {
		t.Fatalf("newer raw-block tip was overwritten: %+v", got)
	}
}

func waitForHistoryWorker(t *testing.T, jm *JobManager) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		jm.historyMu.Lock()
		running := jm.historyRunning
		jm.historyMu.Unlock()
		if !running {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("history worker did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}
