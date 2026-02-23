package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func FuzzStratumFastEncodeSimpleResponsesParity(f *testing.F) {
	f.Add(int64(1), "abc", true)
	f.Add(int64(0), "", false)

	f.Fuzz(func(t *testing.T, idNum int64, idStr string, useStringID bool) {
		var id any = idNum
		if useStringID {
			id = idStr
		}

		cases := []func(mc *MinerConn){
			func(mc *MinerConn) { mc.writeTrueResponse(id) },
			func(mc *MinerConn) { mc.writePongResponse(id) },
			func(mc *MinerConn) { mc.writeEmptySliceResponse(id) },
		}

		for _, fn := range cases {
			fast, normal := captureFastVsNormal(t, fn)
			if !reflect.DeepEqual(fast, normal) {
				t.Fatalf("fast encode mismatch\nfast=%#v\nnormal=%#v", fast, normal)
			}
		}
	})
}

func FuzzStratumFastEncodeSubscribeParity(f *testing.F) {
	f.Add(int64(1), "sid", "01020304", 4, false, false)
	f.Add(int64(0), "", "00", 8, true, true)

	f.Fuzz(func(t *testing.T, idNum int64, subID, extranonce1 string, extranonce2n int, ckpool, nullID bool) {
		if extranonce2n < 0 {
			extranonce2n = -extranonce2n
		}
		if extranonce2n > 64 {
			extranonce2n = 64
		}
		if strings.TrimSpace(extranonce1) == "" {
			extranonce1 = "00"
		}
		if len(extranonce1)%2 != 0 {
			extranonce1 += "0"
		}

		var id any = idNum
		if nullID {
			id = nil
		}

		fastConn := &writeRecorderConn{}
		fastMC := &MinerConn{conn: fastConn, cfg: Config{StratumFastEncodeEnabled: true, CKPoolEmulate: ckpool}}
		fastMC.writeSubscribeResponse(id, extranonce1, extranonce2n, subID)

		normalConn := &writeRecorderConn{}
		normalMC := &MinerConn{conn: normalConn, cfg: Config{StratumFastEncodeEnabled: false, CKPoolEmulate: ckpool}}
		normalMC.writeSubscribeResponse(id, extranonce1, extranonce2n, subID)

		fast := decodeJSONLine(t, fastConn.String())
		normal := decodeJSONLine(t, normalConn.String())
		if !reflect.DeepEqual(fast, normal) {
			t.Fatalf("fast subscribe encode mismatch\nfast=%#v\nnormal=%#v", fast, normal)
		}
	})
}

func FuzzStratumSniffMethodAndIDParity(f *testing.F) {
	f.Add(int64(1), "mining.authorize")
	f.Add(int64(7), "mining.submit")
	f.Add(int64(0), "mining.unknown_ext")

	f.Fuzz(func(t *testing.T, id int64, method string) {
		req := map[string]any{
			"id":     id,
			"meta":   map[string]any{"id": "nested"},
			"method": method,
			"params": []any{},
		}
		line, err := json.Marshal(req)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}

		tag, idRaw, ok := sniffStratumMethodIDTagRawID(line)
		if !ok {
			t.Fatalf("expected sniff ok for valid request: %q", string(line))
		}

		idVal, _, ok := parseJSONValue(idRaw, 0)
		if !ok {
			t.Fatalf("parse raw id failed: %q", string(idRaw))
		}
		switch got := idVal.(type) {
		case int:
			if int64(got) != id {
				t.Fatalf("sniffed id mismatch: got=%#v want=%d", idVal, id)
			}
		case int64:
			if got != id {
				t.Fatalf("sniffed id mismatch: got=%#v want=%d", idVal, id)
			}
		case float64:
			if int64(got) != id {
				t.Fatalf("sniffed id mismatch: got=%#v want=%d", idVal, id)
			}
		default:
			t.Fatalf("unexpected sniffed id type %T (%#v)", idVal, idVal)
		}

		switch method {
		case "mining.ping":
			if tag != stratumMethodMiningPing {
				t.Fatalf("expected mining.ping tag, got %v", tag)
			}
		case "mining.authorize", "mining.auth":
			if tag != stratumMethodMiningAuthorize {
				t.Fatalf("expected authorize tag, got %v", tag)
			}
		case "mining.subscribe":
			if tag != stratumMethodMiningSubscribe {
				t.Fatalf("expected subscribe tag, got %v", tag)
			}
		case "mining.submit":
			if tag != stratumMethodMiningSubmit {
				t.Fatalf("expected submit tag, got %v", tag)
			}
		default:
			if tag != stratumMethodUnknown {
				t.Fatalf("expected unknown tag, got %v", tag)
			}
		}
	})
}

