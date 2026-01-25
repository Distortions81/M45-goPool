package main

import "testing"

var sampleStratumRequest = []byte(`{"id": 509, "method": "mining.ping", "params": []}`)

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
