package main

import "testing"

func TestShortDisplayID_SanitizesASCII(t *testing.T) {
	got := shortDisplayID("abc DEF-._ 123", 100, 100)
	if got != "abcDEF-._123" {
		t.Fatalf("got %q, want %q", got, "abcDEF-._123")
	}
}

func TestShortDisplayID_Shortens(t *testing.T) {
	got := shortDisplayID("0123456789", 3, 3)
	if got != "012...789" {
		t.Fatalf("got %q, want %q", got, "012...789")
	}
}

func TestShortDisplayID_DropsUnicode(t *testing.T) {
	got := shortDisplayID("αβγ123", 100, 100)
	if got != "123" {
		t.Fatalf("got %q, want %q", got, "123")
	}
}

func TestShortWorkerName_PreservesSuffixAfterDot(t *testing.T) {
	got := shortWorkerName("1234567890.01", 3, 3)
	if got != "123...890.01" {
		t.Fatalf("got %q, want %q", got, "123...890.01")
	}
}

func TestShortWorkerName_NoDotUsesShortDisplayID(t *testing.T) {
	got := shortWorkerName("0123456789", 3, 3)
	if got != "012...789" {
		t.Fatalf("got %q, want %q", got, "012...789")
	}
}
