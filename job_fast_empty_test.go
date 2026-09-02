package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/database"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/remeh/sizedwaitgroup"
)

func fastEmptyRawBlockPayload(t *testing.T, height int64, timestamp uint32, bits uint32, previous string) []byte {
	t.Helper()
	previousBytes, err := hex.DecodeString(previous)
	if err != nil || len(previousBytes) != 32 {
		t.Fatalf("decode previous hash: len=%d err=%v", len(previousBytes), err)
	}
	header := make([]byte, 80)
	binary.LittleEndian.PutUint32(header[0:4], 0x20000000)
	copy(header[4:36], reverseBytes(previousBytes))
	binary.LittleEndian.PutUint32(header[68:72], timestamp)
	binary.LittleEndian.PutUint32(header[72:76], bits)

	heightScript := serializeNumberScript(height)
	payload := append([]byte(nil), header...)
	payload = append(payload, 0x01)                   // transaction count
	payload = append(payload, 0x01, 0x00, 0x00, 0x00) // coinbase version
	payload = append(payload, 0x00, 0x01)             // witness marker and flag
	payload = append(payload, 0x01)                   // input count
	payload = append(payload, make([]byte, 36)...)    // null previous output
	payload = append(payload, byte(len(heightScript)))
	payload = append(payload, heightScript...)
	return payload
}

func installFastEmptyHistory(jm *JobManager, tipHash string, tipHeight int64, start int64) []time.Time {
	times := make([]time.Time, fastEmptyMedianTimeWindow)
	for i := range times {
		times[i] = time.Unix(start+int64(i*600), 0).UTC()
	}
	jm.zmqPayloadMu.Lock()
	jm.consensusHistoryTip = tipHash
	jm.consensusHistoryHeight = tipHeight
	jm.consensusHistoryTimes = append([]time.Time(nil), times...)
	jm.zmqPayloadMu.Unlock()
	return times
}

func fastEmptyBaseJob(previous string, height int64, now int64) *Job {
	return &Job{
		JobID:      "full-base",
		Generation: 1,
		CreatedAt:  time.Now(),
		Template: GetBlockTemplateResult{
			Bits:          "1d00ffff",
			Target:        "00000000ffff0000000000000000000000000000000000000000000000000000",
			CurTime:       now,
			Mintime:       now - 1,
			Height:        height,
			Version:       0x20000000,
			Previous:      previous,
			CoinbaseValue: 50 * 1e8,
		},
	}
}

