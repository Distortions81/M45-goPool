package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRuntimeConfigKeepsActiveSessionAndAdaptsNewJobs(t *testing.T) {
	mc, conn := minerConnForNotifyTest(t)
	mc.cfg.Extranonce2Size = 8
	mc.cfg.TemplateExtraNonce2Size = 8
	mc.cfg.SubmitProcessInline = true
	mc.cfg.ShareRequireAuthorizedConnection = true
	mc.cfg.ShareRequireWorkerMatch = true
	mc.cfg.ShareJobFreshnessMode = shareJobFreshnessJobID
	mc.cfg.ShareCheckParamFormat = true
	mc.maxRecentJobs = 10
	mc.rpc = &countingSubmitRPC{}

	oldJob := benchmarkSubmitJobForTest(t)
	oldJob.Extranonce2Size = 8
	oldJob.TemplateExtraNonce2Size = 8
	mc.sendNotifyFor(oldJob, true)
	oldIDs := notifyJobIDsFromOutput(t, conn.String())
	if len(oldIDs) != 1 {
		t.Fatalf("initial notify IDs = %#v", oldIDs)
	}

	oldVarDiff := mc.vardiff
	oldDifficulty := mc.currentDifficulty()
	oldTarget := new(big.Int).Set(mc.shareTargetOrDefault())
	oldMaxRecentJobs := mc.maxRecentJobs
	wireBeforeApply := conn.String()

	oldAddress, oldScript := generateTestWallet(t)
	oldCfg := mc.cfg
	oldCfg.PayoutAddress = oldAddress
	jm := NewJobManager(nil, oldCfg, nil, oldScript, nil)
	s := &StatusServer{jobMgr: jm}
	s.UpdateConfig(oldCfg)

	newAddress, _ := generateTestWallet(t)
	newCfg := oldCfg
	newCfg.PayoutAddress = newAddress
	newCfg.Extranonce2Size = 2
	newCfg.TemplateExtraNonce2Size = 2
	newCfg.MinDifficulty = oldDifficulty * 100
	newCfg.MaxRecentJobs = 1
	if err := s.publishRuntimeConfig(newCfg); err != nil {
		t.Fatalf("publish runtime config: %v", err)
	}

	if mc.cfg.Extranonce2Size != 8 || mc.cfg.TemplateExtraNonce2Size != 8 {
		t.Fatalf("active session extranonce config changed: en2=%d template=%d", mc.cfg.Extranonce2Size, mc.cfg.TemplateExtraNonce2Size)
	}
	if mc.vardiff != oldVarDiff || mc.currentDifficulty() != oldDifficulty {
		t.Fatalf("active session difficulty policy changed: vardiff=%+v difficulty=%v", mc.vardiff, mc.currentDifficulty())
	}
	if got := mc.shareTargetOrDefault(); got.Cmp(oldTarget) != 0 {
		t.Fatalf("active session target changed: got=%x want=%x", got, oldTarget)
	}
	if mc.maxRecentJobs != oldMaxRecentJobs {
		t.Fatalf("active session history limit changed: got=%d want=%d", mc.maxRecentJobs, oldMaxRecentJobs)
	}
	if got := conn.String(); got != wireBeforeApply {
		t.Fatalf("runtime publish wrote unadvertised session changes: before=%q after=%q", wireBeforeApply, got)
	}

	newJob := benchmarkSubmitJobForTest(t)
	newJob.JobID = "new-size-job"
	newJob.Extranonce2Size = 2
	newJob.TemplateExtraNonce2Size = 2
	newJob.CoinbaseScriptSigMaxBytes = 80
	newJob.CoinbaseMsg = strings.Repeat("session-extra", 12)
	newJob.Target = new(big.Int).Set(maxUint256)
	newJob.targetBE = uint256BEFromBigInt(newJob.Target)
	mc.sendNotifyFor(newJob, false)

	notifies := notifyMessagesFromOutput(t, conn.String())
	if len(notifies) != 2 {
		t.Fatalf("notify count = %d, want 2", len(notifies))
	}
	params := notifies[1].Params
	jobID := params[0].(string)
	retained, _, _, _, _, _, _, _, ok := mc.jobForIDWithLast(jobID)
	if !ok {
		t.Fatalf("adapted job %q not retained", jobID)
	}
	if retained == newJob {
		t.Fatalf("manager job was retained without a session-local snapshot")
	}
	if retained.Extranonce2Size != 8 || retained.TemplateExtraNonce2Size != 8 {
		t.Fatalf("retained sizes = %d/%d, want 8/8", retained.Extranonce2Size, retained.TemplateExtraNonce2Size)
	}
	if _, _, _, _, _, _, _, _, ok := mc.jobForIDWithLast(oldIDs[0]); !ok {
		t.Fatalf("outstanding pre-apply job was evicted")
	}

	en2Hex := "0001020304050607"
	coinbaseHex := params[2].(string) + hex.EncodeToString(mc.extranonce1) + en2Hex + params[3].(string)
	coinbase, err := hex.DecodeString(coinbaseHex)
	if err != nil {
		t.Fatalf("decode notified coinbase: %v", err)
	}
	merkle := computeMerkleRootFromBranches(doubleSHA256(coinbase), retained.MerkleBranches)
	version, err := parseUint32BEHex(params[5].(string))
	if err != nil {
		t.Fatalf("parse notify version: %v", err)
	}
	expectedHeader, err := retained.buildBlockHeader(merkle, params[7].(string), "00000000", int32(version))
	if err != nil {
		t.Fatalf("build expected header: %v", err)
	}

	mc.handleSubmit(&StratumRequest{ID: 1, Method: "mining.submit", Params: []any{
		mc.currentWorker(), jobID, en2Hex, params[7].(string), "00000000",
	}})
	flushFoundBlockLog(t)
	rpc := mc.rpc.(*countingSubmitRPC)
	if got := rpc.submitCalls.Load(); got != 1 {
		t.Fatalf("submitblock calls = %d, want 1", got)
	}
	block, err := hex.DecodeString(rpc.blockHex)
	if err != nil {
		t.Fatalf("decode submitted block: %v", err)
	}
	if len(block) < 81 || !bytes.Equal(block[:80], expectedHeader) {
		t.Fatalf("submitted block does not match the adapted notify header")
	}
	if block[80] != 1 || !bytes.Equal(block[81:], coinbase) {
		t.Fatalf("submitted block does not contain the exact notified coinbase")
	}

	newConn := NewMinerConn(context.Background(), nopConn{}, jm, nil, s.Config(), nil, nil, nil, nil, nil, false)
	t.Cleanup(func() { newConn.cleanup() })
	if newConn.cfg.Extranonce2Size != 2 {
		t.Fatalf("new connection extranonce2 size = %d, want 2", newConn.cfg.Extranonce2Size)
	}
}

