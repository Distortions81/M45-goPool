package main

import "time"

const (
	// poolSoftwareName is the name of the pool software used throughout the codebase
	// for branding, coinbase messages, and certificates.
	poolSoftwareName = "goPool"

	// duplicateShareHistory controls how many recent submissions per job are
	// tracked for duplicate-share detection. It is implemented as a fixed-size
	// ring so memory usage is bounded and independent of share rate.
	duplicateShareHistory = 100

	// workerPageCacheLimit bounds the number of cached worker_status pages kept
	// in memory. Entries also expire after overviewRefreshInterval.
	workerPageCacheLimit = 100

	// Config defaults (used for example config + runtime defaults).
	defaultListenAddr    = ":3333"
	defaultStatusAddr    = ":80"
	defaultStatusTLSAddr = ":443"
	defaultStatusTagline = "Solo Mining Pool"
	defaultFiatCurrency  = "usd"
	defaultGitHubURL     = "https://github.com/Distortions81/M45-Core-goPool/blob/main/README.md"
	// StratumTLSListen is the default TLS listen address. Operators can disable
	// TLS by setting this to an empty string in config.
	defaultStratumTLSListen = ":4333"
	defaultRPCURL           = "http://127.0.0.1:8332"

	defaultExtranonce2Size         = 4
	defaultTemplateExtraNonce2Size = 8
	defaultPoolFeePercent          = 2.0
	defaultRecentJobs              = 10
	defaultConnectionTimeout       = 300 * time.Second

	defaultMaxAcceptsPerSecond               = 500
	defaultMaxAcceptBurst                    = 1000
	defaultAcceptReconnectWindow             = 15
	defaultAcceptBurstWindow                 = 5
	defaultAcceptSteadyStateWindow           = 100
	defaultAcceptSteadyStateRate             = 50
	defaultAcceptSteadyStateReconnectPercent = 5.0
	defaultAcceptSteadyStateReconnectWindow  = 60

	defaultCoinbaseSuffixBytes       = 4
	maxCoinbaseSuffixBytes           = 32
	defaultCoinbaseScriptSigMaxBytes = 100

	defaultReplayLimit = int64(16 << 20)
	defaultMaxConns    = 50000

	defaultNTimeForwardSlackSeconds      = 7000
	defaultBanInvalidSubmissionsAfter    = 60
	defaultBanInvalidSubmissionsWindow   = time.Minute
	defaultBanInvalidSubmissionsDuration = 15 * time.Minute
	defaultReconnectBanThreshold         = 0
	defaultReconnectBanWindowSeconds     = 60
	defaultReconnectBanDurationSeconds   = 300

	defaultMaxDifficulty = 65536
	defaultMinDifficulty = 256

	defaultMinVersionBits    = 1
	defaultRefreshInterval   = 10 * time.Second
	defaultZMQReceiveTimeout = 15 * time.Second
	defaultZMQConnectTimeout = 5 * time.Second

	// ZMQ socket liveness/reconnect tuning. These are conservative defaults:
	// - Heartbeats help detect dead peers faster than TCP alone.
	// - Reconnect intervals/backoff avoid spamming the node during restarts.
	defaultZMQHeartbeatInterval  = 5 * time.Second
	defaultZMQHeartbeatTimeout   = 15 * time.Second
	defaultZMQHeartbeatTTL       = 30 * time.Second
	defaultZMQReconnectInterval  = 1 * time.Second
	defaultZMQReconnectMax       = 10 * time.Second
	defaultZMQRecreateBackoffMin = 500 * time.Millisecond
	defaultZMQRecreateBackoffMax = 10 * time.Second

	defaultZMQBlockAddr = "tcp://127.0.0.1:28332"

	defaultShareLogBufferBytes  = 0
	defaultFsyncShareLog        = false
	defaultAutoAcceptRateLimits = true

	defaultOperatorDonationPercent = 0.0

	defaultPeerCleanupEnabled   = true
	defaultPeerCleanupMaxPingMs = 150
	defaultPeerCleanupMinPeers  = 20

	// VarDiff defaults.
	defaultVarDiffTargetSharesPerMin = 5
	defaultVarDiffAdjustmentWindow   = 90 * time.Second
	defaultVarDiffStep               = 2
	defaultVarDiffMaxBurstShares     = 600
	defaultVarDiffBurstWindow        = 60 * time.Second
	defaultVarDiffDampingFactor      = 0.4

	defaultHashrateEMATauSeconds = 600.0
	defaultHashrateEMAMinShares  = 10
	minHashrateEMAMinShares      = 10

	maxStratumMessageSize = 64 * 1024
	stratumWriteTimeout   = 60 * time.Second
	defaultVersionMask    = uint32(0x1fffe000)
	minMinerTimeout       = 30 * time.Second
	// initialReadTimeout limits how long we keep a connection around
	// before it has proven itself by submitting valid shares. This helps
	// protect against floods of idle connections.
	initialReadTimeout = 90 * time.Second
	// Minimum time between difficulty changes so that shares from the
	// previous target have a chance to arrive and be covered by the
	// previous-difficulty grace logic. This caps vardiff moves for a given
	// miner so we don't thrash difficulty on every small fluctuation.
	minDiffChangeInterval = 60 * time.Second
	// previousDiffGracePeriod is how long after a difficulty change we still
	// accept shares at the previous difficulty. This handles in-flight shares
	// that were computed before the miner received the new difficulty.
	previousDiffGracePeriod = time.Minute

	defaultBackblazeBackupIntervalSeconds = 12 * 60 * 60

	// Input validation limits for miner-provided fields
	maxMinerClientIDLen = 256 // mining.subscribe client identifier
	maxWorkerNameLen    = 256 // mining.authorize and submit worker name
	maxJobIDLen         = 128 // submit job_id parameter
	maxVersionHexLen    = 8   // submit version_bits parameter (4-byte hex)

	maxDuplicateShareKeyBytes = 64

	// forceClerkLoginUIForTesting shows the Clerk card when development forces it.
	forceClerkLoginUIForTesting = false

	// clerkDevSessionTokenTTL controls how long our localhost session JWT
	// (exchanged from Clerk's development __clerk_db_jwt) should last.
	// This only applies to development instances; production should rely on
	// first-party cookies on your own domain.
	clerkDevSessionTokenTTL = 12 * time.Hour
)
