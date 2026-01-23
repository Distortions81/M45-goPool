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
	ListenAddr        string // e.g. ":3333"
	StatusAddr        string // HTTP status listen address, e.g. ":80"
	StatusTLSAddr     string // HTTPS status listen address, e.g. ":443"
	StatusBrandName   string
	StatusBrandDomain string
	// StatusPublicURL is the canonical HTTP(S) URL used for redirects and session cookies.
	StatusPublicURL string
	StatusTagline   string
	// StatusConnectMinerTitleExtra is optional extra text appended to the
	// "Connect your miner" header on the overview page.
	StatusConnectMinerTitleExtra string
	// StatusConnectMinerTitleExtraURL optionally turns the extra text into
	// a hyperlink when set.
	StatusConnectMinerTitleExtraURL string
	// FiatCurrency controls which fiat currency (e.g. "usd") is used
	// when displaying approximate BTC prices on the status UI. It is
	// only used for display and never affects payouts or accounting.
	FiatCurrency string
	// PoolDonationAddress is an optional wallet where users can donate TO
	// the pool operator. It is shown in the UI footer and never used for payouts.
	PoolDonationAddress string
	// DiscordURL is an optional Discord invite link shown in the header.
	DiscordURL string
	// DiscordServerID is the Discord server/guild ID used for optional
	// Discord notifications features.
	DiscordServerID string
	// DiscordNotifyChannelID is the Discord channel where offline alerts are posted.
	DiscordNotifyChannelID string
	// DiscordBotToken is the Discord bot token used for optional Discord
	// notifications features. This should be stored in secrets.toml.
	DiscordBotToken string
	// GitHubURL is an optional GitHub link shown in the header and About page.
	GitHubURL string
	// ServerLocation is an optional server location string shown in the header.
	ServerLocation string
	// StratumTLSListen is an optional TCP address for a TLS-enabled
	// Stratum listener (e.g. ":3443"). When empty, TLS for Stratum is
	// disabled and only the plain TCP listener is used.
	StratumTLSListen string
	// ClerkIssuerURL is the host that issues Clerk session tokens for this pool.
	ClerkIssuerURL string
	// ClerkJWKSURL is the URL where Clerk publishes RSA keys for verifying JWTs.
	ClerkJWKSURL string
	// ClerkSignInURL is the hosted Clerk sign-in page (typically auth.clerk.dev/sign-in).
	ClerkSignInURL string
	// ClerkCallbackPath determines where Clerk redirects after sign-in.
	ClerkCallbackPath string
	// ClerkFrontendAPIURL is optionally passed to Clerk so it can identify your front-end instance.
	ClerkFrontendAPIURL string
	// ClerkSessionCookieName is the cookie that Clerk sets for authenticated sessions.
	ClerkSessionCookieName string
	// ClerkSessionAudience, if set, is the expected audience claim for session JWTs.
	ClerkSessionAudience string
	// ClerkSecretKey is the Clerk backend secret key (sk_test_... / sk_live_...).
	// This should be stored in secrets.toml, not config.toml.
	ClerkSecretKey string
	// ClerkPublishableKey is the Clerk publishable key (pk_test_... / pk_live_...)
	// used for rendering the embedded Clerk sign-in UI. Stored in secrets.toml
	// to keep deployments consistent (even though it's not a secret).
	ClerkPublishableKey string
	RPCURL              string // e.g. "http://127.0.0.1:8332"
	RPCUser             string
	RPCPass             string
	// RPCCookiePath optionally points at bitcoind's auth cookie file when RPC
	// credentials are not set in secrets.toml.
	RPCCookiePath           string
	rpCCookiePathFromConfig string
	rpcCookieWatch          bool
	// AllowPublicRPC lets goPool connect to nodes that intentionally expose RPC
	// without any authentication (useful for public/testing nodes). Defaults to
	// false for security.
	AllowPublicRPC bool
	PayoutAddress  string
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
	JobEntropy                int
	CoinbaseMsg               string
	PoolEntropy               string
	PoolTagPrefix             string
	CoinbaseScriptSigMaxBytes int
	ZMQBlockAddr              string
	// BackblazeBackupEnabled toggles periodic uploads of the worker list
	// database snapshot to Backblaze B2.
	BackblazeBackupEnabled bool
	// BackblazeBucket is the B2 bucket where backups are stored.
	BackblazeBucket string
	// BackblazeAccountID is set from secrets and used for authentication.
	BackblazeAccountID string
	// BackblazeApplicationKey is set from secrets and used for authentication.
	BackblazeApplicationKey string
	// BackblazePrefix is an optional namespace prefix applied to uploaded objects.
	BackblazePrefix string
	// BackblazeBackupIntervalSeconds controls how often backups run (seconds).
	BackblazeBackupIntervalSeconds int
	// BackblazeKeepLocalCopy controls whether a local copy of the last
	// successfully uploaded backup is stored in the state dir.
	BackblazeKeepLocalCopy bool
	// BackupSnapshotPath optionally overrides where the local worker database
	// snapshot is written.
	//
	// When empty, the snapshot defaults to `data/state/workers.db.bak` when
	// BackblazeKeepLocalCopy is enabled.
	//
	// (Legacy default suffix was `.b2last`; it is now `.bak`.)
	//
	// When set to a relative path, it is resolved relative to DataDir.
	BackupSnapshotPath string
	DataDir            string
	MaxConns           int
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
	ConnectionTimeout                time.Duration
	VersionMask                      uint32
	MinVersionBits                   int
	IgnoreMinVersionBits             bool
	VersionMaskConfigured            bool
	MaxDifficulty                    float64
	MinDifficulty                    float64
	// If true, workers that call mining.suggest_difficulty will be kept at
	// that difficulty (clamped to min/max) and VarDiff will not adjust them.
	LockSuggestedDifficulty bool
	// HashrateEMATauSeconds controls the time constant (in seconds) for the
	// per-connection hashrate exponential moving average.
	HashrateEMATauSeconds float64
	// HashrateEMAMinShares defines how many shares must be accepted between
	// EMA samples to ensure the starting window spans enough work.
	HashrateEMAMinShares int
	// NTimeForwardSlackSeconds bounds how far ntime may roll forward from
	// the template's curtime/mintime before being rejected.
	NTimeForwardSlackSeconds int
	// CheckDuplicateShares enables duplicate share detection. Disabled by
	// default for solo pools where duplicate checking is unnecessary overhead.
	// Enable with -check-duplicates flag for testing.
	CheckDuplicateShares bool

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
	StatusConnectMinerTitleExtra      string  `json:"status_connect_miner_title_extra,omitempty"`
	StatusConnectMinerTitleExtraURL   string  `json:"status_connect_miner_title_extra_url,omitempty"`
	FiatCurrency                      string  `json:"fiat_currency,omitempty"`
	PoolDonationAddress               string  `json:"pool_donation_address,omitempty"`
	DiscordURL                        string  `json:"discord_url,omitempty"`
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
	PoolListen      string `toml:"pool_listen" comment:"TCP address for the plain stratum listener (default :3333)."`
	StatusListen    string `toml:"status_listen" comment:"HTTP address for the status UI (default :80)."`
	StatusTLSListen string `toml:"status_tls_listen" comment:"Optional HTTPS address that serves the status UI with TLS (default :443)."`
	StatusPublicURL string `toml:"status_public_url" comment:"Canonical public URL used for redirects/session cookies; include scheme (http/https)."`
}

