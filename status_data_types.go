package main

import "time"

type SavedWorkerEntry struct {
	Name           string  `json:"name"`
	Hash           string  `json:"hash"`
	NotifyEnabled  bool    `json:"notify_enabled,omitempty"`
	BestDifficulty float64 `json:"best_difficulty,omitempty"`
}

// SavedWorkerRecord pairs a Clerk user ID with a saved worker entry.
type SavedWorkerRecord struct {
	UserID string
	SavedWorkerEntry
}

type StatusData struct {
	ListenAddr                      string                `json:"listen_addr"`
	StratumTLSListen                string                `json:"stratum_tls_listen,omitempty"`
	StratumV2Listen                 string                `json:"stratum_v2_listen,omitempty"`
	StratumPasswordEnabled          bool                  `json:"-"`
	StratumPasswordPublic           bool                  `json:"-"`
	StratumPassword                 string                `json:"-"`
	ClerkEnabled                    bool                  `json:"clerk_enabled"`
	ClerkLoginURL                   string                `json:"clerk_login_url,omitempty"`
	ClerkUser                       *ClerkUser            `json:"clerk_user,omitempty"`
	ClerkPublishableKey             string                `json:"-"`
	ClerkJSURL                      string                `json:"-"`
	SavedWorkers                    []SavedWorkerEntry    `json:"saved_workers,omitempty"`
	BrandName                       string                `json:"brand_name"`
	BrandDomain                     string                `json:"brand_domain"`
	Tagline                         string                `json:"tagline,omitempty"`
	ConnectMinerTitleExtra          string                `json:"connect_miner_title_extra,omitempty"`
	ConnectMinerTitleExtraURL       string                `json:"connect_miner_title_extra_url,omitempty"`
	ServerLocation                  string                `json:"server_location,omitempty"`
	FiatCurrency                    string                `json:"fiat_currency,omitempty"`
	BTCPriceFiat                    float64               `json:"btc_price_fiat,omitempty"`
	BTCPriceUpdatedAt               string                `json:"btc_price_updated_at,omitempty"`
	PoolDonationAddress             string                `json:"pool_donation_address,omitempty"`
	DiscordURL                      string                `json:"discord_url,omitempty"`
	DiscordNotificationsEnabled     bool                  `json:"discord_notifications_enabled,omitempty"`
	DiscordNotificationsRegistered  bool                  `json:"-"`
	DiscordNotificationsUserEnabled bool                  `json:"-"`
	GitHubURL                       string                `json:"github_url,omitempty"`
	MempoolAddressURL               string                `json:"mempool_address_url,omitempty"`
	NodeNetwork                     string                `json:"node_network,omitempty"`
	NodeSubversion                  string                `json:"node_subversion,omitempty"`
	NodeBlocks                      int64                 `json:"node_blocks"`
	NodeHeaders                     int64                 `json:"node_headers"`
	NodeInitialBlockDownload        bool                  `json:"node_initial_block_download"`
	NodeRPCURL                      string                `json:"node_rpc_url"`
	NodeZMQAddr                     string                `json:"node_zmq_addr,omitempty"`
	PayoutAddress                   string                `json:"payout_address,omitempty"`
	PoolFeePercent                  float64               `json:"pool_fee_percent"`
	OperatorDonationPercent         float64               `json:"operator_donation_percent,omitempty"`
	OperatorDonationAddress         string                `json:"operator_donation_address,omitempty"`
	OperatorDonationName            string                `json:"operator_donation_name,omitempty"`
	OperatorDonationURL             string                `json:"operator_donation_url,omitempty"`
	CoinbaseMessage                 string                `json:"coinbase_message,omitempty"`
	PoolEntropy                     string                `json:"pool_entropy,omitempty"`
	HashrateGraphTitle              string                `json:"-"`
	HashrateGraphDescription        string                `json:"-"`
	HashrateGraphID                 string                `json:"-"`
	DisplayPayoutAddress            string                `json:"display_payout_address,omitempty"`
	DisplayOperatorDonationAddress  string                `json:"display_operator_donation_address,omitempty"`
	DisplayCoinbaseMessage          string                `json:"display_coinbase_message,omitempty"`
	NodeConnections                 int                   `json:"node_connections"`
	NodeConnectionsIn               int                   `json:"node_connections_in"`
	NodeConnectionsOut              int                   `json:"node_connections_out"`
	NodePeerInfos                   []NodePeerInfo        `json:"node_peer_infos,omitempty"`
	NodePruned                      bool                  `json:"node_pruned"`
	NodeSizeOnDiskBytes             uint64                `json:"node_size_on_disk_bytes"`
	NodePeerCleanupEnabled          bool                  `json:"node_peer_cleanup_enabled"`
	NodePeerCleanupMaxPingMs        float64               `json:"node_peer_cleanup_max_ping_ms"`
	NodePeerCleanupMinPeers         int                   `json:"node_peer_cleanup_min_peers"`
	GenesisHash                     string                `json:"genesis_hash,omitempty"`
	GenesisExpected                 string                `json:"genesis_expected,omitempty"`
	GenesisMatch                    bool                  `json:"genesis_match"`
	BestBlockHash                   string                `json:"best_block_hash,omitempty"`
	PoolSoftware                    string                `json:"pool_software"`
	BuildVersion                    string                `json:"build_version,omitempty"`
	BuildTime                       string                `json:"build_time"`
	RenderDuration                  time.Duration         `json:"render_duration"`
	PageCached                      bool                  `json:"page_cached"`
	ActiveMiners                    int                   `json:"active_miners"`
	ActiveTLSMiners                 int                   `json:"active_tls_miners"`
	ActiveSV2Miners                 int                   `json:"active_sv2_miners"`
	SharesPerSecond                 float64               `json:"shares_per_second"`
	SharesPerMinute                 float64               `json:"shares_per_minute,omitempty"`
	Accepted                        uint64                `json:"accepted"`
	Rejected                        uint64                `json:"rejected"`
	StaleShares                     uint64                `json:"stale_shares"`
	LowDiffShares                   uint64                `json:"low_diff_shares"`
	RejectReasons                   map[string]uint64     `json:"reject_reasons,omitempty"`
	CurrentJob                      *Job                  `json:"current_job,omitempty"`
	Uptime                          time.Duration         `json:"uptime"`
	JobCreated                      string                `json:"job_created"`
	TemplateTime                    string                `json:"template_time"`
	Workers                         []WorkerView          `json:"workers"`
	BannedWorkers                   []WorkerView          `json:"banned_workers"`
	WindowAccepted                  uint64                `json:"window_accepted"`
	WindowSubmissions               uint64                `json:"window_submissions"`
	WindowStart                     string                `json:"window_start"`
	RPCError                        string                `json:"rpc_error,omitempty"`
	RPCHealthy                      bool                  `json:"rpc_healthy"`
	RPCDisconnects                  uint64                `json:"rpc_disconnects"`
	RPCReconnects                   uint64                `json:"rpc_reconnects"`
	AccountingError                 string                `json:"accounting_error,omitempty"`
	JobFeed                         JobFeedView           `json:"job_feed"`
	BestShares                      []BestShare           `json:"best_shares"`
	FoundBlocks                     []FoundBlockView      `json:"found_blocks,omitempty"`
	MinerTypes                      []MinerTypeView       `json:"miner_types,omitempty"`
	WorkerLookup                    map[string]WorkerView `json:"-"`
	RecentWork                      []RecentWorkView      `json:"-"`
	VardiffUp                       uint64                `json:"vardiff_up"`
	VardiffDown                     uint64                `json:"vardiff_down"`
	PoolHashrate                    float64               `json:"pool_hashrate,omitempty"`
	BlocksAccepted                  uint64                `json:"blocks_accepted"`
	BlocksErrored                   uint64                `json:"blocks_errored"`
	RPCGBTLastSec                   float64               `json:"rpc_gbt_last_sec"`
	RPCGBTMaxSec                    float64               `json:"rpc_gbt_max_sec"`
	RPCGBTCount                     uint64                `json:"rpc_gbt_count"`
	RPCSubmitLastSec                float64               `json:"rpc_submit_last_sec"`
	RPCSubmitMaxSec                 float64               `json:"rpc_submit_max_sec"`
	RPCSubmitCount                  uint64                `json:"rpc_submit_count"`
	RPCErrors                       uint64                `json:"rpc_errors"`
	ShareErrors                     uint64                `json:"share_errors"`
	RPCGBTMin1hSec                  float64               `json:"rpc_gbt_min_1h_sec"`
	RPCGBTAvg1hSec                  float64               `json:"rpc_gbt_avg_1h_sec"`
	RPCGBTMax1hSec                  float64               `json:"rpc_gbt_max_1h_sec"`
	ErrorHistory                    []PoolErrorEvent      `json:"error_history,omitempty"`
	// Local process / system diagnostics (server-only).
	ProcessGoroutines   int     `json:"process_goroutines"`
	ProcessCPUPercent   float64 `json:"process_cpu_percent"`
	GoMemAllocBytes     uint64  `json:"go_mem_alloc_bytes"`
	GoMemSysBytes       uint64  `json:"go_mem_sys_bytes"`
	ProcessRSSBytes     uint64  `json:"process_rss_bytes"`
	SystemMemTotalBytes uint64  `json:"system_mem_total_bytes"`
	SystemMemFreeBytes  uint64  `json:"system_mem_free_bytes"`
	SystemMemUsedBytes  uint64  `json:"system_mem_used_bytes"`
	SystemLoad1         float64 `json:"system_load1"`
	SystemLoad5         float64 `json:"system_load5"`
	SystemLoad15        float64 `json:"system_load15"`
	// Safe-to-share pool config summary.
	MaxConns                        int      `json:"max_conns"`
	MaxAcceptsPerSecond             int      `json:"max_accepts_per_second"`
	MaxAcceptBurst                  int      `json:"max_accept_burst"`
	MinDifficulty                   float64  `json:"min_difficulty"`
	MaxDifficulty                   float64  `json:"max_difficulty"`
	LockSuggestedDifficulty         bool     `json:"lock_suggested_difficulty"`
	BannedMinerTypes                []string `json:"banned_miner_types,omitempty"`
	TargetSharesPerMin              float64  `json:"target_shares_per_min,omitempty"`
	MinHashrateForTarget            float64  `json:"min_hashrate_for_target,omitempty"`
	MaxHashrateForTarget            float64  `json:"max_hashrate_for_target,omitempty"`
	HashrateEMATauSeconds           float64  `json:"hashrate_ema_tau_seconds"`
	HashrateCumulativeEnabled       bool     `json:"hashrate_cumulative_enabled"`
	HashrateRecentCumulativeEnabled bool     `json:"hashrate_recent_cumulative_enabled"`
	ShareNTimeMaxForwardSeconds     int      `json:"share_ntime_max_forward_seconds"`
	Warnings                        []string `json:"warnings,omitempty"`
}

