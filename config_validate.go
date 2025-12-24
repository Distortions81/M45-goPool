package main

import (
	"fmt"
	"math/bits"
	"strings"
)

func validateConfig(cfg Config) error {
	if cfg.Extranonce2Size <= 0 {
		return fmt.Errorf("extranonce2_size must be > 0, got %d", cfg.Extranonce2Size)
	}
	if cfg.TemplateExtraNonce2Size <= 0 {
		cfg.TemplateExtraNonce2Size = cfg.Extranonce2Size
	}
	if cfg.TemplateExtraNonce2Size < cfg.Extranonce2Size {
		cfg.TemplateExtraNonce2Size = cfg.Extranonce2Size
	}
	if strings.TrimSpace(cfg.RPCUser) == "" {
		return fmt.Errorf("rpc_user is required")
	}
	if strings.TrimSpace(cfg.RPCPass) == "" {
		return fmt.Errorf("rpc_pass is required")
	}
	if strings.TrimSpace(cfg.PayoutAddress) == "" {
		return fmt.Errorf("payout_address is required for coinbase outputs")
	}
	if cfg.MaxConns < 0 {
		return fmt.Errorf("max_conns cannot be negative")
	}
	if cfg.MaxAcceptsPerSecond < 0 {
		return fmt.Errorf("max_accepts_per_second cannot be negative")
	}
	if cfg.MaxAcceptBurst < 0 {
		return fmt.Errorf("max_accept_burst cannot be negative")
	}
	if cfg.MaxRecentJobs <= 0 {
		return fmt.Errorf("max_recent_jobs must be > 0, got %d", cfg.MaxRecentJobs)
	}
	if cfg.CoinbaseSuffixBytes < 0 {
		return fmt.Errorf("coinbase_suffix_bytes cannot be negative")
	}
	if cfg.CoinbaseSuffixBytes > maxCoinbaseSuffixBytes {
		return fmt.Errorf("coinbase_suffix_bytes cannot exceed %d", maxCoinbaseSuffixBytes)
	}
	if cfg.CoinbasePoolTag != "" {
		if len(cfg.CoinbasePoolTag) != poolTagLength {
			return fmt.Errorf("coinbase_pool_tag must be %d characters", poolTagLength)
		}
		if normalizePoolTag(cfg.CoinbasePoolTag) != cfg.CoinbasePoolTag {
			return fmt.Errorf("coinbase_pool_tag must only contain alphanumeric characters")
		}
	}
	if cfg.CoinbaseScriptSigMaxBytes < 0 {
		return fmt.Errorf("coinbase_scriptsig_max_bytes cannot be negative")
	}
	if cfg.ConnectionTimeout < 0 {
		return fmt.Errorf("connection_timeout_seconds cannot be negative")
	}
	if cfg.ConnectionTimeout < minMinerTimeout {
		return fmt.Errorf("connection_timeout_seconds must be >= %s, got %s", minMinerTimeout, cfg.ConnectionTimeout)
	}
	if cfg.MinVersionBits < 0 {
		return fmt.Errorf("min_version_bits cannot be negative")
	}
	if cfg.VersionMask == 0 && cfg.MinVersionBits > 0 {
		return fmt.Errorf("min_version_bits requires version_mask to be non-zero")
	}
	availableBits := bits.OnesCount32(cfg.VersionMask)
	if cfg.MinVersionBits > availableBits {
		return fmt.Errorf("min_version_bits=%d exceeds available bits in version_mask (%d)", cfg.MinVersionBits, availableBits)
	}
	if cfg.MaxDifficulty < 0 {
		return fmt.Errorf("max_difficulty cannot be negative")
	}
	if cfg.MinDifficulty < 0 {
		return fmt.Errorf("min_difficulty cannot be negative")
	}
	if cfg.PoolFeePercent < 0 || cfg.PoolFeePercent >= 100 {
		return fmt.Errorf("pool_fee_percent must be >= 0 and < 100, got %v", cfg.PoolFeePercent)
	}
	if cfg.OperatorDonationPercent < 0 || cfg.OperatorDonationPercent > 100 {
		return fmt.Errorf("operator_donation_percent must be >= 0 and <= 100, got %v", cfg.OperatorDonationPercent)
	}
	if cfg.OperatorDonationPercent > 0 && strings.TrimSpace(cfg.OperatorDonationAddress) == "" {
		return fmt.Errorf("operator_donation_address is required when operator_donation_percent > 0")
	}
	if cfg.HashrateEMATauSeconds <= 0 {
		return fmt.Errorf("hashrate_ema_tau_seconds must be > 0, got %v", cfg.HashrateEMATauSeconds)
	}
	if cfg.HashrateEMAMinShares < minHashrateEMAMinShares {
		return fmt.Errorf("hashrate_ema_min_shares must be >= %d, got %d", minHashrateEMAMinShares, cfg.HashrateEMAMinShares)
	}
	if cfg.NTimeForwardSlackSeconds <= 0 {
		return fmt.Errorf("ntime_forward_slack_seconds must be > 0, got %v", cfg.NTimeForwardSlackSeconds)
	}
	if cfg.BanInvalidSubmissionsAfter < 0 {
		return fmt.Errorf("ban_invalid_submissions_after cannot be negative")
	}
	if cfg.BanInvalidSubmissionsWindow < 0 {
		return fmt.Errorf("ban_invalid_submissions_window_seconds cannot be negative")
	}
	if cfg.BanInvalidSubmissionsDuration < 0 {
		return fmt.Errorf("ban_invalid_submissions_duration_seconds cannot be negative")
	}
	if cfg.ReconnectBanThreshold < 0 {
		return fmt.Errorf("reconnect_ban_threshold cannot be negative")
	}
	if cfg.ReconnectBanWindowSeconds < 0 {
		return fmt.Errorf("reconnect_ban_window_seconds cannot be negative")
	}
	if cfg.ReconnectBanDurationSeconds < 0 {
		return fmt.Errorf("reconnect_ban_duration_seconds cannot be negative")
	}
	return nil
}