type brandingConfig struct {
	StatusBrandName                 string `toml:"status_brand_name" comment:"Optional headline name shown at the top of the status page."`
	StatusBrandDomain               string `toml:"status_brand_domain" comment:"Optional domain used to qualify the brand name in the status UI."`
	StatusTagline                   string `toml:"status_tagline" comment:"Subtitle describing the pool on the status page (default 'Solo Mining Pool')."`
	StatusConnectMinerTitleExtra    string `toml:"status_connect_miner_title_extra" comment:"Extra text appended to the 'Connect your miner' heading."`
	StatusConnectMinerTitleExtraURL string `toml:"status_connect_miner_title_extra_url" comment:"Optional URL to hyperlink the extra text added to the connect header."`
	FiatCurrency                    string `toml:"fiat_currency" comment:"Fiat currency symbol (e.g. usd) used by the status UI price display."`
	PoolDonationAddress             string `toml:"pool_donation_address" comment:"Optional wallet where miners can donate to the pool operator."`
	DiscordURL                      string `toml:"discord_url" comment:"Discord server invite link shown in the status header."`
	DiscordServerID                 string `toml:"discord_server_id" comment:"Discord server ID used for notification features."`
	DiscordNotifyChannelID          string `toml:"discord_notify_channel_id" comment:"Discord channel ID where notifications are posted when enabled."`
	GitHubURL                       string `toml:"github_url" comment:"Optional GitHub repository link shown in the header/about pages."`
	ServerLocation                  string `toml:"server_location" comment:"Optional textual location shown in the footer (e.g. city, datacenter)."`
}

