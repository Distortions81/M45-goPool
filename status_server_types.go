package main

import "time"

// PoolStatsData contains essential pool statistics without worker details
type PoolStatsData struct {
	APIVersion              string            `json:"api_version"`
	BrandName               string            `json:"brand_name"`
	BrandDomain             string            `json:"brand_domain"`
	ListenAddr              string            `json:"listen_addr"`
	StratumTLSListen        string            `json:"stratum_tls_listen,omitempty"`
	PoolSoftware            string            `json:"pool_software"`
	BuildVersion            string            `json:"build_version,omitempty"`
	BuildTime               string            `json:"build_time"`
	Uptime                  time.Duration     `json:"uptime"`
	ActiveMiners            int               `json:"active_miners"`
	PoolHashrate            float64           `json:"pool_hashrate"`
	SharesPerSecond         float64           `json:"shares_per_second"`
	Accepted                uint64            `json:"accepted"`
	Rejected                uint64            `json:"rejected"`
	StaleShares             uint64            `json:"stale_shares"`
	LowDiffShares           uint64            `json:"low_diff_shares"`
	RejectReasons           map[string]uint64 `json:"reject_reasons,omitempty"`
	WindowAccepted          uint64            `json:"window_accepted"`
	WindowSubmissions       uint64            `json:"window_submissions"`
	WindowStart             string            `json:"window_start"`
	VardiffUp               uint64            `json:"vardiff_up"`
	VardiffDown             uint64            `json:"vardiff_down"`
	BlocksAccepted          uint64            `json:"blocks_accepted"`
	BlocksErrored           uint64            `json:"blocks_errored"`
	MinDifficulty           float64           `json:"min_difficulty"`
	MaxDifficulty           float64           `json:"max_difficulty"`
	PoolFeePercent          float64           `json:"pool_fee_percent"`
	OperatorDonationPercent float64           `json:"operator_donation_percent,omitempty"`
	OperatorDonationName    string            `json:"operator_donation_name,omitempty"`
	OperatorDonationURL     string            `json:"operator_donation_url,omitempty"`
	CurrentJob              *Job              `json:"current_job,omitempty"`
	JobCreated              string            `json:"job_created"`
	TemplateTime            string            `json:"template_time"`
	JobFeed                 JobFeedView       `json:"job_feed"`
	BTCPriceFiat            float64           `json:"btc_price_fiat,omitempty"`
	BTCPriceUpdatedAt       string            `json:"btc_price_updated_at,omitempty"`
	FiatCurrency            string            `json:"fiat_currency,omitempty"`
	Warnings                []string          `json:"warnings,omitempty"`
}

// NodePageData contains Bitcoin node information for the node page
type NodePageData struct {
	APIVersion               string         `json:"api_version"`
	NodeNetwork              string         `json:"node_network,omitempty"`
	NodeSubversion           string         `json:"node_subversion,omitempty"`
	NodeBlocks               int64          `json:"node_blocks"`
	NodeHeaders              int64          `json:"node_headers"`
	NodeInitialBlockDownload bool           `json:"node_initial_block_download"`
	NodeConnections          int            `json:"node_connections"`
	NodeConnectionsIn        int            `json:"node_connections_in"`
	NodeConnectionsOut       int            `json:"node_connections_out"`
	NodePeers                []NodePeerInfo `json:"node_peers,omitempty"`
	NodePruned               bool           `json:"node_pruned"`
	NodeSizeOnDiskBytes      uint64         `json:"node_size_on_disk_bytes"`
	NodePeerCleanupEnabled   bool           `json:"node_peer_cleanup_enabled"`
	NodePeerCleanupMaxPingMs float64        `json:"node_peer_cleanup_max_ping_ms"`
	NodePeerCleanupMinPeers  int            `json:"node_peer_cleanup_min_peers"`
	GenesisHash              string         `json:"genesis_hash,omitempty"`
	GenesisExpected          string         `json:"genesis_expected,omitempty"`
	GenesisMatch             bool           `json:"genesis_match"`
	BestBlockHash            string         `json:"best_block_hash,omitempty"`
}

type NodePeerInfo struct {
	Display     string  `json:"display"`
	PingMs      float64 `json:"ping_ms"`
	ConnectedAt int64   `json:"connected_at"`
}

type nextDifficultyRetarget struct {
	Height           int64  `json:"height"`
	BlocksAway       int64  `json:"blocks_away"`
	DurationEstimate string `json:"duration_estimate,omitempty"`
}
