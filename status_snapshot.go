package main

import (
	"context"
	"encoding/hex"
	stdjson "encoding/json"
	"fmt"
	"html/template"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bytedance/sonic"
)

func shareRatePerMinute(stats MinerStats, now time.Time) float64 {
	if stats.WindowStart.IsZero() {
		return 0
	}
	window := now.Sub(stats.WindowStart)
	if window <= 0 {
		return 0
	}
	return float64(stats.WindowAccepted) / window.Minutes()
}

func workerViewFromConn(mc *MinerConn, now time.Time) WorkerView {
	snap := mc.snapshotShareInfo()
	stats := snap.Stats
	name := stats.Worker
	if name == "" {
		name = mc.id
	}
	displayName := shortWorkerName(name, workerNamePrefix, workerNameSuffix)
	workerHash := strings.TrimSpace(stats.WorkerSHA256)
	hashRate := snap.RollingHashrate
	accRate := shareRatePerMinute(stats, now)
	diff := mc.currentDifficulty()
	addr, script, valid := mc.workerWalletData(stats.Worker)
	scriptHex := ""
	if len(script) > 0 {
		scriptHex = strings.ToLower(hex.EncodeToString(script))
	}
	lastShareHash := snap.LastShareHash
	displayHash := ""
	if lastShareHash != "" {
		displayHash = shortDisplayID(lastShareHash, hashPrefix, hashSuffix)
	}
	vardiff := mc.suggestedVardiff(now, snap)
	banned := mc.isBanned(now)
	until, reason, _ := mc.banDetails()
	minerType, minerName, minerVersion := mc.minerClientInfo()
	return WorkerView{
		Name:                name,
		DisplayName:         displayName,
		WorkerSHA256:        workerHash,
		Accepted:            uint64(stats.Accepted),
		Rejected:            uint64(stats.Rejected),
		BalanceSats:         0,
		WalletAddress:       addr,
		WalletScript:        scriptHex,
		MinerType:           minerType,
		MinerName:           minerName,
		MinerVersion:        minerVersion,
		LastShare:           stats.LastShare,
		LastShareHash:       lastShareHash,
		DisplayLastShare:    displayHash,
		LastShareAccepted:   snap.LastShareAccepted,
		LastShareDifficulty: snap.LastShareDifficulty,
		LastShareDetail:     snap.LastShareDetail,
		Difficulty:          diff,
		Vardiff:             vardiff,
		RollingHashrate:     hashRate,
		LastReject:          snap.LastReject,
		Banned:              banned,
		BannedUntil:         until,
		BanReason:           reason,
		WindowStart:         stats.WindowStart,
		WindowAccepted:      stats.WindowAccepted,
		WindowSubmissions:   stats.WindowSubmissions,
		ShareRate:           accRate,
		ConnectionID:        mc.connectionIDString(),
		ConnectionSeq:       atomic.LoadUint64(&mc.connectionSeq),
		ConnectedAt:         mc.connectedAt,
		WalletValidated:     valid,
	}
}

func (s *StatusServer) snapshotWorkerViews(now time.Time) []WorkerView {
	if s.registry == nil {
		return nil
	}
	conns := s.registry.Snapshot()
	views := make([]WorkerView, 0, len(conns))
	for _, mc := range conns {
		views = append(views, workerViewFromConn(mc, now))
	}
	views = mergeWorkerViewsByHash(views)
	sort.Slice(views, func(i, j int) bool {
		return views[i].LastShare.After(views[j].LastShare)
	})
	return views
}

