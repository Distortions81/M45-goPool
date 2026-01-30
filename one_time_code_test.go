package main

import (
	"testing"
	"time"
)

func TestCreateNewOneTimeCode_UniqueAmongActive(t *testing.T) {
	prev := oneTimeCodeGenerator
	t.Cleanup(func() { oneTimeCodeGenerator = prev })

	seq := []string{"dup-code", "dup-code", "fresh-code"}
	oneTimeCodeGenerator = func() string {
		if len(seq) == 0 {
			return "fresh-code"
		}
		out := seq[0]
		seq = seq[1:]
		return out
	}

	s := &StatusServer{}
	s.UpdateConfig(Config{PoolEntropy: "ABCD"})
	now := time.Unix(123, 0)
	s.oneTimeCodes = map[string]oneTimeCodeEntry{
		"userA": {Code: "dup-code", CreatedAt: now, ExpiresAt: now.Add(oneTimeCodeTTL)},
	}

	code, expiresAt := s.createNewOneTimeCode("userB", now)
	if code != "ABCD-fresh-code" {
		t.Fatalf("expected fresh code, got %q", code)
	}
	if expiresAt.IsZero() {
		t.Fatalf("expected expiresAt to be set")
	}
	if s.oneTimeCodes["userA"].Code == s.oneTimeCodes["userB"].Code {
		t.Fatalf("expected unique codes, both were %q", s.oneTimeCodes["userB"].Code)
	}
}

func TestCreateNewOneTimeCode_ReturnsEmptyIfCollisionsNeverResolve(t *testing.T) {
	prev := oneTimeCodeGenerator
	t.Cleanup(func() { oneTimeCodeGenerator = prev })

	oneTimeCodeGenerator = func() string { return "dup-code" }

	s := &StatusServer{}
	s.UpdateConfig(Config{PoolEntropy: "ABCD"})
	now := time.Unix(123, 0)
	s.oneTimeCodes = map[string]oneTimeCodeEntry{
		"userA": {Code: "dup-code", CreatedAt: now, ExpiresAt: now.Add(oneTimeCodeTTL)},
	}

	code, expiresAt := s.createNewOneTimeCode("userB", now)
	if code != "" || !expiresAt.IsZero() {
		t.Fatalf("expected empty result, got code=%q expiresAt=%v", code, expiresAt)
	}
	if _, exists := s.oneTimeCodes["userB"]; exists {
		t.Fatalf("expected no entry for userB when generation fails")
	}
}

func TestRedeemOneTimeCode_RequiresPoolPrefix(t *testing.T) {
	prev := oneTimeCodeGenerator
	t.Cleanup(func() { oneTimeCodeGenerator = prev })
	oneTimeCodeGenerator = func() string { return "fresh-code" }

	s := &StatusServer{}
	s.UpdateConfig(Config{PoolEntropy: "aBcD"})
	now := time.Unix(123, 0)

	code, _ := s.createNewOneTimeCode("userB", now)
	if code != "aBcD-fresh-code" {
		t.Fatalf("expected prefixed code, got %q", code)
	}

	if uid, ok := s.redeemOneTimeCode("fresh-code", now); ok || uid != "" {
		t.Fatalf("expected unprefixed code to be rejected, got uid=%q ok=%v", uid, ok)
	}
	if uid, ok := s.redeemOneTimeCode("ZZZZ-fresh-code", now); ok || uid != "" {
		t.Fatalf("expected wrong prefix to be rejected, got uid=%q ok=%v", uid, ok)
	}
	if uid, ok := s.redeemOneTimeCode("ABCD-fresh-code", now); !ok || uid != "userB" {
		t.Fatalf("expected case-insensitive prefix redeem, got uid=%q ok=%v", uid, ok)
	}
}
