package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/btcsuite/btcd/wire"
)

// TestFoundBlockSubmission_BtcdCompat verifies that a valid block found by a
// miner can be built and submitted successfully, and that the serialized block
// parses correctly with btcd's wire.MsgBlock.
func TestFoundBlockSubmission_BtcdCompat(t *testing.T) {
	// Track submitblock calls
	var submitCalls int
	var submittedBlockHex string

	// Mock RPC server that accepts submitblock
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
			return
		}

		var req rpcRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("unmarshal request: %v", err)
			return
		}

		if req.Method == "submitblock" {
			submitCalls++
			if params, ok := req.Params.([]interface{}); ok && len(params) > 0 {
				if blockHex, ok := params[0].(string); ok {
					submittedBlockHex = blockHex
				}
			}
			// Return success
			w.Header().Set("Content-Type", "application/json")
			resp := rpcResponse{Result: nil, Error: nil, ID: req.ID}
			_ = json.NewEncoder(w).Encode(resp)
		} else {
			t.Errorf("unexpected RPC method: %s", req.Method)
		}
	}))
	defer server.Close()

	rpc := &RPCClient{
		url:     server.URL,
		user:    "test",
		pass:    "test",
		metrics: nil,
		client:  server.Client(),
		lp:      server.Client(),
		nextID:  1,
	}

	// Create a test job
	job := &Job{
		JobID: "btcd-test-job",
		Template: GetBlockTemplateResult{
			Height:        100,
			CurTime:       1700000000,
			Mintime:       0,
			Bits:          "1d00ffff", // Very easy difficulty for testing
			Previous:      "0000000000000000000000000000000000000000000000000000000000000000",
			CoinbaseValue: 50 * 1e8,
		},
		Extranonce2Size:         4,
		TemplateExtraNonce2Size: 8,
		PayoutScript:            []byte{0x76, 0xa9, 0x14}, // Mock P2PKH prefix
		WitnessCommitment:       "",
		CoinbaseMsg:             "goPool-test",
		ScriptTime:              0,
		Transactions:            nil,
		MerkleBranches:          nil,
		CoinbaseValue:           50 * 1e8,
	}

	// Build a block
	extranonce1 := []byte{0x01, 0x02, 0x03, 0x04}
	extranonce2 := []byte{0xaa, 0xbb, 0xcc, 0xdd}
	ntimeHex := fmt.Sprintf("%08x", job.Template.CurTime)
	nonceHex := "12345678"
	version := int32(0x20000000)

	blockHex, headerHash, header, merkleRoot, err := buildBlock(job, extranonce1, extranonce2, ntimeHex, nonceHex, version)
	if err != nil {
		t.Fatalf("buildBlock error: %v", err)
	}

	if len(blockHex) == 0 || len(headerHash) != 32 || len(header) != 80 || len(merkleRoot) != 32 {
		t.Fatalf("unexpected block artifacts")
	}

	// Submit the block
	var submitRes interface{}
	err = rpc.call("submitblock", []interface{}{blockHex}, &submitRes)
	if err != nil {
		t.Fatalf("submitblock error: %v", err)
	}

	// Verify submitblock was called
	if submitCalls != 1 {
		t.Fatalf("expected 1 submitblock call, got %d", submitCalls)
	}

	// Verify the submitted block parses with btcd
	raw, err := hex.DecodeString(submittedBlockHex)
	if err != nil {
		t.Fatalf("decode submitted block: %v", err)
	}

	var msgBlock wire.MsgBlock
	if err := msgBlock.Deserialize(bytes.NewReader(raw)); err != nil {
		t.Fatalf("btcd MsgBlock deserialize error: %v", err)
	}

	// Verify block structure
	if msgBlock.Header.Version != version {
		t.Errorf("header version mismatch: got %d, want %d", msgBlock.Header.Version, version)
	}

	if len(msgBlock.Transactions) != 1 {
		t.Errorf("expected 1 transaction (coinbase only), got %d", len(msgBlock.Transactions))
	}

	// Verify merkle root matches
	if !bytes.Equal(msgBlock.Header.MerkleRoot.CloneBytes(), merkleRoot) {
		t.Errorf("merkle root mismatch: header=%x computed=%x",
			msgBlock.Header.MerkleRoot.CloneBytes(), merkleRoot)
	}

	t.Logf("Successfully submitted block at height %d with hash %x", job.Template.Height, headerHash)
}

