package main

import (
	"strings"
	"testing"
)

func TestHandleGetTransactions_ReturnsTxidsForJobID(t *testing.T) {
	conn := &writeRecorderConn{}
	job := &Job{
		JobID: "job1",
		Transactions: []GBTTransaction{
			{Txid: "aa"},
			{Txid: "bb"},
		},
	}

	mc := &MinerConn{
		id:         "get-txs",
		conn:       conn,
		activeJobs: map[string]*Job{"job1": job},
		lastJob:    job,
	}

	req := &StratumRequest{ID: 1, Method: "mining.get_transactions", Params: []any{"job1"}}
	mc.handleGetTransactions(req)

	out := conn.String()
	if !strings.Contains(out, "\"result\":[\"aa\",\"bb\"]") {
		t.Fatalf("expected txids in result, got: %q", out)
	}
}

func TestHandleGetTransactions_EmptyParamsUsesLastJob(t *testing.T) {
	conn := &writeRecorderConn{}
	job := &Job{
		JobID: "jobLast",
		Transactions: []GBTTransaction{
			{Txid: "cc"},
		},
	}

	mc := &MinerConn{
		id:         "get-txs-last",
		conn:       conn,
		activeJobs: map[string]*Job{"jobLast": job},
		lastJob:    job,
	}

	req := &StratumRequest{ID: 1, Method: "mining.get_transactions", Params: nil}
	mc.handleGetTransactions(req)

	out := conn.String()
	if !strings.Contains(out, "\"result\":[\"cc\"]") {
		t.Fatalf("expected txids from last job, got: %q", out)
	}
}
