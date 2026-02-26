package main

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (s *StatusServer) handleAdminPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	data, _, _ := s.buildAdminPageData(r, r.URL.Query().Get("notice"))
	s.renderAdminPage(w, r, data)
}

func (s *StatusServer) handleAdminMinersPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Redirect(w, r, "/admin/miners", http.StatusSeeOther)
		return
	}
	data, _, _ := s.buildAdminPageData(r, r.URL.Query().Get("notice"))
	if !data.AdminEnabled {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if !data.LoggedIn {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	data.AdminSection = "miners"
	page, perPage := adminPaginationFromRequest(r)
	allRows := s.buildAdminMinerRows()
	data.AdminMinerRows, data.AdminMinerPagination = paginateAdminSlice(allRows, page, perPage)
	s.renderAdminPageTemplate(w, r, data, "admin_miners")
}

func (s *StatusServer) handleAdminLoginsPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Redirect(w, r, "/admin/logins", http.StatusSeeOther)
		return
	}
	data, _, _ := s.buildAdminPageData(r, r.URL.Query().Get("notice"))
	if !data.AdminEnabled {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if !data.LoggedIn {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	data.AdminSection = "logins"
	page, perPage := adminPaginationFromRequest(r)
	allRows, loadErr := s.buildAdminLoginRows()
	data.AdminLoginsLoadError = loadErr
	data.AdminSavedWorkerRows, data.AdminLoginPagination = paginateAdminSlice(allRows, page, perPage)
	s.renderAdminPageTemplate(w, r, data, "admin_logins")
}

func (s *StatusServer) handleAdminBansPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Redirect(w, r, "/admin/bans", http.StatusSeeOther)
		return
	}
	data, _, _ := s.buildAdminPageData(r, r.URL.Query().Get("notice"))
	if !data.AdminEnabled {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if !data.LoggedIn {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	data.AdminSection = "bans"
	page, perPage := adminPaginationFromRequest(r)
	allRows, loadErr := s.buildAdminBannedWorkers()
	data.AdminBansLoadError = loadErr
	data.AdminBannedWorkers, data.AdminBansPagination = paginateAdminSlice(allRows, page, perPage)
	s.renderAdminPageTemplate(w, r, data, "admin_bans")
}

func (s *StatusServer) handleAdminOperatorPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Redirect(w, r, "/admin/operator", http.StatusSeeOther)
		return
	}
	data, _, _ := s.buildAdminPageData(r, r.URL.Query().Get("notice"))
	if !data.AdminEnabled {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if !data.LoggedIn {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	data.AdminSection = "operator"
	s.renderAdminPageTemplate(w, r, data, "admin_operator")
}

func (s *StatusServer) handleAdminConfigPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Redirect(w, r, "/admin/config", http.StatusSeeOther)
		return
	}
	data, _, _ := s.buildAdminPageData(r, r.URL.Query().Get("notice"))
	if !data.AdminEnabled {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if !data.LoggedIn {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	data.AdminSection = "config"
	if configJSON, err := s.buildAdminLoadedConfigOverridesJSON(); err != nil {
		data.AdminLoadedConfigError = err.Error()
	} else {
		data.AdminLoadedConfigJSON = configJSON
	}
	s.renderAdminPageTemplate(w, r, data, "admin_config")
}

func (s *StatusServer) handleAdminLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if !s.allowAdminLoginAttempt() {
		data, _, _ := s.buildAdminPageData(r, "")
		data.AdminLoginError = "Too many login attempts. Please wait a moment and try again."
		w.WriteHeader(http.StatusTooManyRequests)
		s.renderAdminPage(w, r, data)
		return
	}
	if err := r.ParseForm(); err != nil {
		logger.Warn("parse admin login form", "error", err)
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	adminCfg, err := loadAdminConfigFile(s.adminConfigPath)
	data, _, _ := s.buildAdminPageData(r, "")
	if err != nil {
		data.AdminApplyError = fmt.Sprintf("Failed to read admin config: %v", err)
		s.renderAdminPage(w, r, data)
		return
	}
	if !adminCfg.Enabled {
		data.AdminApplyError = "Admin control panel is disabled (set enabled = true in admin.toml)."
		s.renderAdminPage(w, r, data)
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	if username == "" || password == "" || !s.adminCredentialsMatch(adminCfg, username, password) {
		data.AdminLoginError = "Invalid username or password."
		s.renderAdminPage(w, r, data)
		return
	}
	if err := s.scrubAdminPasswordPlaintext(adminCfg); err != nil {
		logger.Warn("admin password scrub failed", "error", err, "path", s.adminConfigPath)
	}
	token, expiry, err := s.createAdminSession(adminCfg.sessionDuration())
	if err != nil {
		logger.Error("create admin session failed", "error", err)
		data.AdminLoginError = "Unable to start admin session."
		s.renderAdminPage(w, r, data)
		return
	}
	s.pruneExpiredAdminSessions()
	http.SetCookie(w, &http.Cookie{
		Name:     adminSessionCookieName,
		Value:    token,
		Path:     "/admin",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
		Expires:  expiry,
	})
	http.Redirect(w, r, "/admin?notice=logged_in", http.StatusSeeOther)
}

func (s *StatusServer) allowAdminLoginAttempt() bool {
	if s == nil {
		return false
	}
	now := time.Now()
	s.adminLoginMu.Lock()
	defer s.adminLoginMu.Unlock()
	if !s.adminLoginNext.IsZero() && now.Before(s.adminLoginNext) {
		return false
	}
	s.adminLoginNext = now.Add(100 * time.Millisecond)
	return true
}

func (s *StatusServer) handleAdminLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if token, ok := s.adminSessionToken(r); ok {
		s.invalidateAdminSession(token)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     adminSessionCookieName,
		Path:     "/admin",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
		Expires:  time.Unix(0, 0),
	})
	http.Redirect(w, r, "/admin?notice=logged_out", http.StatusSeeOther)
}

