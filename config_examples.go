package main

import (
	"errors"
	"fmt"
	"github.com/pelletier/go-toml"
	"os"
	"path/filepath"
)

func ensureExampleFiles(dataDir string) {
	if dataDir == "" {
		dataDir = defaultDataDir
	}
	examplesDir := filepath.Join(dataDir, "config", "examples")
	if err := os.MkdirAll(examplesDir, 0o755); err != nil {
		logger.Warn("create examples directory for example configs failed", "dir", examplesDir, "error", err)
		return
	}

	configExamplePath := filepath.Join(examplesDir, "config.toml.example")
	ensureExampleFile(configExamplePath, exampleConfigBytes())
	ensureExampleFile(filepath.Join(examplesDir, "secrets.toml.example"), secretsConfigExample)
	ensureExampleFile(filepath.Join(examplesDir, "tuning.toml.example"), exampleTuningConfigBytes())
}

func ensureExampleFile(path string, contents []byte) {
	if len(contents) == 0 {
		return
	}
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		logger.Warn("write example config failed", "path", path, "error", err)
	}
}

func withPrependedTOMLComments(data []byte, parts ...[]byte) []byte {
	total := len(data)
	for _, part := range parts {
		total += len(part)
	}
	out := make([]byte, 0, total)
	for _, part := range parts {
		out = append(out, part...)
	}
	out = append(out, data...)
	return out
}

func exampleHeader(text string) []byte {
	return []byte(fmt.Sprintf("# Generated %s example (copy to a real config and edit as needed)\n\n", text))
}

func generatedConfigFileHeader() []byte {
	return []byte(`# goPool config.toml
# This file is read on startup.
# goPool may rewrite it (keeping a .bak) when it needs to persist settings.
#
`)
}

func generatedTuningFileHeader() []byte {
	return []byte(`# goPool tuning.toml
# Optional advanced overrides loaded after config.toml on startup.
# goPool may rewrite it when you use the admin panel "Save to disk".
#
`)
}

func baseConfigDocComments() []byte {
	return []byte(`# Key notes
# - [server].pool_listen: Stratum TCP listener for miners (requires restart).
# - [server].status_listen: HTTP listener for status UI (requires restart).
# - [server].status_tls_listen: HTTPS listener; "" disables TLS (requires restart).
# - [server].status_public_url: Canonical public URL for redirects/cookies; empty = auto-detect.
# - [stratum].stratum_tls_listen: Optional Stratum-over-TLS listener (requires restart).
# - [stratum].stratum_password_enabled: Require miners to send a password on authorize (requires restart).
# - [stratum].stratum_password: Password string checked against mining.authorize params (requires restart).
# - [stratum].stratum_password_public: Show the stratum password on the public connect panel (requires restart).
#
# Mining behavior
# - [mining].solo_mode: Lighter submit validation for solo pools (skips worker-mismatch + some policy checks; requires restart).
# - [mining].direct_submit_processing: Run mining.submit on the connection goroutine (lower latency; can block reads; requires restart).
# - [mining].check_duplicate_shares: Enable duplicate share detection (keeps a per-connection cache; requires restart).
#
# Logging
# - [logging].level: debug, info, warn, error (requires restart).
#
# Advanced settings (rate limits, bans, peer cleaning, difficulty clamps) live in tuning.toml.
#
`)
}

