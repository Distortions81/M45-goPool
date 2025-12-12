package main

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/database"
	_ "github.com/btcsuite/btcd/database/ffldb"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
)

// TestBuildBlockValidAgainstBtcd constructs a simple block using goPool's
// buildBlock and verifies that btcd's BlockChain accepts it via ProcessBlock
// on a regtest chain with a fresh ffldb database.
func TestBuildBlockValidAgainstBtcd(t *testing.T) {
	// Use a temporary ffldb database for the regtest chain.
	dbPath := t.TempDir()
	db, err := database.Create("ffldb", dbPath, chaincfg.RegressionNetParams.Net)
	if err != nil {
		t.Fatalf("database.Open error: %v", err)
	}
	defer db.Close()

	// Initialize Blockchain with regtest params. The regtest genesis block is
	// inserted into the chain state as part of blockchain.New when using a
	// fresh DB, so we don't need (or want) to process it manually here.
	chain, err := blockchain.New(&blockchain.Config{
		DB:               db,
		UtxoCacheMaxSize: 1 << 20, // small but sufficient for this test
		ChainParams:      &chaincfg.RegressionNetParams,
		TimeSource:       blockchain.NewMedianTime(),
		SigCache:         txscript.NewSigCache(100),
	})
	if err != nil {
		t.Fatalf("blockchain.New error: %v", err)
	}
	genesisBlock := btcutil.NewBlock(chaincfg.RegressionNetParams.GenesisBlock)

	// Build a minimal block on top of regtest genesis using goPool's helper.
	job := &Job{
		JobID: "btcd-validity-test",
		Template: GetBlockTemplateResult{
			Height:        1,
			CurTime:       genesisBlock.MsgBlock().Header.Timestamp.Unix() + 1,
			Mintime:       0,
			Bits:          "207fffff", // regtest default very low difficulty
			Previous:      genesisBlock.Hash().String(),
			CoinbaseValue: 50 * 1e8,
		},
		Extranonce2Size:         4,
		TemplateExtraNonce2Size: 8,
		PayoutScript:            []byte{0x51}, // OP_TRUE
		WitnessCommitment:       "",
		CoinbaseMsg:             "goPool-btcd-validity",
		ScriptTime:              0,
		Transactions:            nil,
		MerkleBranches:          nil,
		CoinbaseValue:           50 * 1e8,
	}

	ex1 := []byte{0x01, 0x02, 0x03, 0x04}
	ex2 := []byte{0xaa, 0xbb, 0xcc, 0xdd}
	ntimeHex := hex.EncodeToString(uint32ToBigEndian(uint32(job.Template.CurTime)))
	nonceHex := "00000001"
	version := int32(1)

	blockHex, _, _, _, err := buildBlock(job, ex1, ex2, ntimeHex, nonceHex, version)
	if err != nil {
		t.Fatalf("buildBlock error: %v", err)
	}

	raw, err := hex.DecodeString(blockHex)
	if err != nil {
		t.Fatalf("decode blockHex: %v", err)
	}

	var msgBlock wire.MsgBlock
	if err := msgBlock.Deserialize(bytes.NewReader(raw)); err != nil {
		t.Fatalf("btcd MsgBlock deserialize error: %v", err)
	}

	block := btcutil.NewBlock(&msgBlock)

	// Use BFNoPoWCheck here since the synthetic header is extremely unlikely
	// to satisfy even regtest proof-of-work. We still want btcd to fully
	// validate structure, merkle roots, coinbase, etc.
	isMainChain, isOrphan, err := chain.ProcessBlock(block, blockchain.BFNoPoWCheck)
	if err != nil {
		t.Fatalf("ProcessBlock error: %v", err)
	}
	if !isMainChain || isOrphan {
		t.Fatalf("expected block to extend main chain (isMainChain=%v, isOrphan=%v)", isMainChain, isOrphan)
	}
}

// uint32ToBigEndian encodes v as big-endian bytes.
func uint32ToBigEndian(v uint32) []byte {
	return []byte{
		byte(v >> 24),
		byte(v >> 16),
		byte(v >> 8),
		byte(v),
	}
}