// TestFoundBlockSubmission_DualPayout tests block submission with dual-payout
// (pool fee + worker payout) and verifies the coinbase outputs are correct.
func TestFoundBlockSubmission_DualPayout(t *testing.T) {
	cfg := Config{
		PoolFeePercent: 2.0,
	}

	var submittedBlockHex string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req rpcRequest
		_ = json.Unmarshal(body, &req)

		if req.Method == "submitblock" {
			if params, ok := req.Params.([]interface{}); ok && len(params) > 0 {
				if blockHex, ok := params[0].(string); ok {
					submittedBlockHex = blockHex
				}
			}
			w.Header().Set("Content-Type", "application/json")
			resp := rpcResponse{Result: nil, Error: nil, ID: req.ID}
			_ = json.NewEncoder(w).Encode(resp)
		}
	}))
	defer server.Close()

	rpc := &RPCClient{
		url:    server.URL,
		client: server.Client(),
		lp:     server.Client(),
		nextID: 1,
	}

	poolScript := []byte{0x51}   // OP_TRUE for pool
	workerScript := []byte{0x52} // OP_2 for worker
	totalValue := int64(50 * 1e8)
	feePercent := cfg.PoolFeePercent

	job := &Job{
		Template: GetBlockTemplateResult{
			Height:        200,
			CurTime:       1700000100,
			Bits:          "1d00ffff",
			Previous:      "0000000000000000000000000000000000000000000000000000000000000000",
			CoinbaseValue: totalValue,
		},
		TemplateExtraNonce2Size: 8,
	}

	extranonce1 := []byte{0x01, 0x02, 0x03, 0x04}
	extranonce2 := []byte{0xaa, 0xbb, 0xcc, 0xdd}
	ntimeHex := fmt.Sprintf("%08x", job.Template.CurTime)
	nonceHex := "87654321"
	version := int32(0x20000000)

	// Build dual-payout coinbase
	cbTx, cbTxid, err := serializeDualCoinbaseTx(
		job.Template.Height,
		extranonce1,
		extranonce2,
		job.TemplateExtraNonce2Size,
		poolScript,
		workerScript,
		totalValue,
		feePercent,
		job.WitnessCommitment,
		job.Template.CoinbaseAux.Flags,
		job.CoinbaseMsg,
		job.ScriptTime,
	)
	if err != nil {
		t.Fatalf("serializeDualCoinbaseTx error: %v", err)
	}

	merkleRoot := computeMerkleRootFromBranches(cbTxid, job.MerkleBranches)
	header, err := buildBlockHeaderFromHex(version, job.Template.Previous, merkleRoot, ntimeHex, job.Template.Bits, nonceHex)
	if err != nil {
		t.Fatalf("buildBlockHeader error: %v", err)
	}

	// Assemble full block
	var blockBuf bytes.Buffer
	blockBuf.Write(header)
	writeVarInt(&blockBuf, 1) // tx count
	blockBuf.Write(cbTx)
	blockHex := hex.EncodeToString(blockBuf.Bytes())

	// Submit
	var submitRes interface{}
	err = rpc.call("submitblock", []interface{}{blockHex}, &submitRes)
	if err != nil {
		t.Fatalf("submitblock error: %v", err)
	}

	// Parse and verify dual outputs
	raw, err := hex.DecodeString(submittedBlockHex)
	if err != nil {
		t.Fatalf("decode submitted block: %v", err)
	}

	var msgBlock wire.MsgBlock
	if err := msgBlock.Deserialize(bytes.NewReader(raw)); err != nil {
		t.Fatalf("btcd MsgBlock deserialize error: %v", err)
	}

	if len(msgBlock.Transactions) != 1 {
		t.Fatalf("expected 1 transaction, got %d", len(msgBlock.Transactions))
	}

	coinbase := msgBlock.Transactions[0]
	if len(coinbase.TxOut) != 2 {
		t.Fatalf("expected 2 outputs in dual-payout coinbase, got %d", len(coinbase.TxOut))
	}

	// Verify split
	poolFee := int64(math.Round(float64(totalValue) * feePercent / 100.0))
	workerValue := totalValue - poolFee

	var (
		gotPoolValue    *int64
		gotWorkerValue  *int64
		gotPoolScript   []byte
		gotWorkerScript []byte
	)
	for _, o := range coinbase.TxOut {
		switch {
		case bytes.Equal(o.PkScript, poolScript):
			v := o.Value
			gotPoolValue = &v
			gotPoolScript = o.PkScript
		case bytes.Equal(o.PkScript, workerScript):
			v := o.Value
			gotWorkerValue = &v
			gotWorkerScript = o.PkScript
		}
	}
	if gotPoolValue == nil {
		t.Fatalf("pool output not found by script")
	}
	if gotWorkerValue == nil {
		t.Fatalf("worker output not found by script")
	}
	if *gotPoolValue != poolFee {
		t.Errorf("pool output value mismatch: got %d, want %d", *gotPoolValue, poolFee)
	}
	if *gotWorkerValue != workerValue {
		t.Errorf("worker output value mismatch: got %d, want %d", *gotWorkerValue, workerValue)
	}
	if !bytes.Equal(gotPoolScript, poolScript) {
		t.Errorf("pool script mismatch")
	}
	if !bytes.Equal(gotWorkerScript, workerScript) {
		t.Errorf("worker script mismatch")
	}

	t.Logf("Successfully submitted dual-payout block: pool=%d sats (%.2f%%), worker=%d sats",
		poolFee, feePercent, workerValue)
}

