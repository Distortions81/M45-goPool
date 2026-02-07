package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pelletier/go-toml"
)

func adminPaginationFromRequest(r *http.Request) (int, int) {
	page := 1
	perPage := defaultAdminPerPage
	if r == nil {
		return page, perPage
	}
	query := r.URL.Query()
	if v := strings.TrimSpace(query.Get("page")); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 {
			page = p
		}
	}
	if v := strings.TrimSpace(query.Get("per_page")); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			if p < 1 {
				p = defaultAdminPerPage
			}
			if p > maxAdminPerPage {
				p = maxAdminPerPage
			}
			perPage = p
		}
	}
	return page, perPage
}

func paginateAdminSlice[T any](items []T, page, perPage int) ([]T, AdminPagination) {
	total := len(items)
	if perPage <= 0 {
		perPage = defaultAdminPerPage
	}
	totalPages := (total + perPage - 1) / perPage
	if totalPages == 0 {
		totalPages = 1
	}
	if page < 1 {
		page = 1
	}
	if page > totalPages {
		page = totalPages
	}
	start := (page - 1) * perPage
	if start > total {
		start = total
	}
	end := start + perPage
	if end > total {
		end = total
	}
	var paged []T
	if end > start {
		paged = items[start:end]
	}
	pagination := AdminPagination{
		Page:        page,
		PerPage:     perPage,
		TotalItems:  total,
		TotalPages:  totalPages,
		RangeStart:  0,
		RangeEnd:    end,
		HasPrevPage: page > 1,
		HasNextPage: end < total,
		PrevPage:    page - 1,
		NextPage:    page + 1,
	}
	if total > 0 {
		pagination.RangeStart = start + 1
	}
	return paged, pagination
}

func (s *StatusServer) buildAdminPageData(r *http.Request, noticeKey string) (AdminPageData, adminFileConfig, error) {
	start := time.Now()
	data := AdminPageData{
		StatusData:          s.baseTemplateData(start),
		AdminConfigPath:     s.adminConfigPath,
		AdminNotice:         adminNoticeMessage(noticeKey),
		AdminPerPageOptions: adminPerPageOptions,
	}
	cfg, err := loadAdminConfigFile(s.adminConfigPath)
	if err != nil {
		logger.Warn("load admin config failed", "error", err, "path", s.adminConfigPath)
		data.AdminEnabled = false
		data.AdminApplyError = fmt.Sprintf("Failed to read admin config: %v", err)
		return data, cfg, err
	}
	data.AdminEnabled = cfg.Enabled
	data.LoggedIn = s.isAdminAuthenticated(r)
	data.Settings = buildAdminSettingsData(s.Config())
	data.AdminSection = "settings"
	return data, cfg, nil
}

func (s *StatusServer) renderAdminPage(w http.ResponseWriter, r *http.Request, data AdminPageData) {
	s.renderAdminPageTemplate(w, r, data, "admin")
}

func (s *StatusServer) renderAdminPageTemplate(w http.ResponseWriter, r *http.Request, data AdminPageData, templateName string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.executeTemplate(w, templateName, data); err != nil {
		logger.Error("admin template error", "error", err)
		s.renderErrorPage(w, r, http.StatusInternalServerError,
			"Admin panel error",
			"We couldn't render the admin control panel.",
			"Template error while rendering the admin interface.")
	}
}

func adminNoticeMessage(key string) string {
	switch key {
	case "settings_applied":
		return "Live settings applied in memory."
	case "saved_to_disk":
		return "Saved current in-memory settings to config.toml and tuning.toml."
	case "reboot_requested":
		return "Reboot requested. goPool is shutting down now."
	case "logged_in":
		return "Admin session unlocked."
	case "logged_out":
		return "Admin session cleared."
	case "miner_disconnected":
		return "Miner connection disconnected."
	case "miner_banned":
		return "Miner connection banned and closed."
	case "saved_worker_deleted":
		return "Saved worker entry deleted."
	case "saved_worker_banned":
		return "Worker was banned from saved accounts."
	case "bans_removed":
		return "Selected bans were removed."
	default:
		return ""
	}
}