func tuningConfigDocComments() []byte {
	return []byte(`# Rate limits ([rate_limits])
# - max_conns: Maximum simultaneous Stratum connections allowed (checked on accept; requires restart).
# - auto_accept_rate_limits: When true, computes accept throttles from max_conns on startup (overrides explicit accept_* values; requires restart).
# - max_accepts_per_second: Accepts/sec during the initial restart/reconnect window (requires restart).
# - max_accept_burst: Token bucket burst size for accepts (requires restart).
# - accept_reconnect_window: Target seconds for all miners to reconnect after restart (used for auto_accept_rate_limits).
# - accept_burst_window: Initial burst window (seconds) after restart (used for auto_accept_rate_limits).
# - accept_steady_state_window: Seconds after startup before switching to steady-state throttles (requires restart).
# - accept_steady_state_rate: Accepts/sec once steady-state mode activates (requires restart).
# - accept_steady_state_reconnect_percent: Expected % of miners reconnecting during normal operation (used for auto_accept_rate_limits; requires restart).
# - accept_steady_state_reconnect_window: Seconds to spread expected steady-state reconnects across (used for auto_accept_rate_limits; requires restart).
# - stratum_messages_per_minute: Per-connection Stratum messages/min before disconnect (0 disables; requires restart).
#
# Timeouts ([timeouts])
# - connection_timeout_seconds: Disconnect idle miner connections (requires restart).
#
# Difficulty ([difficulty])
# - default_difficulty: Fallback difficulty if no suggest_* arrives during the startup delay; 0 means "use min_difficulty" (or the built-in minimum if min_difficulty=0).
# - min_difficulty / max_difficulty: VarDiff clamp for miner connections; 0 disables that clamp (no limit; requires restart).
# - lock_suggested_difficulty: If true, the first mining.suggest_difficulty / mining.suggest_target locks that connection to the suggested difficulty (disables VarDiff; requires restart).
# - enforce_suggested_difficulty_limits: If true, ban/disconnect when miner-suggested difficulty is outside min_difficulty/max_difficulty.
#
# Mining ([mining])
# - vardiff_fine: Enable half-step VarDiff adjustments and disable power-of-two snapping (requires restart).
#
# Status UI ([status])
# - mempool_address_url: URL prefix used for external address links in the worker status UI (defaults to "https://mempool.space/address/").
#
# Bans ([bans])
# - clean_expired_on_startup: Remove expired bans from disk on startup (startup-only).
# - ban_invalid_submissions_after: Ban a worker after N invalid submissions within the window (0 disables; requires restart).
# - ban_invalid_submissions_window_seconds: Window size (seconds) for invalid submission counting (requires restart).
# - ban_invalid_submissions_duration_seconds: Ban duration (seconds) when threshold is hit (requires restart).
# - reconnect_ban_threshold: Ban a host after N reconnects within the window (0 disables; requires restart).
# - reconnect_ban_window_seconds: Reconnect ban counting window (seconds; requires restart).
# - reconnect_ban_duration_seconds: Reconnect ban duration (seconds; requires restart).
# - banned_miner_types: List of miner client names or full IDs to disconnect on subscribe (requires restart).
#   Prefer data_dir/config/miner_blacklist.json for managing this list; it overrides this field when present.
#
# Peer cleaning ([peer_cleaning])
# - enabled: If true, goPool may disconnect high-latency peers while refreshing node status.
# - max_ping_ms: Peers above this ping (ms) are candidates for disconnect.
# - min_peers: Minimum number of peers to keep connected.
#
`)
}

func exampleConfigBytes() []byte {
	cfg := defaultConfig()
	cfg.PayoutAddress = "YOUR_POOL_WALLET_ADDRESS_HERE"
	cfg.PoolDonationAddress = "OPTIONAL_POOL_DONATION_WALLET"
	cfg.PoolEntropy = "" // Don't set a default - auto-generated on first run
	fc := buildBaseFileConfig(cfg)
	data, err := toml.Marshal(fc)
	if err != nil {
		logger.Warn("encode config example failed", "error", err)
		return nil
	}
	return withPrependedTOMLComments(data, exampleHeader("base config"), baseConfigDocComments())
}

func exampleTuningConfigBytes() []byte {
	cfg := defaultConfig()
	tf := buildTuningFileConfig(cfg)
	data, err := toml.Marshal(tf)
	if err != nil {
		logger.Warn("encode tuning config example failed", "error", err)
		return nil
	}
	return withPrependedTOMLComments(data, exampleHeader("tuning config"), tuningConfigDocComments())
}

func rewriteConfigFile(path string, cfg Config) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	fc := buildBaseFileConfig(cfg)
	data, err := toml.Marshal(fc)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	data = withPrependedTOMLComments(data, generatedConfigFileHeader(), baseConfigDocComments())

	tmpFile, err := os.CreateTemp(dir, "config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpName := tmpFile.Name()
	removeTemp := true
	defer func() {
		if tmpFile != nil {
			_ = tmpFile.Close()
		}
		if removeTemp {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmpFile.Write(data); err != nil {
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		return fmt.Errorf("sync temp config: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}
	tmpFile = nil

	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("chmod %s: %w", tmpName, err)
	}

	bakPath := path + ".bak"
	if _, err := os.Stat(path); err == nil {
		if err := os.Remove(bakPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", bakPath, err)
		}
		if err := os.Rename(path, bakPath); err != nil {
			return fmt.Errorf("rename %s to %s: %w", path, bakPath, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", path, err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename %s to %s: %w", tmpName, path, err)
	}
	removeTemp = false
	return nil
}
