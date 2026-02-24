package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml"
)

func loadConfig(configPath, secretsPath string) (Config, string) {
	cfg := defaultConfig()

	if configPath == "" {
		configPath = defaultConfigPath()
	}

	var configFileExisted bool
	var needsRewrite bool
	var needsServicesMigration bool
	if bc, ok, err := loadBaseConfigFile(configPath); err != nil {
		fatal("config file", err, "path", configPath)
	} else if ok {
		configFileExisted = true
		needsRewrite, needsServicesMigration = applyBaseConfig(&cfg, *bc)
	} else {
		examplePath := filepath.Join(cfg.DataDir, "config", "examples", "config.toml.example")
		ensureExampleFiles(cfg.DataDir)

		fmt.Printf("\n📝 Configuration file is missing: %s\n\n", configPath)
		fmt.Printf("   To get started:\n")
		fmt.Printf("   1. Copy the example: %s\n", examplePath)
		fmt.Printf("   2. To:               %s\n", configPath)
		fmt.Printf("   3. Edit the file and set your payout_address (required)\n")
		fmt.Printf("   4. Configure other settings as needed\n")
		fmt.Printf("   5. Restart goPool\n\n")

		os.Exit(1)
	}
	ensureExampleFiles(cfg.DataDir)

	if cfg.PoolEntropy == "" {
		cfg.PoolEntropy = generatePoolEntropy()
		needsRewrite = true
	}

	blacklistPath := filepath.Join(cfg.DataDir, "config", "miner_blacklist.json")
	if entries, err := loadMinerTypeBlacklist(blacklistPath); err != nil {
		logger.Warn("load miner blacklist failed", "path", blacklistPath, "error", err)
	} else if len(entries) > 0 {
		cfg.BannedMinerTypes = entries
		logger.Info("loaded miner blacklist", "path", blacklistPath, "count", len(entries))
	}

	if needsRewrite && configFileExisted {
		if err := rewriteConfigFile(configPath, cfg); err != nil {
			logger.Warn("rewrite config file", "path", configPath, "error", err)
		} else if cfg.PoolEntropy != "" {
			logger.Info("rewrote config file", "path", configPath)
		}
	}

	if secretsPath == "" {
		secretsPath = filepath.Join(cfg.DataDir, "config", "secrets.toml")
	}
	ensureSecretFilePermissions(secretsPath)
	if sc, ok, err := loadSecretsFile(secretsPath); err != nil {
		fatal("secrets file", err, "path", secretsPath)
	} else if ok {
		applySecretsConfig(&cfg, *sc)
	}

	// Optional advanced overlays. These files are intentionally absent by default.
	configDir := filepath.Join(cfg.DataDir, "config")
	servicesPath := filepath.Join(configDir, "services.toml")
	policyPath := filepath.Join(configDir, "policy.toml")
	tuningPath := filepath.Join(configDir, "tuning.toml")
	var runtimeOverrides fileOverrideConfig
	var tuningConfigLoaded bool
	var servicesConfigLoaded bool
	if sf, ok, err := loadServicesFile(servicesPath); err != nil {
		fatal("services config file", err, "path", servicesPath)
	} else if ok {
		applyServicesConfig(&cfg, *sf)
		servicesConfigLoaded = true
	}
	if pf, ok, err := loadPolicyFile(policyPath); err != nil {
		fatal("policy config file", err, "path", policyPath)
	} else if ok {
		applyPolicyConfig(&cfg, *pf)
	}
	if pf, ok, err := loadTuningFile(tuningPath); err != nil {
		fatal("tuning config file", err, "path", tuningPath)
	} else if ok {
		applyTuningConfig(&cfg, *pf)
		tuningConfigLoaded = true
		runtimeOverrides.RateLimits = pf.RateLimits
	}

	// Sanitize payout address to strip stray whitespace or unexpected
	// characters before it is used for RPC validation and coinbase outputs.
	cfg.PayoutAddress = sanitizePayoutAddress(cfg.PayoutAddress)
	cfg.MempoolAddressURL = normalizeMempoolAddressURL(cfg.MempoolAddressURL)

	// Auto-configure accept rate limits based on max_conns if they weren't
	// explicitly set in the config file. This ensures miners can reconnect
	// smoothly after pool restarts without hitting rate limits.
	autoConfigureAcceptRateLimits(&cfg, runtimeOverrides, tuningConfigLoaded)

	if needsServicesMigration && !servicesConfigLoaded && configFileExisted {
		if err := rewriteServicesFile(servicesPath, cfg); err != nil {
			logger.Warn("rewrite services file", "path", servicesPath, "error", err)
		} else {
			logger.Warn("migrated legacy settings into services.toml", "path", servicesPath)
		}
	}

	return cfg, secretsPath
}