func mergeWorkerViewsByHash(views []WorkerView) []WorkerView {
	if len(views) <= 1 {
		return views
	}
	merged := make(map[string]WorkerView, len(views))
	order := make([]string, 0, len(views))
	for _, w := range views {
		key := w.WorkerSHA256
		if key == "" {
			key = "conn:" + w.ConnectionID
		}
		current, exists := merged[key]
		if !exists {
			merged[key] = w
			order = append(order, key)
			continue
		}
		current.Accepted += w.Accepted
		current.Rejected += w.Rejected
		current.BalanceSats += w.BalanceSats
		current.RollingHashrate += w.RollingHashrate
		current.WindowAccepted += w.WindowAccepted
		current.WindowSubmissions += w.WindowSubmissions
		current.ShareRate += w.ShareRate
		if w.LastShare.After(current.LastShare) {
			current.LastShare = w.LastShare
			current.LastShareHash = w.LastShareHash
			current.DisplayLastShare = w.DisplayLastShare
			current.LastShareAccepted = w.LastShareAccepted
			current.LastShareDifficulty = w.LastShareDifficulty
			current.LastShareDetail = w.LastShareDetail
			current.LastReject = w.LastReject
			current.Difficulty = w.Difficulty
			current.Vardiff = w.Vardiff
		}
		if w.Banned {
			current.Banned = true
			if w.BannedUntil.After(current.BannedUntil) {
				current.BannedUntil = w.BannedUntil
				current.BanReason = w.BanReason
			}
		}
		if current.ConnectedAt.IsZero() || (!w.ConnectedAt.IsZero() && w.ConnectedAt.Before(current.ConnectedAt)) {
			current.ConnectedAt = w.ConnectedAt
		}
		if w.ConnectionSeq > current.ConnectionSeq {
			current.ConnectionSeq = w.ConnectionSeq
		}
		merged[key] = current
	}
	out := make([]WorkerView, 0, len(order))
	for _, key := range order {
		out = append(out, merged[key])
	}
	return out
}

func (s *StatusServer) computePoolHashrate() float64 {
	if s.metrics != nil {
		return s.metrics.PoolHashrate()
	}
	if s.registry == nil {
		return 0
	}
	var total float64
	for _, mc := range s.registry.Snapshot() {
		snap := mc.snapshotShareInfo()
		if snap.RollingHashrate > 0 {
			total += snap.RollingHashrate
		}
	}
	return total
}

func (s *StatusServer) findWorkerViewByHash(hash string) (WorkerView, bool) {
	if hash == "" {
		return WorkerView{}, false
	}
	data := s.statusDataView()
	lookup := workerLookupFromStatusData(data)
	if lookup == nil {
		return WorkerView{}, false
	}
	if w, ok := lookup[hash]; ok {
		return w, true
	}
	return WorkerView{}, false
}

// findAllWorkerViewsByHash returns all individual worker views for a given hash (unmerged).
// This is useful for showing all connections for the same worker separately.
func (s *StatusServer) findAllWorkerViewsByHash(hash string, now time.Time) []WorkerView {
	if hash == "" || s.workerRegistry == nil {
		return nil
	}

	// Use the efficient lookup to get only connections for this worker
	conns := s.workerRegistry.getConnectionsByHash(hash)
	if len(conns) == 0 {
		return nil
	}

	views := make([]WorkerView, 0, len(conns))
	for _, mc := range conns {
		views = append(views, workerViewFromConn(mc, now))
	}

	return views
}

func formatHashrateValue(h float64) string {
	units := []string{"H/s", "KH/s", "MH/s", "GH/s", "TH/s", "PH/s"}
	unit := units[0]
	val := h
	for i := 0; i < len(units)-1 && val >= 1000; i++ {
		val /= 1000
		unit = units[i+1]
	}
	return fmt.Sprintf("%.3f %s", val, unit)
}

