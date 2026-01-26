package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/pelletier/go-toml"
)

var secretsConfigExample = []byte(`# RPC credentials for bitcoind
rpc_user = "bitcoinrpc"
rpc_pass = "password"

# Optional Discord notifications integration.
# discord_token = "YOUR_DISCORD_BOT_TOKEN"

# Optional Clerk backend API secret key (development only).
# This is needed to exchange the development __clerk_db_jwt query param into a
# first-party __session cookie on localhost. Do NOT use this in production.
# clerk_secret_key = "sk_test_..."
# clerk_publishable_key = "pk_test_..."

# Backblaze B2 credentials for database backups (optional).
# backblaze_account_id = "B1234567890XXXXXXXX"
# backblaze_application_key = "KXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"
`)

type Config struct {
	// Server addresses.
	ListenAddr    string
	StatusAddr    string
	StatusTLSAddr string

	// Branding.
	StatusBrandName                 string
	StatusBrandDomain               string
	StatusPublicURL                 string // canonical URL for redirects/cookies
	StatusTagline                   string
	StatusConnectMinerTitleExtra    string
	StatusConnectMinerTitleExtraURL string
	FiatCurrency                    string // display currency for BTC prices
	PoolDonationAddress             string // shown in footer for tips to operator
	GitHubURL                       string
	ServerLocation                  string

	// Discord integration.
	DiscordURL                          string
	DiscordServerID                     string
	DiscordNotifyChannelID              string
	DiscordBotToken                     string // store in secrets.toml
	DiscordWorkerNotifyThresholdSeconds int    // min seconds online/offline before notify

	// Stratum TLS (empty to disable).
	StratumTLSListen string

	// Clerk authentication.
	ClerkIssuerURL         string
	ClerkJWKSURL           string
	ClerkSignInURL         string
	ClerkCallbackPath      string
	ClerkFrontendAPIURL    string
	ClerkSessionCookieName string
	ClerkSessionAudience   string
	ClerkSecretKey         string // store in secrets.toml
	ClerkPublishableKey    string // store in secrets.toml

	// Bitcoin node RPC.
	RPCURL                  string
	RPCUser                 string
	RPCPass                 string
	RPCCookiePath           string // alternative to user/pass
	rpCCookiePathFromConfig string
	rpcCookieWatch          bool
	AllowPublicRPC          bool // allow unauthenticated RPC (testing only)

	// Payouts.
	PayoutAddress  string
	PoolFeePercent float64

	OperatorDonationPercent float64
	OperatorDonationAddress string
	OperatorDonationName    string
	OperatorDonationURL     string

	// Mining parameters.
	Extranonce2Size           int
	TemplateExtraNonce2Size   int
	JobEntropy                int
	CoinbaseMsg               string
	PoolEntropy               string
	PoolTagPrefix             string
	CoinbaseScriptSigMaxBytes int
	ZMQBlockAddr              string

	// Backblaze B2 backup.
	BackblazeBackupEnabled         bool
	BackblazeBucket                string
	BackblazeAccountID             string // from secrets.toml
	BackblazeApplicationKey        string // from secrets.toml
	BackblazePrefix                string
	BackblazeBackupIntervalSeconds int
	BackblazeKeepLocalCopy         bool
	BackupSnapshotPath             string // defaults to data/state/workers.db.bak

	DataDir  string
	MaxConns int

	// Accept rate limiting (auto-configured from MaxConns when AutoAcceptRateLimits=true).
	MaxAcceptsPerSecond               int
	MaxAcceptBurst                    int
	AutoAcceptRateLimits              bool
	AcceptReconnectWindow             int     // seconds for all miners to reconnect after restart
	AcceptBurstWindow                 int     // seconds of burst before sustained rate kicks in
	AcceptSteadyStateWindow           int     // seconds after start to switch to steady-state mode
	AcceptSteadyStateRate             int     // max accepts/sec in steady state
	AcceptSteadyStateReconnectPercent float64 // expected % of miners reconnecting at once
	AcceptSteadyStateReconnectWindow  int     // seconds to spread steady-state reconnects

	MaxRecentJobs         int
	ConnectionTimeout     time.Duration
	VersionMask           uint32
	MinVersionBits        int
	IgnoreMinVersionBits  bool
	VersionMaskConfigured bool
	MaxDifficulty         float64
	MinDifficulty         float64

	LockSuggestedDifficulty  bool    // keep suggested difficulty instead of vardiff
	HashrateEMATauSeconds    float64 // EMA time constant for hashrate
	HashrateEMAMinShares     int     // min shares before EMA kicks in
	NTimeForwardSlackSeconds int     // max seconds ntime can roll forward
	CheckDuplicateShares     bool    // enable duplicate detection (off by default for solo)

	SoloMode               bool // light validation for solo pools (default true)
	DirectSubmitProcessing bool // process submits on connection goroutine (bypass worker pool)
	LogLevel              string // log level: debug, info, warn, error

	// Maintenance behavior.
	CleanExpiredBansOnStartup bool // rewrite/drop expired bans on startup

	// Auto-ban for invalid submissions (0 disables).
	BanInvalidSubmissionsAfter    int
	BanInvalidSubmissionsWindow   time.Duration
	BanInvalidSubmissionsDuration time.Duration

	// Reconnect flood protection (0 disables).
	ReconnectBanThreshold       int
	ReconnectBanWindowSeconds   int
	ReconnectBanDurationSeconds int

	// High-latency peer cleanup.
	PeerCleanupEnabled   bool
	PeerCleanupMaxPingMs float64
	PeerCleanupMinPeers  int
}