func loadTOMLFile[T any](path string) (*T, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read %s: %w", path, err)
	}

	var cfg T
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, true, fmt.Errorf("parse %s: %w", path, err)
	}

	return &cfg, true, nil
}

func loadBaseConfigFile(path string) (*baseFileConfigRead, bool, error) {
	return loadTOMLFile[baseFileConfigRead](path)
}

func loadPolicyFile(path string) (*policyFileConfig, bool, error) {
	return loadTOMLFile[policyFileConfig](path)
}

func loadServicesFile(path string) (*servicesFileConfig, bool, error) {
	return loadTOMLFile[servicesFileConfig](path)
}

func loadTuningFile(path string) (*tuningFileConfig, bool, error) {
	return loadTOMLFile[tuningFileConfig](path)
}

func loadSecretsFile(path string) (*secretsConfig, bool, error) {
	return loadTOMLFile[secretsConfig](path)
}

func ensureSecretFilePermissions(path string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logger.Warn("secrets file stat failed", "path", path, "error", err)
		}
		return
	}
	if !info.Mode().IsRegular() {
		return
	}
	if info.Mode().Perm()&0o077 == 0 {
		return
	}
	if err := os.Chmod(path, 0o600); err != nil {
		logger.Warn("secrets file chmod failed", "path", path, "error", err)
		return
	}
	logger.Warn("secrets file permissions tightened", "path", path, "mode", "0600")
}