func (s *StatusServer) handleAdminApplySettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		logger.Warn("parse admin settings form", "error", err)
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	data, adminCfg, err := s.buildAdminPageData(r, "")
	if err != nil {
		s.renderAdminPage(w, r, data)
		return
	}
	if !adminCfg.Enabled {
		data.AdminApplyError = "Admin control panel is disabled."
		s.renderAdminPage(w, r, data)
		return
	}
	if !s.isAdminAuthenticated(r) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	password := r.FormValue("password")
	if password == "" || !s.adminPasswordMatches(adminCfg, password) {
		data.AdminApplyError = "Password is required to apply live settings."
		s.renderAdminPage(w, r, data)
		return
	}

	cfg := s.Config()
	if err := applyAdminSettingsForm(&cfg, r); err != nil {
		data.AdminApplyError = err.Error()
		data.Settings = buildAdminSettingsData(cfg)
		s.renderAdminPage(w, r, data)
		return
	}

	// Best-effort helper: keep accept limits consistent when auto mode is enabled.
	autoConfigureAcceptRateLimits(&cfg, fileOverrideConfig{}, false)

	if err := validateConfig(cfg); err != nil {
		data.AdminApplyError = fmt.Sprintf("Validation error: %v", err)
		data.Settings = buildAdminSettingsData(cfg)
		s.renderAdminPage(w, r, data)
		return
	}
	s.UpdateConfig(cfg)
	if s.registry != nil {
		for _, mc := range s.registry.Snapshot() {
			mc.ApplyRuntimeConfig(cfg)
		}
	}
	if s.jobMgr != nil {
		payoutScript, err := fetchPayoutScript(nil, cfg.PayoutAddress)
		if err != nil {
			data.AdminApplyError = fmt.Sprintf("Payout script error: %v", err)
			data.Settings = buildAdminSettingsData(cfg)
			s.renderAdminPage(w, r, data)
			return
		}
		var donationScript []byte
		if cfg.OperatorDonationPercent > 0 && strings.TrimSpace(cfg.OperatorDonationAddress) != "" {
			donationScript, err = fetchPayoutScript(nil, cfg.OperatorDonationAddress)
			if err != nil {
				data.AdminApplyError = fmt.Sprintf("Donation script error: %v", err)
				data.Settings = buildAdminSettingsData(cfg)
				s.renderAdminPage(w, r, data)
				return
			}
		}
		s.jobMgr.ApplyRuntimeConfig(cfg, payoutScript, donationScript)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := s.jobMgr.refreshJobCtxForce(ctx); err != nil && logger.Enabled(logLevelDebug) {
				logger.Debug("admin-triggered job refresh after config apply failed", "component", "admin", "kind", "config_apply", "error", err)
			}
		}()
	}
	if cfg.LogDebug {
		setLogLevel(logLevelDebug)
	} else {
		setLogLevel(logLevelInfo)
	}
	debugLogging = debugEnabled()
	verboseRuntimeLogging = verboseRuntimeEnabled()
	logger.Info("admin applied live settings (in memory)", "component", "admin", "kind", "config_apply", "active_miners", s.registry.Count())
	http.Redirect(w, r, "/admin?notice=settings_applied", http.StatusSeeOther)
}

