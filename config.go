package main

import (
	"context"
	"errors"
	"fmt"
	"math/bits"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml"
)

var secretsConfigExample = []byte(`# RPC credentials for bitcoind
rpc_user = "bitcoinrpc"
rpc_pass = "password"
`)

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
	// PoolDonationAddress is an optional wallet where users can donate TO
	// the pool operator. It is shown in the UI footer and never used for payouts.
	PoolDonationAddress string
	// DiscordURL is an optional Discord invite link shown in the header.
	DiscordURL string
	// GitHubURL is an optional GitHub link shown in the header and About page.
	GitHubURL string
	// ServerLocation is an optional server location string shown in the header.
	ServerLocation string
	// StratumTLSListen is an optional TCP address for a TLS-enabled
	// Stratum listener (e.g. ":3443"). When empty, TLS for Stratum is
	// disabled and only the plain TCP listener is used.
	StratumTLSListen string
	RPCURL           string // e.g. "http://127.0.0.1:8332"
	RPCUser          string
	RPCPass          string
	PayoutAddress    string
	// PayoutScript is reserved for future internal overrides and is not
	// populated from or written to config.toml.
	PayoutScript   string
	PoolFeePercent float64
	// OperatorDonationPercent is the percentage of the pool operator's fee
	// to donate to another wallet. This is a percentage of the pool fee, not
	// the total block reward. For example, if pool_fee_percent is 2% and
	// operator_donation_percent is 10%, then 10% of the 2% pool fee (0.2% of
	// the total reward) goes to the operator's chosen donation address.
	OperatorDonationPercent float64
	// OperatorDonationAddress is the wallet address where the pool operator
	// donates a portion of their fee. This must be set if operator_donation_percent > 0.
	OperatorDonationAddress string
	// OperatorDonationName is an optional display name for the operator's
	// donation recipient shown in the UI when viewing coinbase outputs.
	OperatorDonationName string
	// OperatorDonationURL is an optional hyperlink for the donation recipient.
	// When set, the OperatorDonationName becomes a clickable link in the UI.
	OperatorDonationURL       string
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
	// KickDuplicateWorkerNames controls whether reconnecting with the same
	// worker name closes its prior connection once wallet validation succeeds.
	// Default false allows multiple connections with the same name.
	KickDuplicateWorkerNames bool
	// HashrateEMATauSeconds controls the time constant (in seconds) for the
	// per-connection hashrate exponential moving average.
	HashrateEMATauSeconds float64
	// HashrateEMAMinShares defines how many shares must be accepted between
	// EMA samples to ensure the starting window spans enough work.
	HashrateEMAMinShares int
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

	// PeerCleanupEnabled toggles automatic removal of high-latency node peers.
	PeerCleanupEnabled bool
	// PeerCleanupMaxPingMs sets the latency threshold (ms) above which peers are candidates for cleanup.
	PeerCleanupMaxPingMs float64
	// PeerCleanupMinPeers sets the minimum number of peers to keep after cleanup.
	PeerCleanupMinPeers int
}

