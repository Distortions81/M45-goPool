package main

import (
	"strings"
	"time"
)

func buildBaseFileConfig(cfg Config) baseFileConfig {
	return baseFileConfig{
		Server: serverConfig{
			PoolListen:      cfg.ListenAddr,
			StatusListen:    cfg.StatusAddr,
			StatusTLSListen: &cfg.StatusTLSAddr,
			StatusPublicURL: cfg.StatusPublicURL,
		},
		Branding: brandingConfig{
			StatusBrandName:                 cfg.StatusBrandName,
			StatusBrandDomain:               cfg.StatusBrandDomain,
			StatusTagline:                   cfg.StatusTagline,
			StatusConnectMinerTitleExtra:    cfg.StatusConnectMinerTitleExtra,
			StatusConnectMinerTitleExtraURL: cfg.StatusConnectMinerTitleExtraURL,
			FiatCurrency:                    cfg.FiatCurrency,
			PoolDonationAddress:             cfg.PoolDonationAddress,
			ServerLocation:                  cfg.ServerLocation,
		},
		Stratum: stratumConfig{
			StratumTLSListen:       cfg.StratumTLSListen,
			StratumPasswordEnabled: cfg.StratumPasswordEnabled,
			StratumPassword:        cfg.StratumPassword,
			StratumPasswordPublic:  cfg.StratumPasswordPublic,
			SafeMode:               cfg.SafeMode,
		},
		Node: nodeConfig{
			RPCURL:           cfg.RPCURL,
			PayoutAddress:    cfg.PayoutAddress,
			ZMQHashBlockAddr: cfg.ZMQHashBlockAddr,
			ZMQRawBlockAddr:  cfg.ZMQRawBlockAddr,
			RPCCookiePath:    cfg.RPCCookiePath,
		},
		Mining: miningConfig{
			PoolFeePercent:          new(cfg.PoolFeePercent),
			OperatorDonationPercent: new(cfg.OperatorDonationPercent),
			OperatorDonationAddress: cfg.OperatorDonationAddress,
			OperatorDonationName:    cfg.OperatorDonationName,
			OperatorDonationURL:     cfg.OperatorDonationURL,
			PoolEntropy:             stringPtr(cfg.PoolEntropy),
			PoolTagPrefix:           cfg.PoolTagPrefix,
		},
		Logging: loggingConfig{
			Debug:    boolPtr(cfg.LogDebug),
			NetDebug: boolPtr(cfg.LogNetDebug),
		},
	}
}

func buildServicesFileConfig(cfg Config) servicesFileConfig {
	return servicesFileConfig{
		Auth: authConfig{
			ClerkIssuerURL:         cfg.ClerkIssuerURL,
			ClerkJWKSURL:           cfg.ClerkJWKSURL,
			ClerkSignInURL:         cfg.ClerkSignInURL,
			ClerkCallbackPath:      cfg.ClerkCallbackPath,
			ClerkFrontendAPIURL:    cfg.ClerkFrontendAPIURL,
			ClerkSessionCookieName: cfg.ClerkSessionCookieName,
			ClerkSessionAudience:   cfg.ClerkSessionAudience,
		},
		Backblaze: backblazeBackupConfig{
			Enabled:            cfg.BackblazeBackupEnabled,
			Bucket:             cfg.BackblazeBucket,
			Prefix:             cfg.BackblazePrefix,
			IntervalSeconds:    new(cfg.BackblazeBackupIntervalSeconds),
			KeepLocalCopy:      new(cfg.BackblazeKeepLocalCopy),
			ForceEveryInterval: new(cfg.BackblazeForceEveryInterval),
			SnapshotPath:       cfg.BackupSnapshotPath,
		},
		Discord: servicesDiscordConfig{
			DiscordURL:                   cfg.DiscordURL,
			DiscordServerID:              cfg.DiscordServerID,
			DiscordNotifyChannelID:       cfg.DiscordNotifyChannelID,
			WorkerNotifyThresholdSeconds: new(cfg.DiscordWorkerNotifyThresholdSeconds),
		},
		Status: servicesStatusConfig{
			MempoolAddressURL: cfg.MempoolAddressURL,
			GitHubURL:         cfg.GitHubURL,
		},
	}
}

