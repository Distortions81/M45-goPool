package main

import (
	"fmt"
	"strconv"
	"testing"
)

const cannedSuffix = ",\"result\":true,\"error\":null}\n"

func BenchmarkFastJSONMarshalSimple(b *testing.B) {
	resp := StratumResponse{
		ID:     42,
		Result: true,
		Error:  nil,
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp.ID = i
		bts, err := fastJSONMarshal(resp)
		if err != nil {
			b.Fatal(err)
		}
		_ = append(bts, '\n')
	}
}

func BenchmarkFmtSprintfSimple(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		id := i
		_ = []byte(fmt.Sprintf("{\"id\":%d%s", id, cannedSuffix))
	}
}

func BenchmarkManualAppendSimple(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		id := int64(i)
		_ = buildManualResponse(id)
	}
}

func buildManualResponse(id int64) []byte {
	buf := make([]byte, 0, 64)
	buf = append(buf, `{"id":`...)
	buf = strconv.AppendInt(buf, id, 10)
	buf = append(buf, cannedSuffix...)
	return buf
}