type EffectiveConfig struct {
	ListenAddr                        string  `json:"listen_addr"`
	StatusAddr                        string  `json:"status_addr"`
	StatusTLSAddr                     string  `json:"status_tls_listen,omitempty"`
	StatusBrandName                   string  `json:"status_brand_name,omitempty"`
	StatusBrandDomain                 string  `json:"status_brand_domain,omitempty"`
	StatusTagline                     string  `json:"status_tagline,omitempty"`
	FiatCurrency                      string  `json:"fiat_currency,omitempty"`
	PoolDonationAddress               string  `json:"pool_donation_address,omitempty"`
	DiscordURL                        string  `json:"discord_url,omitempty"`
	GitHubURL                         string  `json:"github_url,omitempty"`
	ServerLocation                    string  `json:"server_location,omitempty"`
	StratumTLSListen                  string  `json:"stratum_tls_listen,omitempty"`
	RPCURL                            string  `json:"rpc_url"`
	RPCUser                           string  `json:"rpc_user"`
	RPCPassSet                        bool    `json:"rpc_pass_set"`
	PayoutAddress                     string  `json:"payout_address"`
	PoolFeePercent                    float64 `json:"pool_fee_percent,omitempty"`
	OperatorDonationPercent           float64 `json:"operator_donation_percent,omitempty"`
	OperatorDonationAddress           string  `json:"operator_donation_address,omitempty"`
	OperatorDonationName              string  `json:"operator_donation_name,omitempty"`
	OperatorDonationURL               string  `json:"operator_donation_url,omitempty"`
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
	KickDuplicateWorkerNames          bool    `json:"kick_duplicate_worker_names,omitempty"`
	HashrateEMATauSeconds             float64 `json:"hashrate_ema_tau_seconds,omitempty"`
	HashrateEMAMinShares              int     `json:"hashrate_ema_min_shares,omitempty"`
	NTimeForwardSlackSec              int     `json:"ntime_forward_slack_seconds,omitempty"`
	BanInvalidSubmissionsAfter        int     `json:"ban_invalid_submissions_after,omitempty"`
	BanInvalidSubmissionsWindow       string  `json:"ban_invalid_submissions_window,omitempty"`
	BanInvalidSubmissionsDuration     string  `json:"ban_invalid_submissions_duration,omitempty"`
	ReconnectBanThreshold             int     `json:"reconnect_ban_threshold,omitempty"`
	ReconnectBanWindowSeconds         int     `json:"reconnect_ban_window_seconds,omitempty"`
	ReconnectBanDurationSeconds       int     `json:"reconnect_ban_duration_seconds,omitempty"`
	PeerCleanupEnabled                bool    `json:"peer_cleanup_enabled,omitempty"`
	PeerCleanupMaxPingMs              float64 `json:"peer_cleanup_max_ping_ms,omitempty"`
	PeerCleanupMinPeers               int     `json:"peer_cleanup_min_peers,omitempty"`
}

type serverConfig struct {
	PoolListen      string `toml:"pool_listen"`
	StatusListen    string `toml:"status_listen"`
	StatusTLSListen string `toml:"status_tls_listen"`
}

type brandingConfig struct {
	StatusBrandName     string `toml:"status_brand_name"`
	StatusBrandDomain   string `toml:"status_brand_domain"`
	StatusTagline       string `toml:"status_tagline"`
	FiatCurrency        string `toml:"fiat_currency"`
	PoolDonationAddress string `toml:"pool_donation_address"`
	DiscordURL          string `toml:"discord_url"`
	GitHubURL           string `toml:"github_url"`
	ServerLocation      string `toml:"server_location"`
}

type stratumConfig struct {
	StratumTLSListen string `toml:"stratum_tls_listen"`
}

type nodeConfig struct {
	RPCURL        string `toml:"rpc_url"`
	PayoutAddress string `toml:"payout_address"`
	DataDir       string `toml:"data_dir"`
	ZMQBlockAddr  string `toml:"zmq_block_addr"`
}

type miningConfig struct {
	PoolFeePercent            *float64 `toml:"pool_fee_percent"`
	OperatorDonationPercent   *float64 `toml:"operator_donation_percent"`
	OperatorDonationAddress   string   `toml:"operator_donation_address"`
	OperatorDonationName      string   `toml:"operator_donation_name"`
	OperatorDonationURL       string   `toml:"operator_donation_url"`
	Extranonce2Size           *int     `toml:"extranonce2_size"`
	TemplateExtraNonce2Size   *int     `toml:"template_extra_nonce2_size"`
	CoinbaseSuffixBytes       *int     `toml:"coinbase_suffix_bytes"`
	CoinbasePoolTag           *string  `toml:"coinbase_pool_tag"`
	CoinbaseMsg               string   `toml:"coinbase_message"`
	CoinbaseScriptSigMaxBytes *int     `toml:"coinbase_scriptsig_max_bytes"`
}

type baseFileConfig struct {
	Server   serverConfig   `toml:"server"`
	Branding brandingConfig `toml:"branding"`
	Stratum  stratumConfig  `toml:"stratum"`
	Node     nodeConfig     `toml:"node"`
	Mining   miningConfig   `toml:"mining"`
}

func float64Ptr(v float64) *float64 { return &v }
func intPtr(v int) *int             { return &v }
func stringPtr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
func boolPtr(v bool) *bool {
	return &v
}
func int64Ptr(v int64) *int64 {
	return &v
}

type loggingTuning struct {
	ShareLogBufferBytes *int   `toml:"share_log_buffer_bytes"`
	FsyncShareLog       *bool  `toml:"fsync_share_log"`
	ShareLogReplayBytes *int64 `toml:"share_log_replay_bytes"`
}

