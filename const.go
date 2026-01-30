package main

import "time"

const (
	poolSoftwareName = "goPool"

	// Duplicate share detection: track last N submissions per job (LRU eviction).
	duplicateShareHistory  = 100
	evictedShareCacheGrace = 60 * time.Second // keep caches for evicted jobs to catch late duplicates

	workerPageCacheLimit = 100

	// Config defaults.
	defaultListenAddr       = ":3333"
	defaultStatusAddr       = ":80"
	defaultStatusTLSAddr    = ":443"
	defaultStatusTagline    = "Solo Mining Pool"
	defaultFiatCurrency     = "usd"
	defaultGitHubURL        = "https://github.com/Distortions81/M45-Core-goPool/blob/main/README.md"
	defaultStratumTLSListen = ":4333"
	defaultRPCURL           = "http://127.0.0.1:8332"

	defaultExtranonce2Size         = 4
	defaultTemplateExtraNonce2Size = 8
	defaultPoolFeePercent          = 2.0
	defaultRecentJobs              = 10
	defaultConnectionTimeout       = 300 * time.Second

	// Accept rate limiting defaults.
	defaultMaxAcceptsPerSecond               = 500
	defaultMaxAcceptBurst                    = 1000
	defaultAcceptReconnectWindow             = 15
	defaultAcceptBurstWindow                 = 5
	defaultAcceptSteadyStateWindow           = 100
	defaultAcceptSteadyStateRate             = 50
	defaultAcceptSteadyStateReconnectPercent = 5.0
	defaultAcceptSteadyStateReconnectWindow  = 60

	defaultJobEntropy                = 4
	maxJobEntropy                    = 16
	defaultCoinbaseScriptSigMaxBytes = 100

	defaultMaxConns = 50000

	// Ban thresholds.
	defaultNTimeForwardSlackSeconds      = 7000
	defaultBanInvalidSubmissionsAfter    = 60
	defaultBanInvalidSubmissionsWindow   = time.Minute
	defaultBanInvalidSubmissionsDuration = 15 * time.Minute
	defaultReconnectBanThreshold         = 0
	defaultReconnectBanWindowSeconds     = 60
	defaultReconnectBanDurationSeconds   = 300

	defaultDiscordWorkerNotifyThresholdSeconds = 300

	defaultMaxDifficulty = 65536
	defaultMinDifficulty = 256

	defaultMinVersionBits    = 1
	defaultRefreshInterval   = 10 * time.Second
	defaultZMQReceiveTimeout = 15 * time.Second
	defaultZMQConnectTimeout = 5 * time.Second

	// ZMQ tuning: heartbeats detect dead peers faster than TCP; backoff avoids spamming during restarts.
	defaultZMQHeartbeatInterval  = 5 * time.Second
	defaultZMQHeartbeatTimeout   = 15 * time.Second
	defaultZMQHeartbeatTTL       = 30 * time.Second
	defaultZMQReconnectInterval  = 1 * time.Second
	defaultZMQReconnectMax       = 10 * time.Second
	defaultZMQRecreateBackoffMin = 500 * time.Millisecond
	defaultZMQRecreateBackoffMax = 10 * time.Second

	defaultAutoAcceptRateLimits    = true
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

	// Grace periods for new/changing connections.
	initialReadTimeout      = 90 * time.Second // kick idle connections that never submit valid shares
	minDiffChangeInterval   = 60 * time.Second // throttle vardiff changes so in-flight shares stay valid
	previousDiffGracePeriod = time.Minute      // accept shares at old difficulty briefly after a change

	defaultBackblazeBackupIntervalSeconds = 12 * 60 * 60

	// Input validation limits.
	maxMinerClientIDLen       = 256
	maxWorkerNameLen          = 256
	maxJobIDLen               = 128
	maxVersionHexLen          = 8
	maxDuplicateShareKeyBytes = 64

	forceClerkLoginUIForTesting = false
	clerkDevSessionTokenTTL     = 12 * time.Hour // localhost dev sessions only
)
