package main

import "testing"

var sampleStratumRequest = []byte(`{"id": 509, "method": "mining.ping", "params": []}`)
var sampleSubmitRequest = []byte(`{"id": 1, "method": "mining.submit", "params": ["worker1","job1","00000000","5f5e1000","00000001"]}`)
var sampleSubscribeRequest = []byte(`{"id": 2, "method": "mining.subscribe", "params": ["cgminer/4.11.1"]}`)
var sampleAuthorizeRequest = []byte(`{"id": 3, "method": "mining.authorize", "params": ["wallet.worker","x,d=1024"]}`)

func BenchmarkStratumDecodeFastJSON(b *testing.B) {
	var req StratumRequest
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		req = StratumRequest{}
		if err := fastJSONUnmarshal(sampleStratumRequest, &req); err != nil {
			b.Fatalf("fast json unmarshal: %v", err)
		}
	}
}

func BenchmarkStratumDecodeFastJSON_MiningSubmit(b *testing.B) {
	var req StratumRequest
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		req = StratumRequest{}
		if err := fastJSONUnmarshal(sampleSubmitRequest, &req); err != nil {
			b.Fatalf("fast json unmarshal: %v", err)
		}
	}
}

func BenchmarkStratumDecodeFastJSON_MiningSubscribe(b *testing.B) {
	var req StratumRequest
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		req = StratumRequest{}
		if err := fastJSONUnmarshal(sampleSubscribeRequest, &req); err != nil {
			b.Fatalf("fast json unmarshal: %v", err)
		}
	}
}

func BenchmarkStratumDecodeFastJSON_MiningAuthorize(b *testing.B) {
	var req StratumRequest
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		req = StratumRequest{}
		if err := fastJSONUnmarshal(sampleAuthorizeRequest, &req); err != nil {
			b.Fatalf("fast json unmarshal: %v", err)
		}
	}
}
