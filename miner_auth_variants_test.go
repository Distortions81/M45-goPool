package main

import (
	"strings"
	"testing"
)

func TestAuthorizePasswordMatchesVariants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		pass     string
		expected string
		ok       bool
	}{
		{pass: "x", expected: "x", ok: true},
		{pass: "x,d=1024", expected: "x", ok: true},
		{pass: "d=1024,x", expected: "x", ok: true},
		{pass: "pass=x,d=1024", expected: "x", ok: true},
		{pass: "password=x;d=1024", expected: "x", ok: true},
		{pass: "d=1024", expected: "x", ok: false},
		{pass: "x", expected: "secret", ok: false},
	}

	for _, tc := range tests {
		got := authorizePasswordMatches(tc.pass, tc.expected)
		if got != tc.ok {
			t.Fatalf("authorizePasswordMatches(%q, %q) = %v, want %v", tc.pass, tc.expected, got, tc.ok)
		}
	}
}

func TestParsePasswordDifficultyHintVariants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		pass string
		diff float64
		ok   bool
	}{
		{pass: "x,d=1024", diff: 1024, ok: true},
		{pass: "diff:64", diff: 64, ok: true},
		{pass: "difficulty=2.5", diff: 2.5, ok: true},
		{pass: "sd=0x400", diff: 1024, ok: true},
		{pass: "x", diff: 0, ok: false},
		{pass: "d=0", diff: 0, ok: false},
		{pass: "d=abc", diff: 0, ok: false},
	}

	for _, tc := range tests {
		diff, ok := parsePasswordDifficultyHint(tc.pass)
		if ok != tc.ok {
			t.Fatalf("parsePasswordDifficultyHint(%q) ok=%v, want %v", tc.pass, ok, tc.ok)
		}
		if ok && diff != tc.diff {
			t.Fatalf("parsePasswordDifficultyHint(%q) diff=%v, want %v", tc.pass, diff, tc.diff)
		}
	}
}

func TestParseSuggestedDifficultyVariants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value any
		diff  float64
		ok    bool
	}{
		{name: "string", value: "256", diff: 256, ok: true},
		{name: "hex_string", value: "0x400", diff: 1024, ok: true},
		{name: "int", value: 16, diff: 16, ok: true},
		{name: "json_number", value: jsonNumber("128"), diff: 128, ok: true},
		{name: "invalid", value: "abc", diff: 0, ok: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseSuggestedDifficulty(tc.value)
			if ok != tc.ok {
				t.Fatalf("parseSuggestedDifficulty(%v) ok=%v, want %v", tc.value, ok, tc.ok)
			}
			if ok && got != tc.diff {
				t.Fatalf("parseSuggestedDifficulty(%v) diff=%v, want %v", tc.value, got, tc.diff)
			}
		})
	}
}

func TestParseWorkerDifficultyHintVariants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		worker        string
		clean         string
		diff          float64
		ok            bool
		wantUnchanged bool
	}{
		{name: "plus_number", worker: "wallet.worker+1024", clean: "wallet.worker", diff: 1024, ok: true},
		{name: "plus_keyed_hex", worker: "wallet.worker+d=0x400", clean: "wallet.worker", diff: 1024, ok: true},
		{name: "hash_keyed_float", worker: "wallet.worker#difficulty=2.5", clean: "wallet.worker", diff: 2.5, ok: true},
		{name: "comma_keyed", worker: "wallet.worker,diff:64", clean: "wallet.worker", diff: 64, ok: true},
		{name: "space_separated", worker: "wallet.worker + 128", clean: "wallet.worker", diff: 128, ok: true},
		{name: "non_diff_suffix", worker: "wallet.worker+garage", clean: "wallet.worker+garage", diff: 0, ok: false, wantUnchanged: true},
		{name: "zero_ignored", worker: "wallet.worker+0", clean: "wallet.worker+0", diff: 0, ok: false, wantUnchanged: true},
		{name: "no_delim", worker: "wallet.worker", clean: "wallet.worker", diff: 0, ok: false, wantUnchanged: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clean, diff, ok := parseWorkerDifficultyHint(tc.worker)
			if ok != tc.ok {
				t.Fatalf("parseWorkerDifficultyHint(%q) ok=%v, want %v", tc.worker, ok, tc.ok)
			}
			if ok && diff != tc.diff {
				t.Fatalf("parseWorkerDifficultyHint(%q) diff=%v, want %v", tc.worker, diff, tc.diff)
			}
			if ok && clean != tc.clean {
				t.Fatalf("parseWorkerDifficultyHint(%q) clean=%q, want %q", tc.worker, clean, tc.clean)
			}
			if !ok && tc.wantUnchanged && clean != tc.worker {
				t.Fatalf("parseWorkerDifficultyHint(%q) clean=%q, want unchanged", tc.worker, clean)
			}
		})
	}
}

func TestHandleConfigureSupportsVariantShapes(t *testing.T) {
	conn := &writeRecorderConn{}
	mc := &MinerConn{
		id:       "configure-variants",
		conn:     conn,
		poolMask: 0x1fffe000,
	}

	req := &StratumRequest{
		ID:     1,
		Method: "mining.configure",
		Params: []any{
			"version_rolling, suggest_difficulty",
			map[string]any{
				"version_rolling_mask":          "1fffe000",
				"version_rolling_min_bit_count": "2",
			},
		},
	}
	mc.handleConfigure(req)

	if !mc.versionRoll {
		t.Fatalf("expected version rolling to be enabled")
	}
	if mc.versionMask == 0 {
		t.Fatalf("expected negotiated version mask to be non-zero")
	}
	out := conn.String()
	if !strings.Contains(out, "\"version-rolling\":true") {
		t.Fatalf("expected configure response to enable version-rolling, got: %q", out)
	}
	if !strings.Contains(out, "\"suggest_difficulty\":true") {
		t.Fatalf("expected configure response to acknowledge suggest_difficulty, got: %q", out)
	}
}

func TestHandleConfigureSubscribeExtranonceSendsSetExtranonce(t *testing.T) {
	conn := &writeRecorderConn{}
	mc := &MinerConn{
		id:             "configure-extranonce",
		conn:           conn,
		extranonce1Hex: "abcdef01",
		cfg:            Config{Extranonce2Size: 4},
	}

	req := &StratumRequest{
		ID:     1,
		Method: "mining.configure",
		Params: []any{"subscribe-extranonce"},
	}
	mc.handleConfigure(req)

	if !mc.extranonceSubscribed {
		t.Fatalf("expected extranonceSubscribed to be enabled")
	}
	out := conn.String()
	if !strings.Contains(out, "\"subscribe-extranonce\":true") {
		t.Fatalf("expected configure response to acknowledge subscribe-extranonce, got: %q", out)
	}
	if !strings.Contains(out, "\"method\":\"mining.set_extranonce\"") {
		t.Fatalf("expected set_extranonce to be sent after configure, got: %q", out)
	}
}

func TestSubscribeResponseAdvertisesSetExtranonce(t *testing.T) {
	conn := &writeRecorderConn{}
	mc := &MinerConn{
		id:   "subscribe-advertise-extranonce",
		conn: conn,
		cfg:  Config{StratumFastEncodeEnabled: false},
	}

	mc.writeSubscribeResponse(1, "00", 4, "sid")

	out := conn.String()
	if !strings.Contains(out, "\"mining.set_extranonce\"") {
		t.Fatalf("expected subscribe response to advertise set_extranonce, got: %q", out)
	}
}
