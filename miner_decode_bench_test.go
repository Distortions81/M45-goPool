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

func BenchmarkStratumDecodeManual(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if method, id, ok := sniffStratumMethodIDTagRawID(sampleStratumRequest); !ok || method == stratumMethodUnknown {
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
		method, _, ok := sniffStratumMethodIDTagRawID(sampleSubmitRequest)
		if !ok || method != stratumMethodMiningSubmit {
			b.Fatalf("manual decode method failed")
		}
		worker, jobID, en2, ntime, nonce, ver, haveVer, ok := sniffStratumSubmitParamsBytes(sampleSubmitRequest)
		if !ok || haveVer || len(worker) == 0 || len(jobID) == 0 {
			b.Fatalf("manual decode params failed")
		}
		if _, err := parseUint32BEHexBytes(ntime); err != nil {
			b.Fatalf("parse ntime: %v", err)
		}
		if _, err := parseUint32BEHexBytes(nonce); err != nil {
			b.Fatalf("parse nonce: %v", err)
		}
		if haveVer {
			if _, err := parseUint32BEHexBytes(ver); err != nil {
				b.Fatalf("parse version: %v", err)
			}
		}
		if _, _, _, err := decodeExtranonce2HexBytes(en2, true, 4); err != nil {
			b.Fatalf("decode extranonce2: %v", err)
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

func BenchmarkStratumDecodeManual_MiningSubscribe(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		method, _, ok := sniffStratumMethodIDTagRawID(sampleSubscribeRequest)
		if !ok || method != stratumMethodMiningSubscribe {
			b.Fatalf("manual decode method failed")
		}
		params, ok := sniffStratumStringParams(sampleSubscribeRequest, 1)
		if !ok || len(params) != 1 {
			b.Fatalf("manual decode params failed")
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

func BenchmarkStratumDecodeManual_MiningAuthorize(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		method, _, ok := sniffStratumMethodIDTagRawID(sampleAuthorizeRequest)
		if !ok || method != stratumMethodMiningAuthorize {
			b.Fatalf("manual decode method failed")
		}
		params, ok := sniffStratumStringParams(sampleAuthorizeRequest, 2)
		if !ok || len(params) != 2 {
			b.Fatalf("manual decode params failed")
		}
	}
}
