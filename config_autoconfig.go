package main

import (
	"context"
	"fmt"
	"math/bits"
	"strings"
	"time"
)

// sanitizePayoutAddress drops any characters that don't belong in a typical
// Bitcoin address (bech32/base58), keeping only [A-Za-z0-9]. This protects
// against stray spaces, newlines, or punctuation without attempting to
// "correct" invalid addresses (validation still relies on bitcoind).
func sanitizePayoutAddress(addr string) string {
	if addr == "" {
		return addr
	}
	var cleaned []rune
	for _, r := range addr {
		switch {
		case r >= 'a' && r <= 'z':
			cleaned = append(cleaned, r)
		case r >= 'A' && r <= 'Z':
			cleaned = append(cleaned, r)
		case r >= '0' && r <= '9':
			cleaned = append(cleaned, r)
		default:
		}
	}
	if len(cleaned) == 0 {
		return ""
	}
	return string(cleaned)
}

func normalizeMempoolAddressURL(raw string) string {
	url := strings.TrimSpace(raw)
	if url == "" {
		return defaultMempoolAddressURL
	}
	if !strings.HasSuffix(url, "/") {
		url += "/"
	}
	return url
}

// versionMaskRPC is the minimal RPC interface needed by
// autoConfigureVersionMaskFromNode. It is satisfied by *RPCClient and by
// test fakes.
type versionMaskRPC interface {
	callCtx(ctx context.Context, method string, params any, out any) error
}

// autoConfigureVersionMaskFromNode inspects the connected Bitcoin node to
// choose a sensible base version-rolling mask for the active network, so
// operators no longer need to set version_mask manually in config.
// - mainnet/testnet/signet: use defaultVersionMask (0x1fffe000)
// - regtest: use a wider mask (0x3fffe000) to keep bit 29 available
// If the RPC call fails or returns an unknown chain, the existing mask is left
// unchanged and the pool falls back to its compiled-in defaults.
func autoConfigureVersionMaskFromNode(ctx context.Context, rpc versionMaskRPC, cfg *Config) {
	if rpc == nil || cfg == nil {
		return
	}
	if cfg.VersionMaskConfigured {
		return
	}

	type blockchainInfo struct {
		Chain string `json:"chain"`
	}

	var (
		callCtx context.Context
		cancel  context.CancelFunc
	)
	if ctx != nil {
		callCtx, cancel = context.WithTimeout(ctx, 5*time.Second)
	} else {
		callCtx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	}
	defer cancel()

	var info blockchainInfo
	if err := rpc.callCtx(callCtx, "getblockchaininfo", nil, &info); err != nil {
		logger.Warn("auto version mask from node failed; using default", "error", err)
		return
	}

	var base uint32
	switch strings.ToLower(strings.TrimSpace(info.Chain)) {
	case "main", "mainnet", "":
		base = defaultVersionMask
	case "test", "testnet", "testnet3", "testnet4", "signet":
		base = defaultVersionMask
	case "regtest":
		// Regtest commonly clears bit 29; use a wider mask so miners
		// still have room to roll version bits.
		base = uint32(0x3fffe000)
	default:
		logger.Warn("unknown bitcoin chain; using default version mask", "chain", info.Chain)
		return
	}

	if base == 0 {
		return
	}

	cfg.VersionMask = base
	cfg.VersionMaskConfigured = true

	// Keep min_version_bits consistent with the new mask.
	availableBits := bits.OnesCount32(cfg.VersionMask)
	if cfg.MinVersionBits < 0 {
		cfg.MinVersionBits = 0
	}
	if cfg.MinVersionBits > availableBits {
		cfg.MinVersionBits = availableBits
	}

	logger.Info("configured version_mask from bitcoin node",
		"chain", info.Chain,
		"version_mask", fmt.Sprintf("%08x", cfg.VersionMask))
}

