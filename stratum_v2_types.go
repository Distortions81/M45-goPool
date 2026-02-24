package main

import "time"

// Minimal typed SV2 message shapes for the incremental submit bridge. These
// are not full protocol coverage; they provide a concrete boundary for submit
// decode/response plumbing before the binary framing/codec is implemented.

type stratumV2SubmitSharesMessage struct {
	RequestID   uint32
	WorkerName  string
	JobID       string
	Extranonce2 []byte
	NTime       uint32
	Nonce       uint32
	Version     uint32
	HasVersion  bool
	ReceivedAt  time.Time
}

type stratumV2SubmitSharesSuccessMessage struct {
	RequestID uint32
}

type stratumV2SubmitSharesErrorMessage struct {
	RequestID uint32
	Code      int
	Message   string
	Banned    bool
}

type stratumV2SetTargetMessage struct {
	// Placeholder target update surface for the current bridge. We keep the Job
	// reference so the future encoder can derive the exact target/channel fields.
	Job *Job
}