func applyBaseConfig(cfg *Config, fc baseFileConfigRead) (configChanged bool, migratedServices bool) {
	if fc.Server.PoolListen != "" {
		cfg.ListenAddr = fc.Server.PoolListen
	}
	if fc.Server.StatusListen != "" {
		cfg.StatusAddr = fc.Server.StatusListen
	}
	if fc.Server.StatusTLSListen != nil {
		cfg.StatusTLSAddr = *fc.Server.StatusTLSListen
	}
	if fc.Server.StatusPublicURL != "" {
		cfg.StatusPublicURL = strings.TrimSpace(fc.Server.StatusPublicURL)
	}
	if fc.Branding.StatusBrandName != "" {
		cfg.StatusBrandName = fc.Branding.StatusBrandName
	}
	if fc.Branding.StatusBrandDomain != "" {
		cfg.StatusBrandDomain = fc.Branding.StatusBrandDomain
	}
	if fc.Branding.StatusTagline != "" {
		cfg.StatusTagline = fc.Branding.StatusTagline
	}
	if fc.Branding.StatusConnectMinerTitleExtra != "" {
		cfg.StatusConnectMinerTitleExtra = strings.TrimSpace(fc.Branding.StatusConnectMinerTitleExtra)
	}
	if fc.Branding.StatusConnectMinerTitleExtraURL != "" {
		cfg.StatusConnectMinerTitleExtraURL = strings.TrimSpace(fc.Branding.StatusConnectMinerTitleExtraURL)
	}
	if fc.Branding.FiatCurrency != "" {
		cfg.FiatCurrency = strings.ToLower(strings.TrimSpace(fc.Branding.FiatCurrency))
	}
	if fc.Branding.PoolDonationAddress != "" {
		cfg.PoolDonationAddress = strings.TrimSpace(fc.Branding.PoolDonationAddress)
	}
	if fc.Branding.ServerLocation != "" {
		cfg.ServerLocation = strings.TrimSpace(fc.Branding.ServerLocation)
	}
	if fc.Stratum.StratumTLSListen != "" {
		addr := strings.TrimSpace(fc.Stratum.StratumTLSListen)
		if addr != "" && !strings.Contains(addr, ":") {
			addr = ":" + addr
		}
		cfg.StratumTLSListen = addr
	}
	if fc.Stratum.StratumV2Listen != "" {
		addr := strings.TrimSpace(fc.Stratum.StratumV2Listen)
		if addr != "" && !strings.Contains(addr, ":") {
			addr = ":" + addr
		}
		cfg.StratumV2Listen = addr
	}
	cfg.StratumPasswordEnabled = fc.Stratum.StratumPasswordEnabled
	if fc.Stratum.StratumPassword != "" {
		cfg.StratumPassword = strings.TrimSpace(fc.Stratum.StratumPassword)
	} else {
		cfg.StratumPassword = ""
	}
	cfg.StratumPasswordPublic = fc.Stratum.StratumPasswordPublic
	cfg.SafeMode = fc.Stratum.SafeMode
	if fc.Node.RPCURL != "" {
		cfg.RPCURL = fc.Node.RPCURL
	}
	if fc.Node.PayoutAddress != "" {
		cfg.PayoutAddress = fc.Node.PayoutAddress
	}
	if fc.Node.ZMQLegacyBlockAddr != "" && fc.Node.ZMQHashBlockAddr == "" && fc.Node.ZMQRawBlockAddr == "" {
		legacy := strings.TrimSpace(fc.Node.ZMQLegacyBlockAddr)
		if legacy != "" {
			logger.Warn("node.zmq_block_addr is deprecated; migrating to node.zmq_hashblock_addr/node.zmq_rawblock_addr", "addr", legacy)
			cfg.ZMQHashBlockAddr = legacy
			cfg.ZMQRawBlockAddr = legacy
			configChanged = true
		}
	}
	if fc.Node.ZMQHashBlockAddr != "" {
		cfg.ZMQHashBlockAddr = fc.Node.ZMQHashBlockAddr
	}
	if fc.Node.ZMQRawBlockAddr != "" {
		cfg.ZMQRawBlockAddr = fc.Node.ZMQRawBlockAddr
	}
	cookiePath := strings.TrimSpace(fc.Node.RPCCookiePath)
	cfg.rpCCookiePathFromConfig = cookiePath
	if cookiePath != "" {
		cfg.RPCCookiePath = cookiePath
	}
	if fc.Mining.PoolFeePercent != nil {
		cfg.PoolFeePercent = *fc.Mining.PoolFeePercent
	}
	if fc.Mining.OperatorDonationPercent != nil {
		cfg.OperatorDonationPercent = *fc.Mining.OperatorDonationPercent
	}
	if fc.Mining.OperatorDonationAddress != "" {
		cfg.OperatorDonationAddress = strings.TrimSpace(fc.Mining.OperatorDonationAddress)
	}
	if fc.Mining.OperatorDonationName != "" {
		cfg.OperatorDonationName = strings.TrimSpace(fc.Mining.OperatorDonationName)
	}
	if fc.Mining.OperatorDonationURL != "" {
		cfg.OperatorDonationURL = strings.TrimSpace(fc.Mining.OperatorDonationURL)
	}
	if fc.Mining.PoolEntropy != nil {
		cfg.PoolEntropy = *fc.Mining.PoolEntropy
	}
	if fc.Mining.PoolTagPrefix != "" {
		cfg.PoolTagPrefix = filterAlphanumeric(strings.TrimSpace(fc.Mining.PoolTagPrefix))
	}
	if fc.Logging.Debug != nil {
		cfg.LogDebug = *fc.Logging.Debug
	}
	if fc.Logging.NetDebug != nil {
		cfg.LogNetDebug = *fc.Logging.NetDebug
	}

	// Legacy config.toml -> services.toml migration:
	// old [auth], [backblaze_backup], and [branding].discord_* fields.
	if fc.Auth.ClerkIssuerURL != "" {
		cfg.ClerkIssuerURL = strings.TrimSpace(fc.Auth.ClerkIssuerURL)
		migratedServices = true
	}
	if fc.Auth.ClerkJWKSURL != "" {
		cfg.ClerkJWKSURL = strings.TrimSpace(fc.Auth.ClerkJWKSURL)
		migratedServices = true
	}
	if fc.Auth.ClerkSignInURL != "" {
		cfg.ClerkSignInURL = strings.TrimSpace(fc.Auth.ClerkSignInURL)
		migratedServices = true
	}
	if fc.Auth.ClerkCallbackPath != "" {
		cfg.ClerkCallbackPath = strings.TrimSpace(fc.Auth.ClerkCallbackPath)
		migratedServices = true
	}
	if fc.Auth.ClerkFrontendAPIURL != "" {
		cfg.ClerkFrontendAPIURL = strings.TrimSpace(fc.Auth.ClerkFrontendAPIURL)
		migratedServices = true
	}
	if fc.Auth.ClerkSessionCookieName != "" {
		cfg.ClerkSessionCookieName = strings.TrimSpace(fc.Auth.ClerkSessionCookieName)
		migratedServices = true
	}
	if fc.Auth.ClerkSessionAudience != "" {
		cfg.ClerkSessionAudience = strings.TrimSpace(fc.Auth.ClerkSessionAudience)
		migratedServices = true
	}
	if fc.Backblaze.Enabled || strings.TrimSpace(fc.Backblaze.Bucket) != "" || strings.TrimSpace(fc.Backblaze.Prefix) != "" || fc.Backblaze.IntervalSeconds != nil || fc.Backblaze.KeepLocalCopy != nil || fc.Backblaze.ForceEveryInterval != nil || strings.TrimSpace(fc.Backblaze.SnapshotPath) != "" {
		cfg.BackblazeBackupEnabled = fc.Backblaze.Enabled
		if fc.Backblaze.Bucket != "" {
			cfg.BackblazeBucket = strings.TrimSpace(fc.Backblaze.Bucket)
		}
		if fc.Backblaze.Prefix != "" {
			cfg.BackblazePrefix = strings.TrimSpace(fc.Backblaze.Prefix)
		}
		if fc.Backblaze.IntervalSeconds != nil && *fc.Backblaze.IntervalSeconds > 0 {
			cfg.BackblazeBackupIntervalSeconds = *fc.Backblaze.IntervalSeconds
		}
		if fc.Backblaze.KeepLocalCopy != nil {
			cfg.BackblazeKeepLocalCopy = *fc.Backblaze.KeepLocalCopy
		}
		if fc.Backblaze.ForceEveryInterval != nil {
			cfg.BackblazeForceEveryInterval = *fc.Backblaze.ForceEveryInterval
		}
		if strings.TrimSpace(fc.Backblaze.SnapshotPath) != "" {
			cfg.BackupSnapshotPath = strings.TrimSpace(fc.Backblaze.SnapshotPath)
		}
		migratedServices = true
	}
	if fc.Branding.DiscordURL != "" {
		cfg.DiscordURL = strings.TrimSpace(fc.Branding.DiscordURL)
		migratedServices = true
	}
	if fc.Branding.DiscordServerID != "" {
		cfg.DiscordServerID = strings.TrimSpace(fc.Branding.DiscordServerID)
		migratedServices = true
	}
	if fc.Branding.DiscordNotifyChannelID != "" {
		cfg.DiscordNotifyChannelID = strings.TrimSpace(fc.Branding.DiscordNotifyChannelID)
		migratedServices = true
	}
	if migratedServices {
		logger.Warn("legacy services settings detected in config.toml; migrate them to services.toml")
		configChanged = true
	}
	return configChanged, migratedServices
}

