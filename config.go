package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/bits"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bytedance/gopkg/util/logger"
)

const (
	defaultRecentJobs         = 3
	defaultSubscribeTimeout   = 15 * time.Second
	defaultAuthorizeTimeout   = 15 * time.Second
	defaultStratumReadTimeout = 5 * time.Minute
	defaultPoolFeePercent     = 2.0
	// Default guardrail tuned for a typical 1 Gbit desktop/server: allows
	// a few hundred new connections per second without overwhelming the
	// node, while still smoothing reconnect storms. Can be raised or
	// lowered (or disabled with 0) in config.
	defaultMaxAcceptsPerSecond = 500
	// Default burst capacity for new accepts; by default we allow roughly
	// two seconds' worth of connections to arrive in a short spike.
	defaultMaxAcceptBurst = 1000
	// Default target window for all miners to reconnect after pool restart.
	// Auto-configuration uses this to calculate appropriate rate limits.
	defaultAcceptReconnectWindow = 15 // seconds
	// Default duration for the initial burst window. The burst capacity
	// handles the immediate reconnection storm in this time period.
	defaultAcceptBurstWindow = 5 // seconds
	// Default time after pool start to switch to steady-state throttling.
	// This gives miners ample time to reconnect after a restart before
	// entering the more restrictive steady-state mode.
	defaultAcceptSteadyStateWindow = 100 // seconds
	// Default accept rate during steady-state operation. Much lower than
	// reconnection rates to protect against sustained attacks.
	defaultAcceptSteadyStateRate = 50 // accepts per second
	// Default percentage of miners expected to reconnect simultaneously during
	// normal operation (not pool restart). Used to auto-calculate steady-state
	// rate. For example, 5% means we expect at most 5% of max_conns to reconnect
	// at once under normal conditions.
	defaultAcceptSteadyStateReconnectPercent = 5.0 // percent
	// Default time window (in seconds) for spreading out expected steady-state
	// reconnections. Combined with the reconnect percentage, this determines the
	// steady-state accept rate.
	defaultAcceptSteadyStateReconnectWindow = 60 // seconds
	defaultCoinbaseSuffixBytes              = 4
	maxCoinbaseSuffixBytes                  = 32
	defaultCoinbaseScriptSigMaxBytes        = 100
)

const (
	defaultReplayLimit = int64(16 << 20)
	defaultMaxConns    = 10000
	// defaultHashrateEMATauSeconds controls how quickly the per-connection
	// hashrate estimate responds to changes. Larger values make hashrate
	// smoother but slower to react.
	defaultHashrateEMATauSeconds = 600.0
	// defaultNTimeForwardSlackSeconds bounds how far miners may roll ntime
	// forward from the template's curtime/mintime before shares are rejected.
	defaultNTimeForwardSlackSeconds = 7000
)

