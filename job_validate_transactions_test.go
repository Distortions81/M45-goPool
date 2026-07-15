package main

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
)

func TestValidateTransactionsUsesBtcdInternalHashOrder(t *testing.T) {
	var prevHash chainhash.Hash
	prevHash[0] = 0x42
	msgTx := wire.NewMsgTx(2)
	msgTx.AddTxIn(wire.NewTxIn(wire.NewOutPoint(&prevHash, 7), nil, wire.TxWitness{
		[]byte{0x30, 0x01},
		[]byte{0x02, 0x03, 0x04},
	}))
	msgTx.AddTxOut(wire.NewTxOut(12345, []byte{0x51}))

	var serialized bytes.Buffer
	if err := msgTx.Serialize(&serialized); err != nil {
		t.Fatalf("serialize transaction: %v", err)
	}
	btcdTx := btcutil.NewTx(msgTx)
	txid := btcdTx.Hash()
	wtxid := msgTx.WitnessHash()
	gbtTx := GBTTransaction{
		Data: hex.EncodeToString(serialized.Bytes()),
		Txid: txid.String(),
		Hash: wtxid.String(),
	}

	transactions, err := validateTransactions([]GBTTransaction{gbtTx})
	if err != nil {
		t.Fatalf("validate transaction: %v", err)
	}
	if len(transactions) != 1 {
		t.Fatalf("validated transaction count=%d, want 1", len(transactions))
	}

	gotHash := transactions[0].Hash()
	if *gotHash != *txid {
		t.Fatalf("parsed txid=%s, want %s", gotHash, txid)
	}
	branches := buildMerkleBranches(transactions)
	if len(branches) != 1 {
		t.Fatalf("branch count=%d, want 1", len(branches))
	}
	wantInternal := hex.EncodeToString(txid[:])
	if branches[0] != wantInternal {
		t.Fatalf("branch=%s, want internal-order hash %s", branches[0], wantInternal)
	}
	if branches[0] == gbtTx.Txid {
		t.Fatalf("branch incorrectly matches display-order txid %s", gbtTx.Txid)
	}

	bad := gbtTx
	bad.Txid = wantInternal
	if _, err := validateTransactions([]GBTTransaction{bad}); err == nil {
		t.Fatal("validateTransactions accepted an internal-order hash in the display-order GBT txid field")
	}
}