func applyServicesConfig(cfg *Config, fc servicesFileConfig) {
	if fc.Auth.ClerkIssuerURL != "" {
		cfg.ClerkIssuerURL = strings.TrimSpace(fc.Auth.ClerkIssuerURL)
	}
	if fc.Auth.ClerkJWKSURL != "" {
		cfg.ClerkJWKSURL = strings.TrimSpace(fc.Auth.ClerkJWKSURL)
	}
	if fc.Auth.ClerkSignInURL != "" {
		cfg.ClerkSignInURL = strings.TrimSpace(fc.Auth.ClerkSignInURL)
	}
	if fc.Auth.ClerkCallbackPath != "" {
		cfg.ClerkCallbackPath = strings.TrimSpace(fc.Auth.ClerkCallbackPath)
	}
	if fc.Auth.ClerkFrontendAPIURL != "" {
		cfg.ClerkFrontendAPIURL = strings.TrimSpace(fc.Auth.ClerkFrontendAPIURL)
	}
	if fc.Auth.ClerkSessionCookieName != "" {
		cfg.ClerkSessionCookieName = strings.TrimSpace(fc.Auth.ClerkSessionCookieName)
	}
	if fc.Auth.ClerkSessionAudience != "" {
		cfg.ClerkSessionAudience = strings.TrimSpace(fc.Auth.ClerkSessionAudience)
	}
	cfg.BackblazeBackupEnabled = fc.Backblaze.Enabled
	if fc.Backblaze.Bucket != "" {
		cfg.BackblazeBucket = strings.TrimSpace(fc.Backblaze.Bucket)
	}
	if fc.Backblaze.Prefix != "" {
		cfg.BackblazePrefix = strings.TrimSpace(fc.Backblaze.Prefix)
	}
	if fc.Backblaze.IntervalSeconds != nil && *fc.Backblaze.IntervalSeconds > 0 {
		cfg.BackblazeBackupIntervalSeconds = *fc.Backblaze.IntervalSeconds
	}
	if fc.Backblaze.KeepLocalCopy != nil {
		cfg.BackblazeKeepLocalCopy = *fc.Backblaze.KeepLocalCopy
	}
	if fc.Backblaze.ForceEveryInterval != nil {
		cfg.BackblazeForceEveryInterval = *fc.Backblaze.ForceEveryInterval
	}
	if strings.TrimSpace(fc.Backblaze.SnapshotPath) != "" {
		cfg.BackupSnapshotPath = strings.TrimSpace(fc.Backblaze.SnapshotPath)
	}
	if fc.Discord.DiscordURL != "" {
		cfg.DiscordURL = strings.TrimSpace(fc.Discord.DiscordURL)
	}
	if fc.Discord.DiscordServerID != "" {
		cfg.DiscordServerID = strings.TrimSpace(fc.Discord.DiscordServerID)
	}
	if fc.Discord.DiscordNotifyChannelID != "" {
		cfg.DiscordNotifyChannelID = strings.TrimSpace(fc.Discord.DiscordNotifyChannelID)
	}
	if fc.Discord.WorkerNotifyThresholdSeconds != nil && *fc.Discord.WorkerNotifyThresholdSeconds > 0 {
		cfg.DiscordWorkerNotifyThresholdSeconds = *fc.Discord.WorkerNotifyThresholdSeconds
	}
	if strings.TrimSpace(fc.Status.MempoolAddressURL) != "" {
		cfg.MempoolAddressURL = strings.TrimSpace(fc.Status.MempoolAddressURL)
	}
	if strings.TrimSpace(fc.Status.GitHubURL) != "" {
		cfg.GitHubURL = strings.TrimSpace(fc.Status.GitHubURL)
	}
}