type rateLimitTuning struct {
	MaxConns                          *int     `toml:"max_conns"`
	MaxAcceptsPerSecond               *int     `toml:"max_accepts_per_second"`
	MaxAcceptBurst                    *int     `toml:"max_accept_burst"`
	AutoAcceptRateLimits              *bool    `toml:"auto_accept_rate_limits"`
	AcceptReconnectWindow             *int     `toml:"accept_reconnect_window"`
	AcceptBurstWindow                 *int     `toml:"accept_burst_window"`
	AcceptSteadyStateWindow           *int     `toml:"accept_steady_state_window"`
	AcceptSteadyStateRate             *int     `toml:"accept_steady_state_rate"`
	AcceptSteadyStateReconnectPercent *float64 `toml:"accept_steady_state_reconnect_percent"`
	AcceptSteadyStateReconnectWindow  *int     `toml:"accept_steady_state_reconnect_window"`
}

type timeoutTuning struct {
	SubscribeTimeoutSec   *int `toml:"subscribe_timeout_seconds"`
	AuthorizeTimeoutSec   *int `toml:"authorize_timeout_seconds"`
	StratumReadTimeoutSec *int `toml:"stratum_read_timeout_seconds"`
}

type difficultyTuning struct {
	MaxDifficulty           *float64 `toml:"max_difficulty"`
	MinDifficulty           *float64 `toml:"min_difficulty"`
	LockSuggestedDifficulty *bool    `toml:"lock_suggested_difficulty"`
}

type hashrateTuning struct {
	HashrateEMATauSeconds    *float64 `toml:"hashrate_ema_tau_seconds"`
	HashrateEMAMinShares     *int     `toml:"hashrate_ema_min_shares"`
	NTimeForwardSlackSeconds *int     `toml:"ntime_forward_slack_seconds"`
}

type peerCleaningTuning struct {
	Enabled   *bool    `toml:"enabled"`
	MaxPingMs *float64 `toml:"max_ping_ms"`
	MinPeers  *int     `toml:"min_peers"`
}

type banTuning struct {
	BanInvalidSubmissionsAfter       *int `toml:"ban_invalid_submissions_after"`
	BanInvalidSubmissionsWindowSec   *int `toml:"ban_invalid_submissions_window_seconds"`
	BanInvalidSubmissionsDurationSec *int `toml:"ban_invalid_submissions_duration_seconds"`
	ReconnectBanThreshold            *int `toml:"reconnect_ban_threshold"`
	ReconnectBanWindowSeconds        *int `toml:"reconnect_ban_window_seconds"`
	ReconnectBanDurationSeconds      *int `toml:"reconnect_ban_duration_seconds"`
}

type versionTuning struct {
	MinVersionBits *int `toml:"min_version_bits"`
}

type workerTuning struct {
	KickDuplicateWorkerNames *bool `toml:"kick_duplicate_worker_names"`
}

type tuningFileConfig struct {
	Logging      loggingTuning      `toml:"logging"`
	RateLimits   rateLimitTuning    `toml:"rate_limits"`
	Timeouts     timeoutTuning      `toml:"timeouts"`
	Difficulty   difficultyTuning   `toml:"difficulty"`
	Hashrate     hashrateTuning     `toml:"hashrate"`
	PeerCleaning peerCleaningTuning `toml:"peer_cleaning"`
	Bans         banTuning          `toml:"bans"`
	Version      versionTuning      `toml:"version"`
	Workers      workerTuning       `toml:"workers"`
}