func FuzzStratumSniffStringParamsParity(f *testing.F) {
	f.Add("worker", "pass")
	f.Add("worker1", `p"w`)

	f.Fuzz(func(t *testing.T, a, b string) {
		req := map[string]any{
			"id":     1,
			"method": "mining.authorize",
			"meta":   map[string]any{"params": []any{"bad"}},
			"params": []any{a, b},
		}
		line, err := json.Marshal(req)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}

		got, ok := sniffStratumStringParams(line, 2)
		if !ok {
			t.Fatalf("expected sniff ok for authorize params: %q", string(line))
		}
		if len(got) != 2 || got[0] != a || got[1] != b {
			t.Fatalf("sniffed params mismatch: got=%#v want=[%q %q]", got, a, b)
		}
	})
}

func FuzzStratumSniffSubmitParamsParityOrFallback(f *testing.F) {
	f.Add("worker", "1", "00000001", "5f5e1000", "00000001", "", false)
	f.Add("worker1", "job", "00000001", "5f5e1000", "00000001", "20000000", true)

	f.Fuzz(func(t *testing.T, worker, jobID, en2, ntime, nonce, version string, includeVersion bool) {
		params := []any{worker, jobID, en2, ntime, nonce}
		if includeVersion {
			params = append(params, version)
		}

		req := map[string]any{
			"id":     1,
			"method": "mining.submit",
			"meta":   map[string]any{"params": []any{"bad"}},
			"params": params,
		}
		line, err := json.Marshal(req)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}

		gotWorker, gotJobID, gotEN2, gotNTime, gotNonce, gotVer, haveVer, ok := sniffStratumSubmitParamsBytes(line)

		// Submit fast-sniff intentionally falls back on any escaped JSON string.
		needsEscape := anyJSONStringNeedsEscape(worker, jobID, en2, ntime, nonce)
		if includeVersion {
			needsEscape = needsEscape || anyJSONStringNeedsEscape(version)
		}

		if needsEscape {
			if ok {
				t.Fatalf("expected submit sniff fallback on escaped strings: %q", string(line))
			}
			return
		}

		if !ok {
			t.Fatalf("expected submit sniff ok: %q", string(line))
		}
		if string(gotWorker) != worker || string(gotJobID) != jobID || string(gotEN2) != en2 || string(gotNTime) != ntime || string(gotNonce) != nonce {
			t.Fatalf("submit sniff mismatch worker/job/en2/ntime/nonce")
		}
		if includeVersion {
			if !haveVer || string(gotVer) != version {
				t.Fatalf("submit sniff version mismatch have=%v got=%q want=%q", haveVer, string(gotVer), version)
			}
		} else if haveVer || gotVer != nil {
			t.Fatalf("unexpected version output have=%v ver=%q", haveVer, string(gotVer))
		}
	})
}

func anyJSONStringNeedsEscape(vals ...string) bool {
	for _, s := range vals {
		b, _ := json.Marshal(s)
		if strings.ContainsRune(string(b), '\\') {
			// JSON string always has surrounding quotes; a backslash means escape.
			return true
		}
	}
	return false
}