type Config struct {
	ListenAddr        string // e.g. ":3333"
	StatusAddr        string // HTTP status listen address, e.g. ":80"
	StatusTLSAddr     string // HTTPS status listen address, e.g. ":443"
	StatusBrandName   string
	StatusBrandDomain string
	StatusTagline     string
	// FiatCurrency controls which fiat currency (e.g. "usd") is used
	// when displaying approximate BTC prices on the status UI. It is
	// only used for display and never affects payouts or accounting.
	FiatCurrency string
	// DonationAddress is an optional pool donation wallet shown in the
	// UI footer. It is never used for payouts.
	DonationAddress string
	// DiscordURL is an optional Discord invite link shown in the header.
	DiscordURL string
	// StratumTLSListen is an optional TCP address for a TLS-enabled
	// Stratum listener (e.g. ":3443"). When empty, TLS for Stratum is
	// disabled and only the plain TCP listener is used.
	StratumTLSListen string
	RPCURL           string // e.g. "http://127.0.0.1:8332"
	RPCUser          string
	RPCPass          string
	PayoutAddress    string
	// PayoutScript is reserved for future internal overrides and is not
	// populated from or written to config.json.
	PayoutScript              string
	PoolFeePercent            float64
	Extranonce2Size           int
	TemplateExtraNonce2Size   int
	CoinbaseSuffixBytes       int
	CoinbaseMsg               string
	CoinbasePoolTag           string
	CoinbaseScriptSigMaxBytes int
	ZMQBlockAddr              string
	DataDir                   string
	ShareLogBufferBytes       int
	FsyncShareLog             bool
	ShareLogReplayBytes       int64
	MaxConns                  int
	// MaxAcceptsPerSecond limits how many new TCP connections the pool
	// will accept per second. Zero disables rate limiting.
	MaxAcceptsPerSecond int
	// MaxAcceptBurst controls how many new accepts can be allowed in a
	// short burst before the average per-second rate is enforced. Zero
	// means "same as MaxAcceptsPerSecond".
	MaxAcceptBurst int
	// AutoAcceptRateLimits when true, automatically calculates and overrides
	// max_accepts_per_second and max_accept_burst based on max_conns to
	// ensure smooth reconnections during pool restarts. When false (default),
	// auto-configuration only applies if these values aren't explicitly set.
	AutoAcceptRateLimits bool
	// AcceptReconnectWindow specifies the target time window (in seconds) for
	// all miners to reconnect after a pool restart. The auto-configuration
	// logic uses this to calculate appropriate rate limits. Default: 15 seconds.
	AcceptReconnectWindow int
	// AcceptBurstWindow specifies how long (in seconds) the initial burst
	// capacity should last before switching to the sustained rate. This handles
	// the immediate reconnection storm. Default: 5 seconds.
	AcceptBurstWindow int
	// AcceptSteadyStateWindow specifies when (in seconds after pool start) to
	// switch from reconnection mode to steady-state mode. After this time,
	// the pool uses AcceptSteadyStateRate for much lower sustained throttling
	// to protect against attacks and misbehaving clients. Default: 80 seconds.
	AcceptSteadyStateWindow int
	// AcceptSteadyStateRate specifies the maximum accepts per second during
	// normal steady-state operation (after AcceptSteadyStateWindow). This is
	// typically much lower than reconnection rates. Zero means no steady-state
	// throttle (use reconnection rate indefinitely). If not explicitly set and
	// auto-configuration is enabled, this is calculated based on max_conns and
	// AcceptSteadyStateReconnectPercent. Default: 50/sec.
	AcceptSteadyStateRate int
	// AcceptSteadyStateReconnectPercent specifies what percentage of miners we
	// expect might reconnect simultaneously during normal steady-state operation
	// (not during pool restart). Used with AcceptSteadyStateReconnectWindow to
	// auto-calculate AcceptSteadyStateRate. For example, 5.0 means we expect at
	// most 5% of max_conns to reconnect at once. Default: 5.0%
	AcceptSteadyStateReconnectPercent float64
	// AcceptSteadyStateReconnectWindow specifies the time window (in seconds)
	// over which to spread expected steady-state reconnections. Combined with
	// AcceptSteadyStateReconnectPercent, this calculates the steady-state rate.
	// For example: 10000 miners × 5% = 500 miners over 60s = ~8/sec.
	// Default: 60 seconds.
	AcceptSteadyStateReconnectWindow int
	MaxRecentJobs                    int
	SubscribeTimeout                 time.Duration
	AuthorizeTimeout                 time.Duration
	StratumReadTimeout               time.Duration
	VersionMask                      uint32
	MinVersionBits                   int
	VersionMaskConfigured            bool
	MaxDifficulty                    float64
	MinDifficulty                    float64
	// If true, workers that call mining.suggest_difficulty will be kept at
	// that difficulty (clamped to min/max) and VarDiff will not adjust them.
	LockSuggestedDifficulty bool
	// If true, low-difficulty shares are still logged and counted for
	// accounting, but the miner receives a generic success instead of an
	// explicit low-diff error to avoid noisy reconnect behavior.
	HideLowDiffErrors bool
	// HashrateEMATauSeconds controls the time constant (in seconds) for the
	// per-connection hashrate exponential moving average.
	HashrateEMATauSeconds float64
	// NTimeForwardSlackSeconds bounds how far ntime may roll forward from
	// the template's curtime/mintime before being rejected.
	NTimeForwardSlackSeconds int

	// BanInvalidSubmissionsAfter controls how many clearly invalid share
	// submissions (bad extranonce/ntime/nonce/coinbase, etc.) are allowed
	// within BanInvalidSubmissionsWindow before a worker is automatically
	// banned. Zero disables auto-bans for invalid submissions.
	BanInvalidSubmissionsAfter int
	// BanInvalidSubmissionsWindow bounds the time window used for counting
	// invalid submissions when deciding whether to ban a worker.
	BanInvalidSubmissionsWindow time.Duration
	// BanInvalidSubmissionsDuration controls how long a worker is banned
	// after exceeding the invalid-submission threshold.
	BanInvalidSubmissionsDuration time.Duration

	// ReconnectBanThreshold controls how many connection attempts from the
	// same remote IP are allowed within ReconnectBanWindowSeconds before the
	// address is temporarily banned at the TCP accept layer. Zero disables
	// reconnect churn bans.
	ReconnectBanThreshold int
	// ReconnectBanWindowSeconds bounds the time window (in seconds) used to
	// count reconnect attempts per IP when deciding whether to ban.
	ReconnectBanWindowSeconds int
	// ReconnectBanDurationSeconds controls how long (in seconds) a remote IP
	// is banned from connecting once it exceeds the reconnect threshold.
	ReconnectBanDurationSeconds int
}

type EffectiveConfig struct {
	ListenAddr                        string  `json:"listen_addr"`
	StatusAddr                        string  `json:"status_addr"`
	StatusTLSAddr                     string  `json:"status_tls_listen,omitempty"`
	StatusBrandName                   string  `json:"status_brand_name,omitempty"`
	StatusBrandDomain                 string  `json:"status_brand_domain,omitempty"`
	StatusTagline                     string  `json:"status_tagline,omitempty"`
	FiatCurrency                      string  `json:"fiat_currency,omitempty"`
	DonationAddress                   string  `json:"donation_address,omitempty"`
	DiscordURL                        string  `json:"discord_url,omitempty"`
	StratumTLSListen                  string  `json:"stratum_tls_listen,omitempty"`
	RPCURL                            string  `json:"rpc_url"`
	RPCUser                           string  `json:"rpc_user"`
	RPCPassSet                        bool    `json:"rpc_pass_set"`
	PayoutAddress                     string  `json:"payout_address"`
	PoolFeePercent                    float64 `json:"pool_fee_percent,omitempty"`
	Extranonce2Size                   int     `json:"extranonce2_size"`
	TemplateExtraNonce2Size           int     `json:"template_extranonce2_size,omitempty"`
	CoinbaseSuffixBytes               int     `json:"coinbase_suffix_bytes"`
	CoinbasePoolTag                   string  `json:"coinbase_pool_tag,omitempty"`
	CoinbaseMsg                       string  `json:"coinbase_message"`
	CoinbaseScriptSigMaxBytes         int     `json:"coinbase_scriptsig_max_bytes"`
	ZMQBlockAddr                      string  `json:"zmq_block_addr,omitempty"`
	DataDir                           string  `json:"data_dir"`
	ShareLogBufferBytes               int     `json:"share_log_buffer_bytes"`
	FsyncShareLog                     bool    `json:"fsync_share_log"`
	ShareLogReplayBytes               int64   `json:"share_log_replay_bytes"`
	MaxConns                          int     `json:"max_conns,omitempty"`
	MaxAcceptsPerSecond               int     `json:"max_accepts_per_second,omitempty"`
	MaxAcceptBurst                    int     `json:"max_accept_burst,omitempty"`
	AutoAcceptRateLimits              bool    `json:"auto_accept_rate_limits,omitempty"`
	AcceptReconnectWindow             int     `json:"accept_reconnect_window,omitempty"`
	AcceptBurstWindow                 int     `json:"accept_burst_window,omitempty"`
	AcceptSteadyStateWindow           int     `json:"accept_steady_state_window,omitempty"`
	AcceptSteadyStateRate             int     `json:"accept_steady_state_rate,omitempty"`
	AcceptSteadyStateReconnectPercent float64 `json:"accept_steady_state_reconnect_percent,omitempty"`
	AcceptSteadyStateReconnectWindow  int     `json:"accept_steady_state_reconnect_window,omitempty"`
	MaxRecentJobs                     int     `json:"max_recent_jobs"`
	SubscribeTimeout                  string  `json:"subscribe_timeout"`
	AuthorizeTimeout                  string  `json:"authorize_timeout"`
	StratumReadTimeout                string  `json:"stratum_read_timeout"`
	VersionMask                       string  `json:"version_mask,omitempty"`
	MinVersionBits                    int     `json:"min_version_bits,omitempty"`
	MaxDifficulty                     float64 `json:"max_difficulty,omitempty"`
	MinDifficulty                     float64 `json:"min_difficulty,omitempty"`
	LockSuggestedDifficulty           bool    `json:"lock_suggested_difficulty,omitempty"`
	HideLowDiffErrors                 bool    `json:"hide_low_diff_errors,omitempty"`
	HashrateEMATauSeconds             float64 `json:"hashrate_ema_tau_seconds,omitempty"`
	NTimeForwardSlackSec              int     `json:"ntime_forward_slack_seconds,omitempty"`
	BanInvalidSubmissionsAfter        int     `json:"ban_invalid_submissions_after,omitempty"`
	BanInvalidSubmissionsWindow       string  `json:"ban_invalid_submissions_window,omitempty"`
	BanInvalidSubmissionsDuration     string  `json:"ban_invalid_submissions_duration,omitempty"`
	ReconnectBanThreshold             int     `json:"reconnect_ban_threshold,omitempty"`
	ReconnectBanWindowSeconds         int     `json:"reconnect_ban_window_seconds,omitempty"`
	ReconnectBanDurationSeconds       int     `json:"reconnect_ban_duration_seconds,omitempty"`
}