// autoConfigureAcceptRateLimits sets sensible defaults for max_accepts_per_second
// and max_accept_burst based on max_conns if they weren't explicitly configured
// or if auto_accept_rate_limits is enabled.
// This ensures that when the pool restarts, all miners can reconnect quickly
// without hitting rate limits.
// The logic uses two configurable time windows:
// 1. Initial burst (accept_burst_window seconds): handles the immediate reconnection storm
//   - Burst capacity allows a percentage of miners to connect immediately
//
// 2. Sustained reconnection (remaining time): handles the rest of reconnections
//   - Per-second rate allows remaining miners to connect over the rest of the window
//
// Combined, this allows all max_conns miners to reconnect within accept_reconnect_window
// seconds of a pool restart without being rate-limited, while still protecting
// the node from connection floods during normal operation.
func autoConfigureAcceptRateLimits(cfg *Config, overrides fileOverrideConfig, overridesLoaded bool) {
	if cfg == nil || cfg.MaxConns <= 0 {
		return
	}
	if cfg.DisableConnectRateLimits {
		return
	}

	reconnectWindow := cfg.AcceptReconnectWindow
	if reconnectWindow <= 0 {
		reconnectWindow = defaultAcceptReconnectWindow
	}

	burstWindow := cfg.AcceptBurstWindow
	if burstWindow <= 0 {
		burstWindow = defaultAcceptBurstWindow
	}
	if burstWindow >= reconnectWindow {
		burstWindow = max(reconnectWindow/2, 1)
	}

	explicitMaxAccepts := overridesLoaded && overrides.RateLimits.MaxAcceptsPerSecond != nil
	explicitMaxBurst := overridesLoaded && overrides.RateLimits.MaxAcceptBurst != nil
	explicitSteadyStateRate := overridesLoaded && overrides.RateLimits.AcceptSteadyStateRate != nil

	// Auto-configure max_accept_burst if:
	// 1. auto_accept_rate_limits is enabled (always override), OR
	// 2. not explicitly set in config AND currently at default value
	// Calculate what percentage of miners can burst based on the burst window
	shouldConfigureBurst := cfg.AutoAcceptRateLimits || (!explicitMaxBurst && cfg.MaxAcceptBurst == defaultMaxAcceptBurst)
	if shouldConfigureBurst {
		// Burst window handles a proportional amount of total miners
		// For 15s total with 5s burst: 5/15 = 33% of miners in burst
		burstFraction := float64(burstWindow) / float64(reconnectWindow)
		burstCapacity := max(int(float64(cfg.MaxConns)*burstFraction),
			// minimum burst of 20
			20)
		// Cap at a reasonable maximum to avoid runaway values.
		if burstCapacity > 500000 {
			burstCapacity = 500000
		}
		cfg.MaxAcceptBurst = burstCapacity
		logger.Info("auto-configured max_accept_burst for initial reconnection",
			"max_conns", cfg.MaxConns,
			"max_accept_burst", cfg.MaxAcceptBurst,
			"burst_window", burstWindow,
			"burst_percentage", int(burstFraction*100))
	}

	// Auto-configure max_accepts_per_second if:
	// 1. auto_accept_rate_limits is enabled (always override), OR
	// 2. not explicitly set in config AND currently at default value
	shouldConfigureRate := cfg.AutoAcceptRateLimits || (!explicitMaxAccepts && cfg.MaxAcceptsPerSecond == defaultMaxAcceptsPerSecond)
	if shouldConfigureRate {
		burstFraction := float64(burstWindow) / float64(reconnectWindow)
		remainingMiners := int(float64(cfg.MaxConns) * (1.0 - burstFraction))
		sustainedWindow := max(reconnectWindow-burstWindow, 1)
		sustainedRate := max(remainingMiners/sustainedWindow, 10)
		if sustainedRate > 100000 {
			sustainedRate = 100000
		}
		cfg.MaxAcceptsPerSecond = sustainedRate
		logger.Info("auto-configured max_accepts_per_second for sustained reconnection",
			"max_conns", cfg.MaxConns,
			"max_accepts_per_second", cfg.MaxAcceptsPerSecond,
			"sustained_window", sustainedWindow,
			"total_reconnect_window", reconnectWindow)
	}

	// Auto-configure accept_steady_state_rate if:
	// 1. auto_accept_rate_limits is enabled (always override), OR
	// 2. not explicitly set in config AND currently at default value
	// The steady-state rate is calculated based on the expected percentage of miners
	// that might reconnect during normal operation (not pool restart).
	shouldConfigureSteadyState := cfg.AutoAcceptRateLimits || (!explicitSteadyStateRate && cfg.AcceptSteadyStateRate == defaultAcceptSteadyStateRate)
	if shouldConfigureSteadyState {
		// Validate steady-state reconnection settings
		reconnectPercent := cfg.AcceptSteadyStateReconnectPercent
		if reconnectPercent <= 0 {
			reconnectPercent = defaultAcceptSteadyStateReconnectPercent
		}
		steadyStateWindow := cfg.AcceptSteadyStateReconnectWindow
		if steadyStateWindow <= 0 {
			steadyStateWindow = defaultAcceptSteadyStateReconnectWindow
		}

		// Calculate: (max_conns × reconnect_percent / 100) / window_seconds
		// For example: 10000 miners × 5% = 500 miners over 60s = ~8/sec
		expectedReconnects := float64(cfg.MaxConns) * (reconnectPercent / 100.0)
		steadyStateRate := max(int(expectedReconnects/float64(steadyStateWindow)), 5)
		if steadyStateRate > 1000 {
			steadyStateRate = 1000
		}

		cfg.AcceptSteadyStateRate = steadyStateRate
		logger.Info("auto-configured accept_steady_state_rate for normal operation",
			"max_conns", cfg.MaxConns,
			"steady_state_rate", cfg.AcceptSteadyStateRate,
			"reconnect_percent", reconnectPercent,
			"steady_state_window", steadyStateWindow,
			"expected_reconnects", int(expectedReconnects))
	}
}