type stratumConfig struct {
	StratumTLSListen string `toml:"stratum_tls_listen" comment:"TLS address for the secure Stratum listener (leave blank to disable)."`
}

type authConfig struct {
	ClerkIssuerURL         string `toml:"clerk_issuer_url" comment:"Issuer URL used by Clerk for session tokens (typically https://clerk.clerk.dev)."`
	ClerkJWKSURL           string `toml:"clerk_jwks_url" comment:"JWKS endpoint Clerk publishes for verifying tokens."`
	ClerkSignInURL         string `toml:"clerk_signin_url" comment:"Hosted Clerk sign-in page that operators link to for authentication."`
	ClerkCallbackPath      string `toml:"clerk_callback_path" comment:"Local path Clerk redirects to after sign-in (prefixed with '/')."`
	ClerkFrontendAPIURL    string `toml:"clerk_frontend_api_url" comment:"Frontend API URL passed to Clerk during session handling."`
	ClerkSessionCookieName string `toml:"clerk_session_cookie_name" comment:"Cookie name Clerk uses for authenticated sessions (default __session)."`
	ClerkSessionAudience   string `toml:"clerk_session_audience" comment:"Optional audience claim required for Clerk-issued JWTs."`
}

type nodeConfig struct {
	RPCURL         string `toml:"rpc_url" comment:"RPC URL of the Bitcoin node used for getblocktemplate and submitblock."`
	PayoutAddress  string `toml:"payout_address" comment:"Wallet where block payouts are sent to miners."`
	DataDir        string `toml:"data_dir" comment:"Directory where goPool stores state, logs, and configs."`
	ZMQBlockAddr   string `toml:"zmq_block_addr" comment:"ZMQ endpoint exposed by the node for block notifications."`
	RPCCookiePath  string `toml:"rpc_cookie_path" comment:"Path to bitcoind's auth cookie (auto-detected if blank)."`
	AllowPublicRPC bool   `toml:"allow_public_rpc" comment:"Allow connecting to RPC endpoints without authentication (useful for trusted/testing nodes)."`
}

type backblazeBackupConfig struct {
	Enabled         bool   `toml:"enabled" comment:"Toggle periodic uploads of the saved workers snapshot to Backblaze B2."`
	Bucket          string `toml:"bucket" comment:"Backblaze B2 bucket where backups are stored."`
	Prefix          string `toml:"prefix" comment:"Optional namespace prefix for Backblaze object keys."`
	IntervalSeconds *int   `toml:"interval_seconds" comment:"Seconds between scheduled uploads (default 43200)."`
	KeepLocalCopy   *bool  `toml:"keep_local_copy" comment:"Keep a local copy of the last successful snapshot even when backups run."`
	SnapshotPath    string `toml:"snapshot_path" comment:"File path where the worker DB snapshot is stored locally."`
}

type miningConfig struct {
	PoolFeePercent            *float64 `toml:"pool_fee_percent" comment:"Pool fee percentage applied to each block reward (default 2.0). Operator donation percent applies on top of this value."`
	OperatorDonationPercent   *float64 `toml:"operator_donation_percent" comment:"Percentage of the pool fee (not total reward) to donate to another wallet. Requires operator_donation_address when >0."`
	OperatorDonationAddress   string   `toml:"operator_donation_address" comment:"Wallet address where operator donations are sent when operator_donation_percent > 0."`
	OperatorDonationName      string   `toml:"operator_donation_name" comment:"Optional display name for the donation recipient shown in the status UI."`
	OperatorDonationURL       string   `toml:"operator_donation_url" comment:"Optional hyperlink for the operator donation recipient."`
	Extranonce2Size           *int     `toml:"extranonce2_size" comment:"Number of extranonce2 bytes miners receive; larger values allow more workers per connection."`
	TemplateExtraNonce2Size   *int     `toml:"template_extra_nonce2_size" comment:"Extra placeholder for extranonce2 inside the template. Must be >= extranonce2_size."`
	JobEntropy                *int     `toml:"job_entropy" comment:"Random alphanumeric chars added per job for uniqueness (0-16). When >0, coinbase adds a suffix '<pool_entropy>-<job_entropy>'."`
	PoolEntropy               *string  `toml:"pool_entropy" comment:"4-char alphanumeric pool identifier used in the coinbase suffix '<pool_entropy>-<job_entropy>'."`
	PoolTagPrefix             string   `toml:"pooltag_prefix" comment:"Optional custom prefix (a-z, 0-9 only). Prepends '<prefix>-' to the goPool coinbase tag."`
	CoinbaseScriptSigMaxBytes *int     `toml:"coinbase_scriptsig_max_bytes" comment:"Clamp coinbase message length (bytes) inside the scriptSig; useful to stay below policy limits."`
}

