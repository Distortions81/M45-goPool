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
	if got, ok := result[1].(string); !ok || got != "0011aabb" {
		t.Fatalf("unexpected extranonce1: %#v", result[1])
	}
	if got, ok := result[2].(float64); !ok || got != 4 {
		t.Fatalf("unexpected extranonce2_size: %#v", result[2])
	}
}