func applyFileOverrides(cfg *Config, fc fileOverrideConfig) {
	if fc.RateLimits.MaxConns != nil {
		cfg.MaxConns = *fc.RateLimits.MaxConns
	}
	if fc.RateLimits.MaxAcceptsPerSecond != nil {
		cfg.MaxAcceptsPerSecond = *fc.RateLimits.MaxAcceptsPerSecond
	}
	if fc.RateLimits.MaxAcceptBurst != nil {
		cfg.MaxAcceptBurst = *fc.RateLimits.MaxAcceptBurst
	}
	if fc.RateLimits.DisableConnectRateLimits != nil {
		cfg.DisableConnectRateLimits = *fc.RateLimits.DisableConnectRateLimits
	}
	if fc.RateLimits.AutoAcceptRateLimits != nil {
		cfg.AutoAcceptRateLimits = *fc.RateLimits.AutoAcceptRateLimits
	}
	if fc.RateLimits.AcceptReconnectWindow != nil {
		cfg.AcceptReconnectWindow = *fc.RateLimits.AcceptReconnectWindow
	}
	if fc.RateLimits.AcceptBurstWindow != nil {
		cfg.AcceptBurstWindow = *fc.RateLimits.AcceptBurstWindow
	}
	if fc.RateLimits.AcceptSteadyStateWindow != nil {
		cfg.AcceptSteadyStateWindow = *fc.RateLimits.AcceptSteadyStateWindow
	}
	if fc.RateLimits.AcceptSteadyStateRate != nil {
		cfg.AcceptSteadyStateRate = *fc.RateLimits.AcceptSteadyStateRate
	}
	if fc.RateLimits.AcceptSteadyStateReconnectPercent != nil {
		cfg.AcceptSteadyStateReconnectPercent = *fc.RateLimits.AcceptSteadyStateReconnectPercent
	}
	if fc.RateLimits.AcceptSteadyStateReconnectWindow != nil {
		cfg.AcceptSteadyStateReconnectWindow = *fc.RateLimits.AcceptSteadyStateReconnectWindow
	}
	if fc.RateLimits.StratumMessagesPerMinute != nil {
		cfg.StratumMessagesPerMinute = *fc.RateLimits.StratumMessagesPerMinute
	}
	if fc.Timeouts.ConnectionTimeoutSec != nil {
		cfg.ConnectionTimeout = time.Duration(*fc.Timeouts.ConnectionTimeoutSec) * time.Second
	}
	if fc.Difficulty.MaxDifficulty != nil {
		cfg.MaxDifficulty = *fc.Difficulty.MaxDifficulty
	}
	if fc.Difficulty.MinDifficulty != nil {
		cfg.MinDifficulty = *fc.Difficulty.MinDifficulty
	}
	if fc.Difficulty.DefaultDifficulty != nil {
		cfg.DefaultDifficulty = *fc.Difficulty.DefaultDifficulty
	}
	if fc.Difficulty.TargetSharesPerMin != nil && *fc.Difficulty.TargetSharesPerMin > 0 {
		cfg.TargetSharesPerMin = *fc.Difficulty.TargetSharesPerMin
	}
	if fc.Difficulty.VarDiffEnabled != nil {
		cfg.VarDiffEnabled = *fc.Difficulty.VarDiffEnabled
	}
	if fc.Difficulty.LockSuggestedDifficulty != nil {
		cfg.LockSuggestedDifficulty = *fc.Difficulty.LockSuggestedDifficulty
	}
	if fc.Difficulty.EnforceSuggestedDifficultyLimits != nil {
		cfg.EnforceSuggestedDifficultyLimits = *fc.Difficulty.EnforceSuggestedDifficultyLimits
	}
	if fc.Mining.DisablePoolJobEntropy != nil && *fc.Mining.DisablePoolJobEntropy {
		// Disables coinbase "<pool entropy>-<job entropy>" suffix by bypassing
		// the suffix builder (which is gated on JobEntropy > 0).
		cfg.JobEntropy = 0
	}
	if fc.Mining.Extranonce2Size != nil {
		cfg.Extranonce2Size = *fc.Mining.Extranonce2Size
	}
	if fc.Mining.TemplateExtraNonce2Size != nil {
		cfg.TemplateExtraNonce2Size = *fc.Mining.TemplateExtraNonce2Size
	}
	if fc.Mining.JobEntropy != nil {
		cfg.JobEntropy = *fc.Mining.JobEntropy
	}
	if fc.Mining.CoinbaseScriptSigMaxBytes != nil {
		cfg.CoinbaseScriptSigMaxBytes = *fc.Mining.CoinbaseScriptSigMaxBytes
	}
	if fc.Mining.DifficultyStepGranularity != nil && *fc.Mining.DifficultyStepGranularity > 0 {
		cfg.DifficultyStepGranularity = *fc.Mining.DifficultyStepGranularity
	}
	if fc.Hashrate.HashrateEMATauSeconds != nil && *fc.Hashrate.HashrateEMATauSeconds > 0 {
		cfg.HashrateEMATauSeconds = *fc.Hashrate.HashrateEMATauSeconds
	}
	if fc.Hashrate.HashrateCumulativeEnabled != nil {
		cfg.HashrateCumulativeEnabled = *fc.Hashrate.HashrateCumulativeEnabled
	}
	if fc.Hashrate.HashrateRecentCumulativeEnabled != nil {
		cfg.HashrateRecentCumulativeEnabled = *fc.Hashrate.HashrateRecentCumulativeEnabled
	}
	if fc.Hashrate.ShareNTimeMaxForwardSeconds != nil && *fc.Hashrate.ShareNTimeMaxForwardSeconds > 0 {
		cfg.ShareNTimeMaxForwardSeconds = *fc.Hashrate.ShareNTimeMaxForwardSeconds
	}
	if fc.PeerCleaning.Enabled != nil {
		cfg.PeerCleanupEnabled = *fc.PeerCleaning.Enabled
	}
	if fc.PeerCleaning.MaxPingMs != nil && *fc.PeerCleaning.MaxPingMs >= 0 {
		cfg.PeerCleanupMaxPingMs = *fc.PeerCleaning.MaxPingMs
	}
	if fc.PeerCleaning.MinPeers != nil && *fc.PeerCleaning.MinPeers >= 0 {
		cfg.PeerCleanupMinPeers = *fc.PeerCleaning.MinPeers
	}
	if fc.Bans.CleanExpiredOnStartup != nil {
		cfg.CleanExpiredBansOnStartup = *fc.Bans.CleanExpiredOnStartup
	}
	if fc.Bans.BanInvalidSubmissionsAfter != nil && *fc.Bans.BanInvalidSubmissionsAfter >= 0 {
		cfg.BanInvalidSubmissionsAfter = *fc.Bans.BanInvalidSubmissionsAfter
	}
	if fc.Bans.BanInvalidSubmissionsWindowSec != nil && *fc.Bans.BanInvalidSubmissionsWindowSec > 0 {
		cfg.BanInvalidSubmissionsWindow = time.Duration(*fc.Bans.BanInvalidSubmissionsWindowSec) * time.Second
	}
	if fc.Bans.BanInvalidSubmissionsDurationSec != nil && *fc.Bans.BanInvalidSubmissionsDurationSec > 0 {
		cfg.BanInvalidSubmissionsDuration = time.Duration(*fc.Bans.BanInvalidSubmissionsDurationSec) * time.Second
	}
	if fc.Bans.ReconnectBanThreshold != nil && *fc.Bans.ReconnectBanThreshold >= 0 {
		cfg.ReconnectBanThreshold = *fc.Bans.ReconnectBanThreshold
	}
	if fc.Bans.ReconnectBanWindowSeconds != nil && *fc.Bans.ReconnectBanWindowSeconds > 0 {
		cfg.ReconnectBanWindowSeconds = *fc.Bans.ReconnectBanWindowSeconds
	}
	if fc.Bans.ReconnectBanDurationSeconds != nil && *fc.Bans.ReconnectBanDurationSeconds > 0 {
		cfg.ReconnectBanDurationSeconds = *fc.Bans.ReconnectBanDurationSeconds
	}
	if fc.Bans.BannedMinerTypes != nil {
		cfg.BannedMinerTypes = fc.Bans.BannedMinerTypes
	}
	if fc.Version.MinVersionBits != nil {
		cfg.MinVersionBits = *fc.Version.MinVersionBits
	}
	if fc.Version.ShareAllowDegradedVersionBits != nil {
		cfg.ShareAllowDegradedVersionBits = *fc.Version.ShareAllowDegradedVersionBits
	}
}

