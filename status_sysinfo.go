package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bytedance/sonic"
)

func (s *StatusServer) logShareTotals(accepted, rejected uint64) {
	s.lastStatsMu.Lock()
	defer s.lastStatsMu.Unlock()
	if accepted != s.lastAccepted || rejected != s.lastRejected {
		logger.Info("share totals", "accepted", accepted, "rejected", rejected)
		s.lastAccepted = accepted
		s.lastRejected = rejected
	}
}

const (
	nodeInfoTTL          = 30 * time.Second
	nodeInfoRPCTimeout   = 5 * time.Second
	maxNodePeerAddresses = 64
	peerLookupTTL        = 5 * time.Minute
)

// ensureNodeInfo returns the cached node snapshot and schedules a non-blocking
// refresh when the data is stale so HTTP handlers avoid direct RPC calls.
func (s *StatusServer) ensureNodeInfo() cachedNodeInfo {
	s.nodeInfoMu.Lock()
	info := s.nodeInfo
	s.nodeInfoMu.Unlock()

	if info.fetchedAt.IsZero() || time.Since(info.fetchedAt) >= nodeInfoTTL {
		s.scheduleNodeInfoRefresh()
	}
	return info
}

func (s *StatusServer) scheduleNodeInfoRefresh() {
	if s.rpc == nil {
		return
	}
	if !atomic.CompareAndSwapInt32(&s.nodeInfoRefreshing, 0, 1) {
		return
	}
	go func() {
		defer atomic.StoreInt32(&s.nodeInfoRefreshing, 0)
		s.refreshNodeInfo()
	}()
}

// refreshNodeInfo refreshes the cached node information using bounded,
// context-aware RPC calls so shutdown and overload do not block handlers.
func (s *StatusServer) refreshNodeInfo() {
	if s.rpc == nil {
		return
	}
	s.nodeInfoMu.Lock()
	info := s.nodeInfo
	s.nodeInfoMu.Unlock()

	var updated bool

	var bc struct {
		Chain                string  `json:"chain"`
		Blocks               int64   `json:"blocks"`
		Headers              int64   `json:"headers"`
		InitialBlockDownload bool    `json:"initialblockdownload"`
		Pruned               bool    `json:"pruned"`
		SizeOnDisk           float64 `json:"size_on_disk"`
	}
	if err := s.rpcCallCtx("getblockchaininfo", nil, &bc); err == nil {
		chain := strings.ToLower(strings.TrimSpace(bc.Chain))
		switch chain {
		case "main", "mainnet", "":
			info.network = "mainnet"
		case "test", "testnet", "testnet3", "testnet4":
			info.network = "testnet"
		case "signet":
			info.network = "signet"
		case "regtest":
			info.network = "regtest"
		default:
			info.network = bc.Chain
		}
		info.blocks = bc.Blocks
		info.headers = bc.Headers
		info.ibd = bc.InitialBlockDownload
		info.pruned = bc.Pruned
		if bc.SizeOnDisk > 0 {
			info.sizeOnDisk = uint64(bc.SizeOnDisk)
		}
		updated = true
	}

	var netInfo struct {
		Subversion     string `json:"subversion"`
		Connections    int    `json:"connections"`
		ConnectionsIn  int    `json:"connections_in"`
		ConnectionsOut int    `json:"connections_out"`
	}
	if err := s.rpcCallCtx("getnetworkinfo", nil, &netInfo); err == nil {
		info.subversion = strings.TrimSpace(netInfo.Subversion)
		info.conns = netInfo.Connections
		info.connsIn = netInfo.ConnectionsIn
		info.connsOut = netInfo.ConnectionsOut
		updated = true
	}

	var peerList []struct {
		Addr       string  `json:"addr"`
		PingTime   float64 `json:"pingtime"`
		Connection float64 `json:"conntime"`
	}
	if err := s.rpcCallCtx("getpeerinfo", nil, &peerList); err == nil {
		peers := make([]peerDisplayInfo, 0, len(peerList))
		for _, p := range peerList {
			host := stripPeerPort(p.Addr)
			if host == "" {
				continue
			}
			name := s.lookupPeerName(host)
			display := formatPeerDisplay(host, name)
			connAt := time.Unix(int64(p.Connection), 0)
			peers = append(peers, peerDisplayInfo{
				host:        host,
				display:     display,
				pingSeconds: p.PingTime,
				connectedAt: connAt,
				rawAddr:     p.Addr,
			})
		}
		if removed := s.cleanupHighPingPeers(peers); len(removed) > 0 {
			filtered := peers[:0]
			for _, peer := range peers {
				if _, skip := removed[peer.rawAddr]; skip {
					continue
				}
				filtered = append(filtered, peer)
			}
			peers = filtered
		}
		if len(peers) > 1 {
			sort.Slice(peers, func(i, j int) bool {
				return peers[i].connectedAt.Before(peers[j].connectedAt)
			})
		}
		infos := make([]cachedPeerInfo, 0, len(peers))
		for _, p := range peers {
			infos = append(infos, cachedPeerInfo{
				host:        p.host,
				display:     p.display,
				pingSeconds: p.pingSeconds,
				connectedAt: p.connectedAt,
			})
		}
		if len(infos) > maxNodePeerAddresses {
			infos = append([]cachedPeerInfo(nil), infos[:maxNodePeerAddresses]...)
		}
		info.peerInfos = infos
		updated = true
	}

	var genesis string
	if err := s.rpcCallCtx("getblockhash", []interface{}{0}, &genesis); err == nil {
		genesis = strings.TrimSpace(genesis)
		if genesis != "" {
			info.genesisHash = genesis
			updated = true
		}
	}

	var best string
	if err := s.rpcCallCtx("getbestblockhash", nil, &best); err == nil {
		best = strings.TrimSpace(best)
		if best != "" {
			info.bestHash = best
			updated = true
		}
	}

	if !updated {
		return
	}

	info.fetchedAt = time.Now()
	s.nodeInfoMu.Lock()
	s.nodeInfo = info
	s.nodeInfoMu.Unlock()
}