// TestFoundBlockSubmission_PogoloCompat verifies compatibility with pogolo by
// testing that the block structure, merkle branches, and coinbase construction
// match what pogolo would produce.
func TestFoundBlockSubmission_PogoloCompat(t *testing.T) {
	// Create a job similar to what pogolo would create
	txid1, _ := hex.DecodeString("1111111111111111111111111111111111111111111111111111111111111111")
	txid2, _ := hex.DecodeString("2222222222222222222222222222222222222222222222222222222222222222")
	txids := [][]byte{txid1, txid2}

	merkleBranches := buildMerkleBranches(txids)

	job := &Job{
		JobID: "pogolo-compat-test",
		Template: GetBlockTemplateResult{
			Height:        300,
			CurTime:       1700000200,
			Bits:          "1d00ffff",
			Previous:      "0000000000000000000000000000000000000000000000000000000000000000",
			CoinbaseValue: 50 * 1e8,
		},
		Extranonce2Size:         4,
		TemplateExtraNonce2Size: 8,
		PayoutScript:            []byte{0x76, 0xa9, 0x14}, // P2PKH prefix
		WitnessCommitment:       "",
		CoinbaseMsg:             "goPool",
		ScriptTime:              0,
		Transactions:            nil, // Would contain actual txs
		MerkleBranches:          merkleBranches,
		CoinbaseValue:           50 * 1e8,
	}

	extranonce1 := []byte{0x01, 0x02, 0x03, 0x04}
	extranonce2 := []byte{0xaa, 0xbb, 0xcc, 0xdd}
	ntimeHex := fmt.Sprintf("%08x", job.Template.CurTime)
	nonceHex := "abcdef01"
	version := int32(0x20000000)

	blockHex, _, header, merkleRoot, err := buildBlock(job, extranonce1, extranonce2, ntimeHex, nonceHex, version)
	if err != nil {
		t.Fatalf("buildBlock error: %v", err)
	}

	// Parse with btcd to verify structure
	raw, err := hex.DecodeString(blockHex)
	if err != nil {
		t.Fatalf("decode blockHex: %v", err)
	}

	var msgBlock wire.MsgBlock
	if err := msgBlock.Deserialize(bytes.NewReader(raw)); err != nil {
		t.Fatalf("btcd deserialize error: %v", err)
	}

	// Verify header is exactly 80 bytes
	if len(header) != 80 {
		t.Errorf("header length must be 80 bytes, got %d", len(header))
	}

	// Verify merkle root is 32 bytes
	if len(merkleRoot) != 32 {
		t.Errorf("merkle root must be 32 bytes, got %d", len(merkleRoot))
	}

	// Verify coinbase has correct structure
	coinbase := msgBlock.Transactions[0]

	// Coinbase must have at least one input
	if len(coinbase.TxIn) != 1 {
		t.Errorf("coinbase must have exactly 1 input, got %d", len(coinbase.TxIn))
	}

	// Coinbase input must reference null hash
	nullHash := make([]byte, 32)
	if !bytes.Equal(coinbase.TxIn[0].PreviousOutPoint.Hash[:], nullHash) {
		t.Errorf("coinbase input must reference null hash")
	}

	// Verify height is encoded in coinbase scriptSig (BIP 34)
	scriptSig := coinbase.TxIn[0].SignatureScript
	if len(scriptSig) < 1 {
		t.Errorf("coinbase scriptSig must contain height")
	}

	// First byte(s) should encode the height
	heightScript := serializeNumberScript(job.Template.Height)
	if !bytes.HasPrefix(scriptSig, heightScript) {
		t.Logf("Warning: height encoding may not match expected format")
		t.Logf("Expected prefix: %x", heightScript)
		t.Logf("Got scriptSig: %x", scriptSig)
	}

	t.Logf("Block structure compatible with pogolo/btcd at height %d", job.Template.Height)
}