func buildBaseFileConfig(cfg Config) baseFileConfig {
	return baseFileConfig{
		Server: serverConfig{
			PoolListen:      cfg.ListenAddr,
			StatusListen:    cfg.StatusAddr,
			StatusTLSListen: cfg.StatusTLSAddr,
		},
		Branding: brandingConfig{
			StatusBrandName:     cfg.StatusBrandName,
			StatusBrandDomain:   cfg.StatusBrandDomain,
			StatusTagline:       cfg.StatusTagline,
			FiatCurrency:        cfg.FiatCurrency,
			PoolDonationAddress: cfg.PoolDonationAddress,
			DiscordURL:          cfg.DiscordURL,
			GitHubURL:           cfg.GitHubURL,
			ServerLocation:      cfg.ServerLocation,
		},
		Stratum: stratumConfig{
			StratumTLSListen: cfg.StratumTLSListen,
		},
		Node: nodeConfig{
			RPCURL:        cfg.RPCURL,
			PayoutAddress: cfg.PayoutAddress,
			DataDir:       cfg.DataDir,
			ZMQBlockAddr:  cfg.ZMQBlockAddr,
		},
		Mining: miningConfig{
			PoolFeePercent:            float64Ptr(cfg.PoolFeePercent),
			OperatorDonationPercent:   float64Ptr(cfg.OperatorDonationPercent),
			OperatorDonationAddress:   cfg.OperatorDonationAddress,
			OperatorDonationName:      cfg.OperatorDonationName,
			OperatorDonationURL:       cfg.OperatorDonationURL,
			Extranonce2Size:           intPtr(cfg.Extranonce2Size),
			TemplateExtraNonce2Size:   intPtr(cfg.TemplateExtraNonce2Size),
			CoinbaseSuffixBytes:       intPtr(cfg.CoinbaseSuffixBytes),
			CoinbasePoolTag:           stringPtr(cfg.CoinbasePoolTag),
			CoinbaseMsg:               cfg.CoinbaseMsg,
			CoinbaseScriptSigMaxBytes: intPtr(cfg.CoinbaseScriptSigMaxBytes),
		},
	}
}

func buildTuningFileConfig(cfg Config) tuningFileConfig {
	return tuningFileConfig{
		Logging: loggingTuning{
			ShareLogBufferBytes: intPtr(cfg.ShareLogBufferBytes),
			FsyncShareLog:       boolPtr(cfg.FsyncShareLog),
			ShareLogReplayBytes: int64Ptr(cfg.ShareLogReplayBytes),
		},
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
		},
		Timeouts: timeoutTuning{
			SubscribeTimeoutSec:   intPtr(int(cfg.SubscribeTimeout / time.Second)),
			AuthorizeTimeoutSec:   intPtr(int(cfg.AuthorizeTimeout / time.Second)),
			StratumReadTimeoutSec: intPtr(int(cfg.StratumReadTimeout / time.Second)),
		},
		Difficulty: difficultyTuning{
			MaxDifficulty:           float64Ptr(cfg.MaxDifficulty),
			MinDifficulty:           float64Ptr(cfg.MinDifficulty),
			LockSuggestedDifficulty: boolPtr(cfg.LockSuggestedDifficulty),
		},
		Hashrate: hashrateTuning{
			HashrateEMATauSeconds:    float64Ptr(cfg.HashrateEMATauSeconds),
			HashrateEMAMinShares:     intPtr(cfg.HashrateEMAMinShares),
			NTimeForwardSlackSeconds: intPtr(cfg.NTimeForwardSlackSeconds),
		},
		PeerCleaning: peerCleaningTuning{
			Enabled:   boolPtr(cfg.PeerCleanupEnabled),
			MaxPingMs: float64Ptr(cfg.PeerCleanupMaxPingMs),
			MinPeers:  intPtr(cfg.PeerCleanupMinPeers),
		},
		Bans: banTuning{
			BanInvalidSubmissionsAfter:       intPtr(cfg.BanInvalidSubmissionsAfter),
			BanInvalidSubmissionsWindowSec:   intPtr(int(cfg.BanInvalidSubmissionsWindow / time.Second)),
			BanInvalidSubmissionsDurationSec: intPtr(int(cfg.BanInvalidSubmissionsDuration / time.Second)),
			ReconnectBanThreshold:            intPtr(cfg.ReconnectBanThreshold),
			ReconnectBanWindowSeconds:        intPtr(cfg.ReconnectBanWindowSeconds),
			ReconnectBanDurationSeconds:      intPtr(cfg.ReconnectBanDurationSeconds),
		},
		Version: versionTuning{
			MinVersionBits: intPtr(cfg.MinVersionBits),
		},
		Workers: workerTuning{
			KickDuplicateWorkerNames: boolPtr(cfg.KickDuplicateWorkerNames),
		},
	}
}

// secretsConfig holds sensitive RPC credentials required for pool operation.
// These values are kept in a separate file (secrets.toml) so the main
// config.toml can be checked into version control or shared without exposing
// credentials.
// Both rpc_user and rpc_pass are required for the pool to communicate with
// bitcoind and must be set in secrets.toml.
type secretsConfig struct {
	RPCUser string `toml:"rpc_user"`
	RPCPass string `toml:"rpc_pass"`
}