// rpcCallCtx issues a single RPC with a short timeout derived from the
// StatusServer's context so node-info refreshes don't block shutdown.
func (s *StatusServer) rpcCallCtx(method string, params interface{}, out interface{}) error {
	if s == nil || s.rpc == nil {
		return fmt.Errorf("rpc client not configured")
	}
	parent := s.ctx
	if parent == nil {
		parent = context.Background()
	}
	callCtx, cancel := context.WithTimeout(parent, nodeInfoRPCTimeout)
	defer cancel()
	return s.rpc.callCtx(callCtx, method, params, out)
}

// handleNodeInfo renders a simple node accountability page showing which
// Bitcoin node the pool is connected to, its network, and basic sync info.
func (s *StatusServer) handleNodeInfo(w http.ResponseWriter, r *http.Request) {
	if err := s.serveCachedHTML(w, "page_node", func() ([]byte, error) {
		start := time.Now()
		data := s.baseTemplateData(start)
		var buf bytes.Buffer
		if err := s.executeTemplate(&buf, "node", data); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}); err != nil {
		logger.Error("node info template error", "error", err)
		s.renderErrorPage(w, r, http.StatusInternalServerError,
			"Node info error",
			"We couldn't render the node info page.",
			"Template error while rendering the node info view.")
	}
}