func buildPolicyFileConfig(cfg Config) policyFileConfig {
	return policyFileConfig{
		Stratum: policyStratumConfig{
			CKPoolEmulate: new(cfg.CKPoolEmulate),
		},
		Mining: policyMiningConfig{
			ShareJobFreshnessMode:            new(cfg.ShareJobFreshnessMode),
			ShareCheckNTimeWindow:            new(cfg.ShareCheckNTimeWindow),
			ShareCheckVersionRolling:         new(cfg.ShareCheckVersionRolling),
			ShareRequireAuthorizedConnection: new(cfg.ShareRequireAuthorizedConnection),
				ShareCheckParamFormat:            new(cfg.ShareCheckParamFormat),
				ShareRequireWorkerMatch:          new(cfg.ShareRequireWorkerMatch),
				SubmitProcessInline:              new(cfg.SubmitProcessInline),
				ShareCheckDuplicate:              new(cfg.ShareCheckDuplicate),
			},
		Hashrate: policyHashrateConfig{
			ShareNTimeMaxForwardSeconds: new(cfg.ShareNTimeMaxForwardSeconds),
		},
		Version: versionTuning{
			MinVersionBits:                new(cfg.MinVersionBits),
			ShareAllowDegradedVersionBits: new(cfg.ShareAllowDegradedVersionBits),
		},
		Bans: banTuning{
			CleanExpiredOnStartup:            new(cfg.CleanExpiredBansOnStartup),
			BanInvalidSubmissionsAfter:       new(cfg.BanInvalidSubmissionsAfter),
			BanInvalidSubmissionsWindowSec:   new(int(cfg.BanInvalidSubmissionsWindow / time.Second)),
			BanInvalidSubmissionsDurationSec: new(int(cfg.BanInvalidSubmissionsDuration / time.Second)),
			ReconnectBanThreshold:            new(cfg.ReconnectBanThreshold),
			ReconnectBanWindowSeconds:        new(cfg.ReconnectBanWindowSeconds),
			ReconnectBanDurationSeconds:      new(cfg.ReconnectBanDurationSeconds),
			BannedMinerTypes:                 cfg.BannedMinerTypes,
		},
		Timeouts: timeoutTuning{
			ConnectionTimeoutSec: new(int(cfg.ConnectionTimeout / time.Second)),
		},
	}
}

func buildTuningFileConfig(cfg Config) tuningFileConfig {
	return tuningFileConfig{
		RateLimits: rateLimitTuning{
			MaxConns:                          new(cfg.MaxConns),
			MaxAcceptsPerSecond:               new(cfg.MaxAcceptsPerSecond),
			MaxAcceptBurst:                    new(cfg.MaxAcceptBurst),
			DisableConnectRateLimits:          new(cfg.DisableConnectRateLimits),
			AutoAcceptRateLimits:              new(cfg.AutoAcceptRateLimits),
			AcceptReconnectWindow:             new(cfg.AcceptReconnectWindow),
			AcceptBurstWindow:                 new(cfg.AcceptBurstWindow),
			AcceptSteadyStateWindow:           new(cfg.AcceptSteadyStateWindow),
			AcceptSteadyStateRate:             new(cfg.AcceptSteadyStateRate),
			AcceptSteadyStateReconnectPercent: new(cfg.AcceptSteadyStateReconnectPercent),
			AcceptSteadyStateReconnectWindow:  new(cfg.AcceptSteadyStateReconnectWindow),
			StratumMessagesPerMinute:          new(cfg.StratumMessagesPerMinute),
		},
		Difficulty: difficultyTuning{
			MaxDifficulty:                    new(cfg.MaxDifficulty),
			MinDifficulty:                    new(cfg.MinDifficulty),
			DefaultDifficulty:                new(cfg.DefaultDifficulty),
			TargetSharesPerMin:               new(cfg.TargetSharesPerMin),
			VarDiffEnabled:                   new(cfg.VarDiffEnabled),
			LockSuggestedDifficulty:          new(cfg.LockSuggestedDifficulty),
			EnforceSuggestedDifficultyLimits: new(cfg.EnforceSuggestedDifficultyLimits),
		},
		Mining: miningTuning{
			Extranonce2Size:           new(cfg.Extranonce2Size),
			TemplateExtraNonce2Size:   new(cfg.TemplateExtraNonce2Size),
			JobEntropy:                new(cfg.JobEntropy),
			CoinbaseScriptSigMaxBytes: new(cfg.CoinbaseScriptSigMaxBytes),
			DisablePoolJobEntropy:     new(false),
			DifficultyStepGranularity: new(cfg.DifficultyStepGranularity),
		},
		Hashrate: tuningHashrateConfig{
			HashrateEMATauSeconds:           new(cfg.HashrateEMATauSeconds),
			HashrateCumulativeEnabled:       new(cfg.HashrateCumulativeEnabled),
			HashrateRecentCumulativeEnabled: new(cfg.HashrateRecentCumulativeEnabled),
		},
		Stratum: tuningStratumConfig{
			FastDecodeEnabled:   new(cfg.StratumFastDecodeEnabled),
			FastEncodeEnabled:   new(cfg.StratumFastEncodeEnabled),
			TCPReadBufferBytes:  new(cfg.StratumTCPReadBufferBytes),
			TCPWriteBufferBytes: new(cfg.StratumTCPWriteBufferBytes),
		},
		PeerCleaning: peerCleaningTuning{
			Enabled:   new(cfg.PeerCleanupEnabled),
			MaxPingMs: new(cfg.PeerCleanupMaxPingMs),
			MinPeers:  new(cfg.PeerCleanupMinPeers),
		},
	}
}