func TestActivateFastEmptyJobBuildsCoinbaseOnlyCandidate(t *testing.T) {
	const previous = "1111111111111111111111111111111111111111111111111111111111111111"
	baseTime := time.Now().Add(-2 * time.Hour).Unix()
	payload := fastEmptyRawBlockPayload(t, 101, uint32(baseTime+12*600), 0x1d00ffff, previous)
	tip, err := parseRawBlockTip(payload)
	if err != nil {
		t.Fatalf("parse raw block: %v", err)
	}

	jm := NewJobManager(nil, Config{Extranonce2Size: 4, TemplateExtraNonce2Size: 8}, nil, []byte{0x51}, nil)
	jm.curJob = fastEmptyBaseJob(previous, tip.Height, baseTime+11*600)
	history := installFastEmptyHistory(jm, previous, tip.Height-1, baseTime)

	job, activated, err := jm.activateFastEmptyJob(tip, time.Now())
	if err != nil {
		t.Fatalf("activate fast empty job: %v", err)
	}
	if !activated || job == nil {
		t.Fatal("fast empty job was not activated")
	}
	if !job.FastEmpty || !job.Clean {
		t.Fatalf("fast job flags: FastEmpty=%v Clean=%v", job.FastEmpty, job.Clean)
	}
	if job.Template.Previous != tip.Hash || job.Template.Height != tip.Height+1 {
		t.Fatalf("fast job parent/height = %s/%d, want %s/%d", job.Template.Previous, job.Template.Height, tip.Hash, tip.Height+1)
	}
	if len(job.Transactions) != 0 || len(job.MerkleBranches) != 0 || job.WitnessCommitment != "" {
		t.Fatalf("fast job is not coinbase-only: txs=%d branches=%d commitment=%q", len(job.Transactions), len(job.MerkleBranches), job.WitnessCommitment)
	}
	if job.CoinbaseValue != calculateBlockSubsidy(job.Template.Height) {
		t.Fatalf("coinbase value=%d want subsidy=%d", job.CoinbaseValue, calculateBlockSubsidy(job.Template.Height))
	}

	medianInputs := make([]int64, 0, fastEmptyMedianTimeWindow)
	for _, ts := range history[len(history)-10:] {
		medianInputs = append(medianInputs, ts.Unix())
	}
	medianInputs = append(medianInputs, tip.Time.Unix())
	sort.Slice(medianInputs, func(i, j int) bool { return medianInputs[i] < medianInputs[j] })
	wantMintime := medianInputs[len(medianInputs)/2] + 1
	if job.Template.Mintime != wantMintime || job.Template.CurTime < wantMintime {
		t.Fatalf("fast job times: mintime=%d curtime=%d want minimum=%d", job.Template.Mintime, job.Template.CurTime, wantMintime)
	}

	blockHex, _, _, _, err := buildBlock(job, []byte{0, 0, 0, 1}, make([]byte, 4), uint32ToBEHex(uint32(job.Template.CurTime)), "00000000", job.Template.Version)
	if err != nil {
		t.Fatalf("build fast empty block: %v", err)
	}
	raw, err := hex.DecodeString(blockHex)
	if err != nil {
		t.Fatalf("decode block: %v", err)
	}
	var block wire.MsgBlock
	if err := block.Deserialize(bytes.NewReader(raw)); err != nil {
		t.Fatalf("deserialize block: %v", err)
	}
	if len(block.Transactions) != 1 {
		t.Fatalf("fast block transaction count=%d want 1", len(block.Transactions))
	}
}

func TestActivatedFastEmptyBlockPassesConsensusValidation(t *testing.T) {
	previousNetwork := ChainParams().Name
	SetChainParams("regtest")
	t.Cleanup(func() { SetChainParams(previousNetwork) })

	db, err := database.Create("ffldb", t.TempDir(), chaincfg.RegressionNetParams.Net)
	if err != nil {
		t.Fatalf("create chain database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	chain, err := blockchain.New(&blockchain.Config{
		DB:               db,
		UtxoCacheMaxSize: 1 << 20,
		ChainParams:      &chaincfg.RegressionNetParams,
		TimeSource:       blockchain.NewMedianTime(),
		SigCache:         txscript.NewSigCache(100),
	})
	if err != nil {
		t.Fatalf("create chain: %v", err)
	}

	genesis := chaincfg.RegressionNetParams.GenesisBlock
	payout := []byte{0x51}
	makeJob := func(height int64, previous string, timestamp int64) *Job {
		target, err := targetFromBits("207fffff")
		if err != nil {
			t.Fatalf("regtest target: %v", err)
		}
		return &Job{
			Template: GetBlockTemplateResult{
				Height:        height,
				Previous:      previous,
				Bits:          "207fffff",
				Target:        fmt.Sprintf("%064x", target),
				Version:       0x20000000,
				CurTime:       timestamp,
				Mintime:       timestamp,
				CoinbaseValue: blockchain.CalcBlockSubsidy(int32(height), &chaincfg.RegressionNetParams),
			},
			Target:                  target,
			CreatedAt:               time.Now(),
			Extranonce2Size:         4,
			TemplateExtraNonce2Size: 8,
			PayoutScript:            payout,
			CoinbaseValue:           blockchain.CalcBlockSubsidy(int32(height), &chaincfg.RegressionNetParams),
			CoinbaseMsg:             "fast-empty-consensus",
		}
	}
	buildAndProcess := func(job *Job) *wire.MsgBlock {
		blockHex, _, _, _, err := buildBlock(job, []byte{0, 0, 0, 1}, make([]byte, 4), uint32ToBEHex(uint32(job.Template.CurTime)), "00000000", job.Template.Version)
		if err != nil {
			t.Fatalf("build height %d: %v", job.Template.Height, err)
		}
		raw, err := hex.DecodeString(blockHex)
		if err != nil {
			t.Fatalf("decode height %d: %v", job.Template.Height, err)
		}
		var msg wire.MsgBlock
		if err := msg.Deserialize(bytes.NewReader(raw)); err != nil {
			t.Fatalf("deserialize height %d: %v", job.Template.Height, err)
		}
		mainChain, orphan, err := chain.ProcessBlock(btcutil.NewBlock(&msg), blockchain.BFNoPoWCheck)
		if err != nil {
			t.Fatalf("consensus rejected height %d: %v", job.Template.Height, err)
		}
		if !mainChain || orphan {
			t.Fatalf("height %d main=%v orphan=%v", job.Template.Height, mainChain, orphan)
		}
		return &msg
	}

	block1Time := genesis.Header.Timestamp.Unix() + 1
	block1 := buildAndProcess(makeJob(1, genesis.BlockHash().String(), block1Time))
	block2Time := block1Time + 1
	base := makeJob(2, block1.BlockHash().String(), block2Time)
	block2 := buildAndProcess(base)

	var rawBlock2 bytes.Buffer
	if err := block2.Serialize(&rawBlock2); err != nil {
		t.Fatalf("serialize connected block: %v", err)
	}
	tip, err := parseRawBlockTip(rawBlock2.Bytes())
	if err != nil {
		t.Fatalf("parse connected raw block: %v", err)
	}

	jm := NewJobManager(nil, Config{Extranonce2Size: 4, TemplateExtraNonce2Size: 8}, nil, payout, nil)
	base.Generation = 1
	jm.curJob = base
	jm.zmqPayloadMu.Lock()
	jm.consensusHistoryTip = block1.BlockHash().String()
	jm.consensusHistoryHeight = 1
	jm.consensusHistoryTimes = []time.Time{genesis.Header.Timestamp, block1.Header.Timestamp}
	jm.zmqPayloadMu.Unlock()

	fastJob, activated, err := jm.activateFastEmptyJob(tip, time.Now())
	if err != nil {
		t.Fatalf("activate fast job: %v", err)
	}
	if !activated {
		t.Fatal("fast job not activated")
	}
	buildAndProcess(fastJob)
}

