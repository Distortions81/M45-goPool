package main

import (
	"fmt"
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
			DiscordURL:                      cfg.DiscordURL,
			DiscordServerID:                 cfg.DiscordServerID,
			DiscordNotifyChannelID:          cfg.DiscordNotifyChannelID,
			GitHubURL:                       cfg.GitHubURL,
			ServerLocation:                  cfg.ServerLocation,
		},
		Stratum: stratumConfig{
			StratumTLSListen: cfg.StratumTLSListen,
		},
		Node: nodeConfig{
			RPCURL:           cfg.RPCURL,
			PayoutAddress:    cfg.PayoutAddress,
			ZMQHashBlockAddr: cfg.ZMQHashBlockAddr,
			ZMQRawBlockAddr:  cfg.ZMQRawBlockAddr,
			RPCCookiePath:    cfg.RPCCookiePath,
			AllowPublicRPC:   cfg.AllowPublicRPC,
		},
		Mining: miningConfig{
			PoolFeePercent:            float64Ptr(cfg.PoolFeePercent),
			OperatorDonationPercent:   float64Ptr(cfg.OperatorDonationPercent),
			OperatorDonationAddress:   cfg.OperatorDonationAddress,
			OperatorDonationName:      cfg.OperatorDonationName,
			OperatorDonationURL:       cfg.OperatorDonationURL,
			Extranonce2Size:           intPtr(cfg.Extranonce2Size),
			TemplateExtraNonce2Size:   intPtr(cfg.TemplateExtraNonce2Size),
			JobEntropy:                intPtr(cfg.JobEntropy),
			PoolEntropy:               stringPtr(cfg.PoolEntropy),
			PoolTagPrefix:             cfg.PoolTagPrefix,
			CoinbaseScriptSigMaxBytes: intPtr(cfg.CoinbaseScriptSigMaxBytes),
			SoloMode:                  boolPtr(cfg.SoloMode),
			DirectSubmitProcessing:    boolPtr(cfg.DirectSubmitProcessing),
			CheckDuplicateShares:      boolPtr(cfg.CheckDuplicateShares),
		},
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
			IntervalSeconds:    intPtr(cfg.BackblazeBackupIntervalSeconds),
			KeepLocalCopy:      boolPtr(cfg.BackblazeKeepLocalCopy),
			ForceEveryInterval: boolPtr(cfg.BackblazeForceEveryInterval),
			SnapshotPath:       cfg.BackupSnapshotPath,
		},
		Logging: loggingConfig{
			Level: cfg.LogLevel,
		},
	}
}