func buildAdminSettingsData(cfg Config) AdminSettingsData {
	timeoutSec := int(cfg.ConnectionTimeout / time.Second)
	if timeoutSec < 0 {
		timeoutSec = 0
	}
	return AdminSettingsData{
		StatusBrandName:                      cfg.StatusBrandName,
		StatusBrandDomain:                    cfg.StatusBrandDomain,
		StatusPublicURL:                      cfg.StatusPublicURL,
		StatusTagline:                        cfg.StatusTagline,
		StatusConnectMinerTitleExtra:         cfg.StatusConnectMinerTitleExtra,
		StatusConnectMinerTitleExtraURL:      cfg.StatusConnectMinerTitleExtraURL,
		FiatCurrency:                         cfg.FiatCurrency,
		GitHubURL:                            cfg.GitHubURL,
		DiscordURL:                           cfg.DiscordURL,
		ServerLocation:                       cfg.ServerLocation,
		MempoolAddressURL:                    cfg.MempoolAddressURL,
		PoolDonationAddress:                  cfg.PoolDonationAddress,
		OperatorDonationName:                 cfg.OperatorDonationName,
		OperatorDonationURL:                  cfg.OperatorDonationURL,
		PayoutAddress:                        cfg.PayoutAddress,
		PoolFeePercent:                       cfg.PoolFeePercent,
		OperatorDonationPercent:              cfg.OperatorDonationPercent,
		PoolEntropy:                          cfg.PoolEntropy,
		PoolTagPrefix:                        cfg.PoolTagPrefix,
		ListenAddr:                           cfg.ListenAddr,
		StatusAddr:                           cfg.StatusAddr,
		StatusTLSAddr:                        cfg.StatusTLSAddr,
		StratumTLSListen:                     cfg.StratumTLSListen,
		MaxConns:                             cfg.MaxConns,
		MaxAcceptsPerSecond:                  cfg.MaxAcceptsPerSecond,
		MaxAcceptBurst:                       cfg.MaxAcceptBurst,
		AutoAcceptRateLimits:                 cfg.AutoAcceptRateLimits,
		AcceptReconnectWindow:                cfg.AcceptReconnectWindow,
		AcceptBurstWindow:                    cfg.AcceptBurstWindow,
		AcceptSteadyStateWindow:              cfg.AcceptSteadyStateWindow,
		AcceptSteadyStateRate:                cfg.AcceptSteadyStateRate,
		AcceptSteadyStateReconnectPercent:    cfg.AcceptSteadyStateReconnectPercent,
		AcceptSteadyStateReconnectWindow:     cfg.AcceptSteadyStateReconnectWindow,
		ConnectionTimeoutSeconds:             timeoutSec,
		MinDifficulty:                        cfg.MinDifficulty,
		MaxDifficulty:                        cfg.MaxDifficulty,
		LockSuggestedDifficulty:              cfg.LockSuggestedDifficulty,
		SoloMode:                             cfg.SoloMode,
		DirectSubmitProcessing:               cfg.DirectSubmitProcessing,
		CheckDuplicateShares:                 cfg.CheckDuplicateShares,
		PeerCleanupEnabled:                   cfg.PeerCleanupEnabled,
		PeerCleanupMaxPingMs:                 cfg.PeerCleanupMaxPingMs,
		PeerCleanupMinPeers:                  cfg.PeerCleanupMinPeers,
		CleanExpiredBansOnStartup:            cfg.CleanExpiredBansOnStartup,
		BanInvalidSubmissionsAfter:           cfg.BanInvalidSubmissionsAfter,
		BanInvalidSubmissionsWindowSeconds:   int(cfg.BanInvalidSubmissionsWindow / time.Second),
		BanInvalidSubmissionsDurationSeconds: int(cfg.BanInvalidSubmissionsDuration / time.Second),
		ReconnectBanThreshold:                cfg.ReconnectBanThreshold,
		ReconnectBanWindowSeconds:            cfg.ReconnectBanWindowSeconds,
		ReconnectBanDurationSeconds:          cfg.ReconnectBanDurationSeconds,
		LogLevel:                             cfg.LogLevel,
		DiscordWorkerNotifyThresholdSeconds:  cfg.DiscordWorkerNotifyThresholdSeconds,
		HashrateEMATauSeconds:                cfg.HashrateEMATauSeconds,
		HashrateEMAMinShares:                 cfg.HashrateEMAMinShares,
		NTimeForwardSlackSeconds:             cfg.NTimeForwardSlackSeconds,
	}
}