func loadConfig(configPath, secretsPath string) Config {
	cfg := defaultConfig()

	if configPath == "" {
		configPath = defaultConfigPath()
	}

	var configFileExisted bool
	if bc, ok, err := loadBaseConfigFile(configPath); err != nil {
		fatal("config file", err, "path", configPath)
	} else if ok {
		configFileExisted = true
		applyBaseConfig(&cfg, *bc)
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

	if cfg.CoinbasePoolTag == "" {
		cfg.CoinbasePoolTag = generatePoolTag()
		if configFileExisted {
			if err := rewriteConfigFile(configPath, cfg); err != nil {
				logger.Warn("persist coinbase_pool_tag", "path", configPath, "error", err)
			} else {
				logger.Info("generated coinbase_pool_tag and updated config", "path", configPath, "coinbase_pool_tag", cfg.CoinbasePoolTag)
			}
		}
	}

	// Load secrets file: RPC credentials are required for pool operation.
	// Secrets are kept in a separate file so the main config can be shared
	// or version-controlled without exposing sensitive credentials.
	if secretsPath == "" {
		secretsPath = filepath.Join(cfg.DataDir, "config", "secrets.toml")
	}
	if sc, ok, err := loadSecretsFile(secretsPath); err != nil {
		fatal("secrets file", err, "path", secretsPath)
	} else if ok {
		applySecretsConfig(&cfg, *sc)
	} else {
		secretsExamplePath := filepath.Join(cfg.DataDir, "config", "examples", "secrets.toml.example")
		fmt.Printf("\n🔐 Secrets file is missing. RPC credentials are required.\n\n")
		fmt.Printf("   To configure:\n")
		fmt.Printf("   1. Copy example: %s\n", secretsExamplePath)
		fmt.Printf("   2. To:           %s\n", secretsPath)
		fmt.Printf("   3. Edit the file and set your rpc_user and rpc_pass\n")
		fmt.Printf("   4. Restart goPool\n\n")

		os.Exit(1)
	}

	// Optional advanced/tuning overlay: if data_dir/config/tuning.toml exists,
	// load it as a second config file and apply it on top of the main config.
	// This lets operators keep advanced knobs separate and delete the file to
	// fall back to defaults.
	tuningPath := filepath.Join(cfg.DataDir, "config", "tuning.toml")
	var tuningOverrides tuningFileConfig
	var tuningConfigLoaded bool
	if tf, ok, err := loadTuningFile(tuningPath); err != nil {
		fatal("tuning config file", err, "path", tuningPath)
	} else if ok {
		applyTuningConfig(&cfg, *tf)
		tuningConfigLoaded = ok
		tuningOverrides = *tf
	}

	// Sanitize payout address to strip stray whitespace or unexpected
	// characters before it is used for RPC validation and coinbase outputs.
	cfg.PayoutAddress = sanitizePayoutAddress(cfg.PayoutAddress)

	// Auto-configure accept rate limits based on max_conns if they weren't
	// explicitly set in the config file. This ensures miners can reconnect
	// smoothly after pool restarts without hitting rate limits.
	autoConfigureAcceptRateLimits(&cfg, tuningOverrides, tuningConfigLoaded)

	return cfg
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

func loadBaseConfigFile(path string) (*baseFileConfig, bool, error) {
	return loadTOMLFile[baseFileConfig](path)
}

func loadTuningFile(path string) (*tuningFileConfig, bool, error) {
	return loadTOMLFile[tuningFileConfig](path)
}

func loadSecretsFile(path string) (*secretsConfig, bool, error) {
	return loadTOMLFile[secretsConfig](path)
}

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

func exampleHeader(text string) []byte {
	return []byte(fmt.Sprintf("# Generated %s example (copy to a real config and edit as needed)\n\n", text))
}

func exampleConfigBytes() []byte {
	cfg := defaultConfig()
	cfg.PayoutAddress = "YOUR_POOL_WALLET_ADDRESS_HERE"
	cfg.PoolDonationAddress = "OPTIONAL_POOL_DONATION_WALLET"
	fc := buildBaseFileConfig(cfg)
	data, err := toml.Marshal(fc)
	if err != nil {
		logger.Warn("encode config example failed", "error", err)
		return nil
	}
	return append(exampleHeader("base config"), data...)
}

func exampleTuningConfigBytes() []byte {
	cfg := defaultConfig()
	tf := buildTuningFileConfig(cfg)
	data, err := toml.Marshal(tf)
	if err != nil {
		logger.Warn("encode tuning config example failed", "error", err)
		return nil
	}
	return append(exampleHeader("tuning config"), data...)
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

func applyBaseConfig(cfg *Config, fc baseFileConfig) {
	if fc.Server.PoolListen != "" {
		cfg.ListenAddr = fc.Server.PoolListen
	}
	if fc.Server.StatusListen != "" {
		cfg.StatusAddr = fc.Server.StatusListen
	}
	if fc.Server.StatusTLSListen != "" {
		cfg.StatusTLSAddr = fc.Server.StatusTLSListen
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
	if fc.Branding.FiatCurrency != "" {
		cfg.FiatCurrency = strings.ToLower(strings.TrimSpace(fc.Branding.FiatCurrency))
	}
	if fc.Branding.PoolDonationAddress != "" {
		cfg.PoolDonationAddress = strings.TrimSpace(fc.Branding.PoolDonationAddress)
	}
	if fc.Branding.DiscordURL != "" {
		cfg.DiscordURL = strings.TrimSpace(fc.Branding.DiscordURL)
	}
	if fc.Branding.GitHubURL != "" {
		cfg.GitHubURL = strings.TrimSpace(fc.Branding.GitHubURL)
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
	if fc.Node.RPCURL != "" {
		cfg.RPCURL = fc.Node.RPCURL
	}
	if fc.Node.PayoutAddress != "" {
		cfg.PayoutAddress = fc.Node.PayoutAddress
	}
	if fc.Node.DataDir != "" {
		cfg.DataDir = fc.Node.DataDir
	}
	if fc.Node.ZMQBlockAddr != "" {
		cfg.ZMQBlockAddr = fc.Node.ZMQBlockAddr
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
	if fc.Mining.Extranonce2Size != nil {
		cfg.Extranonce2Size = *fc.Mining.Extranonce2Size
	}
	if fc.Mining.TemplateExtraNonce2Size != nil {
		cfg.TemplateExtraNonce2Size = *fc.Mining.TemplateExtraNonce2Size
	}
	if fc.Mining.CoinbaseMsg != "" {
		cfg.CoinbaseMsg = fc.Mining.CoinbaseMsg
	}
	if fc.Mining.CoinbasePoolTag != nil {
		cfg.CoinbasePoolTag = *fc.Mining.CoinbasePoolTag
	}
	if fc.Mining.CoinbaseSuffixBytes != nil {
		cfg.CoinbaseSuffixBytes = *fc.Mining.CoinbaseSuffixBytes
	}
	if fc.Mining.CoinbaseScriptSigMaxBytes != nil {
		cfg.CoinbaseScriptSigMaxBytes = *fc.Mining.CoinbaseScriptSigMaxBytes
	}
}

func applyTuningConfig(cfg *Config, fc tuningFileConfig) {
	if fc.Logging.ShareLogBufferBytes != nil {
		cfg.ShareLogBufferBytes = *fc.Logging.ShareLogBufferBytes
	}
	if fc.Logging.FsyncShareLog != nil {
		cfg.FsyncShareLog = *fc.Logging.FsyncShareLog
	}
	if fc.Logging.ShareLogReplayBytes != nil {
		cfg.ShareLogReplayBytes = *fc.Logging.ShareLogReplayBytes
	}
	if fc.RateLimits.MaxConns != nil {
		cfg.MaxConns = *fc.RateLimits.MaxConns
	}
	if fc.RateLimits.MaxAcceptsPerSecond != nil {
		cfg.MaxAcceptsPerSecond = *fc.RateLimits.MaxAcceptsPerSecond
	}
	if fc.RateLimits.MaxAcceptBurst != nil {
		cfg.MaxAcceptBurst = *fc.RateLimits.MaxAcceptBurst
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
	if fc.Timeouts.SubscribeTimeoutSec != nil {
		cfg.SubscribeTimeout = time.Duration(*fc.Timeouts.SubscribeTimeoutSec) * time.Second
	}
	if fc.Timeouts.AuthorizeTimeoutSec != nil {
		cfg.AuthorizeTimeout = time.Duration(*fc.Timeouts.AuthorizeTimeoutSec) * time.Second
	}
	if fc.Timeouts.StratumReadTimeoutSec != nil {
		cfg.StratumReadTimeout = time.Duration(*fc.Timeouts.StratumReadTimeoutSec) * time.Second
	}
	if fc.Difficulty.MaxDifficulty != nil {
		cfg.MaxDifficulty = *fc.Difficulty.MaxDifficulty
	}
	if fc.Difficulty.MinDifficulty != nil {
		cfg.MinDifficulty = *fc.Difficulty.MinDifficulty
	}
	if fc.Difficulty.LockSuggestedDifficulty != nil {
		cfg.LockSuggestedDifficulty = *fc.Difficulty.LockSuggestedDifficulty
	}
	if fc.Hashrate.HashrateEMATauSeconds != nil && *fc.Hashrate.HashrateEMATauSeconds > 0 {
		cfg.HashrateEMATauSeconds = *fc.Hashrate.HashrateEMATauSeconds
	}
	if fc.Hashrate.HashrateEMAMinShares != nil && *fc.Hashrate.HashrateEMAMinShares >= minHashrateEMAMinShares {
		cfg.HashrateEMAMinShares = *fc.Hashrate.HashrateEMAMinShares
	}
	if fc.Hashrate.NTimeForwardSlackSeconds != nil && *fc.Hashrate.NTimeForwardSlackSeconds > 0 {
		cfg.NTimeForwardSlackSeconds = *fc.Hashrate.NTimeForwardSlackSeconds
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
	if fc.Version.MinVersionBits != nil {
		cfg.MinVersionBits = *fc.Version.MinVersionBits
	}
	if fc.Workers.KickDuplicateWorkerNames != nil {
		cfg.KickDuplicateWorkerNames = *fc.Workers.KickDuplicateWorkerNames
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
		PoolDonationAddress:               cfg.PoolDonationAddress,
		DiscordURL:                        cfg.DiscordURL,
		GitHubURL:                         cfg.GitHubURL,
		ServerLocation:                    cfg.ServerLocation,
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
		HashrateEMATauSeconds:         cfg.HashrateEMATauSeconds,
		HashrateEMAMinShares:          cfg.HashrateEMAMinShares,
		NTimeForwardSlackSec:          cfg.NTimeForwardSlackSeconds,
		BanInvalidSubmissionsAfter:    cfg.BanInvalidSubmissionsAfter,
		BanInvalidSubmissionsWindow:   cfg.BanInvalidSubmissionsWindow.String(),
		BanInvalidSubmissionsDuration: cfg.BanInvalidSubmissionsDuration.String(),
		ReconnectBanThreshold:         cfg.ReconnectBanThreshold,
		ReconnectBanWindowSeconds:     cfg.ReconnectBanWindowSeconds,
		ReconnectBanDurationSeconds:   cfg.ReconnectBanDurationSeconds,
		PeerCleanupEnabled:            cfg.PeerCleanupEnabled,
		PeerCleanupMaxPingMs:          cfg.PeerCleanupMaxPingMs,
		PeerCleanupMinPeers:           cfg.PeerCleanupMinPeers,
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

// versionMaskRPC is the minimal RPC interface needed by
// autoConfigureVersionMaskFromNode. It is satisfied by *RPCClient and by
// test fakes.
type versionMaskRPC interface {
	callCtx(ctx context.Context, method string, params interface{}, out interface{}) error
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
// seconds of a pool restart without being rate-limited, while still protecting against
// sustained connection floods during normal operation.
func autoConfigureAcceptRateLimits(cfg *Config, overrides tuningFileConfig, tuningConfigLoaded bool) {
	if cfg == nil || cfg.MaxConns <= 0 {
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
		burstWindow = reconnectWindow / 2
		if burstWindow < 1 {
			burstWindow = 1
		}
	}

	explicitMaxAccepts := tuningConfigLoaded && overrides.RateLimits.MaxAcceptsPerSecond != nil
	explicitMaxBurst := tuningConfigLoaded && overrides.RateLimits.MaxAcceptBurst != nil
	explicitSteadyStateRate := tuningConfigLoaded && overrides.RateLimits.AcceptSteadyStateRate != nil

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
		burstFraction := float64(burstWindow) / float64(reconnectWindow)
		remainingMiners := int(float64(cfg.MaxConns) * (1.0 - burstFraction))
		sustainedWindow := reconnectWindow - burstWindow
		if sustainedWindow < 1 {
			sustainedWindow = 1
		}
		sustainedRate := remainingMiners / sustainedWindow
		if sustainedRate < 10 {
			sustainedRate = 10
		}
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

		if steadyStateRate < 5 {
			steadyStateRate = 5
		}
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
