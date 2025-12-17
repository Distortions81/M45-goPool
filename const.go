package main

import "time"

const (
	defaultHashrateEMATauSeconds = 600.0
	defaultHashrateEMAMinShares  = 10
	minHashrateEMAMinShares      = 10

	maxStratumMessageSize = 64 * 1024
	stratumWriteTimeout   = 5 * time.Minute
	ntimeFutureTolerance  = 2 * time.Hour
	defaultVersionMask    = uint32(0x1fffe000)
	// initialReadTimeout limits how long we keep a connection around
	// before it has proven itself by submitting valid shares. This helps
	// protect against floods of idle connections.
	initialReadTimeout = 10 * time.Second
	// Minimum time between difficulty changes so that shares from the
	// previous target have a chance to arrive and be covered by the
	// previous-difficulty grace logic. This caps vardiff moves for a given
	// miner so we don't thrash difficulty on every small fluctuation.
	minDiffChangeInterval = 60 * time.Second

	// Input validation limits for miner-provided fields
	maxMinerClientIDLen = 256 // mining.subscribe client identifier
	maxWorkerNameLen    = 256 // mining.authorize and submit worker name
	maxJobIDLen         = 128 // submit job_id parameter
	maxVersionHexLen    = 8   // submit version_bits parameter (4-byte hex)

	maxDuplicateShareKeyBytes = 64
)