type baseFileConfig struct {
	Server    serverConfig          `toml:"server"`
	Branding  brandingConfig        `toml:"branding"`
	Stratum   stratumConfig         `toml:"stratum"`
	Auth      authConfig            `toml:"auth"`
	Node      nodeConfig            `toml:"node"`
	Mining    miningConfig          `toml:"mining"`
	Backblaze backblazeBackupConfig `toml:"backblaze_backup"`
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

// filterAlphanumeric returns only lowercase a-z and 0-9 characters from s.
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
	MaxConns                          *int     `toml:"max_conns" comment:"Maximum simultaneous connections tracked by the pool."`
	MaxAcceptsPerSecond               *int     `toml:"max_accepts_per_second" comment:"Rate limit for new TCP accepts per second."`
	MaxAcceptBurst                    *int     `toml:"max_accept_burst" comment:"Short-term burst capacity before rate limits kick in."`
	AutoAcceptRateLimits              *bool    `toml:"auto_accept_rate_limits" comment:"Automatically compute accept limits based on max_conns."`
	AcceptReconnectWindow             *int     `toml:"accept_reconnect_window" comment:"Seconds during which reconnecting miners can reconnect smoothly after a restart."`
	AcceptBurstWindow                 *int     `toml:"accept_burst_window" comment:"Duration of the initial burst phase before steady-state throttling."`
	AcceptSteadyStateWindow           *int     `toml:"accept_steady_state_window" comment:"Time until pool switches from reconnect to steady-state rate limiting."`
	AcceptSteadyStateRate             *int     `toml:"accept_steady_state_rate" comment:"Maximum accepts per second during steady-state operation."`
	AcceptSteadyStateReconnectPercent *float64 `toml:"accept_steady_state_reconnect_percent" comment:"Expected percent of miners reconnecting during steady-state to size steady-state rate."`
	AcceptSteadyStateReconnectWindow  *int     `toml:"accept_steady_state_reconnect_window" comment:"Time window (sec) to observe reconnects for steady-state calculations."`
}

type timeoutTuning struct {
	ConnectionTimeoutSec *int `toml:"connection_timeout_seconds" comment:"Timeout (seconds) for miner connections before they are closed."`
}

type difficultyTuning struct {
	MaxDifficulty           *float64 `toml:"max_difficulty" comment:"Maximum difficulty advertised to miners."`
	MinDifficulty           *float64 `toml:"min_difficulty" comment:"Minimum difficulty advertised to miners."`
	LockSuggestedDifficulty *bool    `toml:"lock_suggested_difficulty" comment:"Lock difficulties suggested by miners to prevent VarDiff adjustments."`
}

type miningTuning struct {
	// DisablePoolJobEntropy, when true, disables adding the per-job
	// "<pool entropy>-<job entropy>" suffix to the coinbase message.
	DisablePoolJobEntropy *bool `toml:"disable_pool_job_entropy" comment:"Disable the '<pool_entropy>-<job_entropy>' coinbase suffix"`
}

type hashrateTuning struct {
	HashrateEMATauSeconds    *float64 `toml:"hashrate_ema_tau_seconds" comment:"Time constant (seconds) for per-worker hashrate EMAs."`
	HashrateEMAMinShares     *int     `toml:"hashrate_ema_min_shares" comment:"Minimum accepted shares before the EMA is considered valid."`
	NTimeForwardSlackSeconds *int     `toml:"ntime_forward_slack_seconds" comment:"Allowed future timestamps miners may submit to guard against time drift."`
}