func applyPolicyConfig(cfg *Config, fc policyFileConfig) {
	if fc.Stratum.CKPoolEmulate != nil {
		cfg.CKPoolEmulate = *fc.Stratum.CKPoolEmulate
	}
	if fc.Mining.ShareJobFreshnessMode != nil {
		mode := normalizeShareJobFreshnessMode(*fc.Mining.ShareJobFreshnessMode)
		if mode >= 0 {
			cfg.ShareJobFreshnessMode = mode
		}
	}
	if fc.Mining.ShareCheckNTimeWindow != nil {
		cfg.ShareCheckNTimeWindow = *fc.Mining.ShareCheckNTimeWindow
	}
	if fc.Mining.ShareCheckVersionRolling != nil {
		cfg.ShareCheckVersionRolling = *fc.Mining.ShareCheckVersionRolling
	}
	if fc.Mining.ShareRequireAuthorizedConnection != nil {
		cfg.ShareRequireAuthorizedConnection = *fc.Mining.ShareRequireAuthorizedConnection
	}
	if fc.Mining.ShareCheckParamFormat != nil {
		cfg.ShareCheckParamFormat = *fc.Mining.ShareCheckParamFormat
	}
	if fc.Mining.ShareRequireWorkerMatch != nil {
		cfg.ShareRequireWorkerMatch = *fc.Mining.ShareRequireWorkerMatch
	}
	if fc.Mining.SubmitProcessInline != nil {
		cfg.SubmitProcessInline = *fc.Mining.SubmitProcessInline
	}
	if fc.Mining.ShareCheckDuplicate != nil {
		cfg.ShareCheckDuplicate = *fc.Mining.ShareCheckDuplicate
	}
	if fc.Mining.ShareRequireJobID != nil {
		cfg.ShareRequireJobID = *fc.Mining.ShareRequireJobID
	}
	if fc.Hashrate.ShareNTimeMaxForwardSeconds != nil && *fc.Hashrate.ShareNTimeMaxForwardSeconds > 0 {
		cfg.ShareNTimeMaxForwardSeconds = *fc.Hashrate.ShareNTimeMaxForwardSeconds
	}
	t := fileOverrideConfig{
		Version:  fc.Version,
		Bans:     fc.Bans,
		Timeouts: fc.Timeouts,
	}
	applyFileOverrides(cfg, t)
}