func TestFastEmptySkipsUncertainTransitions(t *testing.T) {
	const previous = "2222222222222222222222222222222222222222222222222222222222222222"
	baseTime := time.Now().Add(-2 * time.Hour).Unix()

	tests := []struct {
		name      string
		height    int64
		mutateTip func(*ZMQBlockTip)
		history   bool
	}{
		{name: "missing history", height: 101},
		{name: "retarget boundary", height: 2015, history: true},
		{name: "not direct successor", height: 101, history: true, mutateTip: func(tip *ZMQBlockTip) {
			tip.previousHash = "3333333333333333333333333333333333333333333333333333333333333333"
		}},
		{name: "bits changed", height: 101, history: true, mutateTip: func(tip *ZMQBlockTip) { tip.Bits = "1c00ffff" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			payload := fastEmptyRawBlockPayload(t, tc.height, uint32(baseTime+12*600), 0x1d00ffff, previous)
			tip, err := parseRawBlockTip(payload)
			if err != nil {
				t.Fatalf("parse raw block: %v", err)
			}
			if tc.mutateTip != nil {
				tc.mutateTip(&tip)
			}
			jm := NewJobManager(nil, Config{Extranonce2Size: 4, TemplateExtraNonce2Size: 8}, nil, []byte{0x51}, nil)
			base := fastEmptyBaseJob(previous, tc.height, baseTime+11*600)
			jm.curJob = base
			if tc.history {
				installFastEmptyHistory(jm, previous, tc.height-1, baseTime)
			}
			if job, activated, err := jm.activateFastEmptyJob(tip, time.Now()); err != nil || activated || job != nil {
				t.Fatalf("uncertain activation = job=%v activated=%v err=%v", job, activated, err)
			}
			if jm.CurrentJob() != base {
				t.Fatal("uncertain transition replaced the full base job")
			}
		})
	}
}