type peerCleaningTuning struct {
	Enabled   *bool    `toml:"enabled" comment:"Enable periodic cleaning of peers that appear dead."`
	MaxPingMs *float64 `toml:"max_ping_ms" comment:"Max ping time (ms) before a peer is cleaned."`
	MinPeers  *int     `toml:"min_peers" comment:"Minimum peers the pool should keep during cleaning."`
}

type banTuning struct {
	BanInvalidSubmissionsAfter       *int `toml:"ban_invalid_submissions_after" comment:"Seconds before banning miners that submit invalid shares."`
	BanInvalidSubmissionsWindowSec   *int `toml:"ban_invalid_submissions_window_seconds" comment:"Observation window (seconds) for tracking invalid shares."`
	BanInvalidSubmissionsDurationSec *int `toml:"ban_invalid_submissions_duration_seconds" comment:"Duration (seconds) of bans triggered by invalid share thresholds."`
	ReconnectBanThreshold            *int `toml:"reconnect_ban_threshold" comment:"Number of reconnects that trigger a ban."`
	ReconnectBanWindowSeconds        *int `toml:"reconnect_ban_window_seconds" comment:"Seconds over which reconnections are counted for bans."`
	ReconnectBanDurationSeconds      *int `toml:"reconnect_ban_duration_seconds" comment:"Ban duration (seconds) applied when reconnect ban threshold is reached."`
}

type versionTuning struct {
	MinVersionBits       *int  `toml:"min_version_bits" comment:"Minimum number of version bits a miner must advertise."`
	IgnoreMinVersionBits *bool `toml:"ignore_min_version_bits" comment:"Ignore the min version bits requirement when set."`
}

// tuningFileConfig captures the optional overrides that can be set via the
// generated tuning.toml file. goPool merges this on top of the base config so
// operators can tweak advanced knobs without modifying config.toml directly.
type tuningFileConfig struct {
	RateLimits   rateLimitTuning    `toml:"rate_limits"`
	Timeouts     timeoutTuning      `toml:"timeouts"`
	Difficulty   difficultyTuning   `toml:"difficulty"`
	Mining       miningTuning       `toml:"mining"`
	Hashrate     hashrateTuning     `toml:"hashrate"`
	PeerCleaning peerCleaningTuning `toml:"peer_cleaning"`
	Bans         banTuning          `toml:"bans"`
	Version      versionTuning      `toml:"version"`
}

func buildBaseFileConfig(cfg Config) baseFileConfig {
	return baseFileConfig{
		Server: serverConfig{
			PoolListen:      cfg.ListenAddr,
			StatusListen:    cfg.StatusAddr,
			StatusTLSListen: cfg.StatusTLSAddr,
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
			MinVersionBits:       intPtr(cfg.MinVersionBits),
			IgnoreMinVersionBits: boolPtr(cfg.IgnoreMinVersionBits),
		},
	}
}

// secretsConfig holds values from secrets.toml: Clerk secrets and (when enabled)
// RPC user/password for fallback authentication. This file is gitignored so only
// store sensitive credentials here.
type secretsConfig struct {
	RPCUser                 string `toml:"rpc_user" comment:"Optional RPC username when cookie auth is unavailable (requires -allow-rpc-credentials)."`
	RPCPass                 string `toml:"rpc_pass" comment:"RPC password paired with rpc_user for fallback authentication."`
	DiscordBotToken         string `toml:"discord_token" comment:"Token for the Discord bot used in outgoing notifications."`
	ClerkSecretKey          string `toml:"clerk_secret_key" comment:"Secret key for Clerk development integrations (used when exchanging JWTs)."`
	ClerkPublishableKey     string `toml:"clerk_publishable_key" comment:"Publishable key used by the Clerk frontend UI."`
	BackblazeAccountID      string `toml:"backblaze_account_id" comment:"Backblaze B2 account ID for uploading backups."`
	BackblazeApplicationKey string `toml:"backblaze_application_key" comment:"Application key used with Backblaze B2 uploads."`
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
	if fc.Server.StatusTLSListen != "" {
		cfg.StatusTLSAddr = fc.Server.StatusTLSListen
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
