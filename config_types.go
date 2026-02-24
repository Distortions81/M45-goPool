package main

import "time"

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
# Note: Backblaze requires a "key ID" + "application key" pair.
# - If using an Application Key you created in B2, use its Key ID here.
# - If using the master key, the Key ID is your Account ID.
# backblaze_account_id = "003xxxxxxxxxxxxxxxxxxxx"
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
	MempoolAddressURL               string // URL prefix for explorer links (defaults to mempool.space/address/)
	ServerLocation                  string

	// Discord integration.
	DiscordURL                          string
	DiscordServerID                     string
	DiscordNotifyChannelID              string
	DiscordBotToken                     string // store in secrets.toml
	DiscordWorkerNotifyThresholdSeconds int    // min seconds online/offline before notify

	// Stratum TLS (empty to disable).
	StratumTLSListen string
	// Optional Stratum V2 plaintext listener (empty to disable).
	StratumV2Listen string
	// Stratum auth (optional; when enabled, require miners to send the password in mining.authorize).
	StratumPasswordEnabled bool
	StratumPassword        string
	StratumPasswordPublic  bool // show password in public connect panel when enabled
	// Stratum fast decode: enables lightweight request sniffing for common methods
	// (e.g. mining.submit/mining.subscribe/mining.ping) to reduce allocations.
	StratumFastDecodeEnabled bool
	// Stratum fast encode: enables canned/manual JSON encoding for common Stratum
	// responses to reduce allocations (encode path only).
	StratumFastEncodeEnabled bool
	// Safe mode: force conservative compatibility/safety-oriented runtime behavior.
	SafeMode bool
	// CKPool compatibility mode: advertise a minimal CKPool-style subscribe
	// result (mining.notify tuple only) while keeping other compatibility paths.
	CKPoolEmulate bool
	// Stratum TCP socket buffer tuning (0 = leave OS defaults).
	StratumTCPReadBufferBytes  int
	StratumTCPWriteBufferBytes int

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
	ZMQHashBlockAddr          string
	ZMQRawBlockAddr           string

	// Backblaze B2 backup.
	BackblazeBackupEnabled         bool
	BackblazeBucket                string
	BackblazeAccountID             string // from secrets.toml
	BackblazeApplicationKey        string // from secrets.toml
	BackblazePrefix                string
	BackblazeBackupIntervalSeconds int
	BackblazeKeepLocalCopy         bool
	BackblazeForceEveryInterval    bool   // when true, run backups every interval even if DB unchanged
	BackupSnapshotPath             string // defaults to data/state/workers.db.bak

	DataDir  string
	MaxConns int

	// Accept rate limiting (auto-configured from MaxConns when AutoAcceptRateLimits=true).
	MaxAcceptsPerSecond               int
	MaxAcceptBurst                    int
	DisableConnectRateLimits          bool
	AutoAcceptRateLimits              bool
	AcceptReconnectWindow             int     // seconds for all miners to reconnect after restart
	AcceptBurstWindow                 int     // seconds of burst before sustained rate kicks in
	AcceptSteadyStateWindow           int     // seconds after start to switch to steady-state mode
	AcceptSteadyStateRate             int     // max accepts/sec in steady state
	AcceptSteadyStateReconnectPercent float64 // expected % of miners reconnecting at once
	AcceptSteadyStateReconnectWindow  int     // seconds to spread steady-state reconnects
	StratumMessagesPerMinute          int     // per-connection Stratum messages/min (0 disables)

	MaxRecentJobs                 int
	ConnectionTimeout             time.Duration
	VersionMask                   uint32
	MinVersionBits                int
	ShareAllowDegradedVersionBits bool
	VersionMaskConfigured         bool
	MaxDifficulty                 float64
	MinDifficulty                 float64
	DefaultDifficulty             float64
	TargetSharesPerMin            float64 // vardiff target share rate
	VarDiffEnabled                bool    // enable dynamic difficulty retargeting

	LockSuggestedDifficulty          bool    // keep suggested difficulty instead of vardiff
	EnforceSuggestedDifficultyLimits bool    // ban/disconnect when suggest_* outside min/max
	DifficultyStepGranularity        int     // 1=pow2, 2=half, 3=third, 4=quarter steps
	HashrateEMATauSeconds            float64 // EMA time constant for hashrate
	HashrateCumulativeEnabled        bool    // blend per-connection EMA with cumulative hashrate (display)
	HashrateRecentCumulativeEnabled  bool    // allow short-horizon cumulative (vardiff window) to influence display
	ShareNTimeMaxForwardSeconds      int     // max seconds ntime can roll forward
	ShareCheckDuplicate              bool    // enable duplicate detection (off by default for solo)
	ShareRequireJobID                bool    // reject empty job_id in mining.submit (default false)

	ShareJobFreshnessMode            int  // 0=off, 1=job_id, 2=job_id+prevhash
	ShareCheckNTimeWindow            bool // reject ntime outside configured window
	ShareCheckVersionRolling         bool // reject invalid version rolling policy violations
	ShareRequireAuthorizedConnection bool // reject submits from unauthorized connections
	ShareCheckParamFormat            bool // enforce strict submit field format/length checks
	ShareRequireWorkerMatch          bool // enforce submit worker name must match authorized worker
	SubmitProcessInline              bool // process submits on connection goroutine (bypass worker pool)
	LogDebug                         bool // enable debug logs and detailed runtime traces
	LogNetDebug                      bool // enable raw network debug logging (when supported)

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
	BannedMinerTypes            []string

	// High-latency peer cleanup.
	PeerCleanupEnabled   bool
	PeerCleanupMaxPingMs float64
	PeerCleanupMinPeers  int
}