type fileConfig struct {
	PoolListen        string `json:"pool_listen"`
	StatusListen      string `json:"status_listen"`
	StatusTLSListen   string `json:"status_tls_listen"`
	StatusBrandName   string `json:"status_brand_name"`
	StatusBrandDomain string `json:"status_brand_domain"`
	StatusTagline     string `json:"status_tagline"`
	FiatCurrency      string `json:"fiat_currency"`
	DonationAddress   string `json:"donation_address"`
	DiscordURL        string `json:"discord_url"`
	StratumTLSListen  string `json:"stratum_tls_listen"`
	RPCURL            string `json:"rpc_url"`
	// RPC credentials are loaded exclusively from secrets.json and are
	// never read from or written to config.json.
	RPCUser                           string   `json:"-"`
	RPCPass                           string   `json:"-"`
	PayoutAddress                     string   `json:"payout_address"`
	PoolFeePercent                    *float64 `json:"pool_fee_percent"`
	Extranonce2Size                   *int     `json:"extranonce2_size"`
	TemplateExtraNonce2Size           *int     `json:"template_extranonce2_size"`
	CoinbaseSuffixBytes               *int     `json:"coinbase_suffix_bytes"`
	CoinbasePoolTag                   *string  `json:"coinbase_pool_tag"`
	CoinbaseScriptSigMaxBytes         *int     `json:"coinbase_scriptsig_max_bytes"`
	CoinbaseMsg                       string   `json:"coinbase_message"`
	ZMQBlockAddr                      string   `json:"zmq_block_addr"`
	DataDir                           string   `json:"data_dir"`
	ShareLogBufferBytes               *int     `json:"share_log_buffer_bytes"`
	FsyncShareLog                     *bool    `json:"fsync_share_log"`
	ShareLogReplayBytes               *int64   `json:"share_log_replay_bytes"`
	MaxConns                          *int     `json:"max_conns"`
	MaxAcceptsPerSecond               *int     `json:"max_accepts_per_second"`
	MaxAcceptBurst                    *int     `json:"max_accept_burst"`
	AutoAcceptRateLimits              *bool    `json:"auto_accept_rate_limits"`
	AcceptReconnectWindow             *int     `json:"accept_reconnect_window"`
	AcceptBurstWindow                 *int     `json:"accept_burst_window"`
	AcceptSteadyStateWindow           *int     `json:"accept_steady_state_window"`
	AcceptSteadyStateRate             *int     `json:"accept_steady_state_rate"`
	AcceptSteadyStateReconnectPercent *float64 `json:"accept_steady_state_reconnect_percent"`
	AcceptSteadyStateReconnectWindow  *int     `json:"accept_steady_state_reconnect_window"`
	MaxRecentJobs                     *int     `json:"max_recent_jobs"`
	SubscribeTimeoutSec               *int     `json:"subscribe_timeout_seconds"`
	AuthorizeTimeoutSec               *int     `json:"authorize_timeout_seconds"`
	StratumReadTimeoutSec             *int     `json:"stratum_read_timeout_seconds"`
	MinVersionBits                    *int     `json:"min_version_bits"`
	MaxDifficulty                     *float64 `json:"max_difficulty"`
	MinDifficulty                     *float64 `json:"min_difficulty"`
	LockSuggestedDifficulty           *bool    `json:"lock_suggested_difficulty"`
	HideLowDiffErrors                 *bool    `json:"hide_low_diff_errors"`
	HashrateEMATauSeconds             *float64 `json:"hashrate_ema_tau_seconds"`
	NTimeForwardSlackSec              *int     `json:"ntime_forward_slack_seconds"`
	BanInvalidSubmissionsAfter        *int     `json:"ban_invalid_submissions_after"`
	BanInvalidSubmissionsWindowSec    *int     `json:"ban_invalid_submissions_window_seconds"`
	BanInvalidSubmissionsDurationSec  *int     `json:"ban_invalid_submissions_duration_seconds"`
	ReconnectBanThreshold             *int     `json:"reconnect_ban_threshold"`
	ReconnectBanWindowSeconds         *int     `json:"reconnect_ban_window_seconds"`
	ReconnectBanDurationSeconds       *int     `json:"reconnect_ban_duration_seconds"`
}