func (s *StatusServer) buildAdminMinerRows() []AdminMinerRow {
	if s == nil || s.registry == nil {
		return nil
	}
	now := time.Now()
	conns := s.registry.Snapshot()
	if len(conns) == 0 {
		return nil
	}
	rows := make([]AdminMinerRow, 0, len(conns))
	for _, mc := range conns {
		if mc == nil {
			continue
		}
		seq := atomic.LoadUint64(&mc.connectionSeq)
		listener := "Stratum"
		if mc.isTLSConnection {
			listener = "Stratum TLS"
		}
		stats, acceptRate, submitRate := mc.snapshotStatsWithRates(now)
		snap := mc.snapshotShareInfo()
		until, reason, _ := mc.banDetails()
		rows = append(rows, AdminMinerRow{
			ConnectionSeq:       seq,
			ConnectionLabel:     mc.connectionIDString(),
			RemoteAddr:          mc.id,
			Listener:            listener,
			Worker:              mc.currentWorker(),
			WorkerHash:          workerNameHash(mc.currentWorker()),
			ClientName:          strings.TrimSpace(mc.minerClientName),
			ClientVersion:       strings.TrimSpace(mc.minerClientVersion),
			Difficulty:          atomicLoadFloat64(&mc.difficulty),
			Hashrate:            snap.RollingHashrate,
			AcceptRatePerMinute: acceptRate,
			SubmitRatePerMinute: submitRate,
			Stats:               stats,
			ConnectedAt:         mc.connectedAt,
			LastActivity:        mc.lastActivity,
			LastShare:           stats.LastShare,
			Banned:              mc.isBanned(now),
			BanReason:           reason,
			BanUntil:            until,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].ConnectionSeq < rows[j].ConnectionSeq
	})
	return rows
}

func (s *StatusServer) buildAdminSavedWorkerRows() []AdminSavedWorkerRow {
	rows, _ := s.buildAdminLoginRows()
	return rows
}