type EffectiveConfig struct {
	ListenAddr                        string  `json:"listen_addr"`
	StatusAddr                        string  `json:"status_addr"`
	StatusTLSAddr                     string  `json:"status_tls_listen,omitempty"`
	StatusBrandName                   string  `json:"status_brand_name,omitempty"`
	StatusBrandDomain                 string  `json:"status_brand_domain,omitempty"`
	StatusTagline                     string  `json:"status_tagline,omitempty"`
	StatusConnectMinerTitleExtra      string  `json:"status_connect_miner_title_extra,omitempty"`
	StatusConnectMinerTitleExtraURL   string  `json:"status_connect_miner_title_extra_url,omitempty"`
	FiatCurrency                      string  `json:"fiat_currency,omitempty"`
	PoolDonationAddress               string  `json:"pool_donation_address,omitempty"`
	DiscordURL                        string  `json:"discord_url,omitempty"`
	DiscordWorkerNotifyThresholdSec   int     `json:"discord_worker_notify_threshold_seconds,omitempty"`
	GitHubURL                         string  `json:"github_url,omitempty"`
	ServerLocation                    string  `json:"server_location,omitempty"`
	StratumTLSListen                  string  `json:"stratum_tls_listen,omitempty"`
	ClerkIssuerURL                    string  `json:"clerk_issuer_url,omitempty"`
	ClerkJWKSURL                      string  `json:"clerk_jwks_url,omitempty"`
	ClerkSignInURL                    string  `json:"clerk_signin_url,omitempty"`
	ClerkCallbackPath                 string  `json:"clerk_callback_path,omitempty"`
	ClerkFrontendAPIURL               string  `json:"clerk_frontend_api_url,omitempty"`
	ClerkSessionCookieName            string  `json:"clerk_session_cookie_name,omitempty"`
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
	JobEntropy                        int     `json:"job_entropy"`
	PoolID                            string  `json:"pool_id,omitempty"`
	CoinbaseScriptSigMaxBytes         int     `json:"coinbase_scriptsig_max_bytes"`
	ZMQBlockAddr                      string  `json:"zmq_block_addr,omitempty"`
	BackblazeBackupEnabled            bool    `json:"backblaze_backup_enabled,omitempty"`
	BackblazeBucket                   string  `json:"backblaze_bucket,omitempty"`
	BackblazePrefix                   string  `json:"backblaze_prefix,omitempty"`
	BackblazeBackupInterval           string  `json:"backblaze_backup_interval,omitempty"`
	BackblazeKeepLocalCopy            bool    `json:"backblaze_keep_local_copy,omitempty"`
	BackupSnapshotPath                string  `json:"backup_snapshot_path,omitempty"`
	DataDir                           string  `json:"data_dir"`
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
	ConnectionTimeout                 string  `json:"connection_timeout"`
	VersionMask                       string  `json:"version_mask,omitempty"`
	MinVersionBits                    int     `json:"min_version_bits,omitempty"`
	IgnoreMinVersionBits              bool    `json:"ignore_min_version_bits,omitempty"`
	MaxDifficulty                     float64 `json:"max_difficulty,omitempty"`
	MinDifficulty                     float64 `json:"min_difficulty,omitempty"`
	LockSuggestedDifficulty           bool    `json:"lock_suggested_difficulty,omitempty"`
	SoloMode                          bool    `json:"solo_mode"`
	DirectSubmitProcessing            bool    `json:"direct_submit_processing"`
	HashrateEMATauSeconds             float64 `json:"hashrate_ema_tau_seconds,omitempty"`
	HashrateEMAMinShares              int     `json:"hashrate_ema_min_shares,omitempty"`
	NTimeForwardSlackSec              int     `json:"ntime_forward_slack_seconds,omitempty"`
	CheckDuplicateShares              bool    `json:"check_duplicate_shares,omitempty"`
	LogLevel                          string  `json:"log_level,omitempty"`
	CleanExpiredBansOnStartup         bool    `json:"clean_expired_bans_on_startup,omitempty"`
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
	PoolListen      string  `toml:"pool_listen"`
	StatusListen    string  `toml:"status_listen"`
	StatusTLSListen *string `toml:"status_tls_listen"` // nil = default, "" = disabled
	StatusPublicURL string  `toml:"status_public_url"`
}

