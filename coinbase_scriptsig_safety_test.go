package main

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestCoinbaseScriptSigConfigBounds(t *testing.T) {
	validConfig := func() Config {
		cfg := defaultConfig()
		cfg.AllowPublicRPC = true
		cfg.PayoutAddress = "test-payout-address"
		return cfg
	}
	for _, limit := range []int{0, 1, maxCoinbaseScriptSigBytes + 1} {
		cfg := validConfig()
		cfg.CoinbaseScriptSigMaxBytes = limit
		if err := validateConfig(cfg); err == nil {
			t.Fatalf("coinbase_scriptsig_max_bytes=%d passed validation", limit)
		}
	}
	for _, limit := range []int{minCoinbaseScriptSigBytes, maxCoinbaseScriptSigBytes} {
		cfg := validConfig()
		cfg.CoinbaseScriptSigMaxBytes = limit
		if err := validateConfig(cfg); err != nil {
			t.Fatalf("coinbase_scriptsig_max_bytes=%d failed bounds validation: %v", limit, err)
		}
	}
}

func TestAdminCoinbaseScriptSigConfigBounds(t *testing.T) {
	for _, limit := range []string{"0", "1", "101"} {
		cfg := defaultConfig()
		form := url.Values{}
		form.Set("status_tagline", cfg.StatusTagline)
		form.Set("coinbase_scriptsig_max_bytes", limit)
		req := httptest.NewRequest("POST", "/admin/apply", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if err := req.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if err := applyAdminSettingsForm(&cfg, req); err == nil {
			t.Fatalf("admin accepted coinbase_scriptsig_max_bytes=%s", limit)
		}
	}
}

func TestClampCoinbaseMessageUsesStableSmallestTag(t *testing.T) {
	const (
		height     = int64(840_000)
		scriptTime = int64(1_700_000_000)
	)
	fixedLen, err := coinbaseScriptSigFixedLen(height, scriptTime, "", 4, 8)
	if err != nil {
		t.Fatalf("fixed length: %v", err)
	}
	limit := fixedLen + len(serializeStringScript("/"))
	message, truncated, err := clampCoinbaseMessage(strings.Repeat("long-tag", 30), limit, height, scriptTime, "", 4, 8)
	if err != nil {
		t.Fatalf("clamp to smallest tag: %v", err)
	}
	if !truncated {
		t.Fatal("expected long tag to be truncated")
	}
	if message != "/" || normalizeCoinbaseMessage(message) != "/" {
		t.Fatalf("smallest tag = %q normalized=%q, want stable /", message, normalizeCoinbaseMessage(message))
	}
	if got := fixedLen + len(serializeStringScript(normalizeCoinbaseMessage(message))); got != limit {
		t.Fatalf("clamped scriptSig length = %d, want %d", got, limit)
	}
}

func TestClampCoinbaseMessageRejectsImpossibleLimit(t *testing.T) {
	const (
		height     = int64(840_000)
		scriptTime = int64(1_700_000_000)
	)
	fixedLen, err := coinbaseScriptSigFixedLen(height, scriptTime, "", 4, 8)
	if err != nil {
		t.Fatalf("fixed length: %v", err)
	}
	if _, _, err := clampCoinbaseMessage("tag", fixedLen+1, height, scriptTime, "", 4, 8); err == nil {
		t.Fatal("expected an error when the encoded / tag cannot fit")
	}
	for _, limit := range []int{0, maxCoinbaseScriptSigBytes + 1} {
		if _, _, err := clampCoinbaseMessage("tag", limit, height, scriptTime, "", 4, 8); err == nil {
			t.Fatalf("unsafe clamp limit %d was accepted", limit)
		}
	}
}

func TestCoinbaseSerializationRejectsConsensusOversizeScriptSig(t *testing.T) {
	longMessage := strings.Repeat("x", maxCoinbaseScriptSigBytes+32)
	if _, _, err := buildCoinbaseParts(
		840_000,
		[]byte{1, 2, 3, 4},
		4,
		8,
		[]byte{0x51},
		50*1e8,
		"",
		"",
		longMessage,
		1_700_000_000,
	); err == nil {
		t.Fatal("notify coinbase accepted an oversized scriptSig")
	}
	if _, _, err := serializeCoinbaseTxPredecoded(
		840_000,
		[]byte{1, 2, 3, 4},
		[]byte{0, 0, 0, 0},
		8,
		[]byte{0x51},
		50*1e8,
		nil,
		nil,
		longMessage,
		1_700_000_000,
	); err == nil {
		t.Fatal("full coinbase serialization accepted an oversized scriptSig")
	}
}

func TestSessionCoinbaseAdaptationRejectsImpossibleLimit(t *testing.T) {
	mc, job := newSubmitReadyMinerConnForModesTest(t)
	job.CoinbaseScriptSigMaxBytes = minCoinbaseScriptSigBytes
	if _, err := mc.jobForSession(job); err == nil {
		t.Fatal("session adaptation accepted a limit that cannot fit mandatory fields")
	}
}