func applyTuningConfig(cfg *Config, fc tuningFileConfig) {
	if fc.Stratum.FastDecodeEnabled != nil {
		cfg.StratumFastDecodeEnabled = *fc.Stratum.FastDecodeEnabled
	}
	if fc.Stratum.FastEncodeEnabled != nil {
		cfg.StratumFastEncodeEnabled = *fc.Stratum.FastEncodeEnabled
	}
	if fc.Stratum.TCPReadBufferBytes != nil {
		cfg.StratumTCPReadBufferBytes = *fc.Stratum.TCPReadBufferBytes
	}
	if fc.Stratum.TCPWriteBufferBytes != nil {
		cfg.StratumTCPWriteBufferBytes = *fc.Stratum.TCPWriteBufferBytes
	}
	t := fileOverrideConfig{
		RateLimits:   fc.RateLimits,
		Difficulty:   fc.Difficulty,
		Mining:       fc.Mining,
		PeerCleaning: fc.PeerCleaning,
		Hashrate: hashrateTuning{
			HashrateEMATauSeconds:           fc.Hashrate.HashrateEMATauSeconds,
			HashrateCumulativeEnabled:       fc.Hashrate.HashrateCumulativeEnabled,
			HashrateRecentCumulativeEnabled: fc.Hashrate.HashrateRecentCumulativeEnabled,
		},
	}
	applyFileOverrides(cfg, t)
}

func applySecretsConfig(cfg *Config, sc secretsConfig) {
	if sc.DiscordBotToken != "" {
		cfg.DiscordBotToken = strings.TrimSpace(sc.DiscordBotToken)
	}
	if sc.ClerkSecretKey != "" {
		cfg.ClerkSecretKey = sc.ClerkSecretKey
	}
	if sc.ClerkPublishableKey != "" {
		cfg.ClerkPublishableKey = strings.TrimSpace(sc.ClerkPublishableKey)
	}
	if sc.BackblazeAccountID != "" {
		cfg.BackblazeAccountID = strings.TrimSpace(sc.BackblazeAccountID)
	}
	if sc.BackblazeApplicationKey != "" {
		cfg.BackblazeApplicationKey = strings.TrimSpace(sc.BackblazeApplicationKey)
	}
}