func (s *StatusServer) handleAdminReloadUI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		logger.Warn("parse admin reload ui form", "component", "admin", "kind", "http_parse", "error", err)
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	data, adminCfg, err := s.buildAdminPageData(r, "")
	if err != nil {
		s.renderAdminPage(w, r, data)
		return
	}
	if !adminCfg.Enabled {
		data.AdminReloadError = "Admin control panel is disabled."
		s.renderAdminPage(w, r, data)
		return
	}
	if !s.isAdminAuthenticated(r) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	password := r.FormValue("password")
	if password == "" || !s.adminPasswordMatches(adminCfg, password) {
		data.AdminReloadError = "Password is required to reload UI assets."
		s.renderAdminPage(w, r, data)
		return
	}
	if !strings.EqualFold(strings.TrimSpace(r.FormValue("confirm")), "RELOAD") {
		data.AdminReloadError = "Please type RELOAD to confirm."
		s.renderAdminPage(w, r, data)
		return
	}
	if err := s.ReloadTemplates(); err != nil {
		data.AdminReloadError = fmt.Sprintf("Failed to reload templates: %v", err)
		s.renderAdminPage(w, r, data)
		return
	}
	if err := s.ReloadStaticFiles(); err != nil {
		data.AdminReloadError = fmt.Sprintf("Failed to reload static assets: %v", err)
		s.renderAdminPage(w, r, data)
		return
	}
	http.Redirect(w, r, "/admin?notice=ui_reloaded", http.StatusSeeOther)
}

func (s *StatusServer) handleAdminPersist(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		logger.Warn("parse admin persist form", "component", "admin", "kind", "http_parse", "error", err)
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	data, adminCfg, err := s.buildAdminPageData(r, "")
	if err != nil {
		s.renderAdminPage(w, r, data)
		return
	}
	if !adminCfg.Enabled {
		data.AdminPersistError = "Admin control panel is disabled."
		s.renderAdminPage(w, r, data)
		return
	}
	if !s.isAdminAuthenticated(r) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if !s.adminPasswordMatches(adminCfg, r.FormValue("password")) {
		data.AdminPersistError = "Password is required to save to disk."
		s.renderAdminPage(w, r, data)
		return
	}
	if !strings.EqualFold(strings.TrimSpace(r.FormValue("confirm")), "SAVE") {
		data.AdminPersistError = "Please type SAVE to confirm."
		s.renderAdminPage(w, r, data)
		return
	}

	cfg := s.Config()
	if err := rewriteConfigFile(s.configPath, cfg); err != nil {
		data.AdminPersistError = fmt.Sprintf("Failed to write config.toml: %v", err)
		s.renderAdminPage(w, r, data)
		return
	}
	configDir := filepath.Dir(s.configPath)
	servicesPath := filepath.Join(configDir, "services.toml")
	policyPath := filepath.Join(configDir, "policy.toml")
	tuningPath := filepath.Join(configDir, "tuning.toml")
	if err := rewriteServicesFile(servicesPath, cfg); err != nil {
		data.AdminPersistError = fmt.Sprintf("Failed to write services.toml: %v", err)
		s.renderAdminPage(w, r, data)
		return
	}
	if err := rewritePolicyFile(policyPath, cfg); err != nil {
		data.AdminPersistError = fmt.Sprintf("Failed to write policy.toml: %v", err)
		s.renderAdminPage(w, r, data)
		return
	}
	if err := rewriteTuningFile(tuningPath, cfg); err != nil {
		data.AdminPersistError = fmt.Sprintf("Failed to write tuning.toml: %v", err)
		s.renderAdminPage(w, r, data)
		return
	}

	logger.Info("admin persisted in-memory config to disk", "component", "admin", "kind", "config_persist", "config_path", s.configPath, "services_path", servicesPath, "policy_path", policyPath, "tuning_path", tuningPath)
	http.Redirect(w, r, "/admin?notice=saved_to_disk", http.StatusSeeOther)
}

