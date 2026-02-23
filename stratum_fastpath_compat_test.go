package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

func decodeJSONLine(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("json unmarshal failed: %v; input=%q", err, s)
	}
	return v
}

func captureFastVsNormal(t *testing.T, fn func(mc *MinerConn)) (fast any, normal any) {
	t.Helper()
	fastConn := &writeRecorderConn{}
	fastMC := &MinerConn{conn: fastConn, cfg: Config{StratumFastEncodeEnabled: true}}
	fn(fastMC)

	normalConn := &writeRecorderConn{}
	normalMC := &MinerConn{conn: normalConn, cfg: Config{StratumFastEncodeEnabled: false}}
	fn(normalMC)

	return decodeJSONLine(t, fastConn.String()), decodeJSONLine(t, normalConn.String())
}

func TestFastEncodeSimpleResponsesMatchNormal(t *testing.T) {
	cases := []struct {
		name string
		fn   func(mc *MinerConn)
	}{
		{name: "true_int_id", fn: func(mc *MinerConn) { mc.writeTrueResponse(1) }},
		{name: "true_string_id", fn: func(mc *MinerConn) { mc.writeTrueResponse("abc") }},
		{name: "true_null_id", fn: func(mc *MinerConn) { mc.writeTrueResponse(nil) }},
		{name: "pong", fn: func(mc *MinerConn) { mc.writePongResponse(7) }},
		{name: "empty_slice", fn: func(mc *MinerConn) { mc.writeEmptySliceResponse(9) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fast, normal := captureFastVsNormal(t, tc.fn)
			if !reflect.DeepEqual(fast, normal) {
				t.Fatalf("fast response != normal response\nfast=%#v\nnormal=%#v", fast, normal)
			}
		})
	}
}

func TestFastEncodeSubscribeResponseMatchesNormal(t *testing.T) {
	cases := []struct {
		name         string
		ckpool       bool
		subID        string
		extranonce1  string
		extranonce2n int
		id           any
	}{
		{name: "default", ckpool: false, subID: "sid", extranonce1: "abcdef01", extranonce2n: 4, id: 1},
		{name: "ckpool", ckpool: true, subID: "1", extranonce1: "00", extranonce2n: 8, id: "sub-1"},
		{name: "empty_subid_defaults", ckpool: false, subID: "", extranonce1: "00", extranonce2n: 4, id: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fastConn := &writeRecorderConn{}
			fastMC := &MinerConn{conn: fastConn, cfg: Config{StratumFastEncodeEnabled: true, CKPoolEmulate: tc.ckpool}}
			fastMC.writeSubscribeResponse(tc.id, tc.extranonce1, tc.extranonce2n, tc.subID)

			normalConn := &writeRecorderConn{}
			normalMC := &MinerConn{conn: normalConn, cfg: Config{StratumFastEncodeEnabled: false, CKPoolEmulate: tc.ckpool}}
			normalMC.writeSubscribeResponse(tc.id, tc.extranonce1, tc.extranonce2n, tc.subID)

			fast := decodeJSONLine(t, fastConn.String())
			normal := decodeJSONLine(t, normalConn.String())
			if !reflect.DeepEqual(fast, normal) {
				t.Fatalf("fast subscribe response != normal\nfast=%#v\nnormal=%#v", fast, normal)
			}
		})
	}
}

func TestFastEncodeRawIDResponsesMatchNormal(t *testing.T) {
	rawIDs := [][]byte{
		[]byte(`1`),
		[]byte(`"abc"`),
		[]byte(`null`),
	}
	for _, rawID := range rawIDs {
		t.Run(string(rawID), func(t *testing.T) {
			fastConn := &writeRecorderConn{}
			fastMC := &MinerConn{conn: fastConn, cfg: Config{StratumFastEncodeEnabled: true}}
			fastMC.writeTrueResponseRawID(rawID)

			normalConn := &writeRecorderConn{}
			normalMC := &MinerConn{conn: normalConn, cfg: Config{StratumFastEncodeEnabled: false}}
			normalMC.writeTrueResponseRawID(rawID)

			fast := decodeJSONLine(t, fastConn.String())
			normal := decodeJSONLine(t, normalConn.String())
			if !reflect.DeepEqual(fast, normal) {
				t.Fatalf("fast raw-id response != normal\nfast=%#v\nnormal=%#v", fast, normal)
			}
		})
	}
}

