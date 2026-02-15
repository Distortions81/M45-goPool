package main

import (
	"fmt"
	"net"
	"sort"
	"strings"
	"time"
)

type cachedNodeInfo struct {
	network     string
	subversion  string
	blocks      int64
	headers     int64
	ibd         bool
	pruned      bool
	sizeOnDisk  uint64
	conns       int
	connsIn     int
	connsOut    int
	genesisHash string
	bestHash    string
	peerInfos   []cachedPeerInfo
	fetchedAt   time.Time
}

type cachedPeerInfo struct {
	host        string
	display     string
	pingSeconds float64
	connectedAt time.Time
}

type peerDisplayInfo struct {
	host        string
	display     string
	pingSeconds float64
	connectedAt time.Time
	rawAddr     string
}

type peerLookupEntry struct {
	name      string
	expiresAt time.Time
}

func (s *StatusServer) lookupPeerName(host string) string {
	if host == "" {
		return ""
	}
	now := time.Now()
	s.peerLookupMu.Lock()
	if entry, ok := s.peerLookupCache[host]; ok && now.Before(entry.expiresAt) {
		name := entry.name
		s.peerLookupMu.Unlock()
		return name
	}
	s.peerLookupMu.Unlock()

	var name string
	if ptrs, err := net.LookupAddr(host); err == nil && len(ptrs) > 0 {
		name = strings.TrimSuffix(strings.TrimSpace(ptrs[0]), ".")
	}

	s.peerLookupMu.Lock()
	if s.peerLookupCache == nil {
		s.peerLookupCache = make(map[string]peerLookupEntry)
	}
	s.peerLookupCache[host] = peerLookupEntry{
		name:      name,
		expiresAt: now.Add(peerLookupTTL),
	}
	s.peerLookupMu.Unlock()

	return name
}

func buildNodePeerInfos(peers []cachedPeerInfo) []NodePeerInfo {
	out := make([]NodePeerInfo, 0, len(peers))
	for _, p := range peers {
		out = append(out, NodePeerInfo{
			Display:     p.display,
			PingMs:      p.pingSeconds * 1000,
			ConnectedAt: p.connectedAt.Unix(),
		})
	}
	return out
}

func (s *StatusServer) cleanupHighPingPeers(peers []peerDisplayInfo) map[string]struct{} {
	if !s.Config().PeerCleanupEnabled || s.rpc == nil {
		return nil
	}
	minPeers := s.Config().PeerCleanupMinPeers
	if minPeers <= 0 {
		minPeers = 20
	}
	totalPeers := len(peers)
	if totalPeers <= minPeers {
		return nil
	}
	maxPingMs := s.Config().PeerCleanupMaxPingMs
	if maxPingMs <= 0 {
		return nil
	}
	thresholdSec := maxPingMs / 1000
	candidates := make([]peerDisplayInfo, 0, len(peers))
	for _, p := range peers {
		if p.pingSeconds > thresholdSec {
			candidates = append(candidates, p)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].pingSeconds > candidates[j].pingSeconds
	})
	maxDisconnect := totalPeers - minPeers
	if maxDisconnect <= 0 {
		return nil
	}
	removed := make(map[string]struct{})
	disconnects := 0
	for _, candidate := range candidates {
		if disconnects >= maxDisconnect {
			break
		}
		if candidate.rawAddr == "" {
			continue
		}
		if err := s.disconnectPeer(candidate.rawAddr); err != nil {
			logger.Warn("peer cleanup disconnect failed",
				"peer", candidate.rawAddr,
				"error", err,
			)
			continue
		}
		removed[candidate.rawAddr] = struct{}{}
		logger.Info("peer cleanup disconnected high-ping peer",
			"peer", candidate.rawAddr,
			"ping_ms", candidate.pingSeconds*1000,
			"min_peers", minPeers,
		)
		disconnects++
	}
	if len(removed) == 0 {
		return nil
	}
	return removed
}

func (s *StatusServer) disconnectPeer(addr string) error {
	if s.rpc == nil {
		return fmt.Errorf("rpc client not configured")
	}
	return s.rpcCallCtx("disconnectnode", []any{addr}, nil)
}