func (s *StatusServer) handleAdminReboot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		logger.Warn("parse admin reboot form", "component", "admin", "kind", "http_parse", "error", err)
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	data, adminCfg, err := s.buildAdminPageData(r, "reboot_requested")
	if err != nil {
		s.renderAdminPage(w, r, data)
		return
	}
	if !adminCfg.Enabled {
		data.AdminRebootError = "Admin control panel is disabled."
		s.renderAdminPage(w, r, data)
		return
	}
	if !s.isAdminAuthenticated(r) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if !s.adminPasswordMatches(adminCfg, r.FormValue("password")) {
		data.AdminRebootError = "Password is required to reboot."
		s.renderAdminPage(w, r, data)
		return
	}
	if !strings.EqualFold(strings.TrimSpace(r.FormValue("confirm")), "REBOOT") {
		data.AdminRebootError = "Please type REBOOT to confirm."
		s.renderAdminPage(w, r, data)
		return
	}
	logger.Info("admin requested reboot", "component", "admin", "kind", "reboot")
	s.renderAdminPage(w, r, data)
	if s.requestShutdown != nil {
		s.requestShutdown()
	}
}

func (s *StatusServer) handleAdminMinerDisconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin/miners", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		logger.Warn("parse admin miner disconnect form", "error", err)
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	data, adminCfg, _ := s.buildAdminPageData(r, "")
	data.AdminSection = "miners"
	page, perPage := adminPaginationFromRequest(r)
	allRows := s.buildAdminMinerRows()
	data.AdminMinerRows, data.AdminMinerPagination = paginateAdminSlice(allRows, page, perPage)
	if !adminCfg.Enabled {
		data.AdminApplyError = "Admin control panel is disabled."
		s.renderAdminPageTemplate(w, r, data, "admin_miners")
		return
	}
	if !s.isAdminAuthenticated(r) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if r.FormValue("password") == "" || !s.adminPasswordMatches(adminCfg, r.FormValue("password")) {
		data.AdminApplyError = "Password is required to disconnect miners."
		s.renderAdminPageTemplate(w, r, data, "admin_miners")
		return
	}
	rawSeqs := r.Form["connection_seq"]
	if len(rawSeqs) == 0 || s.workerRegistry == nil {
		data.AdminApplyError = "Connection not found."
		s.renderAdminPageTemplate(w, r, data, "admin_miners")
		return
	}

	seen := make(map[uint64]struct{})
	disconnected := 0
	for _, raw := range rawSeqs {
		seq, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
		if err != nil || seq == 0 {
			continue
		}
		if _, ok := seen[seq]; ok {
			continue
		}
		seen[seq] = struct{}{}
		if mc := s.workerRegistry.connectionBySeq(seq); mc != nil {
			disconnected++
			mc.Close("admin disconnect")
		}
	}
	if disconnected > 0 {
		http.Redirect(w, r, "/admin/miners?notice=miner_disconnected", http.StatusSeeOther)
		return
	}
	data.AdminApplyError = "Connection not found."
	s.renderAdminPageTemplate(w, r, data, "admin_miners")
}