type ServerPageJobFeed struct {
	LastError         string   `json:"last_error,omitempty"`
	LastErrorAt       string   `json:"last_error_at,omitempty"`
	ErrorHistory      []string `json:"error_history,omitempty"`
	ZMQHealthy        bool     `json:"zmq_healthy"`
	ZMQDisconnects    uint64   `json:"zmq_disconnects"`
	ZMQReconnects     uint64   `json:"zmq_reconnects"`
	LastRawBlockAt    string   `json:"last_raw_block_at,omitempty"`
	LastRawBlockBytes int      `json:"last_raw_block_bytes,omitempty"`
	BlockHash         string   `json:"block_hash,omitempty"`
	BlockHeight       int64    `json:"block_height,omitempty"`
	BlockTime         string   `json:"block_time,omitempty"`
	BlockBits         string   `json:"block_bits,omitempty"`
	BlockDifficulty   float64  `json:"block_difficulty,omitempty"`
}

// OverviewPageData contains data for the overview page (minimal payload)
type OverviewPageData struct {
	APIVersion      string           `json:"api_version"`
	ActiveMiners    int              `json:"active_miners"`
	ActiveTLSMiners int              `json:"active_tls_miners"`
	ActiveSV2Miners int              `json:"active_sv2_miners"`
	SharesPerMinute float64          `json:"shares_per_minute,omitempty"`
	PoolHashrate    float64          `json:"pool_hashrate,omitempty"`
	PoolTag         string           `json:"pool_tag,omitempty"`
	BTCPriceFiat    float64          `json:"btc_price_fiat,omitempty"`
	BTCPriceUpdated string           `json:"btc_price_updated_at,omitempty"`
	FiatCurrency    string           `json:"fiat_currency,omitempty"`
	RenderDuration  time.Duration    `json:"render_duration"`
	Workers         []RecentWorkView `json:"workers"`
	BannedWorkers   []WorkerView     `json:"banned_workers"`
	BestShares      []BestShare      `json:"best_shares"`
	MinerTypes      []MinerTypeView  `json:"miner_types,omitempty"`
}