// buildTemplateFuncs returns the template.FuncMap used for all HTML templates.
func buildTemplateFuncs() template.FuncMap {
	return template.FuncMap{
		"humanDuration": func(d time.Duration) string {
			if d < 0 {
				return "0s"
			}
			return d.Round(time.Second).String()
		},
		"shortID": func(s string) string {
			// Shorten IDs / hashes to a stable, display-safe form.
			return shortDisplayID(s, hashPrefix, hashSuffix)
		},
		"join": func(ss []string, sep string) string {
			return strings.Join(ss, sep)
		},
		"formatHashrate": formatHashrateValue,
		"formatDiff": func(d float64) string {
			if d <= 0 {
				return "0"
			}
			if d < 1 {
				// Display small difficulties as decimals (e.g. 0.5) instead of rounding to 0.
				//
				// We intentionally truncate instead of round so values slightly below 1 don't
				// display as "1" due to formatting.
				prec := int(math.Ceil(-math.Log10(d))) + 2
				if prec < 3 {
					prec = 3
				}
				if prec > 8 {
					prec = 8
				}
				scale := math.Pow10(prec)
				trunc := math.Trunc(d*scale) / scale
				s := strconv.FormatFloat(trunc, 'f', prec, 64)
				s = strings.TrimRight(s, "0")
				s = strings.TrimRight(s, ".")
				if s == "" || s == "0" {
					// Extremely small values may truncate to 0 at our precision cap.
					return strconv.FormatFloat(d, 'g', 3, 64)
				}
				return s
			}
			if d < 1_000_000 {
				return fmt.Sprintf("%.0f", math.Round(d))
			}
			switch {
			case d >= 1_000_000_000_000:
				return fmt.Sprintf("%.1fP", d/1_000_000_000_000.0)
			case d >= 1_000_000_000:
				return fmt.Sprintf("%.1fG", d/1_000_000_000.0)
			default:
				return fmt.Sprintf("%.1fM", d/1_000_000.0)
			}
		},
		"formatTime": func(t time.Time) string {
			if t.IsZero() {
				return "—"
			}
			s := humanShortDuration(time.Since(t))
			if s == "just now" {
				return "Just now"
			}
			return s + " ago"
		},
		"formatTimeUTC": func(t time.Time) string {
			if t.IsZero() {
				return "—"
			}
			return t.UTC().Format("2006-01-02 15:04:05 UTC")
		},
		"addrPort": func(addr string) string {
			if addr == "" {
				return "—"
			}
			_, port, err := net.SplitHostPort(addr)
			if err != nil {
				return addr
			}
			return port
		},
		"formatBytes": func(b uint64) string {
			const unit = 1024.0
			if b == 0 {
				return "0 B"
			}
			val := float64(b)
			units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
			u := units[0]
			for i := 0; i < len(units)-1 && val >= unit; i++ {
				val /= unit
				u = units[i+1]
			}
			return fmt.Sprintf("%.2f %s", val, u)
		},
		"formatShareRate": func(r float64) string {
			if r < 0 {
				r = 0
			}
			units := []string{"", "K", "M", "G"}
			val := r
			unit := units[0]
			for i := 0; i < len(units)-1 && val >= 1000; i++ {
				val /= 1000
				unit = units[i+1]
			}
			if unit == "" {
				return fmt.Sprintf("%.2f", val)
			}
			return fmt.Sprintf("%.2f %s", val, unit)
		},
		"formatBTC": func(sats int64) string {
			if sats == 0 {
				return "0 BTC"
			}
			btc := float64(sats) / 1e8
			return fmt.Sprintf("%.8f BTC", btc)
		},
		"formatBTCShort": func(sats int64) string {
			btc := float64(sats) / 1e8
			return fmt.Sprintf("%.8f BTC", btc)
		},
		"formatFiat": func(sats int64, price float64, currency string) string {
			if sats == 0 || price <= 0 {
				return ""
			}
			btc := float64(sats) / 1e8
			amt := btc * price
			cur := strings.ToUpper(strings.TrimSpace(currency))
			if cur == "" {
				cur = "USD"
			}
			return fmt.Sprintf("≈ %.2f %s", amt, cur)
		},
		"formatRenderDuration": func(d time.Duration) string {
			if d <= 0 {
				return "0s"
			}
			if d < time.Millisecond {
				return "<1ms"
			}
			ms := float64(d) / float64(time.Millisecond)
			return fmt.Sprintf("%.0fms", ms)
		},
	}
}