// loadFoundBlocks reads the append-only found_blocks.jsonl log and returns up
// to limit most recent entries for display on the status page. It is
// best-effort: parse errors or missing files simply result in an empty slice.
func loadFoundBlocks(dataDir string, limit int) []FoundBlockView {
	if dataDir == "" {
		dataDir = defaultDataDir
	}
	// Use the shared state database connection
	db := getSharedStateDB()
	if db == nil {
		return nil
	}

	type foundRecord struct {
		Timestamp        time.Time `json:"timestamp"`
		Height           int64     `json:"height"`
		Hash             string    `json:"hash"`
		Worker           string    `json:"worker"`
		ShareDiff        float64   `json:"share_diff"`
		PoolFeeSats      int64     `json:"pool_fee_sats"`
		WorkerPayoutSats int64     `json:"worker_payout_sats"`
	}

	var recs []FoundBlockView
	q := "SELECT json FROM found_blocks_log ORDER BY id DESC"
	args := []any{}
	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			continue
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var r foundRecord
		if err := sonic.Unmarshal([]byte(line), &r); err != nil {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(r.Hash), "dummyhash") {
			continue
		}
		recs = append(recs, FoundBlockView{
			Height:           r.Height,
			Hash:             r.Hash,
			DisplayHash:      shortDisplayID(r.Hash, hashPrefix, hashSuffix),
			Worker:           r.Worker,
			DisplayWorker:    shortWorkerName(r.Worker, 12, 6),
			Timestamp:        r.Timestamp,
			ShareDiff:        r.ShareDiff,
			PoolFeeSats:      r.PoolFeeSats,
			WorkerPayoutSats: r.WorkerPayoutSats,
		})
	}
	if err := rows.Err(); err != nil {
		return nil
	}
	if len(recs) == 0 {
		return nil
	}
	sort.Slice(recs, func(i, j int) bool {
		return recs[i].Timestamp.After(recs[j].Timestamp)
	})
	return recs
}

// readProcessRSS returns the current process resident set size (RSS) in bytes.
// It parses /proc/self/statm, which is Linux-specific; on failure it returns 0.
func readProcessRSS() uint64 {
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return 0
	}
	residentPages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	pageSize := uint64(os.Getpagesize())
	return residentPages * pageSize
}

// sampleCPUPercent returns an approximate process CPU usage percentage by
// taking a ratio of deltas between /proc/self/stat (process ticks) and
// /proc/stat (system ticks) across calls. It is Linux-specific and best-effort.
func (s *StatusServer) sampleCPUPercent() float64 {
	procTicks, err1 := readProcessCPUTicks()
	totalTicks, err2 := readSystemCPUTicks()
	if err1 != nil || err2 != nil {
		return s.lastCPUUsage
	}

	s.cpuMu.Lock()
	defer s.cpuMu.Unlock()

	if s.lastCPUTotal != 0 && totalTicks > s.lastCPUTotal && procTicks >= s.lastCPUProc {
		dProc := procTicks - s.lastCPUProc
		dTotal := totalTicks - s.lastCPUTotal
		if dTotal > 0 {
			s.lastCPUUsage = (float64(dProc) / float64(dTotal)) * 100.0
		}
	}
	s.lastCPUProc = procTicks
	s.lastCPUTotal = totalTicks
	return s.lastCPUUsage
}

func readProcessCPUTicks() (uint64, error) {
	data, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0, err
	}
	line := string(data)
	// /proc/self/stat has the form: pid (comm) state ... utime stime ...
	// Find the closing ')' and split from there so spaces in comm don't break parsing.
	idx := strings.LastIndex(line, ")")
	if idx == -1 || idx+2 >= len(line) {
		return 0, fmt.Errorf("invalid /proc/self/stat format")
	}
	rest := line[idx+2:]
	fields := strings.Fields(rest)
	// After ") ", fields start at position 3 (state). utime is field 14, stime is field 15,
	// so within "rest" they are indices 11 and 12.
	if len(fields) < 13 {
		return 0, fmt.Errorf("short /proc/self/stat")
	}
	utime, err := strconv.ParseUint(fields[11], 10, 64)
	if err != nil {
		return 0, err
	}
	stime, err := strconv.ParseUint(fields[12], 10, 64)
	if err != nil {
		return 0, err
	}
	return utime + stime, nil
}