func (cfg Config) Effective() EffectiveConfig {
	backblazeInterval := ""
	if cfg.BackblazeBackupIntervalSeconds > 0 {
		backblazeInterval = (time.Duration(cfg.BackblazeBackupIntervalSeconds) * time.Second).String()
	}

	return EffectiveConfig{
		ListenAddr:                        cfg.ListenAddr,
		StatusAddr:                        cfg.StatusAddr,
		StatusTLSAddr:                     cfg.StatusTLSAddr,
		StatusBrandName:                   cfg.StatusBrandName,
		StatusBrandDomain:                 cfg.StatusBrandDomain,
		StatusTagline:                     cfg.StatusTagline,
		FiatCurrency:                      cfg.FiatCurrency,
		PoolDonationAddress:               cfg.PoolDonationAddress,
		DiscordURL:                        cfg.DiscordURL,
		DiscordWorkerNotifyThresholdSec:   cfg.DiscordWorkerNotifyThresholdSeconds,
		GitHubURL:                         cfg.GitHubURL,
		ServerLocation:                    cfg.ServerLocation,
		StratumTLSListen:                  cfg.StratumTLSListen,
		StratumFastDecodeEnabled:          cfg.StratumFastDecodeEnabled,
		StratumFastEncodeEnabled:          cfg.StratumFastEncodeEnabled,
		SafeMode:                          cfg.SafeMode,
		CKPoolEmulate:                     cfg.CKPoolEmulate,
		StratumTCPReadBufferBytes:         cfg.StratumTCPReadBufferBytes,
		StratumTCPWriteBufferBytes:        cfg.StratumTCPWriteBufferBytes,
		ClerkIssuerURL:                    cfg.ClerkIssuerURL,
		ClerkJWKSURL:                      cfg.ClerkJWKSURL,
		ClerkSignInURL:                    cfg.ClerkSignInURL,
		ClerkCallbackPath:                 cfg.ClerkCallbackPath,
		ClerkFrontendAPIURL:               cfg.ClerkFrontendAPIURL,
		ClerkSessionCookieName:            cfg.ClerkSessionCookieName,
		RPCURL:                            cfg.RPCURL,
		RPCUser:                           cfg.RPCUser,
		RPCPassSet:                        strings.TrimSpace(cfg.RPCPass) != "",
		PayoutAddress:                     cfg.PayoutAddress,
		PoolFeePercent:                    cfg.PoolFeePercent,
		OperatorDonationPercent:           cfg.OperatorDonationPercent,
		OperatorDonationAddress:           cfg.OperatorDonationAddress,
		OperatorDonationName:              cfg.OperatorDonationName,
		OperatorDonationURL:               cfg.OperatorDonationURL,
		Extranonce2Size:                   cfg.Extranonce2Size,
		TemplateExtraNonce2Size:           cfg.TemplateExtraNonce2Size,
		JobEntropy:                        cfg.JobEntropy,
		PoolID:                            cfg.PoolEntropy,
		CoinbaseScriptSigMaxBytes:         cfg.CoinbaseScriptSigMaxBytes,
		ZMQHashBlockAddr:                  cfg.ZMQHashBlockAddr,
		ZMQRawBlockAddr:                   cfg.ZMQRawBlockAddr,
		BackblazeBackupEnabled:            cfg.BackblazeBackupEnabled,
		BackblazeBucket:                   cfg.BackblazeBucket,
		BackblazePrefix:                   cfg.BackblazePrefix,
		BackblazeBackupInterval:           backblazeInterval,
		BackblazeKeepLocalCopy:            cfg.BackblazeKeepLocalCopy,
		BackblazeForceEveryInterval:       cfg.BackblazeForceEveryInterval,
		BackupSnapshotPath:                cfg.BackupSnapshotPath,
		MaxConns:                          cfg.MaxConns,
		MaxAcceptsPerSecond:               cfg.MaxAcceptsPerSecond,
		MaxAcceptBurst:                    cfg.MaxAcceptBurst,
		DisableConnectRateLimits:          cfg.DisableConnectRateLimits,
		AutoAcceptRateLimits:              cfg.AutoAcceptRateLimits,
		AcceptReconnectWindow:             cfg.AcceptReconnectWindow,
		AcceptBurstWindow:                 cfg.AcceptBurstWindow,
		AcceptSteadyStateWindow:           cfg.AcceptSteadyStateWindow,
		AcceptSteadyStateRate:             cfg.AcceptSteadyStateRate,
		AcceptSteadyStateReconnectPercent: cfg.AcceptSteadyStateReconnectPercent,
		AcceptSteadyStateReconnectWindow:  cfg.AcceptSteadyStateReconnectWindow,
		StratumMessagesPerMinute:          cfg.StratumMessagesPerMinute,
		MaxRecentJobs:                     cfg.MaxRecentJobs,
		ConnectionTimeout:                 cfg.ConnectionTimeout.String(),
		VersionMask:                       uint32ToHex8Lower(cfg.VersionMask),
		MinVersionBits:                    cfg.MinVersionBits,
		ShareAllowDegradedVersionBits:     cfg.ShareAllowDegradedVersionBits,
		MaxDifficulty:                     cfg.MaxDifficulty,
		MinDifficulty:                     cfg.MinDifficulty,
		TargetSharesPerMin:                cfg.TargetSharesPerMin,
		VarDiffEnabled:                    cfg.VarDiffEnabled,
		// Effective config mirrors whether suggested difficulty locking is enabled.
		LockSuggestedDifficulty:          cfg.LockSuggestedDifficulty,
		DifficultyStepGranularity:        cfg.DifficultyStepGranularity,
		ShareJobFreshnessMode:            cfg.ShareJobFreshnessMode,
		ShareCheckNTimeWindow:            cfg.ShareCheckNTimeWindow,
		ShareCheckVersionRolling:         cfg.ShareCheckVersionRolling,
		ShareRequireAuthorizedConnection: cfg.ShareRequireAuthorizedConnection,
		ShareCheckParamFormat:            cfg.ShareCheckParamFormat,
		ShareRequireWorkerMatch:          cfg.ShareRequireWorkerMatch,
		SubmitProcessInline:              cfg.SubmitProcessInline,
			HashrateEMATauSeconds:            cfg.HashrateEMATauSeconds,
			ShareNTimeMaxForwardSeconds:      cfg.ShareNTimeMaxForwardSeconds,
			ShareCheckDuplicate:              cfg.ShareCheckDuplicate,
			LogDebug:                         cfg.LogDebug,
		LogNetDebug:                      cfg.LogNetDebug,
		CleanExpiredBansOnStartup:        cfg.CleanExpiredBansOnStartup,
		BanInvalidSubmissionsAfter:       cfg.BanInvalidSubmissionsAfter,
		BanInvalidSubmissionsWindow:      cfg.BanInvalidSubmissionsWindow.String(),
		BanInvalidSubmissionsDuration:    cfg.BanInvalidSubmissionsDuration.String(),
		ReconnectBanThreshold:            cfg.ReconnectBanThreshold,
		ReconnectBanWindowSeconds:        cfg.ReconnectBanWindowSeconds,
		ReconnectBanDurationSeconds:      cfg.ReconnectBanDurationSeconds,
		BannedMinerTypes:                 cfg.BannedMinerTypes,
		PeerCleanupEnabled:               cfg.PeerCleanupEnabled,
		PeerCleanupMaxPingMs:             cfg.PeerCleanupMaxPingMs,
		PeerCleanupMinPeers:              cfg.PeerCleanupMinPeers,
	}
}