func TestFastEmptyNetworkGating(t *testing.T) {
	previousNetwork := ChainParams().Name
	t.Cleanup(func() { SetChainParams(previousNetwork) })

	tests := []struct {
		network string
		want    bool
	}{
		{network: "mainnet", want: true},
		{network: "regtest", want: true},
		{network: "testnet3", want: false},
		{network: "signet", want: false},
	}
	for _, tc := range tests {
		SetChainParams(tc.network)
		if got := fastEmptyCanReuseTipBits(101); got != tc.want {
			t.Fatalf("network %s fast-path eligibility=%v want %v", tc.network, got, tc.want)
		}
	}
}

func TestRawBlockPublishesFastEmptyBeforeBlockedFullGBT(t *testing.T) {
	const previous = "4444444444444444444444444444444444444444444444444444444444444444"
	baseTime := time.Now().Add(-2 * time.Hour).Unix()
	payload := fastEmptyRawBlockPayload(t, 101, uint32(baseTime+12*600), 0x1d00ffff, previous)
	tip, err := parseRawBlockTip(payload)
	if err != nil {
		t.Fatalf("parse raw block: %v", err)
	}

	requestStarted := make(chan struct{})
	releaseGBT := make(chan struct{})
	defer closeTestChannel(releaseGBT)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if req.Method != "getblocktemplate" {
			t.Errorf("unexpected RPC method %q", req.Method)
			return
		}
		close(requestStarted)
		<-releaseGBT
		full := fastEmptyBaseJob(tip.Hash, tip.Height+1, baseTime+12*600).Template
		full.Mintime = baseTime + 6*600 + 1
		full.DefaultWitnessCommitment = "00"
		data, _ := json.Marshal(full)
		_ = json.NewEncoder(w).Encode(rpcResponse{ID: req.ID, Result: data})
	}))
	t.Cleanup(srv.Close)

	metrics := NewPoolMetrics()
	rpc := &RPCClient{url: srv.URL, client: srv.Client(), lp: srv.Client(), metrics: metrics}
	jm := NewJobManager(rpc, Config{Extranonce2Size: 4, TemplateExtraNonce2Size: 8}, metrics, []byte{0x51}, nil)
	jm.curJob = fastEmptyBaseJob(previous, tip.Height, baseTime+11*600)
	installFastEmptyHistory(jm, previous, tip.Height-1, baseTime)
	historyCtx, cancelHistory := context.WithCancel(context.Background())
	cancelHistory()
	jm.historyCtx = historyCtx

	jobCh := jm.Subscribe()
	ctx, cancel := context.WithCancel(context.Background())
	jm.notifyWg = sizedwaitgroup.New(1)
	jm.notifyWg.Add()
	go jm.notificationWorker(ctx, 0)
	t.Cleanup(func() {
		cancel()
		jm.notifyWg.Wait()
	})

	done := make(chan error, 1)
	go func() { done <- jm.handleZMQNotification(context.Background(), "rawblock", payload) }()

	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("full GBT did not start")
	}
	select {
	case job := <-jobCh:
		if !job.FastEmpty || job.Template.Previous != tip.Hash {
			t.Fatalf("first published job is not fast empty work for new parent: %#v", job)
		}
	case <-time.After(time.Second):
		t.Fatal("miners did not receive fast empty job while full GBT was blocked")
	}

	close(releaseGBT)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("raw-block handler: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("raw-block handler did not finish")
	}
	select {
	case job := <-jobCh:
		if job.FastEmpty || job.Template.Previous != tip.Hash || job.Clean {
			t.Fatalf("second published job is not non-clean full work: FastEmpty=%v parent=%s Clean=%v", job.FastEmpty, job.Template.Previous, job.Clean)
		}
	case <-time.After(time.Second):
		t.Fatal("miners did not receive full replacement job")
	}

	deadline := time.Now().Add(time.Second)
	for metrics.SnapshotGBTTiming().FastEmptyNotifyCount == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := metrics.SnapshotGBTTiming().FastEmptyNotifyCount; got != 1 {
		t.Fatalf("fast empty notify metric count=%d want 1", got)
	}
}