func (s *StatusServer) handleAdminMinerBan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin/miners", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		logger.Warn("parse admin miner ban form", "error", err)
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	data, adminCfg, _ := s.buildAdminPageData(r, "")
	data.AdminSection = "miners"
	page, perPage := adminPaginationFromRequest(r)
	allRows := s.buildAdminMinerRows()
	data.AdminMinerRows, data.AdminMinerPagination = paginateAdminSlice(allRows, page, perPage)
	if !adminCfg.Enabled {
		data.AdminApplyError = "Admin control panel is disabled."
		s.renderAdminPageTemplate(w, r, data, "admin_miners")
		return
	}
	if !s.isAdminAuthenticated(r) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if r.FormValue("password") == "" || !s.adminPasswordMatches(adminCfg, r.FormValue("password")) {
		data.AdminApplyError = "Password is required to ban miners."
		s.renderAdminPageTemplate(w, r, data, "admin_miners")
		return
	}
	rawSeqs := r.Form["connection_seq"]
	if len(rawSeqs) == 0 || s.workerRegistry == nil {
		data.AdminApplyError = "Connection not found."
		s.renderAdminPageTemplate(w, r, data, "admin_miners")
		return
	}

	seen := make(map[uint64]struct{})
	banned := 0
	duration := s.Config().BanInvalidSubmissionsDuration
	for _, raw := range rawSeqs {
		seq, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
		if err != nil || seq == 0 {
			continue
		}
		if _, ok := seen[seq]; ok {
			continue
		}
		seen[seq] = struct{}{}
		if mc := s.workerRegistry.connectionBySeq(seq); mc != nil {
			banned++
			mc.adminBan("admin ban", duration)
			mc.Close("admin ban")
		}
	}
	if banned > 0 {
		http.Redirect(w, r, "/admin/miners?notice=miner_banned", http.StatusSeeOther)
		return
	}
	data.AdminApplyError = "Connection not found."
	s.renderAdminPageTemplate(w, r, data, "admin_miners")
}

func (s *StatusServer) handleAdminLoginDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin/logins", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		logger.Warn("parse admin login delete form", "error", err)
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	data, adminCfg, _ := s.buildAdminPageData(r, "")
	data.AdminSection = "logins"
	page, perPage := adminPaginationFromRequest(r)
	allRows, loadErr := s.buildAdminLoginRows()
	data.AdminLoginsLoadError = loadErr
	data.AdminSavedWorkerRows, data.AdminLoginPagination = paginateAdminSlice(allRows, page, perPage)
	if !adminCfg.Enabled {
		data.AdminApplyError = "Admin control panel is disabled."
		s.renderAdminPageTemplate(w, r, data, "admin_logins")
		return
	}
	if !s.isAdminAuthenticated(r) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if r.FormValue("password") == "" || !s.adminPasswordMatches(adminCfg, r.FormValue("password")) {
		data.AdminApplyError = "Password is required to delete saved workers."
		s.renderAdminPageTemplate(w, r, data, "admin_logins")
		return
	}
	userIDs := r.Form["user_id"]
	if len(userIDs) == 0 || s.workerLists == nil {
		data.AdminApplyError = "Saved worker record not found."
		s.renderAdminPageTemplate(w, r, data, "admin_logins")
		return
	}
	seen := make(map[string]struct{})
	for _, rawID := range userIDs {
		id := strings.TrimSpace(rawID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		if err := s.workerLists.RemoveUser(id); err != nil {
			logger.Warn("delete saved worker", "error", err, "user_id", id)
		}
	}
	http.Redirect(w, r, "/admin/logins?notice=saved_worker_deleted", http.StatusSeeOther)
}