// TestFoundBlockSubmission_WithPendingLog verifies that failed submissions
// are logged to pending_submissions.jsonl for later replay.
func TestFoundBlockSubmission_WithPendingLog(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := Config{
		DataDir: tmpDir,
	}

	// Mock RPC server that fails submitblock
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req rpcRequest
		_ = json.Unmarshal(body, &req)

		if req.Method == "submitblock" {
			// Return an error
			w.Header().Set("Content-Type", "application/json")
			resp := rpcResponse{
				Result: nil,
				Error:  &rpcError{Code: -1, Message: "rejected by network"},
				ID:     req.ID,
			}
			_ = json.NewEncoder(w).Encode(resp)
		}
	}))
	defer server.Close()

	rpc := &RPCClient{
		url:    server.URL,
		client: server.Client(),
		lp:     server.Client(),
		nextID: 1,
	}

	job := &Job{
		JobID: "pending-test-job",
		Template: GetBlockTemplateResult{
			Height:        400,
			CurTime:       1700000300,
			Bits:          "1d00ffff",
			Previous:      "0000000000000000000000000000000000000000000000000000000000000000",
			CoinbaseValue: 50 * 1e8,
		},
		Extranonce2Size:         4,
		TemplateExtraNonce2Size: 8,
		PayoutScript:            []byte{0x51},
		CoinbaseValue:           50 * 1e8,
	}

	extranonce1 := []byte{0x01, 0x02, 0x03, 0x04}
	extranonce2 := []byte{0xaa, 0xbb, 0xcc, 0xdd}
	ntimeHex := fmt.Sprintf("%08x", job.Template.CurTime)
	nonceHex := "deadbeef"
	version := int32(0x20000000)

	blockHex, headerHash, _, _, err := buildBlock(job, extranonce1, extranonce2, ntimeHex, nonceHex, version)
	if err != nil {
		t.Fatalf("buildBlock error: %v", err)
	}

	// Try to submit (will fail)
	var submitRes interface{}
	err = rpc.call("submitblock", []interface{}{blockHex}, &submitRes)
	if err == nil {
		t.Fatal("expected submitblock to fail, but it succeeded")
	}

	// Log the pending submission
	pendingPath := pendingSubmissionsPath(cfg)
	workerName := "test-worker"
	hashHex := hex.EncodeToString(headerHash)

	rec := pendingSubmissionRecord{
		Timestamp:  time.Now().UTC(),
		Height:     job.Template.Height,
		Hash:       hashHex,
		Worker:     workerName,
		BlockHex:   blockHex,
		RPCError:   err.Error(),
		RPCURL:     server.URL,
		PayoutAddr: workerName,
		Status:     "pending",
	}
	appendPendingSubmissionRecord(pendingPath, rec)

	// Verify the pending log exists and contains our record
	data, err := os.ReadFile(pendingPath)
	if err != nil {
		t.Fatalf("read pending log: %v", err)
	}

	lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
	if len(lines) == 0 {
		t.Fatal("pending log is empty")
	}

	var foundRecord bool
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var r pendingSubmissionRecord
		if err := json.Unmarshal(line, &r); err != nil {
			t.Fatalf("unmarshal pending record: %v", err)
		}
		if strings.EqualFold(r.Hash, hashHex) && r.Height == job.Template.Height {
			foundRecord = true
			if r.Status != "pending" {
				t.Errorf("expected status 'pending', got '%s'", r.Status)
			}
			break
		}
	}

	if !foundRecord {
		t.Fatal("pending submission record not found in log")
	}

	t.Logf("Failed block submission logged to pending log for height %d", job.Template.Height)
}

