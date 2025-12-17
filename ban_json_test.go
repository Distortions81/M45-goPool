package main

import (
	"testing"
	"time"

	"github.com/bytedance/sonic"
)

func TestEncodeDecodeBanEntries(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	entries := []banEntry{
		{Worker: "miner1", Until: now.Add(10 * time.Minute), Reason: "too many invalid shares"},
		{Worker: "miner2", Until: now.Add(2 * time.Hour), Reason: ""},
	}

	data, err := encodeBanEntries(entries)
	if err != nil {
		t.Fatalf("encodeBanEntries: %v", err)
	}

	decoded, err := decodeBanEntries(data)
	if err != nil {
		t.Fatalf("decodeBanEntries: %v", err)
	}
	if len(decoded) != len(entries) {
		t.Fatalf("decoded %d entries, want %d", len(decoded), len(entries))
	}
	for i, want := range entries {
		got := decoded[i]
		if got.Worker != want.Worker {
			t.Fatalf("entry %d worker: got %q, want %q", i, got.Worker, want.Worker)
		}
		if !got.Until.Equal(want.Until) {
			t.Fatalf("entry %d until: got %v, want %v", i, got.Until, want.Until)
		}
		if got.Reason != want.Reason {
			t.Fatalf("entry %d reason: got %q, want %q", i, got.Reason, want.Reason)
		}
	}
}

func TestDecodeBanEntriesEmpty(t *testing.T) {
	entries, err := decodeBanEntries(nil)
	if err != nil {
		t.Fatalf("decodeBanEntries(nil): %v", err)
	}
	if entries != nil {
		t.Fatalf("expected nil entries, got %v", entries)
	}
}

func TestDecodeBanEntriesLegacyArray(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	entries := []banEntry{
		{Worker: "legacy", Until: now.Add(time.Minute), Reason: "legacy"},
	}
	data, err := sonic.ConfigDefault.Marshal(entries)
	if err != nil {
		t.Fatalf("setup Marshal: %v", err)
	}
	decoded, err := decodeBanEntries(data)
	if err != nil {
		t.Fatalf("decodeBanEntries legacy: %v", err)
	}
	if len(decoded) != 1 {
		t.Fatalf("decoded %d entries, want 1", len(decoded))
	}
	if decoded[0].Worker != "legacy" {
		t.Fatalf("decoded entry missing legacy worker")
	}
}