func (s *StatusServer) buildAdminLoginRows() ([]AdminSavedWorkerRow, string) {
	if s == nil || s.workerLists == nil {
		return nil, "Saved worker store is not enabled (workers.db not available)."
	}
	savedRecords, err := s.workerLists.ListAllSavedWorkers()
	if err != nil {
		logger.Warn("list saved workers for admin", "error", err)
	}
	loadErr := ""
	if err != nil {
		loadErr = fmt.Sprintf("Failed to list saved workers: %v", err)
	}
	users, err := s.workerLists.ListAllClerkUsers()
	if err != nil {
		logger.Warn("list clerk users for admin", "error", err)
		if loadErr == "" {
			loadErr = fmt.Sprintf("Failed to list clerk users: %v", err)
		} else {
			loadErr = loadErr + fmt.Sprintf(" (also failed to list clerk users: %v)", err)
		}
	}

	rowsByUser := make(map[string]*AdminSavedWorkerRow, len(users))
	for _, u := range users {
		userID := strings.TrimSpace(u.UserID)
		if userID == "" {
			continue
		}
		rowsByUser[userID] = &AdminSavedWorkerRow{
			UserID:    userID,
			FirstSeen: u.FirstSeen,
			LastSeen:  u.LastSeen,
			SeenCount: u.SeenCount,
		}
	}

	for _, record := range savedRecords {
		userID := strings.TrimSpace(record.UserID)
		if userID == "" {
			continue
		}
		row, exists := rowsByUser[userID]
		if !exists {
			row = &AdminSavedWorkerRow{UserID: userID}
			rowsByUser[userID] = row
		}
		row.Workers = append(row.Workers, record.SavedWorkerEntry)
		if record.NotifyEnabled {
			row.NotifyCount++
		}
		if record.Hash != "" {
			row.WorkerHashes = append(row.WorkerHashes, record.Hash)
		}
	}

	rows := make([]AdminSavedWorkerRow, 0, len(rowsByUser))
	for _, row := range rowsByUser {
		if len(row.WorkerHashes) > 0 {
			seenHashes := make(map[string]struct{})
			dedup := row.WorkerHashes[:0]
			for _, h := range row.WorkerHashes {
				if h == "" {
					continue
				}
				lower := strings.ToLower(h)
				if _, ok := seenHashes[lower]; ok {
					continue
				}
				seenHashes[lower] = struct{}{}
				dedup = append(dedup, lower)
			}
			row.WorkerHashes = dedup
		}
		rows = append(rows, *row)
	}

	if s.workerRegistry != nil {
		for i := range rows {
			seen := make(map[uint64]struct{})
			for _, hash := range rows[i].WorkerHashes {
				if hash == "" {
					continue
				}
				conns := s.workerRegistry.getConnectionsByHash(hash)
				for _, mc := range conns {
					if mc == nil {
						continue
					}
					seq := atomic.LoadUint64(&mc.connectionSeq)
					if seq == 0 {
						continue
					}
					if _, duplicate := seen[seq]; duplicate {
						continue
					}
					seen[seq] = struct{}{}
					listener := "Stratum"
					if mc.isTLSConnection {
						listener = "Stratum TLS"
					}
					rows[i].OnlineConnections = append(rows[i].OnlineConnections, AdminMinerConnection{
						ConnectionSeq:   seq,
						ConnectionLabel: mc.connectionIDString(),
						RemoteAddr:      mc.id,
						Listener:        listener,
					})
				}
			}
		}
	}

	sort.Slice(rows, func(i, j int) bool {
		a := rows[i].LastSeen
		b := rows[j].LastSeen
		switch {
		case a.IsZero() && b.IsZero():
			return rows[i].UserID < rows[j].UserID
		case a.IsZero():
			return false
		case b.IsZero():
			return true
		case !a.Equal(b):
			return a.After(b)
		default:
			return rows[i].UserID < rows[j].UserID
		}
	})
	return rows, loadErr
}