type brandingConfig struct {
	StatusBrandName                 string `toml:"status_brand_name"`
	StatusBrandDomain               string `toml:"status_brand_domain"`
	StatusTagline                   string `toml:"status_tagline"`
	StatusConnectMinerTitleExtra    string `toml:"status_connect_miner_title_extra"`
	StatusConnectMinerTitleExtraURL string `toml:"status_connect_miner_title_extra_url"`
	FiatCurrency                    string `toml:"fiat_currency"`
	PoolDonationAddress             string `toml:"pool_donation_address"`
	DiscordURL                      string `toml:"discord_url"`
	DiscordServerID                 string `toml:"discord_server_id"`
	DiscordNotifyChannelID          string `toml:"discord_notify_channel_id"`
	GitHubURL                       string `toml:"github_url"`
	ServerLocation                  string `toml:"server_location"`
}

type stratumConfig struct {
	StratumTLSListen string `toml:"stratum_tls_listen"`
}

type authConfig struct {
	ClerkIssuerURL         string `toml:"clerk_issuer_url"`
	ClerkJWKSURL           string `toml:"clerk_jwks_url"`
	ClerkSignInURL         string `toml:"clerk_signin_url"`
	ClerkCallbackPath      string `toml:"clerk_callback_path"`
	ClerkFrontendAPIURL    string `toml:"clerk_frontend_api_url"`
	ClerkSessionCookieName string `toml:"clerk_session_cookie_name"`
	ClerkSessionAudience   string `toml:"clerk_session_audience"`
}

type nodeConfig struct {
	RPCURL         string `toml:"rpc_url"`
	PayoutAddress  string `toml:"payout_address"`
	DataDir        string `toml:"data_dir"`
	ZMQBlockAddr   string `toml:"zmq_block_addr"`
	RPCCookiePath  string `toml:"rpc_cookie_path"`
	AllowPublicRPC bool   `toml:"allow_public_rpc"`
}

type loggingConfig struct {
	Level string `toml:"level"`
}

type backblazeBackupConfig struct {
	Enabled         bool   `toml:"enabled"`
	Bucket          string `toml:"bucket"`
	Prefix          string `toml:"prefix"`
	IntervalSeconds *int   `toml:"interval_seconds"`
	KeepLocalCopy   *bool  `toml:"keep_local_copy"`
	SnapshotPath    string `toml:"snapshot_path"`
}

type miningConfig struct {
	PoolFeePercent            *float64 `toml:"pool_fee_percent"`
	OperatorDonationPercent   *float64 `toml:"operator_donation_percent"`
	OperatorDonationAddress   string   `toml:"operator_donation_address"`
	OperatorDonationName      string   `toml:"operator_donation_name"`
	OperatorDonationURL       string   `toml:"operator_donation_url"`
	Extranonce2Size           *int     `toml:"extranonce2_size"`
	TemplateExtraNonce2Size   *int     `toml:"template_extra_nonce2_size"`
	JobEntropy                *int     `toml:"job_entropy"`
	PoolEntropy               *string  `toml:"pool_entropy"`
	PoolTagPrefix             string   `toml:"pooltag_prefix"`
	CoinbaseScriptSigMaxBytes *int     `toml:"coinbase_scriptsig_max_bytes"`
	SoloMode                  *bool    `toml:"solo_mode"`
	DirectSubmitProcessing    *bool    `toml:"direct_submit_processing"`
	CheckDuplicateShares      *bool    `toml:"check_duplicate_shares"`
}

