package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
)

const bitcoinCoreRegtestIntegrationEnv = "GOPOOL_TEST_BITCOIN_CORE_REGTEST"

func TestBitcoinCoreRegtestAcceptsGoPoolBlock(t *testing.T) {
	if os.Getenv(bitcoinCoreRegtestIntegrationEnv) != "1" {
		t.Skipf("set %s=1 to run the Bitcoin Core regtest integration test", bitcoinCoreRegtestIntegrationEnv)
	}

	bitcoind := strings.TrimSpace(os.Getenv("GOPOOL_BITCOIND"))
	if bitcoind == "" {
		var err error
		bitcoind, err = exec.LookPath("bitcoind")
		if err != nil {
			t.Skip("bitcoind not found in PATH; set GOPOOL_BITCOIND to its path")
		}
	}

	rpcPort := reserveLocalPort(t)
	dataDir := t.TempDir()
	logPath := filepath.Join(dataDir, "bitcoind-test.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create bitcoind log: %v", err)
	}

	const (
		rpcUser = "gopool-regtest"
		rpcPass = "gopool-regtest-password"
	)
	cmd := exec.Command(bitcoind,
		"-regtest",
		"-datadir="+dataDir,
		"-server=1",
		"-listen=0",
		"-dnsseed=0",
		"-discover=0",
		"-persistmempool=0",
		"-fallbackfee=0.0002",
		"-printtoconsole=1",
		fmt.Sprintf("-rpcport=%d", rpcPort),
		"-rpcbind=127.0.0.1",
		"-rpcallowip=127.0.0.1",
		"-rpcuser="+rpcUser,
		"-rpcpassword="+rpcPass,
	)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		t.Fatalf("start bitcoind: %v", err)
	}

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Signal(os.Interrupt)
		}
		select {
		case <-waitCh:
		case <-time.After(5 * time.Second):
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			<-waitCh
		}
		_ = logFile.Close()
	})

	rpc := NewRPCClient(Config{
		RPCURL:  fmt.Sprintf("http://127.0.0.1:%d", rpcPort),
		RPCUser: rpcUser,
		RPCPass: rpcPass,
	}, nil)
	readyCtx, cancelReady := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelReady()
	var chainInfo struct {
		Chain  string `json:"chain"`
		Blocks int64  `json:"blocks"`
	}
	if err := rpc.callCtx(readyCtx, "getblockchaininfo", nil, &chainInfo); err != nil {
		t.Fatalf("wait for bitcoind RPC: %v (log: %s)", err, logPath)
	}
	if chainInfo.Chain != "regtest" {
		t.Fatalf("bitcoind chain=%q, want regtest", chainInfo.Chain)
	}

	var deploymentInfo struct {
		Deployments map[string]struct {
			Active bool `json:"active"`
		} `json:"deployments"`
	}
	if err := rpc.callCtx(readyCtx, "getdeploymentinfo", nil, &deploymentInfo); err != nil {
		t.Fatalf("get regtest deployment state: %v", err)
	}
	segwit, ok := deploymentInfo.Deployments["segwit"]
	if !ok || !segwit.Active {
		t.Fatal("Bitcoin Core regtest reports SegWit inactive")
	}

	const walletName = "gopool-integration"
	var walletCreated any
	if err := rpc.callCtx(readyCtx, "createwallet", []any{walletName}, &walletCreated); err != nil {
		t.Fatalf("create regtest wallet: %v", err)
	}
	walletRPC := NewRPCClient(Config{
		RPCURL:  fmt.Sprintf("http://127.0.0.1:%d/wallet/%s", rpcPort, walletName),
		RPCUser: rpcUser,
		RPCPass: rpcPass,
	}, nil)
	var payoutAddress string
	if err := walletRPC.callCtx(readyCtx, "getnewaddress", []any{"", "bech32"}, &payoutAddress); err != nil {
		t.Fatalf("get regtest payout address: %v", err)
	}
	payoutAddr, err := btcutil.DecodeAddress(payoutAddress, &chaincfg.RegressionNetParams)
	if err != nil {
		t.Fatalf("decode regtest payout address: %v", err)
	}
	payoutScript, err := scriptForAddress(payoutAddr.EncodeAddress(), &chaincfg.RegressionNetParams)
	if err != nil {
		t.Fatalf("build regtest payout script: %v", err)
	}

	var generated []string
	if err := rpc.callCtx(readyCtx, "generatetoaddress", []any{101, payoutAddress}, &generated); err != nil {
		t.Fatalf("mature regtest wallet funds: %v", err)
	}
	var destination string
	if err := walletRPC.callCtx(readyCtx, "getnewaddress", []any{"", "bech32"}, &destination); err != nil {
		t.Fatalf("get regtest spend destination: %v", err)
	}
	var spendTxID string
	if err := walletRPC.callCtx(readyCtx, "sendtoaddress", []any{destination, 1.0}, &spendTxID); err != nil {
		t.Fatalf("create witness-bearing mempool transaction: %v", err)
	}

	cfg := defaultConfig()
	cfg.Extranonce2Size = 4
	cfg.TemplateExtraNonce2Size = 8
	cfg.CoinbaseMsg = "goPool-regtest-integration"
	jm := NewJobManager(rpc, cfg, nil, payoutScript, nil)

	testCtx, cancelTest := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelTest()
	tpl, err := jm.fetchTemplateCtx(testCtx, map[string]any{
		"rules":        []string{"segwit"},
		"capabilities": []string{"coinbasetxn", "workid", "coinbase/append"},
	}, false)
	if err != nil {
		t.Fatalf("getblocktemplate: %v", err)
	}
	if tpl.DefaultWitnessCommitment == "" {
		t.Fatal("Bitcoin Core template omitted default_witness_commitment")
	}
	if len(tpl.Transactions) == 0 {
		t.Fatal("Bitcoin Core template omitted the witness-bearing mempool transaction")
	}
	hasWitnessTransaction := false
	for _, tx := range tpl.Transactions {
		raw, err := hex.DecodeString(tx.Data)
		if err != nil {
			t.Fatalf("decode template transaction: %v", err)
		}
		_, hasWitness, err := stripWitnessData(raw)
		if err != nil {
			t.Fatalf("inspect template transaction witness: %v", err)
		}
		if hasWitness {
			hasWitnessTransaction = true
			break
		}
	}
	if !hasWitnessTransaction {
		t.Fatalf("template contains %d transactions but none carry witness data (spend %s)", len(tpl.Transactions), spendTxID)
	}
	t.Logf("SegWit active; template has %d transaction(s) and witness commitment %s", len(tpl.Transactions), tpl.DefaultWitnessCommitment)
	job, err := jm.buildJob(testCtx, tpl)
	if err != nil {
		t.Fatalf("build goPool job: %v", err)
	}

	blockHex, blockHash := solveRegtestBlock(t, job, payoutScript)
	var submitResult any
	if err := rpc.callCtx(testCtx, "submitblock", []any{blockHex}, &submitResult); err != nil {
		t.Fatalf("submitblock RPC: %v", err)
	}
	if err := submitBlockResultError(&submitResult); err != nil {
		t.Fatalf("Bitcoin Core rejected goPool block %s: %v", blockHash, err)
	}

	var header struct {
		Confirmations int64 `json:"confirmations"`
	}
	if err := rpc.callCtx(testCtx, "getblockheader", []any{blockHash, true}, &header); err != nil {
		t.Fatalf("get accepted block header: %v", err)
	}
	if header.Confirmations < 1 {
		t.Fatalf("accepted block confirmations=%d, want at least 1", header.Confirmations)
	}
}

func reserveLocalPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve RPC port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatalf("release RPC port: %v", err)
	}
	return port
}

func solveRegtestBlock(t *testing.T, job *Job, payoutScript []byte) (blockHex, blockHash string) {
	t.Helper()
	extranonce1 := []byte{0x01, 0x02, 0x03, 0x04}
	extranonce2 := make([]byte, job.Extranonce2Size)
	ntime := fmt.Sprintf("%08x", uint32(job.Template.CurTime))

	for nonce := uint32(0); ; nonce++ {
		nonceHex := fmt.Sprintf("%08x", nonce)
		candidate, headerHash, _, _, err := buildBlockWithScriptTime(
			job,
			extranonce1,
			extranonce2,
			ntime,
			nonceHex,
			job.Template.Version,
			payoutScript,
			job.ScriptTime,
		)
		if err != nil {
			t.Fatalf("build candidate block: %v", err)
		}

		hashBE := reverseBytes(headerHash)
		if new(big.Int).SetBytes(hashBE).Cmp(job.Target) <= 0 {
			return candidate, fmt.Sprintf("%x", hashBE)
		}
		if nonce == ^uint32(0) {
			t.Fatal("exhausted nonce space without solving regtest block")
		}
	}
}
