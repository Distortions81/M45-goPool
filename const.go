package main

import "time"

const (
	poolSoftwareName = "goPool"

	// Duplicate share detection: track last N submissions per job (LRU eviction).
	duplicateShareHistory  = 100
	evictedShareCacheGrace = 60 * time.Second // keep caches for evicted jobs to catch late duplicates

	workerPageCacheLimit = 100

	// Config defaults.
	defaultListenAddr        = ":3333"
	defaultStatusAddr        = ":80"
	defaultStatusTLSAddr     = ":443"
	defaultStatusTagline     = "Solo Mining Pool"
	defaultFiatCurrency      = "usd"
	defaultGitHubURL         = "https://github.com/Distortions81/M45-Core-goPool/blob/main/README.md"
	defaultMempoolAddressURL = "https://mempool.space/address/"
	defaultStratumTLSListen  = ":4333"
	defaultRPCURL            = "http://127.0.0.1:8332"

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
	defaultStratumMessagesPerMinute          = 0

	defaultJobEntropy                = 4
	maxJobEntropy                    = 16
	defaultCoinbaseScriptSigMaxBytes = 100

	defaultMaxConns = 50000

	// Ban thresholds.
	defaultNTimeForwardSlackSeconds      = 7000
	defaultBanInvalidSubmissionsAfter    = 40
	defaultBanInvalidSubmissionsWindow   = 5 * time.Minute
	defaultBanInvalidSubmissionsDuration = 15 * time.Minute
	// Accepted-share forgiveness for invalid-submission bans:
	// every N accepted shares reduce effective invalid count by 1, up to a cap.
	banInvalidForgiveSharesPerUnit     = 2
	banInvalidForgiveCapFraction       = 0.5
	defaultReconnectBanThreshold       = 60
	defaultReconnectBanWindowSeconds   = 60
	defaultReconnectBanDurationSeconds = 3600

	defaultDiscordWorkerNotifyThresholdSeconds = 300

	defaultMaxDifficulty = 0
	defaultMinDifficulty = 256.0

	defaultMinVersionBits    = 1
	defaultRefreshInterval   = 10 * time.Second
	defaultZMQReceiveTimeout = 15 * time.Second
	defaultZMQConnectTimeout = 5 * time.Second

	// ZMQ tuning: heartbeats detect dead peers faster than TCP; backoff avoids spamming during restarts.
	defaultZMQHeartbeatInterval   = 5 * time.Second
	defaultZMQHeartbeatTimeout    = 15 * time.Second
	defaultZMQHeartbeatTTL        = 30 * time.Second
	defaultZMQReconnectInterval   = 1 * time.Second
	defaultZMQReconnectMax        = 10 * time.Second
	defaultZMQRecreateBackoffMin  = 500 * time.Millisecond
	defaultZMQRecreateBackoffMax  = 10 * time.Second
	defaultInitialDifficultyDelay = 2 * time.Second
	defaultZMQHashBlockAddr       = "tcp://127.0.0.1:28334"
	defaultZMQRawBlockAddr        = "tcp://127.0.0.1:28332"

	defaultAutoAcceptRateLimits    = true
	defaultOperatorDonationPercent = 0.0

	defaultPeerCleanupEnabled   = true
	defaultPeerCleanupMaxPingMs = 150
	defaultPeerCleanupMinPeers  = 20

	// VarDiff defaults.
	defaultVarDiffTargetSharesPerMin = 10
	defaultVarDiffAdjustmentWindow   = 60 * time.Second
	defaultVarDiffStep               = 2
	defaultVarDiffDampingFactor      = 0.7
	defaultVarDiffRetargetDelay      = 30 * time.Second
	defaultDifficultyStepGranularity = 4
	vardiffAdaptiveMinWindow         = 30 * time.Second
	vardiffAdaptiveMaxWindow         = 4 * time.Minute
	vardiffAdaptiveHighShareCount    = 24.0
	vardiffAdaptiveLowShareCount     = 6.0
	vardiffLowHashrateExpectedShares = 8.0
	vardiffLowHashrateMinAccepted    = 3
	// Safety rails for vardiff decisions to avoid extreme share cadences.
	// - below min shares/min can cause long no-share gaps and miner timeouts.
	// - above max shares/min can spam submits and overload the pool.
	vardiffSafetyMinSharesPerMin = 2.0
	vardiffSafetyMaxSharesPerMin = 30.0
	vardiffLargeUpJumpFactor     = 4.0
	vardiffLargeUpCooldown       = 2 * time.Minute
	vardiffHighWarmupP95MS       = 12000.0
	vardiffHighWarmupSamplesMin  = 8
	vardiffHighWarmupStreakNeed  = 3
	vardiffWarmupDownwardBias    = 0.93
	vardiffTimeoutGuardMinQuiet  = 20 * time.Second
	vardiffTimeoutGuardLead      = 5 * time.Second
	vardiffTimeoutGuardThreshold = 0.7
	vardiffTimeoutGuardMaxPZero  = 0.01
	vardiffUncertaintyAbsRatio   = 2.0
	vardiffUncertaintyMinSamples = 4
	hashrateControlTauFactor     = 0.35
	hashrateControlTauMin        = 45 * time.Second
	startupDiffPrimingFactor     = 0.75
	startupDiffPrimingMinFactor  = 0.60

	defaultHashrateEMATauSeconds = 300.0
	initialHashrateEMATau        = 45 * time.Second
	// When anchoring a fresh sampling window after reset, place WindowStart at
	// this percent of the elapsed time from reset to first share (0-100).
	windowStartLagPercent = 55

	maxStratumMessageSize = 64 * 1024
	stratumWriteTimeout   = 60 * time.Second
	defaultVersionMask    = uint32(0x1fffe000)
	minMinerTimeout       = 30 * time.Second

	// Grace periods for new/changing connections.
	initialReadTimeout          = 90 * time.Second // kick idle connections that never submit valid shares
	previousDiffGracePeriod     = time.Minute      // accept shares at old difficulty briefly after a change
	earlySubmitHalfWeightWindow = defaultVarDiffAdjustmentWindow * 4
	stratumFloodLimitMultiplier = 2

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