// loadTemplates loads and parses all HTML templates from the specified data directory.
// It returns a fully configured template or an error if any template fails to load or parse.
func loadTemplates(dataDir string) (*template.Template, error) {
	funcs := buildTemplateFuncs()

	// Build template paths
	layoutPath := filepath.Join(dataDir, "templates", "layout.tmpl")
	statusPath := filepath.Join(dataDir, "templates", "overview.tmpl")
	statusBoxesPath := filepath.Join(dataDir, "templates", "status_boxes.tmpl")
	hashrateGraphPath := filepath.Join(dataDir, "templates", "hashrate_graph.tmpl")
	hashrateGraphScriptPath := filepath.Join(dataDir, "templates", "hashrate_graph_script.tmpl")
	serverInfoPath := filepath.Join(dataDir, "templates", "server.tmpl")
	workerLoginPath := filepath.Join(dataDir, "templates", "worker_login.tmpl")
	signInPath := filepath.Join(dataDir, "templates", "sign_in.tmpl")
	savedWorkersPath := filepath.Join(dataDir, "templates", "saved_workers.tmpl")
	workerStatusPath := filepath.Join(dataDir, "templates", "worker_status.tmpl")
	workerWalletSearchPath := filepath.Join(dataDir, "templates", "worker_wallet_search.tmpl")
	nodeInfoPath := filepath.Join(dataDir, "templates", "node.tmpl")
	poolInfoPath := filepath.Join(dataDir, "templates", "pool.tmpl")
	aboutPath := filepath.Join(dataDir, "templates", "about.tmpl")
	helpPath := filepath.Join(dataDir, "templates", "help.tmpl")
	adminPath := filepath.Join(dataDir, "templates", "admin.tmpl")
	adminMinersPath := filepath.Join(dataDir, "templates", "admin_miners.tmpl")
	adminLoginsPath := filepath.Join(dataDir, "templates", "admin_logins.tmpl")
	adminBansPath := filepath.Join(dataDir, "templates", "admin_bans.tmpl")
	errorPath := filepath.Join(dataDir, "templates", "error.tmpl")

	// Load template files
	layoutHTML, err := os.ReadFile(layoutPath)
	if err != nil {
		return nil, fmt.Errorf("load layout template: %w", err)
	}
	statusHTML, err := os.ReadFile(statusPath)
	if err != nil {
		return nil, fmt.Errorf("load status template: %w", err)
	}
	statusBoxesHTML, err := os.ReadFile(statusBoxesPath)
	if err != nil {
		return nil, fmt.Errorf("load status boxes template: %w", err)
	}
	hashrateGraphHTML, err := os.ReadFile(hashrateGraphPath)
	if err != nil {
		return nil, fmt.Errorf("load hashrate graph template: %w", err)
	}
	hashrateGraphScriptHTML, err := os.ReadFile(hashrateGraphScriptPath)
	if err != nil {
		return nil, fmt.Errorf("load hashrate graph script template: %w", err)
	}
	serverInfoHTML, err := os.ReadFile(serverInfoPath)
	if err != nil {
		return nil, fmt.Errorf("load server info template: %w", err)
	}
	workerLoginHTML, err := os.ReadFile(workerLoginPath)
	if err != nil {
		return nil, fmt.Errorf("load worker login template: %w", err)
	}
	signInHTML, err := os.ReadFile(signInPath)
	if err != nil {
		return nil, fmt.Errorf("load sign in template: %w", err)
	}
	savedWorkersHTML, err := os.ReadFile(savedWorkersPath)
	if err != nil {
		return nil, fmt.Errorf("load saved workers template: %w", err)
	}
	workerStatusHTML, err := os.ReadFile(workerStatusPath)
	if err != nil {
		return nil, fmt.Errorf("load worker status template: %w", err)
	}
	workerWalletSearchHTML, err := os.ReadFile(workerWalletSearchPath)
	if err != nil {
		return nil, fmt.Errorf("load worker wallet search template: %w", err)
	}
	nodeInfoHTML, err := os.ReadFile(nodeInfoPath)
	if err != nil {
		return nil, fmt.Errorf("load node info template: %w", err)
	}
	poolInfoHTML, err := os.ReadFile(poolInfoPath)
	if err != nil {
		return nil, fmt.Errorf("load pool info template: %w", err)
	}
	aboutHTML, err := os.ReadFile(aboutPath)
	if err != nil {
		return nil, fmt.Errorf("load about template: %w", err)
	}
	helpHTML, err := os.ReadFile(helpPath)
	if err != nil {
		return nil, fmt.Errorf("load help template: %w", err)
	}
	adminHTML, err := os.ReadFile(adminPath)
	if err != nil {
		return nil, fmt.Errorf("load admin template: %w", err)
	}
	adminMinersHTML, err := os.ReadFile(adminMinersPath)
	if err != nil {
		return nil, fmt.Errorf("load admin miners template: %w", err)
	}
	adminLoginsHTML, err := os.ReadFile(adminLoginsPath)
	if err != nil {
		return nil, fmt.Errorf("load admin logins template: %w", err)
	}
	adminBansHTML, err := os.ReadFile(adminBansPath)
	if err != nil {
		return nil, fmt.Errorf("load admin bans template: %w", err)
	}
	errorHTML, err := os.ReadFile(errorPath)
	if err != nil {
		return nil, fmt.Errorf("load error template: %w", err)
	}

	// Parse templates
	tmpl := template.New("layout").Funcs(funcs)
	if _, err := tmpl.Parse(string(layoutHTML)); err != nil {
		return nil, fmt.Errorf("parse layout template: %w", err)
	}
	if _, err := tmpl.New("overview").Parse(string(statusHTML)); err != nil {
		return nil, fmt.Errorf("parse status template: %w", err)
	}
	if _, err := tmpl.New("status_boxes").Parse(string(statusBoxesHTML)); err != nil {
		return nil, fmt.Errorf("parse status boxes template: %w", err)
	}
	if _, err := tmpl.New("hashrate_graph").Parse(string(hashrateGraphHTML)); err != nil {
		return nil, fmt.Errorf("parse hashrate graph template: %w", err)
	}
	if _, err := tmpl.New("hashrate_graph_script").Parse(string(hashrateGraphScriptHTML)); err != nil {
		return nil, fmt.Errorf("parse hashrate graph script template: %w", err)
	}
	if _, err := tmpl.New("server").Parse(string(serverInfoHTML)); err != nil {
		return nil, fmt.Errorf("parse server info template: %w", err)
	}
	if _, err := tmpl.New("worker_login").Parse(string(workerLoginHTML)); err != nil {
		return nil, fmt.Errorf("parse worker login template: %w", err)
	}
	if _, err := tmpl.New("sign_in").Parse(string(signInHTML)); err != nil {
		return nil, fmt.Errorf("parse sign in template: %w", err)
	}
	if _, err := tmpl.New("saved_workers").Parse(string(savedWorkersHTML)); err != nil {
		return nil, fmt.Errorf("parse saved workers template: %w", err)
	}
	if _, err := tmpl.New("worker_status").Parse(string(workerStatusHTML)); err != nil {
		return nil, fmt.Errorf("parse worker status template: %w", err)
	}
	if _, err := tmpl.New("worker_wallet_search").Parse(string(workerWalletSearchHTML)); err != nil {
		return nil, fmt.Errorf("parse worker wallet search template: %w", err)
	}
	if _, err := tmpl.New("node").Parse(string(nodeInfoHTML)); err != nil {
		return nil, fmt.Errorf("parse node info template: %w", err)
	}
	if _, err := tmpl.New("pool").Parse(string(poolInfoHTML)); err != nil {
		return nil, fmt.Errorf("parse pool template: %w", err)
	}
	if _, err := tmpl.New("about").Parse(string(aboutHTML)); err != nil {
		return nil, fmt.Errorf("parse about template: %w", err)
	}
	if _, err := tmpl.New("help").Parse(string(helpHTML)); err != nil {
		return nil, fmt.Errorf("parse help template: %w", err)
	}
	if _, err := tmpl.New("admin").Parse(string(adminHTML)); err != nil {
		return nil, fmt.Errorf("parse admin template: %w", err)
	}
	if _, err := tmpl.New("admin_miners").Parse(string(adminMinersHTML)); err != nil {
		return nil, fmt.Errorf("parse admin miners template: %w", err)
	}
	if _, err := tmpl.New("admin_logins").Parse(string(adminLoginsHTML)); err != nil {
		return nil, fmt.Errorf("parse admin logins template: %w", err)
	}
	if _, err := tmpl.New("admin_bans").Parse(string(adminBansHTML)); err != nil {
		return nil, fmt.Errorf("parse admin bans template: %w", err)
	}
	if _, err := tmpl.New("error").Parse(string(errorHTML)); err != nil {
		return nil, fmt.Errorf("parse error template: %w", err)
	}

	return tmpl, nil
}

