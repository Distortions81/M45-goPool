package main

import "testing"

func TestJobManagerNextJobID_Base58CounterStartsAtZero(t *testing.T) {
	jm := &JobManager{}

	if got := jm.nextJobID(); got != "1" {
		t.Fatalf("unexpected first job id: got %q, want %q", got, "1")
	}
	if got := jm.nextJobID(); got != "2" {
		t.Fatalf("unexpected second job id: got %q, want %q", got, "2")
	}
	if got := jm.nextJobID(); got != "3" {
		t.Fatalf("unexpected third job id: got %q, want %q", got, "3")
	}
}