// secretsConfig holds sensitive values that operators may prefer to keep out
// of the main config.json so it can be checked into version control or shared
// more freely.
//
// When present, these values override any corresponding fields from
// config.json.
type secretsConfig struct {
	RPCUser string `json:"rpc_user"`
	RPCPass string `json:"rpc_pass"`
}

func loadConfig(configPath, secretsPath string) Config {
	cfg := defaultConfig()

	if configPath == "" {
		configPath = defaultConfigPath()
	}

	var fileConfigLoaded bool
	if fc, ok, err := loadConfigFile(configPath); err != nil {
		fatal("config file", err, "path", configPath)
	} else if ok {
		applyFileConfig(&cfg, *fc)
		fileConfigLoaded = ok
	} else {
		// Config file doesn't exist, write out defaults
		if err := rewriteConfigFile(configPath, cfg); err != nil {
			fatal("write default config", err, "path", configPath)
		}
		logger.Info("created default config file", "path", configPath)
		fileConfigLoaded = false
	}

	// Optional secrets overlay: if data_dir/secrets.json exists, values
	// from that file override sensitive fields like RPC credentials.
	if secretsPath == "" {
		// Prefer the newer data_dir/state/secrets.json, but fall back to
		// data_dir/secrets.json for backward compatibility.
		stateSecretsPath := filepath.Join(cfg.DataDir, "state", "secrets.json")
		if _, err := os.Stat(stateSecretsPath); err == nil {
			secretsPath = stateSecretsPath
		} else {
			secretsPath = filepath.Join(cfg.DataDir, "secrets.json")
		}
	}
	if sc, ok, err := loadSecretsFile(secretsPath); err != nil {
		fatal("secrets file", err, "path", secretsPath)
	} else if ok {
		applySecretsConfig(&cfg, *sc)
	}

	// Optional advanced/tuning overlay: if data_dir/state/tuning.json (or the
	// legacy data_dir/tuning.json) exists, load it as a second config file and
	// apply it on top of the main config. This lets operators keep advanced
	// knobs separate and delete the file to fall back to defaults.
	tuningPath := filepath.Join(cfg.DataDir, "state", "tuning.json")
	if _, err := os.Stat(tuningPath); errors.Is(err, os.ErrNotExist) {
		legacy := filepath.Join(cfg.DataDir, "tuning.json")
		if _, err2 := os.Stat(legacy); err2 == nil {
			tuningPath = legacy
		} else {
			tuningPath = ""
		}
	}
	if tuningPath != "" {
		if tf, ok, err := loadConfigFile(tuningPath); err != nil {
			fatal("tuning config file", err, "path", tuningPath)
		} else if ok {
			applyFileConfig(&cfg, *tf)
		}
	}

	// Sanitize payout address to strip stray whitespace or unexpected
	// characters before it is used for RPC validation and coinbase outputs.
	cfg.PayoutAddress = sanitizePayoutAddress(cfg.PayoutAddress)

	// Auto-configure accept rate limits based on max_conns if they weren't
	// explicitly set in the config file. This ensures miners can reconnect
	// smoothly after pool restarts without hitting rate limits.
	autoConfigureAcceptRateLimits(&cfg, fileConfigLoaded)

	return cfg
}

// defaultConfig returns a Config populated with built-in defaults that act
// as the base for both runtime config loading and example config generation.
func defaultConfig() Config {
	return Config{
		ListenAddr:        ":3333",
		StatusAddr:        ":80",
		StatusTLSAddr:     ":443",
		StatusBrandName:   "",
		StatusBrandDomain: "",
		StatusTagline:     "Solo Mining Pool",
		FiatCurrency:      "usd",
		DonationAddress:   "",
		DiscordURL:        "",
		// StratumTLSListen defaults to empty (disabled) so operators
		// explicitly opt in to TLS for miner connections.
		StratumTLSListen: ":4333",
		RPCURL:           "http://127.0.0.1:8332",
		RPCUser:          "bitcoinrpc",
		RPCPass:          "password",
		PayoutAddress:    "",
		PoolFeePercent:   defaultPoolFeePercent,
		// Mining / Stratum defaults.
		Extranonce2Size:                   4,
		TemplateExtraNonce2Size:           8,
		CoinbaseSuffixBytes:               defaultCoinbaseSuffixBytes,
		CoinbaseMsg:                       poolSoftwareName,
		CoinbaseScriptSigMaxBytes:         defaultCoinbaseScriptSigMaxBytes,
		ZMQBlockAddr:                      "",
		DataDir:                           defaultDataDir,
		ShareLogBufferBytes:               0,
		FsyncShareLog:                     false,
		ShareLogReplayBytes:               defaultReplayLimit,
		MaxConns:                          defaultMaxConns,
		MaxAcceptsPerSecond:               defaultMaxAcceptsPerSecond,
		MaxAcceptBurst:                    defaultMaxAcceptBurst,
		AutoAcceptRateLimits:              true,
		AcceptReconnectWindow:             defaultAcceptReconnectWindow,
		AcceptBurstWindow:                 defaultAcceptBurstWindow,
		AcceptSteadyStateWindow:           defaultAcceptSteadyStateWindow,
		AcceptSteadyStateRate:             defaultAcceptSteadyStateRate,
		AcceptSteadyStateReconnectPercent: defaultAcceptSteadyStateReconnectPercent,
		AcceptSteadyStateReconnectWindow:  defaultAcceptSteadyStateReconnectWindow,
		MaxRecentJobs:                     defaultRecentJobs,
		SubscribeTimeout:                  defaultSubscribeTimeout,
		AuthorizeTimeout:                  defaultAuthorizeTimeout,
		StratumReadTimeout:                defaultStratumReadTimeout,
		VersionMask:                       defaultVersionMask,
		MinVersionBits:                    1,
		// Default difficulty range: 512–16000 so all live targets are powers
		// of two within a practical range for typical ASICs.
		MaxDifficulty:                 16000,
		MinDifficulty:                 512,
		LockSuggestedDifficulty:       false,
		HideLowDiffErrors:             true,
		HashrateEMATauSeconds:         defaultHashrateEMATauSeconds,
		NTimeForwardSlackSeconds:      defaultNTimeForwardSlackSeconds,
		BanInvalidSubmissionsAfter:    60,
		BanInvalidSubmissionsWindow:   time.Minute,
		BanInvalidSubmissionsDuration: 15 * time.Minute,
		ReconnectBanThreshold:         0,
		ReconnectBanWindowSeconds:     60,
		ReconnectBanDurationSeconds:   300,
	}
}