func NewStatusServer(ctx context.Context, jobMgr *JobManager, metrics *PoolMetrics, registry *MinerRegistry, workerRegistry *workerConnectionRegistry, accounting *AccountStore, rpc *RPCClient, cfg Config, start time.Time, clerk *ClerkVerifier, workerLists *workerListStore, configPath, adminConfigPath, tuningPath string, shutdown func()) *StatusServer {
	// Load HTML templates from data_dir/templates so operators can customize the
	// UI without recompiling. These are treated as required assets.
	tmpl, err := loadTemplates(cfg.DataDir)
	if err != nil {
		fatal("load templates", err)
	}

	if ctx == nil {
		ctx = context.Background()
	}

	server := &StatusServer{
		tmpl:                tmpl,
		jobMgr:              jobMgr,
		metrics:             metrics,
		registry:            registry,
		workerRegistry:      workerRegistry,
		accounting:          accounting,
		rpc:                 rpc,
		ctx:                 ctx,
		start:               start,
		clerk:               clerk,
		workerLookupLimiter: newWorkerLookupRateLimiter(workerLookupRateLimitMax, workerLookupRateLimitWindow),
		workerLists:         workerLists,
		priceSvc:            NewPriceService(),
		jsonCache:           make(map[string]cachedJSONResponse),
		configPath:          configPath,
		adminConfigPath:     adminConfigPath,
		tuningPath:          tuningPath,
		adminSessions:       make(map[string]time.Time),
		requestShutdown:     shutdown,
	}
	server.UpdateConfig(cfg)
	server.scheduleNodeInfoRefresh()
	return server
}