func TestSniffStringParamsSupportsEscapes(t *testing.T) {
	line := []byte(`{"id":1,"method":"mining.authorize","params":["worker\u0031","p\"w"]}`)
	params, ok := sniffStratumStringParams(line, 2)
	if !ok {
		t.Fatalf("expected fast string param sniff to succeed")
	}
	if len(params) != 2 || params[0] != "worker1" || params[1] != `p"w` {
		t.Fatalf("unexpected params: %#v", params)
	}
}

func TestSniffSubmitParamsFallsBackOnEscapes(t *testing.T) {
	line := []byte(`{"id":1,"method":"mining.submit","params":["worker\u0031","1","00000001","5f5e1000","00000001"]}`)
	_, _, _, _, _, _, _, ok := sniffStratumSubmitParamsBytes(line)
	if ok {
		t.Fatalf("expected fast submit sniff to fall back when escapes are present")
	}
}

func TestSniffMethodKeepsIDForUnknownMethod(t *testing.T) {
	line := []byte(`{"id":"abc-1","method":"mining.unknown_ext","params":[]}`)
	method, idRaw, ok := sniffStratumMethodIDTagRawID(line)
	if !ok {
		t.Fatalf("expected sniff to return id even for unknown method")
	}
	if method != stratumMethodUnknown {
		t.Fatalf("expected unknown method tag, got %v", method)
	}
	if string(idRaw) != `"abc-1"` {
		t.Fatalf("unexpected raw id: %q", idRaw)
	}
}

func TestSniffMethodIDUsesTopLevelIDWhenFieldsReordered(t *testing.T) {
	line := []byte(`{"params":["id"],"method":"mining.ping","id":1}`)
	method, idRaw, ok := sniffStratumMethodIDTagRawID(line)
	if !ok {
		t.Fatalf("expected sniff ok")
	}
	if method != stratumMethodMiningPing {
		t.Fatalf("expected mining.ping, got %v", method)
	}
	if string(idRaw) != "1" {
		t.Fatalf("expected top-level id raw=1, got %q", idRaw)
	}
}

func TestSniffStringParamsUsesTopLevelParamsKey(t *testing.T) {
	line := []byte(`{"id":1,"meta":{"params":["bad"]},"method":"mining.authorize","params":["worker","pass"]}`)
	params, ok := sniffStratumStringParams(line, 2)
	if !ok {
		t.Fatalf("expected sniff ok")
	}
	if len(params) != 2 || params[0] != "worker" || params[1] != "pass" {
		t.Fatalf("unexpected params: %#v", params)
	}
}

func TestSniffSubmitParamsUsesTopLevelParamsKey(t *testing.T) {
	line := []byte(`{"id":1,"meta":{"params":["bad"]},"method":"mining.submit","params":["worker","1","00000001","5f5e1000","00000001"]}`)
	worker, jobID, en2, ntime, nonce, ver, haveVer, ok := sniffStratumSubmitParamsBytes(line)
	if !ok {
		t.Fatalf("expected submit sniff ok")
	}
	if haveVer || ver != nil {
		t.Fatalf("unexpected version field: have=%v ver=%q", haveVer, ver)
	}
	if string(worker) != "worker" || string(jobID) != "1" || string(en2) != "00000001" || string(ntime) != "5f5e1000" || string(nonce) != "00000001" {
		t.Fatalf("unexpected submit fields: worker=%q job=%q en2=%q ntime=%q nonce=%q", worker, jobID, en2, ntime, nonce)
	}
}

func TestSniffSubmitParamsWithVersionField(t *testing.T) {
	line := []byte(`{"id":1,"method":"mining.submit","params":["worker","1","00000001","5f5e1000","00000001","20000000"]}`)
	worker, jobID, en2, ntime, nonce, ver, haveVer, ok := sniffStratumSubmitParamsBytes(line)
	if !ok {
		t.Fatalf("expected submit sniff ok with version field")
	}
	if !haveVer || ver == nil {
		t.Fatalf("expected version field to be present")
	}
	if string(worker) != "worker" || string(jobID) != "1" || string(en2) != "00000001" || string(ntime) != "5f5e1000" || string(nonce) != "00000001" || string(ver) != "20000000" {
		t.Fatalf("unexpected submit fields/version: worker=%q job=%q en2=%q ntime=%q nonce=%q ver=%q", worker, jobID, en2, ntime, nonce, ver)
	}
}
