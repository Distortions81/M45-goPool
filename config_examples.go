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
	ensureExampleFile(filepath.Join(examplesDir, "services.toml.example"), exampleServicesConfigBytes())
	ensureExampleFile(filepath.Join(examplesDir, "policy.toml.example"), examplePolicyConfigBytes())
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
	return fmt.Appendf(nil, "# Generated %s example (copy to a real config and edit as needed)\n\n", text)
}

func generatedConfigFileHeader() []byte {
	return []byte(`# goPool config.toml
# This file is read on startup.
# goPool may rewrite it (keeping a .bak) when it needs to persist settings.
#
`)
}

func generatedPolicyFileHeader() []byte {
	return []byte(`# goPool policy.toml
# Optional policy/security overrides loaded after config.toml on startup.
# Keep this file absent unless you need to override defaults.
#
`)
}

func generatedServicesFileHeader() []byte {
	return []byte(`# goPool services.toml
# Optional services/integrations settings loaded after config.toml on startup.
# Keep this file absent unless you need to override defaults.
#
`)
}

func generatedTuningFileHeader() []byte {
	return []byte(`# goPool tuning.toml
# Optional tuning/capacity overrides loaded after config.toml on startup.
# Keep this file absent unless you need to override defaults.
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
# - [stratum].fast_decode_enabled: Enable fast-path decoding (sniffing) for common Stratum methods (requires restart).
# - [stratum].fast_encode_enabled: Enable fast-path response encoding (canned/manual JSON) for common Stratum responses (requires restart).
# - [stratum].tcp_read_buffer_bytes / tcp_write_buffer_bytes: Set TCP socket buffer sizes in bytes (0 = OS default; requires restart).
#
# Logging
# - [logging].level: debug, info, warn, error (requires restart).
#
# Advanced settings can be split across services.toml, policy.toml, and tuning.toml.
#
`)
}

func servicesConfigDocComments() []byte {
	return []byte(`# Services / Integrations
# - [auth]: Clerk/OIDC endpoints and session cookie settings.
# - [backblaze_backup]: Cloud backup service toggle, bucket, prefix, and cadence.
# - [discord]: Discord integration endpoints/channels and worker notification threshold.
# - [status]: UI external links (mempool_address_url, github_url).
#
`)
}

func tuningConfigDocComments() []byte {
	return []byte(`# Rate limits ([rate_limits])
# - max_conns: Maximum simultaneous Stratum connections allowed (checked on accept; requires restart).
# - disable_connect_rate_limits: Disable accept/connect throttling entirely (intended for local-only pools on trusted networks; requires restart).
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
# Difficulty ([difficulty])
# - default_difficulty: Fallback difficulty if no suggest_* arrives during the startup delay; 0 means "use min_difficulty" (or the built-in minimum if min_difficulty=0).
# - target_shares_per_min: VarDiff target share cadence used for difficulty adjustment and hashrate EMA sample window sizing.
# - min_difficulty / max_difficulty: VarDiff clamp for miner connections; 0 disables that clamp (no limit; requires restart).
# - lock_suggested_difficulty: If true, the first mining.suggest_difficulty / mining.suggest_target locks that connection to the suggested difficulty (disables VarDiff; requires restart).
# - enforce_suggested_difficulty_limits: If true, ban/disconnect when miner-suggested difficulty is outside min_difficulty/max_difficulty.
#
# Mining ([mining])
# - extranonce2_size: Per-share extranonce2 byte length used for submit parsing and validation (requires restart).
# - template_extra_nonce2_size: Template extranonce2 byte length used in generated jobs (requires restart).
# - job_entropy: Entropy bytes added to per-job coinbase tags (requires restart).
# - coinbase_scriptsig_max_bytes: Maximum allowed coinbase scriptSig size in bytes (requires restart).
# - difficulty_step_granularity: Quantize difficulty to 2^(k/N) steps (N=1 power-of-two, N=2 half, N=3 third, N=4 quarter). Higher values are finer; requires restart.
#
# Hashrate ([hashrate])
# - hashrate_ema_tau_seconds: EMA time constant for per-connection hashrate smoothing (seconds; requires restart).
# - hashrate_cumulative_enabled: Blend per-connection EMA with cumulative hashrate for per-worker display (requires restart).
# - hashrate_recent_cumulative_enabled: Allow short-window cumulative (vardiff window) to influence per-worker display (requires restart).
#
# Peer cleaning ([peer_cleaning])
# - enabled/max_ping_ms/min_peers: Optional cleanup of high-latency peers.
#
#
`)
}

func policyConfigDocComments() []byte {
	return []byte(`# Mining policy ([mining])
# - share_job_freshness_mode: 0=off, 1=job_id, 2=job_id+prevhash.
# - share_check_ntime_window: Enforce nTime policy window.
# - share_check_version_rolling: Enforce version-rolling policy.
# - share_require_authorized_connection: Require authorized connection for submit.
# - share_check_param_format: Enforce submit parameter format checks.
# - share_require_worker_match: Require submit worker matches authorized worker.
# - submit_process_inline: Process mining.submit inline on connection goroutine.
# - share_check_duplicate: Enable duplicate share checks.
# - share_require_job_id: Require non-empty job_id.
#
# Hashrate policy ([hashrate])
# - share_ntime_max_forward_seconds: max allowed forward nTime skew.
#
# Version policy ([version])
# - min_version_bits
# - share_allow_degraded_version_bits
#
# Timeouts ([timeouts])
# - connection_timeout_seconds
#
# Bans ([bans])
# - invalid-submit and reconnect ban thresholds/windows.
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
	pf := buildTuningFileConfig(cfg)
	data, err := toml.Marshal(pf)
	if err != nil {
		logger.Warn("encode tuning config example failed", "error", err)
		return nil
	}
	return withPrependedTOMLComments(data, exampleHeader("tuning config"), tuningConfigDocComments())
}

func examplePolicyConfigBytes() []byte {
	cfg := defaultConfig()
	pf := buildPolicyFileConfig(cfg)
	data, err := toml.Marshal(pf)
	if err != nil {
		logger.Warn("encode policy config example failed", "error", err)
		return nil
	}
	return withPrependedTOMLComments(data, exampleHeader("policy config"), policyConfigDocComments())
}

func exampleServicesConfigBytes() []byte {
	cfg := defaultConfig()
	sf := buildServicesFileConfig(cfg)
	data, err := toml.Marshal(sf)
	if err != nil {
		logger.Warn("encode services config example failed", "error", err)
		return nil
	}
	return withPrependedTOMLComments(data, exampleHeader("services config"), servicesConfigDocComments())
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