// defaultConfigPath returns the preferred path for the main pool config.
// Newer deployments keep config under data/state/config.json; if that file
// is missing, we fall back to the legacy data/config.json location.
func defaultConfigPath() string {
	stateCfg := filepath.Join(defaultDataDir, "state", "config.json")
	if _, err := os.Stat(stateCfg); err == nil {
		return stateCfg
	}
	return filepath.Join(defaultDataDir, "config.json")
}

func loadConfigFile(path string) (*fileConfig, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read %s: %w", path, err)
	}

	var cfg fileConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, true, fmt.Errorf("parse %s: %w", path, err)
	}

	return &cfg, true, nil
}

func loadSecretsFile(path string) (*secretsConfig, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read %s: %w", path, err)
	}

	var cfg secretsConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, true, fmt.Errorf("parse %s: %w", path, err)
	}
	return &cfg, true, nil
}

func rewriteConfigFile(path string, cfg Config) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	fc := fileConfig{
		PoolListen:      cfg.ListenAddr,
		StatusListen:    cfg.StatusAddr,
		StatusTLSListen: cfg.StatusTLSAddr,
		// Never write secrets back into config.json; RPC credentials
		// belong exclusively in secrets.json.
		StatusBrandName:   cfg.StatusBrandName,
		StatusBrandDomain: cfg.StatusBrandDomain,
		StatusTagline:     cfg.StatusTagline,
		FiatCurrency:      cfg.FiatCurrency,
		DonationAddress:   cfg.DonationAddress,
		DiscordURL:        cfg.DiscordURL,
		StratumTLSListen:  cfg.StratumTLSListen,
		RPCURL:            cfg.RPCURL,
		RPCUser:           "",
		RPCPass:           "",
		PayoutAddress:     cfg.PayoutAddress,
		CoinbaseMsg:       cfg.CoinbaseMsg,
		ZMQBlockAddr:      cfg.ZMQBlockAddr,
		DataDir:           cfg.DataDir,
	}

	// Helper lambdas for pointers.
	intPtr := func(v int) *int { return &v }
	int64Ptr := func(v int64) *int64 { return &v }
	boolPtr := func(v bool) *bool { return &v }
	float64Ptr := func(v float64) *float64 { return &v }
	stringPtr := func(v string) *string { return &v }

	fc.Extranonce2Size = intPtr(cfg.Extranonce2Size)
	fc.TemplateExtraNonce2Size = intPtr(cfg.TemplateExtraNonce2Size)
	fc.ShareLogBufferBytes = intPtr(cfg.ShareLogBufferBytes)
	fc.CoinbaseSuffixBytes = intPtr(cfg.CoinbaseSuffixBytes)
	fc.CoinbaseScriptSigMaxBytes = intPtr(cfg.CoinbaseScriptSigMaxBytes)
	fc.FsyncShareLog = boolPtr(cfg.FsyncShareLog)
	fc.ShareLogReplayBytes = int64Ptr(cfg.ShareLogReplayBytes)
	fc.MaxConns = intPtr(cfg.MaxConns)
	fc.MaxAcceptsPerSecond = intPtr(cfg.MaxAcceptsPerSecond)
	fc.MaxAcceptBurst = intPtr(cfg.MaxAcceptBurst)
	fc.AutoAcceptRateLimits = boolPtr(cfg.AutoAcceptRateLimits)
	fc.AcceptReconnectWindow = intPtr(cfg.AcceptReconnectWindow)
	fc.AcceptBurstWindow = intPtr(cfg.AcceptBurstWindow)
	fc.AcceptSteadyStateWindow = intPtr(cfg.AcceptSteadyStateWindow)
	fc.AcceptSteadyStateRate = intPtr(cfg.AcceptSteadyStateRate)
	fc.AcceptSteadyStateReconnectPercent = float64Ptr(cfg.AcceptSteadyStateReconnectPercent)
	fc.AcceptSteadyStateReconnectWindow = intPtr(cfg.AcceptSteadyStateReconnectWindow)
	fc.MaxRecentJobs = intPtr(cfg.MaxRecentJobs)
	fc.SubscribeTimeoutSec = intPtr(int(cfg.SubscribeTimeout / time.Second))
	fc.AuthorizeTimeoutSec = intPtr(int(cfg.AuthorizeTimeout / time.Second))
	fc.StratumReadTimeoutSec = intPtr(int(cfg.StratumReadTimeout / time.Second))
	fc.MinVersionBits = intPtr(cfg.MinVersionBits)
	fc.MaxDifficulty = float64Ptr(cfg.MaxDifficulty)
	fc.MinDifficulty = float64Ptr(cfg.MinDifficulty)
	fc.PoolFeePercent = float64Ptr(cfg.PoolFeePercent)
	fc.LockSuggestedDifficulty = boolPtr(cfg.LockSuggestedDifficulty)
	fc.HideLowDiffErrors = boolPtr(cfg.HideLowDiffErrors)
	fc.CoinbasePoolTag = stringPtr(cfg.CoinbasePoolTag)
	fc.HashrateEMATauSeconds = float64Ptr(cfg.HashrateEMATauSeconds)
	fc.NTimeForwardSlackSec = intPtr(cfg.NTimeForwardSlackSeconds)
	if cfg.BanInvalidSubmissionsAfter > 0 {
		fc.BanInvalidSubmissionsAfter = intPtr(cfg.BanInvalidSubmissionsAfter)
	}
	if cfg.BanInvalidSubmissionsWindow > 0 {
		fc.BanInvalidSubmissionsWindowSec = intPtr(int(cfg.BanInvalidSubmissionsWindow / time.Second))
	}
	if cfg.BanInvalidSubmissionsDuration > 0 {
		fc.BanInvalidSubmissionsDurationSec = intPtr(int(cfg.BanInvalidSubmissionsDuration / time.Second))
	}
	if cfg.ReconnectBanThreshold > 0 {
		fc.ReconnectBanThreshold = intPtr(cfg.ReconnectBanThreshold)
	}
	if cfg.ReconnectBanWindowSeconds > 0 {
		fc.ReconnectBanWindowSeconds = intPtr(cfg.ReconnectBanWindowSeconds)
	}
	if cfg.ReconnectBanDurationSeconds > 0 {
		fc.ReconnectBanDurationSeconds = intPtr(cfg.ReconnectBanDurationSeconds)
	}

	data, err := json.MarshalIndent(fc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}

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