func TestPublishRuntimeConfigFailureIsAtomic(t *testing.T) {
	oldAddress, oldScript := generateTestWallet(t)
	oldCfg := defaultConfig()
	oldCfg.PayoutAddress = oldAddress
	jm := NewJobManager(nil, oldCfg, nil, oldScript, nil)
	s := &StatusServer{jobMgr: jm}
	s.UpdateConfig(oldCfg)

	badCfg := oldCfg
	badCfg.StatusTagline = "must not publish"
	badCfg.PayoutAddress = "not-a-bitcoin-address"
	if err := s.publishRuntimeConfig(badCfg); err == nil {
		t.Fatalf("expected invalid payout address to fail")
	}
	if got := s.Config(); got.StatusTagline != oldCfg.StatusTagline || got.PayoutAddress != oldCfg.PayoutAddress {
		t.Fatalf("status config changed after failed publish: tagline=%q payout=%q", got.StatusTagline, got.PayoutAddress)
	}

	jm.applyMu.Lock()
	gotAddress := jm.cfg.PayoutAddress
	gotScript := append([]byte(nil), jm.payoutScript...)
	jm.applyMu.Unlock()
	if gotAddress != oldCfg.PayoutAddress || !bytes.Equal(gotScript, oldScript) {
		t.Fatalf("job config changed after failed publish: address=%q script=%x", gotAddress, gotScript)
	}
}

func TestJobManagerRuntimeConfigConcurrentSnapshots(t *testing.T) {
	const bestHash = "0000000000000000000000000000000000000000000000000000000000000001"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode RPC request: %v", err)
			return
		}
		result, _ := json.Marshal(bestHash)
		_ = json.NewEncoder(w).Encode(rpcResponse{ID: req.ID, Result: result})
	}))
	t.Cleanup(srv.Close)

	rpc := &RPCClient{url: srv.URL, client: srv.Client(), lp: srv.Client()}
	cfgA := defaultConfig()
	cfgA.PayoutAddress = "policy-a"
	cfgA.Extranonce2Size = 4
	cfgA.TemplateExtraNonce2Size = 8
	cfgB := cfgA
	cfgB.PayoutAddress = "policy-b"
	cfgB.Extranonce2Size = 8
	cfgB.TemplateExtraNonce2Size = 8
	scriptA := []byte{0x51}
	scriptB := []byte{0x52}
	jm := NewJobManager(rpc, cfgA, nil, scriptA, nil)
	tpl := GetBlockTemplateResult{
		Bits:                     "1d00ffff",
		Target:                   "00000000ffff0000000000000000000000000000000000000000000000000000",
		CurTime:                  1_700_000_000,
		Height:                   101,
		Version:                  0x20000000,
		Previous:                 bestHash,
		CoinbaseValue:            50 * 1e8,
		DefaultWitnessCommitment: "00",
	}
	initial, err := jm.buildJob(context.Background(), tpl)
	if err != nil {
		t.Fatalf("build initial job: %v", err)
	}
	jm.mu.Lock()
	jm.curJob = initial
	jm.mu.Unlock()

	var wg sync.WaitGroup
	wg.Add(4)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			if i%2 == 0 {
				jm.ApplyRuntimeConfig(cfgA, scriptA, nil)
			} else {
				jm.ApplyRuntimeConfig(cfgB, scriptB, nil)
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_ = jm.FeedStatus()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_, _ = jm.templateChanged(tpl)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 30; i++ {
			job, err := jm.buildJob(context.Background(), tpl)
			if err != nil {
				t.Errorf("build concurrent job: %v", err)
				return
			}
			isA := job.PayoutAddress == cfgA.PayoutAddress && job.Extranonce2Size == cfgA.Extranonce2Size && bytes.Equal(job.PayoutScript, scriptA)
			isB := job.PayoutAddress == cfgB.PayoutAddress && job.Extranonce2Size == cfgB.Extranonce2Size && bytes.Equal(job.PayoutScript, scriptB)
			if !isA && !isB {
				t.Errorf("incoherent runtime snapshot: address=%q en2=%d script=%x", job.PayoutAddress, job.Extranonce2Size, job.PayoutScript)
				return
			}
		}
	}()
	wg.Wait()

	// FeedStatus uses startup ZMQ endpoints and must not wait for a mutable job
	// policy lock held by a build/refresh.
	jm.applyMu.Lock()
	done := make(chan struct{})
	go func() {
		_ = jm.FeedStatus()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("FeedStatus blocked on mutable job configuration")
	}
	jm.applyMu.Unlock()
}
