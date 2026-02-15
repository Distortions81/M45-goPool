package main

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
