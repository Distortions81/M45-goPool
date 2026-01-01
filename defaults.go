package main

import (
	"path/filepath"
)

// defaultConfig returns a Config populated with built-in defaults that act
// as the base for both runtime config loading and example config generation.
func defaultConfig() Config {
	return Config{
		ListenAddr:                      defaultListenAddr,
		StatusAddr:                      defaultStatusAddr,
		StatusTLSAddr:                   defaultStatusTLSAddr,
		StatusBrandName:                 "",
		StatusBrandDomain:               "",
		StatusTagline:                   defaultStatusTagline,
		StatusConnectMinerTitleExtra:    "",
		StatusConnectMinerTitleExtraURL: "",
		FiatCurrency:                    defaultFiatCurrency,
		PoolDonationAddress:             "",
		DiscordURL:                      "",
		DiscordServerID:                 "",
		DiscordNotifyChannelID:          "",
		DiscordBotToken:                 "",
		GitHubURL:                       defaultGitHubURL,
		// StratumTLSListen defaults to empty (disabled) so operators
		// explicitly opt in to TLS for miner connections.
		StratumTLSListen:        defaultStratumTLSListen,
		ClerkIssuerURL:          defaultClerkIssuerURL,
		ClerkJWKSURL:            defaultClerkJWKSURL,
		ClerkSignInURL:          defaultClerkSignInURL,
		ClerkCallbackPath:       defaultClerkCallbackPath,
		ClerkFrontendAPIURL:     "",
		ClerkSessionCookieName:  defaultClerkSessionCookieName,
		RPCURL:                  defaultRPCURL,
		RPCUser:                 "",
		RPCPass:                 "",
		AllowPublicRPC:          false,
		CoinbasePoolTag:         generatePoolTag(),
		PayoutAddress:           "",
		PoolFeePercent:          defaultPoolFeePercent,
		OperatorDonationPercent: defaultOperatorDonationPercent,
		OperatorDonationAddress: "",
		OperatorDonationName:    "",
		OperatorDonationURL:     "",
		// Mining / Stratum defaults.
		Extranonce2Size:                   defaultExtranonce2Size,
		TemplateExtraNonce2Size:           defaultTemplateExtraNonce2Size,
		CoinbaseSuffixBytes:               defaultCoinbaseSuffixBytes,
		CoinbaseMsg:                       poolSoftwareName,
		CoinbaseScriptSigMaxBytes:         defaultCoinbaseScriptSigMaxBytes,
		ZMQBlockAddr:                      defaultZMQBlockAddr,
		BackblazeBackupEnabled:            false,
		BackblazeAccountID:                "",
		BackblazeApplicationKey:           "",
		BackblazeBucket:                   "",
		BackblazePrefix:                   "",
		BackblazeBackupIntervalSeconds:    defaultBackblazeBackupIntervalSeconds,
		DataDir:                           defaultDataDir,
		ShareLogBufferBytes:               defaultShareLogBufferBytes,
		FsyncShareLog:                     defaultFsyncShareLog,
		ShareLogReplayBytes:               defaultReplayLimit,
		MaxConns:                          defaultMaxConns,
		MaxAcceptsPerSecond:               defaultMaxAcceptsPerSecond,
		MaxAcceptBurst:                    defaultMaxAcceptBurst,
		AutoAcceptRateLimits:              defaultAutoAcceptRateLimits,
		AcceptReconnectWindow:             defaultAcceptReconnectWindow,
		AcceptBurstWindow:                 defaultAcceptBurstWindow,
		AcceptSteadyStateWindow:           defaultAcceptSteadyStateWindow,
		AcceptSteadyStateRate:             defaultAcceptSteadyStateRate,
		AcceptSteadyStateReconnectPercent: defaultAcceptSteadyStateReconnectPercent,
		AcceptSteadyStateReconnectWindow:  defaultAcceptSteadyStateReconnectWindow,
		MaxRecentJobs:                     defaultRecentJobs,
		ConnectionTimeout:                 defaultConnectionTimeout,
		VersionMask:                       defaultVersionMask,
		MinVersionBits:                    defaultMinVersionBits,
		// Default difficulty range is bounded and power-of-two quantized.
		MaxDifficulty:                 defaultMaxDifficulty,
		MinDifficulty:                 defaultMinDifficulty,
		LockSuggestedDifficulty:       false,
		HashrateEMATauSeconds:         defaultHashrateEMATauSeconds,
		HashrateEMAMinShares:          defaultHashrateEMAMinShares,
		NTimeForwardSlackSeconds:      defaultNTimeForwardSlackSeconds,
		BanInvalidSubmissionsAfter:    defaultBanInvalidSubmissionsAfter,
		BanInvalidSubmissionsWindow:   defaultBanInvalidSubmissionsWindow,
		BanInvalidSubmissionsDuration: defaultBanInvalidSubmissionsDuration,
		ReconnectBanThreshold:         defaultReconnectBanThreshold,
		ReconnectBanWindowSeconds:     defaultReconnectBanWindowSeconds,
		ReconnectBanDurationSeconds:   defaultReconnectBanDurationSeconds,
		PeerCleanupEnabled:            defaultPeerCleanupEnabled,
		PeerCleanupMaxPingMs:          defaultPeerCleanupMaxPingMs,
		PeerCleanupMinPeers:           defaultPeerCleanupMinPeers,
	}
}

// defaultConfigPath returns the path for the main pool config.
func defaultConfigPath() string {
	return filepath.Join(defaultDataDir, "config", "config.toml")
}
