package main

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestBuildSubscribeResponseBytes(t *testing.T) {
	b, err := buildSubscribeResponseBytes(int64(7), "0011aabb", 4)
	if err != nil {
		t.Fatalf("buildSubscribeResponseBytes: %v", err)
	}
	if !bytes.HasSuffix(b, []byte{'\n'}) {
		t.Fatalf("expected newline-terminated response")
	}

	var resp StratumResponse
	if err := json.Unmarshal(bytes.TrimSpace(b), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.ID != float64(7) {
		t.Fatalf("unexpected id: %#v", resp.ID)
	}
	if resp.Error != nil {
		t.Fatalf("expected null error, got: %#v", resp.Error)
	}

	result, ok := resp.Result.([]any)
	if !ok || len(result) != 3 {
		t.Fatalf("unexpected result shape: %#v", resp.Result)
	}

	// Verify we advertise expected notifications in subscriptions list.
	subs, ok := result[0].([]any)
	if !ok || len(subs) == 0 {
		t.Fatalf("unexpected subscriptions shape: %#v", result[0])
	}
	methods := make(map[string]bool, len(subs))
	for _, item := range subs {
		pair, ok := item.([]any)
		if !ok || len(pair) < 1 {
			continue
		}
		method, ok := pair[0].(string)
		if !ok || method == "" {
			continue
		}
		methods[method] = true
	}
	if !methods["mining.set_difficulty"] || !methods["mining.notify"] {
		t.Fatalf("expected set_difficulty and notify in subscriptions list: %#v", subs)
	}
	if !methods["mining.set_extranonce"] {
		t.Fatalf("expected set_extranonce in subscriptions list: %#v", subs)
	}
	if !methods["mining.set_version_mask"] {
		t.Fatalf("expected set_version_mask in subscriptions list: %#v", subs)
	}
	if got, ok := result[1].(string); !ok || got != "0011aabb" {
		t.Fatalf("unexpected extranonce1: %#v", result[1])
	}
	if got, ok := result[2].(float64); !ok || got != 4 {
		t.Fatalf("unexpected extranonce2_size: %#v", result[2])
	}
}