func (s *StatusServer) handleAdminLoginBan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin/logins", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		logger.Warn("parse admin login ban form", "error", err)
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	data, adminCfg, _ := s.buildAdminPageData(r, "")
	data.AdminSection = "logins"
	page, perPage := adminPaginationFromRequest(r)
	allRows, loadErr := s.buildAdminLoginRows()
	data.AdminLoginsLoadError = loadErr
	data.AdminSavedWorkerRows, data.AdminLoginPagination = paginateAdminSlice(allRows, page, perPage)
	if !adminCfg.Enabled {
		data.AdminApplyError = "Admin control panel is disabled."
		s.renderAdminPageTemplate(w, r, data, "admin_logins")
		return
	}
	if !s.isAdminAuthenticated(r) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if r.FormValue("password") == "" || !s.adminPasswordMatches(adminCfg, r.FormValue("password")) {
		data.AdminApplyError = "Password is required to ban saved workers."
		s.renderAdminPageTemplate(w, r, data, "admin_logins")
		return
	}
	hashes := r.Form["worker_hash"]
	userID := strings.TrimSpace(r.FormValue("user_id"))
	if len(hashes) == 0 || s.workerRegistry == nil {
		data.AdminApplyError = "Worker hash is required."
		s.renderAdminPageTemplate(w, r, data, "admin_logins")
		return
	}
	connsFound := false
	for _, hash := range hashes {
		if hash == "" {
			continue
		}
		hash = strings.ToLower(hash)
		conns := s.workerRegistry.getConnectionsByHash(hash)
		if len(conns) == 0 {
			continue
		}
		connsFound = true
		duration := s.Config().BanInvalidSubmissionsDuration
		reason := "admin login ban"
		if userID != "" {
			reason = fmt.Sprintf("admin login ban (%s)", userID)
		}
		for _, mc := range conns {
			if mc == nil {
				continue
			}
			mc.adminBan(reason, duration)
			mc.Close("admin login ban")
		}
	}
	if !connsFound {
		data.AdminApplyError = "No active connections for this worker."
		s.renderAdminPageTemplate(w, r, data, "admin_logins")
		return
	}
	http.Redirect(w, r, "/admin/logins?notice=saved_worker_banned", http.StatusSeeOther)
}

func (s *StatusServer) handleAdminBanRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin/bans", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		logger.Warn("parse admin ban remove form", "error", err)
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	data, adminCfg, _ := s.buildAdminPageData(r, "")
	data.AdminSection = "bans"
	page, perPage := adminPaginationFromRequest(r)
	allRows, loadErr := s.buildAdminBannedWorkers()
	data.AdminBansLoadError = loadErr
	data.AdminBannedWorkers, data.AdminBansPagination = paginateAdminSlice(allRows, page, perPage)
	if !adminCfg.Enabled {
		data.AdminApplyError = "Admin control panel is disabled."
		s.renderAdminPageTemplate(w, r, data, "admin_bans")
		return
	}
	if !s.isAdminAuthenticated(r) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if r.FormValue("password") == "" || !s.adminPasswordMatches(adminCfg, r.FormValue("password")) {
		data.AdminApplyError = "Password is required to remove bans."
		s.renderAdminPageTemplate(w, r, data, "admin_bans")
		return
	}
	if s.accounting == nil || !s.accounting.Ready() {
		data.AdminApplyError = "Accounting store is not available."
		s.renderAdminPageTemplate(w, r, data, "admin_bans")
		return
	}
	names := r.Form["worker_name"]
	if len(names) == 0 {
		data.AdminApplyError = "No banned workers selected."
		s.renderAdminPageTemplate(w, r, data, "admin_bans")
		return
	}
	seen := make(map[string]struct{})
	removed := 0
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		s.accounting.MarkBan(name, time.Time{}, "")
		removed++
	}
	if removed > 0 {
		http.Redirect(w, r, "/admin/bans?notice=bans_removed", http.StatusSeeOther)
		return
	}
	data.AdminApplyError = "No banned workers selected."
	s.renderAdminPageTemplate(w, r, data, "admin_bans")
}
