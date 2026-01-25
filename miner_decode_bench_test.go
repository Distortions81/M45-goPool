package main

import "testing"

var sampleStratumRequest = []byte(`{"id": 509, "method": "mining.ping", "params": []}`)
var sampleSubmitRequest = []byte(`{"id": 1, "method": "mining.submit", "params": ["worker1","job1","00000000","5f5e1000","00000001"]}`)

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

func BenchmarkStratumDecodeManual(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if method, id, ok := sniffStratumMethodID(sampleStratumRequest); !ok || method == "" {
			b.Fatalf("manual decode failed")
		} else {
			_ = id
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

func BenchmarkStratumDecodeManual_MiningSubmit(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		method, _, ok := sniffStratumMethodID(sampleSubmitRequest)
		if !ok || method != "mining.submit" {
			b.Fatalf("manual decode method failed")
		}
		params, ok := sniffStratumStringParams(sampleSubmitRequest, 6)
		if !ok || len(params) != 5 {
			b.Fatalf("manual decode params failed")
		}
	}
}