type baseFileConfig struct {
	Server    serverConfig          `toml:"server"`
	Branding  brandingConfig        `toml:"branding"`
	Stratum   stratumConfig         `toml:"stratum"`
	Auth      authConfig            `toml:"auth"`
	Node      nodeConfig            `toml:"node"`
	Mining    miningConfig          `toml:"mining"`
	Backblaze backblazeBackupConfig `toml:"backblaze_backup"`
	Logging   loggingConfig         `toml:"logging"`
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

func filterAlphanumeric(s string) string {
	var buf []byte
	for i := 0; i < len(s); i++ {
		b := s[i]
		if (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') {
			buf = append(buf, b)
		} else if b >= 'A' && b <= 'Z' {
			buf = append(buf, b+32) // lowercase
		}
	}
	return string(buf)
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
	ConnectionTimeoutSec *int `toml:"connection_timeout_seconds"`
}

type difficultyTuning struct {
	MaxDifficulty           *float64 `toml:"max_difficulty"`
	MinDifficulty           *float64 `toml:"min_difficulty"`
	LockSuggestedDifficulty *bool    `toml:"lock_suggested_difficulty"`
}

type miningTuning struct {
	DisablePoolJobEntropy *bool `toml:"disable_pool_job_entropy"`
}

type hashrateTuning struct {
	HashrateEMATauSeconds    *float64 `toml:"hashrate_ema_tau_seconds"`
	HashrateEMAMinShares     *int     `toml:"hashrate_ema_min_shares"`
	NTimeForwardSlackSeconds *int     `toml:"ntime_forward_slack_seconds"`
}

type discordTuning struct {
	WorkerNotifyThresholdSeconds *int `toml:"worker_notify_threshold_seconds"`
}

type peerCleaningTuning struct {
	Enabled   *bool    `toml:"enabled"`
	MaxPingMs *float64 `toml:"max_ping_ms"`
	MinPeers  *int     `toml:"min_peers"`
}

type banTuning struct {
	CleanExpiredOnStartup            *bool `toml:"clean_expired_on_startup"`
	BanInvalidSubmissionsAfter       *int `toml:"ban_invalid_submissions_after"`
	BanInvalidSubmissionsWindowSec   *int `toml:"ban_invalid_submissions_window_seconds"`
	BanInvalidSubmissionsDurationSec *int `toml:"ban_invalid_submissions_duration_seconds"`
	ReconnectBanThreshold            *int `toml:"reconnect_ban_threshold"`
	ReconnectBanWindowSeconds        *int `toml:"reconnect_ban_window_seconds"`
	ReconnectBanDurationSeconds      *int `toml:"reconnect_ban_duration_seconds"`
}

type versionTuning struct {
	MinVersionBits       *int  `toml:"min_version_bits"`
	IgnoreMinVersionBits *bool `toml:"ignore_min_version_bits"`
}

// tuningFileConfig holds optional overrides from tuning.toml.
type tuningFileConfig struct {
	RateLimits   rateLimitTuning    `toml:"rate_limits"`
	Timeouts     timeoutTuning      `toml:"timeouts"`
	Difficulty   difficultyTuning   `toml:"difficulty"`
	Mining       miningTuning       `toml:"mining"`
	Hashrate     hashrateTuning     `toml:"hashrate"`
	Discord      discordTuning      `toml:"discord"`
	PeerCleaning peerCleaningTuning `toml:"peer_cleaning"`
	Bans         banTuning          `toml:"bans"`
	Version      versionTuning      `toml:"version"`
}

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
			RPCURL:         cfg.RPCURL,
			PayoutAddress:  cfg.PayoutAddress,
			DataDir:        cfg.DataDir,
			ZMQBlockAddr:   cfg.ZMQBlockAddr,
			RPCCookiePath:  cfg.RPCCookiePath,
			AllowPublicRPC: cfg.AllowPublicRPC,
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
			Enabled:         cfg.BackblazeBackupEnabled,
			Bucket:          cfg.BackblazeBucket,
			Prefix:          cfg.BackblazePrefix,
			IntervalSeconds: intPtr(cfg.BackblazeBackupIntervalSeconds),
			KeepLocalCopy:   boolPtr(cfg.BackblazeKeepLocalCopy),
			SnapshotPath:    cfg.BackupSnapshotPath,
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
		},
		Timeouts: timeoutTuning{
			ConnectionTimeoutSec: intPtr(int(cfg.ConnectionTimeout / time.Second)),
		},
		Difficulty: difficultyTuning{
			MaxDifficulty:           float64Ptr(cfg.MaxDifficulty),
			MinDifficulty:           float64Ptr(cfg.MinDifficulty),
			LockSuggestedDifficulty: boolPtr(cfg.LockSuggestedDifficulty),
		},
		Mining: miningTuning{
			DisablePoolJobEntropy: boolPtr(false),
		},
		Hashrate: hashrateTuning{
			HashrateEMATauSeconds:    float64Ptr(cfg.HashrateEMATauSeconds),
			HashrateEMAMinShares:     intPtr(cfg.HashrateEMAMinShares),
			NTimeForwardSlackSeconds: intPtr(cfg.NTimeForwardSlackSeconds),
		},
		Discord: discordTuning{
			WorkerNotifyThresholdSeconds: intPtr(cfg.DiscordWorkerNotifyThresholdSeconds),
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
		},
		Version: versionTuning{
			MinVersionBits:       intPtr(cfg.MinVersionBits),
			IgnoreMinVersionBits: boolPtr(cfg.IgnoreMinVersionBits),
		},
	}
}