func applyFileConfig(cfg *Config, fc fileConfig) {
	if fc.PoolListen != "" {
		cfg.ListenAddr = fc.PoolListen
	}
	if fc.StatusListen != "" {
		cfg.StatusAddr = fc.StatusListen
	}
	if fc.StatusTLSListen != "" {
		cfg.StatusTLSAddr = fc.StatusTLSListen
	}
	if fc.StatusBrandName != "" {
		cfg.StatusBrandName = fc.StatusBrandName
	}
	if fc.StatusBrandDomain != "" {
		cfg.StatusBrandDomain = fc.StatusBrandDomain
	}
	if fc.StatusTagline != "" {
		cfg.StatusTagline = fc.StatusTagline
	}
	if fc.FiatCurrency != "" {
		cfg.FiatCurrency = strings.ToLower(strings.TrimSpace(fc.FiatCurrency))
	}
	if fc.DonationAddress != "" {
		cfg.DonationAddress = strings.TrimSpace(fc.DonationAddress)
	}
	if fc.DiscordURL != "" {
		cfg.DiscordURL = strings.TrimSpace(fc.DiscordURL)
	}
	if fc.StratumTLSListen != "" {
		addr := strings.TrimSpace(fc.StratumTLSListen)
		// Be forgiving: if the operator specified only a port like "4333",
		// treat it as ":4333" so net.Listen/tls.Listen accept it.
		if addr != "" && !strings.Contains(addr, ":") {
			addr = ":" + addr
		}
		cfg.StratumTLSListen = addr
	}
	if fc.RPCURL != "" {
		cfg.RPCURL = fc.RPCURL
	}
	if fc.PayoutAddress != "" {
		cfg.PayoutAddress = fc.PayoutAddress
	}
	if fc.PoolFeePercent != nil {
		cfg.PoolFeePercent = *fc.PoolFeePercent
	}
	if fc.Extranonce2Size != nil {
		cfg.Extranonce2Size = *fc.Extranonce2Size
	}
	if fc.TemplateExtraNonce2Size != nil {
		cfg.TemplateExtraNonce2Size = *fc.TemplateExtraNonce2Size
	}
	if fc.CoinbaseMsg != "" {
		cfg.CoinbaseMsg = fc.CoinbaseMsg
	}
	if fc.CoinbasePoolTag != nil {
		cfg.CoinbasePoolTag = *fc.CoinbasePoolTag
	}
	if fc.ZMQBlockAddr != "" {
		cfg.ZMQBlockAddr = fc.ZMQBlockAddr
	}
	if fc.DataDir != "" {
		cfg.DataDir = fc.DataDir
	}
	if fc.ShareLogBufferBytes != nil {
		cfg.ShareLogBufferBytes = *fc.ShareLogBufferBytes
	}
	if fc.CoinbaseSuffixBytes != nil {
		cfg.CoinbaseSuffixBytes = *fc.CoinbaseSuffixBytes
	}
	if fc.CoinbaseScriptSigMaxBytes != nil {
		cfg.CoinbaseScriptSigMaxBytes = *fc.CoinbaseScriptSigMaxBytes
	}
	if fc.FsyncShareLog != nil {
		cfg.FsyncShareLog = *fc.FsyncShareLog
	}
	if fc.ShareLogReplayBytes != nil {
		cfg.ShareLogReplayBytes = *fc.ShareLogReplayBytes
	}
	if fc.MaxConns != nil {
		cfg.MaxConns = *fc.MaxConns
	}
	if fc.MaxAcceptsPerSecond != nil {
		cfg.MaxAcceptsPerSecond = *fc.MaxAcceptsPerSecond
	}
	if fc.MaxAcceptBurst != nil {
		cfg.MaxAcceptBurst = *fc.MaxAcceptBurst
	}
	if fc.AutoAcceptRateLimits != nil {
		cfg.AutoAcceptRateLimits = *fc.AutoAcceptRateLimits
	}
	if fc.AcceptReconnectWindow != nil {
		cfg.AcceptReconnectWindow = *fc.AcceptReconnectWindow
	}
	if fc.AcceptBurstWindow != nil {
		cfg.AcceptBurstWindow = *fc.AcceptBurstWindow
	}
	if fc.AcceptSteadyStateWindow != nil {
		cfg.AcceptSteadyStateWindow = *fc.AcceptSteadyStateWindow
	}
	if fc.AcceptSteadyStateRate != nil {
		cfg.AcceptSteadyStateRate = *fc.AcceptSteadyStateRate
	}
	if fc.AcceptSteadyStateReconnectPercent != nil {
		cfg.AcceptSteadyStateReconnectPercent = *fc.AcceptSteadyStateReconnectPercent
	}
	if fc.AcceptSteadyStateReconnectWindow != nil {
		cfg.AcceptSteadyStateReconnectWindow = *fc.AcceptSteadyStateReconnectWindow
	}
	if fc.MaxRecentJobs != nil {
		cfg.MaxRecentJobs = *fc.MaxRecentJobs
	}
	if fc.SubscribeTimeoutSec != nil {
		cfg.SubscribeTimeout = time.Duration(*fc.SubscribeTimeoutSec) * time.Second
	}
	if fc.AuthorizeTimeoutSec != nil {
		cfg.AuthorizeTimeout = time.Duration(*fc.AuthorizeTimeoutSec) * time.Second
	}
	if fc.StratumReadTimeoutSec != nil {
		cfg.StratumReadTimeout = time.Duration(*fc.StratumReadTimeoutSec) * time.Second
	}
	if fc.MinVersionBits != nil {
		cfg.MinVersionBits = *fc.MinVersionBits
	}
	if fc.MaxDifficulty != nil {
		cfg.MaxDifficulty = *fc.MaxDifficulty
	}
	if fc.MinDifficulty != nil {
		cfg.MinDifficulty = *fc.MinDifficulty
	}
	if fc.LockSuggestedDifficulty != nil {
		cfg.LockSuggestedDifficulty = *fc.LockSuggestedDifficulty
	}
	if fc.HideLowDiffErrors != nil {
		cfg.HideLowDiffErrors = *fc.HideLowDiffErrors
	}
	if fc.HashrateEMATauSeconds != nil && *fc.HashrateEMATauSeconds > 0 {
		cfg.HashrateEMATauSeconds = *fc.HashrateEMATauSeconds
	}
	if fc.NTimeForwardSlackSec != nil && *fc.NTimeForwardSlackSec > 0 {
		cfg.NTimeForwardSlackSeconds = *fc.NTimeForwardSlackSec
	}
	if fc.BanInvalidSubmissionsAfter != nil && *fc.BanInvalidSubmissionsAfter >= 0 {
		cfg.BanInvalidSubmissionsAfter = *fc.BanInvalidSubmissionsAfter
	}
	if fc.BanInvalidSubmissionsWindowSec != nil && *fc.BanInvalidSubmissionsWindowSec > 0 {
		cfg.BanInvalidSubmissionsWindow = time.Duration(*fc.BanInvalidSubmissionsWindowSec) * time.Second
	}
	if fc.BanInvalidSubmissionsDurationSec != nil && *fc.BanInvalidSubmissionsDurationSec > 0 {
		cfg.BanInvalidSubmissionsDuration = time.Duration(*fc.BanInvalidSubmissionsDurationSec) * time.Second
	}
	if fc.ReconnectBanThreshold != nil && *fc.ReconnectBanThreshold >= 0 {
		cfg.ReconnectBanThreshold = *fc.ReconnectBanThreshold
	}
	if fc.ReconnectBanWindowSeconds != nil && *fc.ReconnectBanWindowSeconds > 0 {
		cfg.ReconnectBanWindowSeconds = *fc.ReconnectBanWindowSeconds
	}
	if fc.ReconnectBanDurationSeconds != nil && *fc.ReconnectBanDurationSeconds > 0 {
		cfg.ReconnectBanDurationSeconds = *fc.ReconnectBanDurationSeconds
	}
	if cfg.LockSuggestedDifficulty {
		cfg.HideLowDiffErrors = false
	}
}

