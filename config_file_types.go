package main

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
	StratumTLSListen       string `toml:"stratum_tls_listen"`
	StratumPasswordEnabled bool   `toml:"stratum_password_enabled"`
	StratumPassword        string `toml:"stratum_password"`
	StratumPasswordPublic  bool   `toml:"stratum_password_public"`
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
	RPCURL           string `toml:"rpc_url"`
	PayoutAddress    string `toml:"payout_address"`
	ZMQHashBlockAddr string `toml:"zmq_hashblock_addr"`
	ZMQRawBlockAddr  string `toml:"zmq_rawblock_addr"`
	RPCCookiePath    string `toml:"rpc_cookie_path"`
	AllowPublicRPC   bool   `toml:"allow_public_rpc"`
}

type nodeConfigRead struct {
	RPCURL             string `toml:"rpc_url"`
	PayoutAddress      string `toml:"payout_address"`
	ZMQLegacyBlockAddr string `toml:"zmq_block_addr"`
	ZMQHashBlockAddr   string `toml:"zmq_hashblock_addr"`
	ZMQRawBlockAddr    string `toml:"zmq_rawblock_addr"`
	RPCCookiePath      string `toml:"rpc_cookie_path"`
	AllowPublicRPC     bool   `toml:"allow_public_rpc"`
}

type loggingConfig struct {
	Level string `toml:"level"`
}

type backblazeBackupConfig struct {
	Enabled            bool   `toml:"enabled"`
	Bucket             string `toml:"bucket"`
	Prefix             string `toml:"prefix"`
	IntervalSeconds    *int   `toml:"interval_seconds"`
	KeepLocalCopy      *bool  `toml:"keep_local_copy"`
	ForceEveryInterval *bool  `toml:"force_every_interval"`
	SnapshotPath       string `toml:"snapshot_path"`
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
	RelaxedSubmitValidation   *bool    `toml:"relaxed_submit_validation"`
	SubmitWorkerNameMatch     *bool    `toml:"submit_worker_name_match"`
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

type baseFileConfigRead struct {
	Server    serverConfig          `toml:"server"`
	Branding  brandingConfig        `toml:"branding"`
	Stratum   stratumConfig         `toml:"stratum"`
	Auth      authConfig            `toml:"auth"`
	Node      nodeConfigRead        `toml:"node"`
	Mining    miningConfig          `toml:"mining"`
	Backblaze backblazeBackupConfig `toml:"backblaze_backup"`
	Logging   loggingConfig         `toml:"logging"`
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
	StratumMessagesPerMinute          *int     `toml:"stratum_messages_per_minute"`
}

type timeoutTuning struct {
	ConnectionTimeoutSec *int `toml:"connection_timeout_seconds"`
}

type difficultyTuning struct {
	MaxDifficulty                    *float64 `toml:"max_difficulty"`
	MinDifficulty                    *float64 `toml:"min_difficulty"`
	DefaultDifficulty                *float64 `toml:"default_difficulty"`
	TargetSharesPerMin               *float64 `toml:"target_shares_per_min"`
	LockSuggestedDifficulty          *bool    `toml:"lock_suggested_difficulty"`
	EnforceSuggestedDifficultyLimits *bool    `toml:"enforce_suggested_difficulty_limits"`
}

type miningTuning struct {
	DisablePoolJobEntropy     *bool `toml:"disable_pool_job_entropy"`
	DifficultyStepGranularity *int  `toml:"difficulty_step_granularity"`
}

type hashrateTuning struct {
	HashrateEMATauSeconds    *float64 `toml:"hashrate_ema_tau_seconds"`
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
	CleanExpiredOnStartup            *bool    `toml:"clean_expired_on_startup"`
	BanInvalidSubmissionsAfter       *int     `toml:"ban_invalid_submissions_after"`
	BanInvalidSubmissionsWindowSec   *int     `toml:"ban_invalid_submissions_window_seconds"`
	BanInvalidSubmissionsDurationSec *int     `toml:"ban_invalid_submissions_duration_seconds"`
	ReconnectBanThreshold            *int     `toml:"reconnect_ban_threshold"`
	ReconnectBanWindowSeconds        *int     `toml:"reconnect_ban_window_seconds"`
	ReconnectBanDurationSeconds      *int     `toml:"reconnect_ban_duration_seconds"`
	BannedMinerTypes                 []string `toml:"banned_miner_types"`
}

type versionTuning struct {
	MinVersionBits       *int  `toml:"min_version_bits"`
	IgnoreMinVersionBits *bool `toml:"ignore_min_version_bits"`
}

type statusTuning struct {
	MempoolAddressURL *string `toml:"mempool_address_url"`
}

// tuningFileConfig holds optional overrides from tuning.toml.
type tuningFileConfig struct {
	RateLimits   rateLimitTuning    `toml:"rate_limits"`
	Timeouts     timeoutTuning      `toml:"timeouts"`
	Difficulty   difficultyTuning   `toml:"difficulty"`
	Mining       miningTuning       `toml:"mining"`
	Hashrate     hashrateTuning     `toml:"hashrate"`
	Discord      discordTuning      `toml:"discord"`
	Status       statusTuning       `toml:"status"`
	PeerCleaning peerCleaningTuning `toml:"peer_cleaning"`
	Bans         banTuning          `toml:"bans"`
	Version      versionTuning      `toml:"version"`
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