// secretsConfig holds values from secrets.toml: Clerk secrets and (when enabled)
// RPC user/password for fallback authentication. This file is gitignored so only
// store sensitive credentials here.
type secretsConfig struct {
	RPCUser                 string `toml:"rpc_user"`
	RPCPass                 string `toml:"rpc_pass"`
	DiscordBotToken         string `toml:"discord_token"`
	ClerkSecretKey          string `toml:"clerk_secret_key"`
	ClerkPublishableKey     string `toml:"clerk_publishable_key"`
	BackblazeAccountID      string `toml:"backblaze_account_id"`
	BackblazeApplicationKey string `toml:"backblaze_application_key"`
}

func loadConfig(configPath, secretsPath string) (Config, string) {
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

	if cfg.PoolEntropy == "" {
		cfg.PoolEntropy = generatePoolEntropy()
		if configFileExisted {
			if err := rewriteConfigFile(configPath, cfg); err != nil {
				logger.Warn("persist pool_entropy", "path", configPath, "error", err)
			} else {
				logger.Info("generated pool_entropy and updated config", "path", configPath, "pool_entropy", cfg.PoolEntropy)
			}
		}
	}

	if secretsPath == "" {
		secretsPath = filepath.Join(cfg.DataDir, "config", "secrets.toml")
	}
	if sc, ok, err := loadSecretsFile(secretsPath); err != nil {
		fatal("secrets file", err, "path", secretsPath)
	} else if ok {
		applySecretsConfig(&cfg, *sc)
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

func loadBaseConfigFile(path string) (*baseFileConfig, bool, error) {
	return loadTOMLFile[baseFileConfig](path)
}

func loadTuningFile(path string) (*tuningFileConfig, bool, error) {
	return loadTOMLFile[tuningFileConfig](path)
}

func loadSecretsFile(path string) (*secretsConfig, bool, error) {
	return loadTOMLFile[secretsConfig](path)
}

func applyBaseConfig(cfg *Config, fc baseFileConfig) {
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
	if fc.Branding.DiscordURL != "" {
		cfg.DiscordURL = strings.TrimSpace(fc.Branding.DiscordURL)
	}
	if fc.Branding.DiscordServerID != "" {
		cfg.DiscordServerID = strings.TrimSpace(fc.Branding.DiscordServerID)
	}
	if fc.Branding.DiscordNotifyChannelID != "" {
		cfg.DiscordNotifyChannelID = strings.TrimSpace(fc.Branding.DiscordNotifyChannelID)
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
	cookiePath := strings.TrimSpace(fc.Node.RPCCookiePath)
	cfg.rpCCookiePathFromConfig = cookiePath
	if cookiePath != "" {
		cfg.RPCCookiePath = cookiePath
	}
	if fc.Node.AllowPublicRPC {
		cfg.AllowPublicRPC = true
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
	if fc.Mining.PoolEntropy != nil {
		cfg.PoolEntropy = *fc.Mining.PoolEntropy
	}
	if fc.Mining.PoolTagPrefix != "" {
		cfg.PoolTagPrefix = filterAlphanumeric(strings.TrimSpace(fc.Mining.PoolTagPrefix))
	}
	if fc.Mining.JobEntropy != nil {
		cfg.JobEntropy = *fc.Mining.JobEntropy
	}
	if fc.Mining.CoinbaseScriptSigMaxBytes != nil {
		cfg.CoinbaseScriptSigMaxBytes = *fc.Mining.CoinbaseScriptSigMaxBytes
	}
	if fc.Mining.SoloMode != nil {
		cfg.SoloMode = *fc.Mining.SoloMode
	}
	if fc.Mining.DirectSubmitProcessing != nil {
		cfg.DirectSubmitProcessing = *fc.Mining.DirectSubmitProcessing
	}
	if fc.Mining.CheckDuplicateShares != nil {
		cfg.CheckDuplicateShares = *fc.Mining.CheckDuplicateShares
	}
	if fc.Logging.Level != "" {
		cfg.LogLevel = strings.ToLower(strings.TrimSpace(fc.Logging.Level))
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
	if strings.TrimSpace(fc.Backblaze.SnapshotPath) != "" {
		cfg.BackupSnapshotPath = strings.TrimSpace(fc.Backblaze.SnapshotPath)
	}
}

func applyTuningConfig(cfg *Config, fc tuningFileConfig) {
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
	if fc.Timeouts.ConnectionTimeoutSec != nil {
		cfg.ConnectionTimeout = time.Duration(*fc.Timeouts.ConnectionTimeoutSec) * time.Second
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
	if fc.Mining.DisablePoolJobEntropy != nil && *fc.Mining.DisablePoolJobEntropy {
		// Disables coinbase "<pool entropy>-<job entropy>" suffix by bypassing
		// the suffix builder (which is gated on JobEntropy > 0).
		cfg.JobEntropy = 0
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
	if fc.Discord.WorkerNotifyThresholdSeconds != nil && *fc.Discord.WorkerNotifyThresholdSeconds > 0 {
		cfg.DiscordWorkerNotifyThresholdSeconds = *fc.Discord.WorkerNotifyThresholdSeconds
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
	if fc.Version.MinVersionBits != nil {
		cfg.MinVersionBits = *fc.Version.MinVersionBits
	}
	if fc.Version.IgnoreMinVersionBits != nil {
		cfg.IgnoreMinVersionBits = *fc.Version.IgnoreMinVersionBits
	}
}

func finalizeRPCCredentials(cfg *Config, secretsPath string, forceCredentials bool, configPath string) error {
	if forceCredentials {
		if err := loadRPCredentialsFromSecrets(cfg, secretsPath); err != nil {
			return err
		}
		return nil
	}

	if cfg.AllowPublicRPC && strings.TrimSpace(cfg.RPCCookiePath) == "" {
		return nil
	}

	if strings.TrimSpace(cfg.RPCCookiePath) == "" {
		auto, found, tried := autodetectRPCCookiePath()
		if auto != "" {
			cfg.RPCCookiePath = auto
			if found {
				logger.Info("autodetected bitcoind rpc cookie", "path", auto)
			} else {
				pathsDesc := "none"
				if len(tried) > 0 {
					pathsDesc = strings.Join(tried, ", ")
				}
				warnCookieMissing("rpc cookie not present yet; will keep watching", "path", auto, "tried_paths", pathsDesc)
			}
		} else {
			pathsDesc := "none"
			if len(tried) > 0 {
				pathsDesc = strings.Join(tried, ", ")
			}
			warnCookieMissing("rpc cookie autodetect failed", "tried_paths", pathsDesc)
			return fmt.Errorf("node.rpc_cookie_path is required when RPC credentials are not forced; configure it to use bitcoind's auth cookie (autodetect checked: %s)", pathsDesc)
		}
	}
	cfg.rpcCookieWatch = strings.TrimSpace(cfg.RPCCookiePath) != ""
	if cfg.rpcCookieWatch {
		if loaded, _, err := applyRPCCookieCredentials(cfg); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				warnCookieMissing("rpc cookie missing; will keep watching", "path", cfg.RPCCookiePath)
			} else {
				warnCookieMissing("failed to read rpc cookie", "path", cfg.RPCCookiePath, "error", err)
			}
		} else if loaded {
			logger.Info("rpc cookie loaded", "path", cfg.RPCCookiePath)
			persistRPCCookiePathIfNeeded(configPath, cfg)
		}
	}
	return nil
}

func loadRPCredentialsFromSecrets(cfg *Config, secretsPath string) error {
	sc, ok, err := loadSecretsFile(secretsPath)
	if err != nil {
		return err
	}
	if !ok {
		printRPCSecretHint(cfg, secretsPath)
		return fmt.Errorf("secrets file missing: %s", secretsPath)
	}
	user := strings.TrimSpace(sc.RPCUser)
	pass := strings.TrimSpace(sc.RPCPass)
	if user == "" || pass == "" {
		return fmt.Errorf("secrets file %s must include rpc_user and rpc_pass", secretsPath)
	}
	cfg.RPCUser = user
	cfg.RPCPass = pass
	return nil
}

func printRPCSecretHint(cfg *Config, secretsPath string) {
	secretsExamplePath := filepath.Join(cfg.DataDir, "config", "examples", "secrets.toml.example")
	fmt.Printf("\n🔐 RPC credentials are required when node.rpc_cookie_path is not configured.\n\n")
	fmt.Printf("   To configure RPC credentials:\n")
	fmt.Printf("   1. Copy the example: %s\n", secretsExamplePath)
	fmt.Printf("   2. To:                 %s\n", secretsPath)
	fmt.Printf("   3. Edit the file and set your rpc_user and rpc_pass\n")
	fmt.Printf("   4. Restart goPool\n\n")
}

func applyRPCCookieCredentials(cfg *Config) (bool, string, error) {
	path := strings.TrimSpace(cfg.RPCCookiePath)
	if path == "" {
		return false, path, nil
	}
	actualPath, user, pass, err := readRPCCookieWithFallback(path)
	if actualPath != "" {
		cfg.RPCCookiePath = actualPath
	}
	if err != nil {
		return false, actualPath, err
	}
	cfg.RPCUser = strings.TrimSpace(user)
	cfg.RPCPass = strings.TrimSpace(pass)
	return true, actualPath, nil
}

func rpcCookiePathCandidates(basePath string) []string {
	trimmed := strings.TrimSpace(basePath)
	if trimmed == "" {
		return nil
	}
	if info, err := os.Stat(trimmed); err == nil && info.IsDir() {
		return []string{filepath.Join(trimmed, ".cookie")}
	}
	candidates := []string{trimmed}
	if !strings.HasSuffix(trimmed, ".cookie") {
		candidates = append(candidates, filepath.Join(trimmed, ".cookie"))
	}
	return candidates
}

func readRPCCookieWithFallback(basePath string) (string, string, string, error) {
	candidates := rpcCookiePathCandidates(basePath)
	if len(candidates) == 0 {
		return "", "", "", fmt.Errorf("invalid cookie path")
	}
	var lastErr error
	for _, candidate := range candidates {
		data, err := os.ReadFile(candidate)
		if err != nil {
			lastErr = fmt.Errorf("read %s: %w", candidate, err)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return candidate, "", "", lastErr
		}
		token := strings.TrimSpace(string(data))
		parts := strings.SplitN(token, ":", 2)
		if len(parts) != 2 {
			return candidate, "", "", fmt.Errorf("unexpected cookie format")
		}
		return candidate, strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("read %s: %w", candidates[len(candidates)-1], os.ErrNotExist)
	}
	return candidates[len(candidates)-1], "", "", lastErr
}

func readRPCCookie(path string) (string, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("read %s: %w", path, err)
	}
	token := strings.TrimSpace(string(data))
	parts := strings.SplitN(token, ":", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("unexpected cookie format")
	}
	return parts[0], parts[1], nil
}

func rpcCookieCandidates() []string {
	var candidates []string
	if envDir := strings.TrimSpace(os.Getenv("BITCOIN_DATADIR")); envDir != "" {
		candidates = append(candidates, filepath.Join(envDir, ".cookie"))
		for _, net := range []string{"regtest", "testnet3", "signet"} {
			candidates = append(candidates, filepath.Join(envDir, net, ".cookie"))
		}
	}
	candidates = append(candidates, btcdCookieCandidates()...)
	candidates = append(candidates, linuxCookieCandidates()...)
	return candidates
}

func autodetectRPCCookiePath() (string, bool, []string) {
	candidates := rpcCookieCandidates()
	tried := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		tried = append(tried, candidate)
		if fileExists(candidate) {
			return candidate, true, tried
		}
	}
	if len(tried) > 0 {
		return tried[0], false, tried
	}
	return "", false, tried
}

func warnCookieMissing(msg string, attrs ...any) {
	logger.Warn(msg, attrs...)
	entry := msg
	if formatted := formatAttrs(attrs); formatted != "" {
		entry += " " + formatted
	}
	entry += "\n"
	_, _ = os.Stdout.Write([]byte(entry))
}

func persistRPCCookiePathIfNeeded(configPath string, cfg *Config) {
	if configPath == "" {
		return
	}
	path := strings.TrimSpace(cfg.RPCCookiePath)
	if path == "" || cfg.rpCCookiePathFromConfig == path {
		return
	}
	if err := rewriteConfigFile(configPath, *cfg); err != nil {
		logger.Warn("persist rpc cookie path", "path", path, "error", err)
		return
	}
	cfg.rpCCookiePathFromConfig = path
	logger.Info("persisted rpc cookie path", "path", path, "config", configPath)
}

func linuxCookieCandidates() []string {
	home, _ := os.UserHomeDir()
	h := func(p string) string {
		if strings.HasPrefix(p, "~/") && home != "" {
			return filepath.Join(home, p[2:])
		}
		return p
	}
	return []string{
		h("~/.bitcoin/.cookie"),
		h("~/.bitcoin/regtest/.cookie"),
		h("~/.bitcoin/testnet3/.cookie"),
		h("~/.bitcoin/signet/.cookie"),
		"/var/lib/bitcoin/.cookie",
		"/var/lib/bitcoin/regtest/.cookie",
		"/var/lib/bitcoin/testnet3/.cookie",
		"/var/lib/bitcoin/signet/.cookie",
		"/home/bitcoin/.bitcoin/.cookie",
		"/home/bitcoin/.bitcoin/regtest/.cookie",
		"/home/bitcoin/.bitcoin/testnet3/.cookie",
		"/home/bitcoin/.bitcoin/signet/.cookie",
		"/etc/bitcoin/.cookie",
	}
}

// btcdCookieCandidates mirrors btcsuite/btcd/rpcclient's layout for btcd's default
// datadir so we can reuse the same cookie locations before falling back to the
// general linux list.
func btcdCookieCandidates() []string {
	home := btcutil.AppDataDir("btcd", false)
	if home == "" {
		return nil
	}
	dataDir := filepath.Join(home, "data")
	networks := []string{"regtest", "testnet3", "testnet4", "signet", "simnet"}
	candidates := make([]string, 0, len(networks)+1)
	candidates = append(candidates, filepath.Join(dataDir, ".cookie"))
	for _, net := range networks {
		candidates = append(candidates, filepath.Join(dataDir, net, ".cookie"))
	}
	return candidates
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	if _, err := os.Stat(path); err == nil {
		return true
	}
	return false
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
		ZMQBlockAddr:                      cfg.ZMQBlockAddr,
		BackblazeBackupEnabled:            cfg.BackblazeBackupEnabled,
		BackblazeBucket:                   cfg.BackblazeBucket,
		BackblazePrefix:                   cfg.BackblazePrefix,
		BackblazeBackupInterval:           backblazeInterval,
		BackblazeKeepLocalCopy:            cfg.BackblazeKeepLocalCopy,
		BackupSnapshotPath:                cfg.BackupSnapshotPath,
		DataDir:                           cfg.DataDir,
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
		ConnectionTimeout:                 cfg.ConnectionTimeout.String(),
		VersionMask:                       fmt.Sprintf("%08x", cfg.VersionMask),
		MinVersionBits:                    cfg.MinVersionBits,
		IgnoreMinVersionBits:              cfg.IgnoreMinVersionBits,
		MaxDifficulty:                     cfg.MaxDifficulty,
		MinDifficulty:                     cfg.MinDifficulty,
		// Effective config mirrors whether suggested difficulty locking is enabled.
		LockSuggestedDifficulty:       cfg.LockSuggestedDifficulty,
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
		PeerCleanupEnabled:            cfg.PeerCleanupEnabled,
		PeerCleanupMaxPingMs:          cfg.PeerCleanupMaxPingMs,
		PeerCleanupMinPeers:           cfg.PeerCleanupMinPeers,
	}
}