type EffectiveConfig struct {
	ListenAddr                        string   `json:"listen_addr"`
	StatusAddr                        string   `json:"status_addr"`
	StatusTLSAddr                     string   `json:"status_tls_listen,omitempty"`
	StatusBrandName                   string   `json:"status_brand_name,omitempty"`
	StatusBrandDomain                 string   `json:"status_brand_domain,omitempty"`
	StatusTagline                     string   `json:"status_tagline,omitempty"`
	StatusConnectMinerTitleExtra      string   `json:"status_connect_miner_title_extra,omitempty"`
	StatusConnectMinerTitleExtraURL   string   `json:"status_connect_miner_title_extra_url,omitempty"`
	FiatCurrency                      string   `json:"fiat_currency,omitempty"`
	PoolDonationAddress               string   `json:"pool_donation_address,omitempty"`
	DiscordURL                        string   `json:"discord_url,omitempty"`
	DiscordWorkerNotifyThresholdSec   int      `json:"discord_worker_notify_threshold_seconds,omitempty"`
	GitHubURL                         string   `json:"github_url,omitempty"`
	ServerLocation                    string   `json:"server_location,omitempty"`
	StratumTLSListen                  string   `json:"stratum_tls_listen,omitempty"`
	StratumV2Listen                   string   `json:"stratum_v2_listen,omitempty"`
	StratumFastDecodeEnabled          bool     `json:"stratum_fast_decode_enabled"`
	StratumFastEncodeEnabled          bool     `json:"stratum_fast_encode_enabled"`
	SafeMode                          bool     `json:"safe_mode,omitempty"`
	CKPoolEmulate                     bool     `json:"ckpool_emulate"`
	StratumTCPReadBufferBytes         int      `json:"stratum_tcp_read_buffer_bytes,omitempty"`
	StratumTCPWriteBufferBytes        int      `json:"stratum_tcp_write_buffer_bytes,omitempty"`
	ClerkIssuerURL                    string   `json:"clerk_issuer_url,omitempty"`
	ClerkJWKSURL                      string   `json:"clerk_jwks_url,omitempty"`
	ClerkSignInURL                    string   `json:"clerk_signin_url,omitempty"`
	ClerkCallbackPath                 string   `json:"clerk_callback_path,omitempty"`
	ClerkFrontendAPIURL               string   `json:"clerk_frontend_api_url,omitempty"`
	ClerkSessionCookieName            string   `json:"clerk_session_cookie_name,omitempty"`
	RPCURL                            string   `json:"rpc_url"`
	RPCUser                           string   `json:"rpc_user"`
	RPCPassSet                        bool     `json:"rpc_pass_set"`
	PayoutAddress                     string   `json:"payout_address"`
	PoolFeePercent                    float64  `json:"pool_fee_percent,omitempty"`
	OperatorDonationPercent           float64  `json:"operator_donation_percent,omitempty"`
	OperatorDonationAddress           string   `json:"operator_donation_address,omitempty"`
	OperatorDonationName              string   `json:"operator_donation_name,omitempty"`
	OperatorDonationURL               string   `json:"operator_donation_url,omitempty"`
	Extranonce2Size                   int      `json:"extranonce2_size"`
	TemplateExtraNonce2Size           int      `json:"template_extranonce2_size,omitempty"`
	JobEntropy                        int      `json:"job_entropy"`
	PoolID                            string   `json:"pool_id,omitempty"`
	CoinbaseScriptSigMaxBytes         int      `json:"coinbase_scriptsig_max_bytes"`
	ZMQHashBlockAddr                  string   `json:"zmq_hashblock_addr,omitempty"`
	ZMQRawBlockAddr                   string   `json:"zmq_rawblock_addr,omitempty"`
	BackblazeBackupEnabled            bool     `json:"backblaze_backup_enabled,omitempty"`
	BackblazeBucket                   string   `json:"backblaze_bucket,omitempty"`
	BackblazePrefix                   string   `json:"backblaze_prefix,omitempty"`
	BackblazeBackupInterval           string   `json:"backblaze_backup_interval,omitempty"`
	BackblazeKeepLocalCopy            bool     `json:"backblaze_keep_local_copy,omitempty"`
	BackblazeForceEveryInterval       bool     `json:"backblaze_force_every_interval,omitempty"`
	BackupSnapshotPath                string   `json:"backup_snapshot_path,omitempty"`
	MaxConns                          int      `json:"max_conns,omitempty"`
	MaxAcceptsPerSecond               int      `json:"max_accepts_per_second,omitempty"`
	MaxAcceptBurst                    int      `json:"max_accept_burst,omitempty"`
	DisableConnectRateLimits          bool     `json:"disable_connect_rate_limits,omitempty"`
	AutoAcceptRateLimits              bool     `json:"auto_accept_rate_limits,omitempty"`
	AcceptReconnectWindow             int      `json:"accept_reconnect_window,omitempty"`
	AcceptBurstWindow                 int      `json:"accept_burst_window,omitempty"`
	AcceptSteadyStateWindow           int      `json:"accept_steady_state_window,omitempty"`
	AcceptSteadyStateRate             int      `json:"accept_steady_state_rate,omitempty"`
	AcceptSteadyStateReconnectPercent float64  `json:"accept_steady_state_reconnect_percent,omitempty"`
	AcceptSteadyStateReconnectWindow  int      `json:"accept_steady_state_reconnect_window,omitempty"`
	StratumMessagesPerMinute          int      `json:"stratum_messages_per_minute,omitempty"`
	MaxRecentJobs                     int      `json:"max_recent_jobs"`
	ConnectionTimeout                 string   `json:"connection_timeout"`
	VersionMask                       string   `json:"version_mask,omitempty"`
	MinVersionBits                    int      `json:"min_version_bits,omitempty"`
	ShareAllowDegradedVersionBits     bool     `json:"share_allow_degraded_version_bits,omitempty"`
	MaxDifficulty                     float64  `json:"max_difficulty,omitempty"`
	MinDifficulty                     float64  `json:"min_difficulty,omitempty"`
	TargetSharesPerMin                float64  `json:"target_shares_per_min,omitempty"`
	VarDiffEnabled                    bool     `json:"vardiff_enabled"`
	LockSuggestedDifficulty           bool     `json:"lock_suggested_difficulty,omitempty"`
	DifficultyStepGranularity         int      `json:"difficulty_step_granularity,omitempty"`
	ShareJobFreshnessMode             int      `json:"share_job_freshness_mode"`
	ShareCheckNTimeWindow             bool     `json:"share_check_ntime_window"`
	ShareCheckVersionRolling          bool     `json:"share_check_version_rolling"`
	ShareRequireAuthorizedConnection  bool     `json:"share_require_authorized_connection"`
	ShareCheckParamFormat             bool     `json:"share_check_param_format"`
	ShareRequireWorkerMatch           bool     `json:"share_require_worker_match"`
	SubmitProcessInline               bool     `json:"submit_process_inline"`
	HashrateEMATauSeconds             float64  `json:"hashrate_ema_tau_seconds,omitempty"`
	ShareNTimeMaxForwardSeconds       int      `json:"share_ntime_max_forward_seconds,omitempty"`
	ShareCheckDuplicate               bool     `json:"share_check_duplicate,omitempty"`
	ShareRequireJobID                 bool     `json:"share_require_job_id,omitempty"`
	LogDebug                          bool     `json:"log_debug,omitempty"`
	LogNetDebug                       bool     `json:"log_net_debug,omitempty"`
	CleanExpiredBansOnStartup         bool     `json:"clean_expired_bans_on_startup,omitempty"`
	BanInvalidSubmissionsAfter        int      `json:"ban_invalid_submissions_after,omitempty"`
	BanInvalidSubmissionsWindow       string   `json:"ban_invalid_submissions_window,omitempty"`
	BanInvalidSubmissionsDuration     string   `json:"ban_invalid_submissions_duration,omitempty"`
	ReconnectBanThreshold             int      `json:"reconnect_ban_threshold,omitempty"`
	ReconnectBanWindowSeconds         int      `json:"reconnect_ban_window_seconds,omitempty"`
	ReconnectBanDurationSeconds       int      `json:"reconnect_ban_duration_seconds,omitempty"`
	BannedMinerTypes                  []string `json:"banned_miner_types,omitempty"`
	PeerCleanupEnabled                bool     `json:"peer_cleanup_enabled,omitempty"`
	PeerCleanupMaxPingMs              float64  `json:"peer_cleanup_max_ping_ms,omitempty"`
	PeerCleanupMinPeers               int      `json:"peer_cleanup_min_peers,omitempty"`
}