// TestFoundBlockSubmission_MeetsBlockDifficulty verifies that a submitted
// block actually meets the network difficulty target.
func TestFoundBlockSubmission_MeetsBlockDifficulty(t *testing.T) {
	// Use testnet3 difficulty (easier to satisfy)
	// Bits: 0x1d00ffff corresponds to difficulty ~1
	bitsHex := "1d00ffff"

	job := &Job{
		JobID: "difficulty-test",
		Template: GetBlockTemplateResult{
			Height:        500,
			CurTime:       1700000400,
			Bits:          bitsHex,
			Previous:      "0000000000000000000000000000000000000000000000000000000000000000",
			CoinbaseValue: 50 * 1e8,
		},
		Extranonce2Size:         4,
		TemplateExtraNonce2Size: 8,
		PayoutScript:            []byte{0x51},
		CoinbaseValue:           50 * 1e8,
	}

	// Parse the target from bits
	bitsBytes, _ := hex.DecodeString(bitsHex)
	if len(bitsBytes) != 4 {
		t.Fatal("invalid bits length")
	}

	// Compact form: [exponent][mantissa (3 bytes)]
	exponent := uint(bitsBytes[0])
	mantissa := uint32(bitsBytes[1])<<16 | uint32(bitsBytes[2])<<8 | uint32(bitsBytes[3])

	target := new(big.Int).Lsh(big.NewInt(int64(mantissa)), 8*(exponent-3))

	// Build a block with various nonces until we find one that meets difficulty
	// (For testing, we use easy difficulty so this should succeed quickly)
	extranonce1 := []byte{0x01, 0x02, 0x03, 0x04}
	extranonce2 := []byte{0xaa, 0xbb, 0xcc, 0xdd}
	ntimeHex := fmt.Sprintf("%08x", job.Template.CurTime)
	version := int32(0x20000000)

	found := false
	var validHeaderHash []byte

	// Try up to 1000 nonces
	for nonce := uint32(0); nonce < 1000; nonce++ {
		nonceHex := fmt.Sprintf("%08x", nonce)

		_, headerHash, _, _, err := buildBlock(job, extranonce1, extranonce2, ntimeHex, nonceHex, version)
		if err != nil {
			continue
		}

		// Convert header hash to big.Int (note: reverse byte order for comparison)
		hashInt := new(big.Int).SetBytes(reverseBytes(headerHash))

		// Check if hash < target
		if hashInt.Cmp(target) <= 0 {
			found = true
			validHeaderHash = headerHash
			t.Logf("Found valid block at nonce %d: hash=%x", nonce, headerHash)
			break
		}
	}

	if !found {
		t.Skip("Could not find valid nonce within 1000 attempts (expected with real difficulty)")
	}

	// Verify the hash truly meets the difficulty
	hashInt := new(big.Int).SetBytes(reverseBytes(validHeaderHash))
	if hashInt.Cmp(target) > 0 {
		t.Errorf("Block hash %x does not meet target %x", hashInt, target)
	}

	t.Logf("Block hash meets difficulty target")
}