func (s *StatusServer) executeTemplate(w io.Writer, name string, data any) error {
	if s == nil {
		return fmt.Errorf("status server is nil")
	}
	s.tmplMu.RLock()
	tmpl := s.tmpl
	s.tmplMu.RUnlock()
	if tmpl == nil {
		return fmt.Errorf("templates not initialized")
	}
	return tmpl.ExecuteTemplate(w, name, data)
}

// ReloadTemplates reloads all HTML templates from disk. This allows operators
// to update templates without restarting the pool server. It's designed to be
// called in response to SIGUSR1 or other reload triggers.
func (s *StatusServer) ReloadTemplates() error {
	if s == nil {
		return fmt.Errorf("status server is nil")
	}

	tmpl, err := loadTemplates(s.Config().DataDir)
	if err != nil {
		return err
	}

	// Atomically replace the template
	s.tmplMu.Lock()
	s.tmpl = tmpl
	s.tmplMu.Unlock()
	logger.Info("templates reloaded successfully")
	return nil
}

// handleRPCResult is registered as an RPCClient result hook to opportunistically
// warm cached node info based on normal RPC traffic. It never changes how
// callers use the RPC client; it only updates StatusServer's own cache.
func (s *StatusServer) handleRPCResult(method string, params interface{}, raw stdjson.RawMessage) {
	if s == nil {
		return
	}

	switch method {
	case "getblockchaininfo":
		var bc struct {
			Chain                string  `json:"chain"`
			Blocks               int64   `json:"blocks"`
			Headers              int64   `json:"headers"`
			InitialBlockDownload bool    `json:"initialblockdownload"`
			Pruned               bool    `json:"pruned"`
			SizeOnDisk           float64 `json:"size_on_disk"`
		}
		if err := sonic.Unmarshal(raw, &bc); err != nil {
			return
		}
		s.nodeInfoMu.Lock()
		defer s.nodeInfoMu.Unlock()
		now := time.Now()
		if s.nodeInfo.fetchedAt.IsZero() || now.Sub(s.nodeInfo.fetchedAt) >= nodeInfoTTL {
			var info cachedNodeInfo = s.nodeInfo
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
			info.fetchedAt = now
			s.nodeInfo = info
		}
	case "getnetworkinfo":
		var netInfo struct {
			Subversion     string `json:"subversion"`
			Connections    int    `json:"connections"`
			ConnectionsIn  int    `json:"connections_in"`
			ConnectionsOut int    `json:"connections_out"`
		}
		if err := sonic.Unmarshal(raw, &netInfo); err != nil {
			return
		}
		s.nodeInfoMu.Lock()
		defer s.nodeInfoMu.Unlock()
		now := time.Now()
		if s.nodeInfo.fetchedAt.IsZero() || now.Sub(s.nodeInfo.fetchedAt) >= nodeInfoTTL {
			var info cachedNodeInfo = s.nodeInfo
			info.subversion = strings.TrimSpace(netInfo.Subversion)
			info.conns = netInfo.Connections
			info.connsIn = netInfo.ConnectionsIn
			info.connsOut = netInfo.ConnectionsOut
			info.fetchedAt = now
			s.nodeInfo = info
		}
	case "getblockhash":
		// Only care about genesis hash (height 0) to avoid polluting cache
		// with unrelated getblockhash calls.
		args, ok := params.([]interface{})
		if !ok || len(args) != 1 {
			return
		}
		h, ok := args[0].(float64)
		if !ok || int64(h) != 0 {
			return
		}
		var genesis string
		if err := sonic.Unmarshal(raw, &genesis); err != nil {
			return
		}
		genesis = strings.TrimSpace(genesis)
		if genesis == "" {
			return
		}
		s.nodeInfoMu.Lock()
		if s.nodeInfo.genesisHash == "" {
			s.nodeInfo.genesisHash = genesis
		}
		s.nodeInfoMu.Unlock()
	case "getbestblockhash":
		var best string
		if err := sonic.Unmarshal(raw, &best); err != nil {
			return
		}
		best = strings.TrimSpace(best)
		if best == "" {
			return
		}
		s.nodeInfoMu.Lock()
		s.nodeInfo.bestHash = best
		s.nodeInfoMu.Unlock()
	}
}

// SetJobManager attaches a JobManager after the status server has started.
