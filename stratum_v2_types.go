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

// On-wire message shapes (SV2 Mining Protocol submit path). These map directly
// to the binary payload fields defined in the spec and are used by the SV2
// frame/payload codec.
type stratumV2WireSubmitSharesStandard struct {
	ChannelID      uint32
	SequenceNumber uint32
	JobID          uint32
	Nonce          uint32
	NTime          uint32
	Version        uint32
}

type stratumV2WireSubmitSharesExtended struct {
	ChannelID      uint32
	SequenceNumber uint32
	JobID          uint32
	Nonce          uint32
	NTime          uint32
	Version        uint32
	Extranonce     []byte // B0_32
}

type stratumV2WireSubmitSharesSuccess struct {
	ChannelID               uint32
	LastSequenceNumber      uint32
	NewSubmitsAcceptedCount uint32
	NewSharesSum            uint64
}

type stratumV2WireSubmitSharesError struct {
	ChannelID      uint32
	SequenceNumber uint32
	ErrorCode      string // STR0_255
}

type stratumV2WireOpenStandardMiningChannel struct {
	RequestID       uint32
	UserIdentity    string // STR0_255
	NominalHashRate float32
	MaxTarget       [32]byte // U256 (opaque bytes in this incremental codec)
}

type stratumV2WireOpenExtendedMiningChannel struct {
	stratumV2WireOpenStandardMiningChannel
	MinExtranonceSize uint16
}

type stratumV2WireOpenStandardMiningChannelSuccess struct {
	RequestID        uint32
	ChannelID        uint32
	Target           [32]byte // U256 (opaque bytes in this incremental codec)
	ExtranoncePrefix []byte   // B0_32
	GroupChannelID   uint32
}

type stratumV2WireOpenExtendedMiningChannelSuccess struct {
	RequestID        uint32
	ChannelID        uint32
	Target           [32]byte // U256 (opaque bytes in this incremental codec)
	ExtranonceSize   uint16
	ExtranoncePrefix []byte // B0_32
	GroupChannelID   uint32
}

// Minimal outbound job/update message shapes used to keep the submit mapper in
// sync as sv2_conn grows beyond the submit-only skeleton.
type stratumV2WireSetExtranoncePrefix struct {
	ChannelID        uint32
	ExtranoncePrefix []byte // B0_32
}

type stratumV2WireNewMiningJob struct {
	ChannelID   uint32
	JobID       uint32
	HasMinNTime bool
	MinNTime    uint32
	Version     uint32
	MerkleRoot  [32]byte // U256
}

type stratumV2WireSetTarget struct {
	ChannelID     uint32
	MaximumTarget [32]byte // U256
}