func readSystemCPUTicks() (uint64, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, err
	}
	defer f.Close()

	buf := make([]byte, 4096)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		return 0, err
	}
	lines := strings.Split(string(buf[:n]), "\n")
	if len(lines) == 0 {
		return 0, fmt.Errorf("empty /proc/stat")
	}
	fields := strings.Fields(lines[0])
	if len(fields) < 2 || fields[0] != "cpu" {
		return 0, fmt.Errorf("invalid /proc/stat cpu line")
	}
	var total uint64
	for _, f := range fields[1:] {
		v, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			continue
		}
		total += v
	}
	return total, nil
}

// readSystemMemory parses /proc/meminfo and returns total and "available"
// memory in bytes. On error it returns zero values.
func readSystemMemory() (total, free uint64) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	var (
		memTotalKB     uint64
		memAvailableKB uint64
		memFreeKB      uint64
	)
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		val, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch key {
		case "MemTotal":
			memTotalKB = val
		case "MemAvailable":
			memAvailableKB = val
		case "MemFree":
			memFreeKB = val
		}
	}
	if memTotalKB == 0 {
		return 0, 0
	}
	total = memTotalKB * 1024
	// Prefer MemAvailable when present; fall back to MemFree.
	if memAvailableKB > 0 {
		free = memAvailableKB * 1024
	} else {
		free = memFreeKB * 1024
	}
	return total, free
}

// readLoadAverages parses /proc/loadavg and returns the 1/5/15 minute load
// averages. On error it returns zeros.
func readLoadAverages() (float64, float64, float64) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, 0
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return 0, 0, 0
	}
	parse := func(s string) float64 {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0
		}
		return v
	}
	return parse(fields[0]), parse(fields[1]), parse(fields[2])
}

// renderErrorPage renders a generic error page for HTML endpoints using the
// shared layout and styling. It falls back to http.Error if rendering fails.
func (s *StatusServer) renderErrorPage(w http.ResponseWriter, r *http.Request, statusCode int, title, message, detail string) {
	start := time.Now()
	base := s.baseTemplateData(start)
	data := ErrorPageData{
		StatusData: base,
		StatusCode: statusCode,
		Title:      title,
		Message:    message,
		Detail:     detail,
		Path:       r.URL.Path,
	}
	setShortHTMLCacheHeaders(w, true)
	w.WriteHeader(statusCode)
	if err := s.executeTemplate(w, "error", data); err != nil {
		logger.Error("error page template error", "error", err)
		http.Error(w, message, statusCode)
	}
}

// handleStaticFile returns an http.HandlerFunc that serves a static HTML file
// from the www directory. This is used for legal pages like privacy.html and terms.html.
func (s *StatusServer) handleStaticFile(filename string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		setShortHTMLCacheHeaders(w, false)
		if s != nil && s.staticFiles != nil {
			cleanPath := filepath.Clean(filename)
			if s.staticFiles.ServeCached(w, r, cleanPath) {
				return
			}
		}
		cfg := s.Config()
		wwwDir := filepath.Join(cfg.DataDir, "www")
		filePath := filepath.Join(wwwDir, filename)

		// Security check: ensure the resolved path is within wwwDir
		cleanPath := filepath.Clean(filePath)
		wwwDirClean := filepath.Clean(wwwDir)
		if !strings.HasPrefix(cleanPath, wwwDirClean) {
			http.Error(w, "Invalid file path", http.StatusBadRequest)
			return
		}

		// Check if file exists
		info, err := os.Stat(cleanPath)
		if err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}

		// Serve the file
		http.ServeFile(w, r, cleanPath)
	}
}

func (s *StatusServer) Ready() bool {
	if s.jobMgr == nil || !s.jobMgr.Ready() {
		return false
	}
	if s.accounting != nil && !s.accounting.Ready() {
		return false
	}
	return true
}