func applySecretsConfig(cfg *Config, sc secretsConfig) {
	if sc.RPCUser != "" {
		cfg.RPCUser = sc.RPCUser
	}
	if sc.RPCPass != "" {
		cfg.RPCPass = sc.RPCPass
	}
}

func (cfg Config) Effective() EffectiveConfig {
	return EffectiveConfig{
		ListenAddr:                        cfg.ListenAddr,
		StatusAddr:                        cfg.StatusAddr,
		StatusTLSAddr:                     cfg.StatusTLSAddr,
		StatusBrandName:                   cfg.StatusBrandName,
		StatusBrandDomain:                 cfg.StatusBrandDomain,
		StatusTagline:                     cfg.StatusTagline,
		FiatCurrency:                      cfg.FiatCurrency,
		DonationAddress:                   cfg.DonationAddress,
		DiscordURL:                        cfg.DiscordURL,
		RPCURL:                            cfg.RPCURL,
		RPCUser:                           cfg.RPCUser,
		RPCPassSet:                        strings.TrimSpace(cfg.RPCPass) != "",
		PayoutAddress:                     cfg.PayoutAddress,
		PoolFeePercent:                    cfg.PoolFeePercent,
		Extranonce2Size:                   cfg.Extranonce2Size,
		TemplateExtraNonce2Size:           cfg.TemplateExtraNonce2Size,
		CoinbaseSuffixBytes:               cfg.CoinbaseSuffixBytes,
		CoinbasePoolTag:                   cfg.CoinbasePoolTag,
		CoinbaseMsg:                       cfg.CoinbaseMsg,
		CoinbaseScriptSigMaxBytes:         cfg.CoinbaseScriptSigMaxBytes,
		ZMQBlockAddr:                      cfg.ZMQBlockAddr,
		DataDir:                           cfg.DataDir,
		ShareLogBufferBytes:               cfg.ShareLogBufferBytes,
		FsyncShareLog:                     cfg.FsyncShareLog,
		ShareLogReplayBytes:               cfg.ShareLogReplayBytes,
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
		MaxRecentJobs:                     cfg.MaxRecentJobs,
		SubscribeTimeout:                  cfg.SubscribeTimeout.String(),
		AuthorizeTimeout:                  cfg.AuthorizeTimeout.String(),
		StratumReadTimeout:                cfg.StratumReadTimeout.String(),
		VersionMask:                       fmt.Sprintf("%08x", cfg.VersionMask),
		MinVersionBits:                    cfg.MinVersionBits,
		MaxDifficulty:                     cfg.MaxDifficulty,
		MinDifficulty:                     cfg.MinDifficulty,
		// Effective config mirrors whether suggested difficulty locking is enabled.
		LockSuggestedDifficulty:       cfg.LockSuggestedDifficulty,
		HideLowDiffErrors:             cfg.HideLowDiffErrors,
		HashrateEMATauSeconds:         cfg.HashrateEMATauSeconds,
		NTimeForwardSlackSec:          cfg.NTimeForwardSlackSeconds,
		BanInvalidSubmissionsAfter:    cfg.BanInvalidSubmissionsAfter,
		BanInvalidSubmissionsWindow:   cfg.BanInvalidSubmissionsWindow.String(),
		BanInvalidSubmissionsDuration: cfg.BanInvalidSubmissionsDuration.String(),
		ReconnectBanThreshold:         cfg.ReconnectBanThreshold,
		ReconnectBanWindowSeconds:     cfg.ReconnectBanWindowSeconds,
		ReconnectBanDurationSeconds:   cfg.ReconnectBanDurationSeconds,
	}
}

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
	if cfg.CoinbaseScriptSigMaxBytes < 0 {
		return fmt.Errorf("coinbase_scriptsig_max_bytes cannot be negative")
	}
	if cfg.SubscribeTimeout < 0 {
		return fmt.Errorf("subscribe_timeout_seconds cannot be negative")
	}
	if cfg.AuthorizeTimeout < 0 {
		return fmt.Errorf("authorize_timeout_seconds cannot be negative")
	}
	if cfg.StratumReadTimeout <= 0 {
		return fmt.Errorf("stratum_read_timeout_seconds must be > 0, got %s", cfg.StratumReadTimeout)
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
	if cfg.HashrateEMATauSeconds <= 0 {
		return fmt.Errorf("hashrate_ema_tau_seconds must be > 0, got %v", cfg.HashrateEMATauSeconds)
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
			// drop any other characters (spaces, newlines, punctuation, etc.)
		}
	}
	if len(cleaned) == 0 {
		return ""
	}
	return string(cleaned)
}