type PoolErrorEvent struct {
	At      string `json:"at,omitempty"`
	Type    string `json:"type"`
	Message string `json:"message"`
}

type PoolDisconnectEvent struct {
	At           string `json:"at,omitempty"`
	Disconnected int    `json:"disconnected"`
	Reason       string `json:"reason,omitempty"`
	Detail       string `json:"detail,omitempty"`
}

// PoolPageData contains data for the pool info page
type PoolPageData struct {
	APIVersion                      string                `json:"api_version"`
	BlocksAccepted                  uint64                `json:"blocks_accepted"`
	BlocksErrored                   uint64                `json:"blocks_errored"`
	RPCGBTLastSec                   float64               `json:"rpc_gbt_last_sec"`
	RPCGBTMaxSec                    float64               `json:"rpc_gbt_max_sec"`
	RPCGBTCount                     uint64                `json:"rpc_gbt_count"`
	RPCSubmitLastSec                float64               `json:"rpc_submit_last_sec"`
	RPCSubmitMaxSec                 float64               `json:"rpc_submit_max_sec"`
	RPCSubmitCount                  uint64                `json:"rpc_submit_count"`
	RPCErrors                       uint64                `json:"rpc_errors"`
	ShareErrors                     uint64                `json:"share_errors"`
	RPCGBTMin1hSec                  float64               `json:"rpc_gbt_min_1h_sec"`
	RPCGBTAvg1hSec                  float64               `json:"rpc_gbt_avg_1h_sec"`
	RPCGBTMax1hSec                  float64               `json:"rpc_gbt_max_1h_sec"`
	StratumSafeguardDisconnectCount uint64                `json:"stratum_safeguard_disconnect_count,omitempty"`
	StratumSafeguardDisconnects     []PoolDisconnectEvent `json:"stratum_safeguard_disconnects,omitempty"`
	ErrorHistory                    []PoolErrorEvent      `json:"error_history,omitempty"`
}

