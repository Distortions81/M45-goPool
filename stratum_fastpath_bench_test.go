package main

import (
	"net"
	"testing"
	"time"
)

type benchDiscardConn struct{}

func (benchDiscardConn) Read([]byte) (int, error)         { return 0, nil }
func (benchDiscardConn) Write(b []byte) (int, error)      { return len(b), nil }
func (benchDiscardConn) Close() error                     { return nil }
func (benchDiscardConn) LocalAddr() net.Addr              { return &net.IPAddr{} }
func (benchDiscardConn) RemoteAddr() net.Addr             { return &net.IPAddr{} }
func (benchDiscardConn) SetDeadline(time.Time) error      { return nil }
func (benchDiscardConn) SetReadDeadline(time.Time) error  { return nil }
func (benchDiscardConn) SetWriteDeadline(time.Time) error { return nil }

func benchmarkEncodeMinerConn(fastEncode bool, ckpool bool) *MinerConn {
	return &MinerConn{
		id:   "bench-encode",
		conn: benchDiscardConn{},
		cfg: Config{
			StratumFastEncodeEnabled: fastEncode,
			CKPoolEmulate:            ckpool,
		},
	}
}

func BenchmarkStratumEncodeTrueResponse_Normal(b *testing.B) {
	mc := benchmarkEncodeMinerConn(false, true)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		mc.writeTrueResponse(1)
	}
}

func BenchmarkStratumEncodeTrueResponse_Fast(b *testing.B) {
	mc := benchmarkEncodeMinerConn(true, true)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		mc.writeTrueResponse(1)
	}
}

func BenchmarkStratumEncodePongResponse_Normal(b *testing.B) {
	mc := benchmarkEncodeMinerConn(false, true)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		mc.writePongResponse(7)
	}
}

func BenchmarkStratumEncodePongResponse_Fast(b *testing.B) {
	mc := benchmarkEncodeMinerConn(true, true)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		mc.writePongResponse(7)
	}
}

func BenchmarkStratumEncodeSubscribeResponse_CKPool_Normal(b *testing.B) {
	mc := benchmarkEncodeMinerConn(false, true)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		mc.writeSubscribeResponse(2, "01020304", 4, "sid")
	}
}

func BenchmarkStratumEncodeSubscribeResponse_CKPool_Fast(b *testing.B) {
	mc := benchmarkEncodeMinerConn(true, true)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		mc.writeSubscribeResponse(2, "01020304", 4, "sid")
	}
}

func BenchmarkStratumEncodeSubscribeResponse_Expanded_Normal(b *testing.B) {
	mc := benchmarkEncodeMinerConn(false, false)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		mc.writeSubscribeResponse(2, "01020304", 4, "sid")
	}
}

func BenchmarkStratumEncodeSubscribeResponse_Expanded_Fast(b *testing.B) {
	mc := benchmarkEncodeMinerConn(true, false)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		mc.writeSubscribeResponse(2, "01020304", 4, "sid")
	}
}