// versionMaskRPC is the minimal RPC interface needed by
// autoConfigureVersionMaskFromNode. It is satisfied by *RPCClient and by
// test fakes.
type versionMaskRPC interface {
	callCtx(ctx context.Context, method string, params interface{}, out interface{}) error
}

// autoConfigureVersionMaskFromNode inspects the connected Bitcoin node to
// choose a sensible base version-rolling mask for the active network, so
// operators no longer need to set version_mask manually in config.
//
// - mainnet/testnet/signet: use defaultVersionMask (0x1fffe000)
// - regtest: use a wider mask (0x3fffe000) to keep bit 29 available
//
// If the RPC call fails or returns an unknown chain, the existing mask is left
// unchanged and the pool falls back to its compiled-in defaults.
func autoConfigureVersionMaskFromNode(ctx context.Context, rpc versionMaskRPC, cfg *Config) {
	if rpc == nil || cfg == nil {
		return
	}
	// If an explicit mask was set programmatically, don't override it.
	if cfg.VersionMaskConfigured {
		return
	}

	type blockchainInfo struct {
		Chain string `json:"chain"`
	}

	// Bound the RPC call so we don't block shutdown indefinitely.
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
//
// This ensures that when the pool restarts, all miners can reconnect quickly
// without hitting rate limits.
//
// The logic uses two configurable time windows:
// 1. Initial burst (accept_burst_window seconds): handles the immediate reconnection storm
//   - Burst capacity allows a percentage of miners to connect immediately
//
// 2. Sustained reconnection (remaining time): handles the rest of reconnections
//   - Per-second rate allows remaining miners to connect over the rest of the window
//
// Combined, this allows all max_conns miners to reconnect within accept_reconnect_window
// seconds of a pool restart without being rate-limited, while still protecting against
// sustained connection floods during normal operation.
func autoConfigureAcceptRateLimits(cfg *Config, fileConfigLoaded bool) {
	if cfg == nil || cfg.MaxConns <= 0 {
		return
	}

	// Ensure we have valid window settings
	reconnectWindow := cfg.AcceptReconnectWindow
	if reconnectWindow <= 0 {
		reconnectWindow = defaultAcceptReconnectWindow
	}
	burstWindow := cfg.AcceptBurstWindow
	if burstWindow <= 0 {
		burstWindow = defaultAcceptBurstWindow
	}
	// Ensure burst window doesn't exceed reconnect window
	if burstWindow >= reconnectWindow {
		burstWindow = reconnectWindow / 2
		if burstWindow < 1 {
			burstWindow = 1
		}
	}

	// Read the config file to check if rate limits were explicitly set
	var explicitMaxAccepts, explicitMaxBurst, explicitSteadyStateRate bool
	if fileConfigLoaded {
		configPath := defaultConfigPath()
		if fc, ok, err := loadConfigFile(configPath); err == nil && ok {
			explicitMaxAccepts = fc.MaxAcceptsPerSecond != nil
			explicitMaxBurst = fc.MaxAcceptBurst != nil
			explicitSteadyStateRate = fc.AcceptSteadyStateRate != nil
		}
	}

	// Auto-configure max_accept_burst if:
	// 1. auto_accept_rate_limits is enabled (always override), OR
	// 2. not explicitly set in config AND currently at default value
	// Calculate what percentage of miners can burst based on the burst window
	shouldConfigureBurst := cfg.AutoAcceptRateLimits || (!explicitMaxBurst && cfg.MaxAcceptBurst == defaultMaxAcceptBurst)
	if shouldConfigureBurst {
		// Burst window handles a proportional amount of total miners
		// For 15s total with 5s burst: 5/15 = 33% of miners in burst
		burstFraction := float64(burstWindow) / float64(reconnectWindow)
		burstCapacity := int(float64(cfg.MaxConns) * burstFraction)
		if burstCapacity < 20 {
			burstCapacity = 20 // minimum burst of 20
		}
		// Cap at reasonable maximum to avoid memory issues in token bucket
		if burstCapacity > 25000 {
			burstCapacity = 25000
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
		// Remaining miners after burst
		burstFraction := float64(burstWindow) / float64(reconnectWindow)
		remainingMiners := int(float64(cfg.MaxConns) * (1.0 - burstFraction))
		// Spread over remaining time
		sustainedWindow := reconnectWindow - burstWindow
		if sustainedWindow < 1 {
			sustainedWindow = 1
		}
		sustainedRate := remainingMiners / sustainedWindow
		if sustainedRate < 10 {
			sustainedRate = 10 // minimum 10 accepts/sec
		}
		// Cap at a reasonable maximum to avoid overwhelming the system
		if sustainedRate > 10000 {
			sustainedRate = 10000
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
		steadyStateRate := int(expectedReconnects / float64(steadyStateWindow))

		// Apply minimum to ensure we don't set rate too low
		if steadyStateRate < 5 {
			steadyStateRate = 5 // minimum 5 accepts/sec for steady-state
		}
		// Cap at reasonable maximum
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