// ServerPageData contains data for the server diagnostics page
type ServerPageData struct {
	APIVersion          string            `json:"api_version"`
	Uptime              time.Duration     `json:"uptime"`
	RPCError            string            `json:"rpc_error,omitempty"`
	RPCHealthy          bool              `json:"rpc_healthy"`
	RPCDisconnects      uint64            `json:"rpc_disconnects"`
	RPCReconnects       uint64            `json:"rpc_reconnects"`
	AccountingError     string            `json:"accounting_error,omitempty"`
	JobFeed             ServerPageJobFeed `json:"job_feed"`
	ProcessGoroutines   int               `json:"process_goroutines"`
	ProcessCPUPercent   float64           `json:"process_cpu_percent"`
	GoMemAllocBytes     uint64            `json:"go_mem_alloc_bytes"`
	GoMemSysBytes       uint64            `json:"go_mem_sys_bytes"`
	ProcessRSSBytes     uint64            `json:"process_rss_bytes"`
	SystemMemTotalBytes uint64            `json:"system_mem_total_bytes"`
	SystemMemFreeBytes  uint64            `json:"system_mem_free_bytes"`
	SystemMemUsedBytes  uint64            `json:"system_mem_used_bytes"`
	SystemLoad1         float64           `json:"system_load1"`
	SystemLoad5         float64           `json:"system_load5"`
	SystemLoad15        float64           `json:"system_load15"`
}

type JobFeedView struct {
	Ready             bool     `json:"ready"`
	LastSuccess       string   `json:"last_success"`
	LastError         string   `json:"last_error,omitempty"`
	LastErrorAt       string   `json:"last_error_at,omitempty"`
	ErrorHistory      []string `json:"error_history,omitempty"`
	ZMQHealthy        bool     `json:"zmq_healthy"`
	ZMQDisconnects    uint64   `json:"zmq_disconnects"`
	ZMQReconnects     uint64   `json:"zmq_reconnects"`
	LastRawBlockAt    string   `json:"last_raw_block_at,omitempty"`
	LastRawBlockBytes int      `json:"last_raw_block_bytes,omitempty"`
	BlockHash         string   `json:"block_hash,omitempty"`
	BlockHeight       int64    `json:"block_height,omitempty"`
	BlockTime         string   `json:"block_time,omitempty"`
	BlockBits         string   `json:"block_bits,omitempty"`
	BlockDifficulty   float64  `json:"block_difficulty,omitempty"`
}

type MinerTypeView struct {
	Name     string                 `json:"name"`
	Total    int                    `json:"total_workers"`
	Versions []MinerTypeVersionView `json:"versions"`
}

type MinerTypeVersionView struct {
	Version string `json:"version,omitempty"`
	Workers int    `json:"workers"`
}

type FoundBlockView struct {
	Height           int64     `json:"height"`
	Hash             string    `json:"hash"`
	DisplayHash      string    `json:"display_hash"`
	Worker           string    `json:"worker"`
	DisplayWorker    string    `json:"display_worker"`
	Timestamp        time.Time `json:"timestamp"`
	ShareDiff        float64   `json:"share_diff"`
	PoolFeeSats      int64     `json:"pool_fee_sats,omitempty"`
	WorkerPayoutSats int64     `json:"worker_payout_sats,omitempty"`
	Confirmations    int64     `json:"confirmations,omitempty"`
	// Result is derived from confirmations and indicates whether the block is
	// merely a candidate ("possible"), a confirmed winner ("winning"), or a
	// stale/orphan block ("stale").
	Result string `json:"result,omitempty"`
}