func buildTuningFileConfig(cfg Config) tuningFileConfig {
	return tuningFileConfig{
		RateLimits: rateLimitTuning{
			MaxConns:                          intPtr(cfg.MaxConns),
			MaxAcceptsPerSecond:               intPtr(cfg.MaxAcceptsPerSecond),
			MaxAcceptBurst:                    intPtr(cfg.MaxAcceptBurst),
			AutoAcceptRateLimits:              boolPtr(cfg.AutoAcceptRateLimits),
			AcceptReconnectWindow:             intPtr(cfg.AcceptReconnectWindow),
			AcceptBurstWindow:                 intPtr(cfg.AcceptBurstWindow),
			AcceptSteadyStateWindow:           intPtr(cfg.AcceptSteadyStateWindow),
			AcceptSteadyStateRate:             intPtr(cfg.AcceptSteadyStateRate),
			AcceptSteadyStateReconnectPercent: float64Ptr(cfg.AcceptSteadyStateReconnectPercent),
			AcceptSteadyStateReconnectWindow:  intPtr(cfg.AcceptSteadyStateReconnectWindow),
			StratumMessagesPerMinute:          intPtr(cfg.StratumMessagesPerMinute),
		},
		Timeouts: timeoutTuning{
			ConnectionTimeoutSec: intPtr(int(cfg.ConnectionTimeout / time.Second)),
		},
		Difficulty: difficultyTuning{
			MaxDifficulty:                    float64Ptr(cfg.MaxDifficulty),
			MinDifficulty:                    float64Ptr(cfg.MinDifficulty),
			DefaultDifficulty:                float64Ptr(cfg.DefaultDifficulty),
			LockSuggestedDifficulty:          boolPtr(cfg.LockSuggestedDifficulty),
			EnforceSuggestedDifficultyLimits: boolPtr(cfg.EnforceSuggestedDifficultyLimits),
		},
		Mining: miningTuning{
			DisablePoolJobEntropy: boolPtr(false),
			VardiffFine:           boolPtr(cfg.VardiffFine),
		},
		Hashrate: hashrateTuning{
			HashrateEMATauSeconds:    float64Ptr(cfg.HashrateEMATauSeconds),
			HashrateEMAMinShares:     intPtr(cfg.HashrateEMAMinShares),
			NTimeForwardSlackSeconds: intPtr(cfg.NTimeForwardSlackSeconds),
		},
		Discord: discordTuning{
			WorkerNotifyThresholdSeconds: intPtr(cfg.DiscordWorkerNotifyThresholdSeconds),
		},
		Status: statusTuning{
			MempoolAddressURL: stringPtr(cfg.MempoolAddressURL),
		},
		PeerCleaning: peerCleaningTuning{
			Enabled:   boolPtr(cfg.PeerCleanupEnabled),
			MaxPingMs: float64Ptr(cfg.PeerCleanupMaxPingMs),
			MinPeers:  intPtr(cfg.PeerCleanupMinPeers),
		},
		Bans: banTuning{
			CleanExpiredOnStartup:            boolPtr(cfg.CleanExpiredBansOnStartup),
			BanInvalidSubmissionsAfter:       intPtr(cfg.BanInvalidSubmissionsAfter),
			BanInvalidSubmissionsWindowSec:   intPtr(int(cfg.BanInvalidSubmissionsWindow / time.Second)),
			BanInvalidSubmissionsDurationSec: intPtr(int(cfg.BanInvalidSubmissionsDuration / time.Second)),
			ReconnectBanThreshold:            intPtr(cfg.ReconnectBanThreshold),
			ReconnectBanWindowSeconds:        intPtr(cfg.ReconnectBanWindowSeconds),
			ReconnectBanDurationSeconds:      intPtr(cfg.ReconnectBanDurationSeconds),
			BannedMinerTypes:                 cfg.BannedMinerTypes,
		},
		Version: versionTuning{
			MinVersionBits:       intPtr(cfg.MinVersionBits),
			IgnoreMinVersionBits: boolPtr(cfg.IgnoreMinVersionBits),
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
		VersionMask:                       fmt.Sprintf("%08x", cfg.VersionMask),
		MinVersionBits:                    cfg.MinVersionBits,
		IgnoreMinVersionBits:              cfg.IgnoreMinVersionBits,
		MaxDifficulty:                     cfg.MaxDifficulty,
		MinDifficulty:                     cfg.MinDifficulty,
		// Effective config mirrors whether suggested difficulty locking is enabled.
		LockSuggestedDifficulty:       cfg.LockSuggestedDifficulty,
		VardiffFine:                   cfg.VardiffFine,
		SoloMode:                      cfg.SoloMode,
		DirectSubmitProcessing:        cfg.DirectSubmitProcessing,
		HashrateEMATauSeconds:         cfg.HashrateEMATauSeconds,
		HashrateEMAMinShares:          cfg.HashrateEMAMinShares,
		NTimeForwardSlackSec:          cfg.NTimeForwardSlackSeconds,
		CheckDuplicateShares:          cfg.CheckDuplicateShares,
		LogLevel:                      cfg.LogLevel,
		CleanExpiredBansOnStartup:     cfg.CleanExpiredBansOnStartup,
		BanInvalidSubmissionsAfter:    cfg.BanInvalidSubmissionsAfter,
		BanInvalidSubmissionsWindow:   cfg.BanInvalidSubmissionsWindow.String(),
		BanInvalidSubmissionsDuration: cfg.BanInvalidSubmissionsDuration.String(),
		ReconnectBanThreshold:         cfg.ReconnectBanThreshold,
		ReconnectBanWindowSeconds:     cfg.ReconnectBanWindowSeconds,
		ReconnectBanDurationSeconds:   cfg.ReconnectBanDurationSeconds,
		BannedMinerTypes:              cfg.BannedMinerTypes,
		PeerCleanupEnabled:            cfg.PeerCleanupEnabled,
		PeerCleanupMaxPingMs:          cfg.PeerCleanupMaxPingMs,
		PeerCleanupMinPeers:           cfg.PeerCleanupMinPeers,
	}
}