func (s *StatusServer) buildAdminBannedWorkers() ([]WorkerView, string) {
	if s == nil || s.accounting == nil {
		return nil, "Accounting store is not available."
	}
	if !s.accounting.Ready() {
		return nil, "Accounting store is still initializing."
	}
	workers := s.accounting.WorkersSnapshot()
	if len(workers) == 0 {
		return nil, ""
	}
	sort.Slice(workers, func(i, j int) bool {
		a := workers[i]
		b := workers[j]
		if a.BannedUntil.IsZero() != b.BannedUntil.IsZero() {
			return b.BannedUntil.IsZero()
		}
		if !a.BannedUntil.Equal(b.BannedUntil) {
			if a.BannedUntil.IsZero() {
				return false
			}
			if b.BannedUntil.IsZero() {
				return true
			}
			return a.BannedUntil.Before(b.BannedUntil)
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	return workers, ""
}

func applyAdminSettingsForm(cfg *Config, r *http.Request) error {
	if cfg == nil || r == nil {
		return fmt.Errorf("missing request/config")
	}

	orig := *cfg
	next := orig

	getTrim := func(key string) string { return strings.TrimSpace(r.FormValue(key)) }
	getBool := func(key string) bool { return strings.TrimSpace(r.FormValue(key)) != "" }
	fieldProvided := func(key string) bool {
		_, ok := r.Form[key]
		return ok
	}

	parseInt := func(key string, current int) (int, error) {
		raw := getTrim(key)
		if raw == "" {
			return current, nil
		}
		v, err := strconv.Atoi(raw)
		if err != nil {
			return current, fmt.Errorf("%s must be an integer", key)
		}
		return v, nil
	}

	parseFloat := func(key string, current float64) (float64, error) {
		raw := getTrim(key)
		if raw == "" {
			return current, nil
		}
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return current, fmt.Errorf("%s must be a number", key)
		}
		return v, nil
	}

	normalizeListen := func(s string) string {
		s = strings.TrimSpace(s)
		if s == "" {
			return s
		}
		if !strings.Contains(s, ":") {
			return ":" + s
		}
		return s
	}

	next.StatusBrandName = orig.StatusBrandName
	if fieldProvided("status_brand_name") {
		next.StatusBrandName = getTrim("status_brand_name")
	}
	next.StatusBrandDomain = orig.StatusBrandDomain
	if fieldProvided("status_brand_domain") {
		next.StatusBrandDomain = getTrim("status_brand_domain")
	}
	next.StatusTagline = getTrim("status_tagline")
	next.StatusConnectMinerTitleExtra = getTrim("status_connect_miner_title_extra")
	next.StatusConnectMinerTitleExtraURL = getTrim("status_connect_miner_title_extra_url")
	next.FiatCurrency = strings.ToLower(getTrim("fiat_currency"))
	next.GitHubURL = getTrim("github_url")
	next.DiscordURL = getTrim("discord_url")
	next.ServerLocation = orig.ServerLocation
	if fieldProvided("server_location") {
		next.ServerLocation = getTrim("server_location")
	}
	next.MempoolAddressURL = normalizeMempoolAddressURL(getTrim("mempool_address_url"))
	next.PoolDonationAddress = getTrim("pool_donation_address")
	// operator_donation_* are sensitive and intentionally disabled in the admin
	// UI. If the fields are absent (as disabled inputs are), keep the original
	// values so Apply doesn't fail with a sensitive-settings error.
	next.OperatorDonationName = orig.OperatorDonationName
	if fieldProvided("operator_donation_name") {
		next.OperatorDonationName = getTrim("operator_donation_name")
	}
	next.OperatorDonationURL = orig.OperatorDonationURL
	if fieldProvided("operator_donation_url") {
		next.OperatorDonationURL = getTrim("operator_donation_url")
	}
	next.StatusPublicURL = orig.StatusPublicURL
	if fieldProvided("status_public_url") {
		next.StatusPublicURL = getTrim("status_public_url")
	}

	next.ListenAddr = orig.ListenAddr
	if fieldProvided("pool_listen") {
		next.ListenAddr = normalizeListen(getTrim("pool_listen"))
	}
	next.StatusAddr = orig.StatusAddr
	if fieldProvided("status_listen") {
		next.StatusAddr = normalizeListen(getTrim("status_listen"))
	}
	next.StatusTLSAddr = orig.StatusTLSAddr
	if fieldProvided("status_tls_listen") {
		next.StatusTLSAddr = normalizeListen(getTrim("status_tls_listen"))
	}
	next.StratumTLSListen = orig.StratumTLSListen
	if fieldProvided("stratum_tls_listen") {
		next.StratumTLSListen = normalizeListen(getTrim("stratum_tls_listen"))
	}

	var err error
	if next.MaxConns, err = parseInt("max_conns", next.MaxConns); err != nil {
		return err
	}
	if next.MaxConns < adminMinConnsLimit || next.MaxConns > adminMaxConnsLimit {
		return fmt.Errorf("max_conns must be between %d and %d", adminMinConnsLimit, adminMaxConnsLimit)
	}
	next.AutoAcceptRateLimits = getBool("auto_accept_rate_limits")
	if next.MaxAcceptsPerSecond, err = parseInt("max_accepts_per_second", next.MaxAcceptsPerSecond); err != nil {
		return err
	}
	if next.MaxAcceptsPerSecond < adminMinAcceptsPerSecondLimit || next.MaxAcceptsPerSecond > adminMaxAcceptsPerSecondLimit {
		return fmt.Errorf("max_accepts_per_second must be between %d and %d", adminMinAcceptsPerSecondLimit, adminMaxAcceptsPerSecondLimit)
	}
	if next.MaxAcceptBurst, err = parseInt("max_accept_burst", next.MaxAcceptBurst); err != nil {
		return err
	}
	if next.MaxAcceptBurst < adminMinAcceptBurstLimit || next.MaxAcceptBurst > adminMaxAcceptBurstLimit {
		return fmt.Errorf("max_accept_burst must be between %d and %d", adminMinAcceptBurstLimit, adminMaxAcceptBurstLimit)
	}
	if next.AcceptReconnectWindow, err = parseInt("accept_reconnect_window", next.AcceptReconnectWindow); err != nil {
		return err
	}
	if next.AcceptBurstWindow, err = parseInt("accept_burst_window", next.AcceptBurstWindow); err != nil {
		return err
	}
	if next.AcceptSteadyStateWindow, err = parseInt("accept_steady_state_window", next.AcceptSteadyStateWindow); err != nil {
		return err
	}
	if next.AcceptSteadyStateRate, err = parseInt("accept_steady_state_rate", next.AcceptSteadyStateRate); err != nil {
		return err
	}
	if next.AcceptSteadyStateReconnectPercent, err = parseFloat("accept_steady_state_reconnect_percent", next.AcceptSteadyStateReconnectPercent); err != nil {
		return err
	}
	if next.AcceptSteadyStateReconnectWindow, err = parseInt("accept_steady_state_reconnect_window", next.AcceptSteadyStateReconnectWindow); err != nil {
		return err
	}

	timeoutSec, err := parseInt("connection_timeout_seconds", int(next.ConnectionTimeout/time.Second))
	if err != nil {
		return err
	}
	if timeoutSec < adminMinConnectionTimeoutSeconds || timeoutSec > adminMaxConnectionTimeoutSeconds {
		return fmt.Errorf("connection_timeout_seconds must be between %d and %d", adminMinConnectionTimeoutSeconds, adminMaxConnectionTimeoutSeconds)
	}
	next.ConnectionTimeout = time.Duration(timeoutSec) * time.Second

	if next.MinDifficulty, err = parseFloat("min_difficulty", next.MinDifficulty); err != nil {
		return err
	}
	if next.MaxDifficulty, err = parseFloat("max_difficulty", next.MaxDifficulty); err != nil {
		return err
	}
	next.LockSuggestedDifficulty = getBool("lock_suggested_difficulty")

	next.CleanExpiredBansOnStartup = getBool("clean_expired_on_startup")
	if next.BanInvalidSubmissionsAfter, err = parseInt("ban_invalid_submissions_after", next.BanInvalidSubmissionsAfter); err != nil {
		return err
	}
	windowSec, err := parseInt("ban_invalid_submissions_window_seconds", int(next.BanInvalidSubmissionsWindow/time.Second))
	if err != nil {
		return err
	}
	next.BanInvalidSubmissionsWindow = time.Duration(windowSec) * time.Second
	durSec, err := parseInt("ban_invalid_submissions_duration_seconds", int(next.BanInvalidSubmissionsDuration/time.Second))
	if err != nil {
		return err
	}
	next.BanInvalidSubmissionsDuration = time.Duration(durSec) * time.Second
	if next.ReconnectBanThreshold, err = parseInt("reconnect_ban_threshold", next.ReconnectBanThreshold); err != nil {
		return err
	}
	if next.ReconnectBanWindowSeconds, err = parseInt("reconnect_ban_window_seconds", next.ReconnectBanWindowSeconds); err != nil {
		return err
	}
	if next.ReconnectBanDurationSeconds, err = parseInt("reconnect_ban_duration_seconds", next.ReconnectBanDurationSeconds); err != nil {
		return err
	}

	next.PeerCleanupEnabled = getBool("peer_cleanup_enabled")
	if next.PeerCleanupMaxPingMs, err = parseFloat("peer_cleanup_max_ping_ms", next.PeerCleanupMaxPingMs); err != nil {
		return err
	}
	if next.PeerCleanupMinPeers, err = parseInt("peer_cleanup_min_peers", next.PeerCleanupMinPeers); err != nil {
		return err
	}

	next.SoloMode = getBool("solo_mode")
	next.DirectSubmitProcessing = getBool("direct_submit_processing")
	next.CheckDuplicateShares = getBool("check_duplicate_shares")

	if lvl := strings.ToLower(getTrim("log_level")); lvl != "" {
		next.LogLevel = lvl
	}

	if next.DiscordWorkerNotifyThresholdSeconds, err = parseInt("discord_worker_notify_threshold_seconds", next.DiscordWorkerNotifyThresholdSeconds); err != nil {
		return err
	}
	if next.DiscordWorkerNotifyThresholdSeconds < 0 {
		return fmt.Errorf("discord_worker_notify_threshold_seconds must be >= 0")
	}
	if next.HashrateEMATauSeconds, err = parseFloat("hashrate_ema_tau_seconds", next.HashrateEMATauSeconds); err != nil {
		return err
	}
	if next.HashrateEMATauSeconds <= 0 {
		return fmt.Errorf("hashrate_ema_tau_seconds must be > 0")
	}
	if next.HashrateEMAMinShares, err = parseInt("hashrate_ema_min_shares", next.HashrateEMAMinShares); err != nil {
		return err
	}
	if next.HashrateEMAMinShares < minHashrateEMAMinShares {
		return fmt.Errorf("hashrate_ema_min_shares must be >= %d", minHashrateEMAMinShares)
	}
	if next.NTimeForwardSlackSeconds, err = parseInt("ntime_forward_slack_seconds", next.NTimeForwardSlackSeconds); err != nil {
		return err
	}
	if next.NTimeForwardSlackSeconds <= 0 {
		return fmt.Errorf("ntime_forward_slack_seconds must be > 0")
	}

	if changed := adminSensitiveFieldsChanged(orig, next); len(changed) > 0 {
		return fmt.Errorf("sensitive settings cannot be changed via the admin panel: %s", strings.Join(changed, ", "))
	}

	*cfg = next
	return nil
}

func adminSensitiveFieldsChanged(orig, next Config) []string {
	var changed []string
	if orig.ListenAddr != next.ListenAddr {
		changed = append(changed, "pool_listen")
	}
	if orig.StatusAddr != next.StatusAddr {
		changed = append(changed, "status_listen")
	}
	if orig.StatusTLSAddr != next.StatusTLSAddr {
		changed = append(changed, "status_tls_listen")
	}
	if orig.StratumTLSListen != next.StratumTLSListen {
		changed = append(changed, "stratum_tls_listen")
	}
	if orig.StatusBrandName != next.StatusBrandName {
		changed = append(changed, "status_brand_name")
	}
	if orig.StatusBrandDomain != next.StatusBrandDomain {
		changed = append(changed, "status_brand_domain")
	}
	if orig.ServerLocation != next.ServerLocation {
		changed = append(changed, "server_location")
	}
	if orig.StatusPublicURL != next.StatusPublicURL {
		changed = append(changed, "status_public_url")
	}
	if orig.PayoutAddress != next.PayoutAddress {
		changed = append(changed, "payout_address")
	}
	if orig.PoolFeePercent != next.PoolFeePercent {
		changed = append(changed, "pool_fee_percent")
	}
	if orig.OperatorDonationPercent != next.OperatorDonationPercent {
		changed = append(changed, "operator_donation_percent")
	}
	if orig.OperatorDonationName != next.OperatorDonationName {
		changed = append(changed, "operator_donation_name")
	}
	if orig.OperatorDonationURL != next.OperatorDonationURL {
		changed = append(changed, "operator_donation_url")
	}
	if orig.PoolEntropy != next.PoolEntropy {
		changed = append(changed, "pool_entropy")
	}
	if orig.PoolTagPrefix != next.PoolTagPrefix {
		changed = append(changed, "pool_tag_prefix")
	}
	return changed
}

func rewriteTuningFile(path string, cfg Config) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	tf := buildTuningFileConfig(cfg)
	data, err := toml.Marshal(tf)
	if err != nil {
		return fmt.Errorf("encode tuning: %w", err)
	}
	data = withPrependedTOMLComments(data, generatedTuningFileHeader(), tuningConfigDocComments())
	if err := atomicWriteFile(path, data); err != nil {
		return err
	}
	_ = os.Chmod(path, 0o644)
	return nil
}
