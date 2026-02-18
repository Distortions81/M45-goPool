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
		_ = fmt.Appendf(nil, "{\"id\":%d%s", id, cannedSuffix)
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

func BenchmarkFastJSONMarshalSubscribe(b *testing.B) {
	ex1 := "aabbccdd"
	en2Size := 4
	result := []any{
		[][]any{
			{"mining.set_difficulty", "1"},
			{"mining.notify", "1"},
		},
		ex1,
		en2Size,
	}
	resp := StratumResponse{
		ID:     42,
		Result: result,
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

func BenchmarkManualAppendSubscribe(b *testing.B) {
	ex1 := "aabbccdd"
	en2Size := 4
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := buildSubscribeResponseBytes(int64(i), ex1, en2Size); err != nil {
			b.Fatal(err)
		}
	}
}

func buildManualResponse(id int64) []byte {
	buf := make([]byte, 0, 64)
	buf = append(buf, `{"id":`...)
	buf = strconv.AppendInt(buf, id, 10)
	buf = append(buf, cannedSuffix...)
	return buf
}
