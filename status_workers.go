package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/bytedance/sonic"
)

const maxSavedWorkersPerNameDisplay = 16

func safeRedirectPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if !strings.HasPrefix(value, "/") {
		return ""
	}
	if strings.HasPrefix(value, "//") {
		return ""
	}
	if strings.ContainsAny(value, "\n\r") {
		return ""
	}
	return value
}

func isSameOriginRequest(r *http.Request, host string) bool {
	host = strings.TrimSpace(host)
	if host == "" {
		host = "localhost"
	}
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
		if parsed, err := url.Parse(origin); err != nil || parsed.Host == "" || !strings.EqualFold(parsed.Host, host) {
			return false
		}
	}
	if referer := strings.TrimSpace(r.Header.Get("Referer")); referer != "" {
		if parsed, err := url.Parse(referer); err != nil || parsed.Host == "" || !strings.EqualFold(parsed.Host, host) {
			return false
		}
	}
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))) {
	case "cross-site":
		return false
	}
	return true
}

func savedWorkerLookupHashes(savedName, savedHash string) (primary string, fallbacks []string) {
	primary = strings.ToLower(strings.TrimSpace(savedHash))
	if primary == "" {
		primary = workerNameHash(savedName)
	}
	if primary == "" {
		return "", nil
	}

	base := workerBaseAddress(savedName)
	if base != "" && base != strings.TrimSpace(savedName) {
		if baseHash := workerNameHash(base); baseHash != "" && baseHash != primary {
			fallbacks = append(fallbacks, baseHash)
		}
	}
	return primary, fallbacks
}

func (s *StatusServer) findSavedWorkerConnections(savedName, savedHash string, now time.Time) (views []WorkerView, queryHash string) {
	if s == nil {
		return nil, ""
	}
	primary, fallbacks := savedWorkerLookupHashes(savedName, savedHash)
	if primary == "" {
		return nil, ""
	}
	if views = s.findAllWorkerViewsByHash(primary, now); len(views) > 0 {
		return views, primary
	}
	for _, h := range fallbacks {
		if v := s.findAllWorkerViewsByHash(h, now); len(v) > 0 {
			return v, h
		}
	}
	return nil, primary
}

func buildWorkerLookupByHash(workers []WorkerView, banned []WorkerView) map[string]WorkerView {
	if len(workers) == 0 && len(banned) == 0 {
		return nil
	}
	lookup := make(map[string]WorkerView, len(workers)+len(banned))
	add := func(w WorkerView) {
		hash := strings.TrimSpace(w.WorkerSHA256)
		if hash == "" {
			return
		}
		if _, exists := lookup[hash]; exists {
			return
		}
		lookup[hash] = w
	}
	for _, w := range workers {
		add(w)
	}
	for _, w := range banned {
		add(w)
	}
	if len(lookup) == 0 {
		return nil
	}
	return lookup
}

func workerLookupFromStatusData(data StatusData) map[string]WorkerView {
	if data.WorkerLookup != nil {
		return data.WorkerLookup
	}
	return buildWorkerLookupByHash(data.Workers, data.BannedWorkers)
}

// handleWorkerStatus renders the worker login page.
func (s *StatusServer) handleWorkerStatus(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	base := s.baseTemplateData(start)

	data := WorkerStatusData{
		StatusData: base,
	}
	s.enrichStatusDataWithClerk(r, &data.StatusData)
	if data.ClerkUser != nil {
		http.Redirect(w, r, "/saved-workers", http.StatusSeeOther)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "worker_login", data); err != nil {
		logger.Error("worker login template error", "error", err)
		s.renderErrorPage(w, r, http.StatusInternalServerError,
			"Worker login page error",
			"We couldn't render the worker login page.",
			"Template error while rendering worker login.")
	}
}

func (s *StatusServer) handleWorkerWalletSearch(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	base := s.baseTemplateData(start)

	data := WorkerWalletSearchData{
		StatusData: base,
	}
	s.enrichStatusDataWithClerk(r, &data.StatusData)

	hash := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("hash")))
	data.QueriedWalletHash = hash

	if hash == "" {
		data.Error = "Please provide a wallet hash to search."
	} else if len(hash) != 64 {
		data.Error = "Invalid hash format; expected 64 hex characters."
	} else if _, err := hex.DecodeString(hash); err != nil {
		data.Error = "Invalid hash value."
	} else if s.workerRegistry == nil {
		data.Error = "Worker registry unavailable."
	} else {
		conns := s.workerRegistry.getConnectionsByWalletHash(hash)
		if len(conns) == 0 {
			data.Error = "No active workers were found for that wallet."
		} else {
			now := time.Now()
			views := make([]WorkerView, 0, len(conns))
			for _, mc := range conns {
				views = append(views, workerViewFromConn(mc, now))
			}
			results := mergeWorkerViewsByHash(views)
			sort.Slice(results, func(i, j int) bool {
				return results[i].LastShare.After(results[j].LastShare)
			})
			data.Results = results
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "worker_wallet_search", data); err != nil {
		logger.Error("worker wallet search template error", "error", err)
		s.renderErrorPage(w, r, http.StatusInternalServerError,
			"Wallet search error",
			"We couldn't render the wallet search page.",
			"Template error while rendering wallet results.")
		return
	}
}

func (s *StatusServer) handleClerkLogin(w http.ResponseWriter, r *http.Request) {
	if s == nil {
		http.NotFound(w, r)
		return
	}
	redirect := safeRedirectPath(r.URL.Query().Get("redirect"))
	if redirect == "" {
		redirect = "/saved-workers"
	}
	http.Redirect(w, r, s.clerkLoginURL(r, redirect), http.StatusSeeOther)
}

func (s *StatusServer) handleClerkCallback(w http.ResponseWriter, r *http.Request) {
	if s == nil {
		http.NotFound(w, r)
		return
	}
	redirect := safeRedirectPath(r.URL.Query().Get("redirect"))
	if redirect == "" {
		redirect = "/saved-workers"
	}
	if s.clerk == nil {
		http.Redirect(w, r, redirect, http.StatusSeeOther)
		return
	}
	if s.clerkUserFromRequest(r) == nil {
		if devBrowserJWT := strings.TrimSpace(r.URL.Query().Get(clerkDevBrowserJWTQueryParam)); devBrowserJWT != "" {
			if jwtToken, claims, err := s.clerk.ExchangeDevBrowserJWT(r.Context(), devBrowserJWT); err != nil {
				logger.Warn("clerk dev browser exchange failed", "error", err)
			} else {
				cookie := &http.Cookie{
					Name:     s.clerk.SessionCookieName(),
					Value:    jwtToken,
					Path:     "/",
					HttpOnly: true,
					SameSite: http.SameSiteLaxMode,
				}
				cookie.Secure = s.clerkCookieSecure(r)
				if claims != nil && claims.ExpiresAt != nil {
					cookie.Expires = claims.ExpiresAt.Time
				}
				http.SetCookie(w, cookie)
				http.Redirect(w, r, redirect, http.StatusSeeOther)
				return
			}
		}
		loginTarget := "/sign-in?redirect=" + url.QueryEscape(redirect)
		http.Redirect(w, r, loginTarget, http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

func (s *StatusServer) handleSignIn(w http.ResponseWriter, r *http.Request) {
	if s == nil {
		http.NotFound(w, r)
		return
	}
	start := time.Now()
	base := s.baseTemplateData(start)

	pk := strings.TrimSpace(s.Config().ClerkPublishableKey)
	if pk == "" {
		s.renderErrorPage(w, r, http.StatusInternalServerError,
			"Sign-in misconfigured",
			"Sign-in is not configured on this server.",
			"Missing secrets.toml value clerk_publishable_key (expected pk_live_... or pk_test_...).")
		return
	}

	redirect := safeRedirectPath(r.URL.Query().Get("redirect"))
	if redirect == "" {
		redirect = "/saved-workers"
	}
	// If the user already has a valid session cookie, don't render the sign-in
	// UI (which can lead to an extra click). Just send them to the target page.
	if s.clerk != nil && s.clerkUserFromRequest(r) != nil {
		http.Redirect(w, r, redirect, http.StatusSeeOther)
		return
	}

	clerkJSHost := strings.TrimRight(strings.TrimSpace(s.Config().ClerkFrontendAPIURL), "/")
	if clerkJSHost == "" {
		clerkJSHost = strings.TrimRight(strings.TrimSpace(s.Config().ClerkIssuerURL), "/")
	}
	if clerkJSHost == "" {
		clerkJSHost = "https://clerk.clerk.com"
	}
	clerkJSURL := clerkJSHost + "/npm/@clerk/clerk-js@5/dist/clerk.browser.js"

	data := SignInPageData{
		StatusData:          base,
		ClerkPublishableKey: pk,
		ClerkJSURL:          clerkJSURL,
		AfterSignInURL:      redirect,
		AfterSignUpURL:      redirect,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "sign_in", data); err != nil {
		logger.Error("sign in template error", "error", err)
		s.renderErrorPage(w, r, http.StatusInternalServerError,
			"Sign-in page error",
			"We couldn't render the sign-in page.",
			"Template error while rendering sign-in.")
	}
}

func (s *StatusServer) handleSavedWorkers(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	base := s.baseTemplateData(start)

	type savedWorkerEntry struct {
		Name              string
		Hash              string
		NotifyEnabled     bool
		LastOnlineAt      string
		Hashrate          float64
		ShareRate         float64
		Accepted          uint64
		Difficulty        float64
		ConnectedDuration time.Duration
		ConnectionID      string
		ConnectionSeq     uint64
	}
	data := struct {
		StatusData
		OnlineWorkerEntries  []savedWorkerEntry
		OfflineWorkerEntries []savedWorkerEntry
		SavedWorkersCount    int
		SavedWorkersOnline   int
		SavedWorkersMax      int
	}{StatusData: base}
	data.HashrateGraphTitle = "Total Hashrate"
	data.HashrateGraphID = "savedWorkersHashrateChart"
	s.enrichStatusDataWithClerk(r, &data.StatusData)

	if data.ClerkUser == nil {
		http.Redirect(w, r, "/worker", http.StatusSeeOther)
		return
	}

	data.SavedWorkersMax = maxSavedWorkersPerUser
	data.SavedWorkersCount = len(data.SavedWorkers)
	now := time.Now()

	perNameRowsShown := make(map[string]int, 16)
	for _, saved := range data.SavedWorkers {
		views, lookupHash := s.findSavedWorkerConnections(saved.Name, saved.Hash, now)
		if lookupHash == "" {
			continue
		}

		if len(views) == 0 {
			// Worker is offline
			if perNameRowsShown[lookupHash] >= maxSavedWorkersPerNameDisplay {
				continue
			}
			entry := savedWorkerEntry{
				Name:          saved.Name,
				Hash:          lookupHash,
				NotifyEnabled: saved.NotifyEnabled,
			}
			perNameRowsShown[lookupHash]++
			data.OfflineWorkerEntries = append(data.OfflineWorkerEntries, entry)
		} else {
			// Worker is online, show each connection separately
			for _, view := range views {
				if perNameRowsShown[lookupHash] >= maxSavedWorkersPerNameDisplay {
					break
				}
				hashrate := view.RollingHashrate
				if hashrate <= 0 && view.ShareRate > 0 && view.Difficulty > 0 {
					hashrate = (view.Difficulty * hashPerShare * view.ShareRate) / 60.0
				}
				duration := now.Sub(view.ConnectedAt)
				if duration < 0 {
					duration = 0
				}
				entry := savedWorkerEntry{
					Name:              saved.Name,
					Hash:              view.WorkerSHA256,
					NotifyEnabled:     saved.NotifyEnabled,
					Hashrate:          hashrate,
					ShareRate:         view.ShareRate,
					Accepted:          view.Accepted,
					Difficulty:        view.Difficulty,
					ConnectedDuration: duration,
					ConnectionID:      view.ConnectionID,
					ConnectionSeq:     view.ConnectionSeq,
				}
				data.SavedWorkersOnline++
				perNameRowsShown[lookupHash]++
				data.OnlineWorkerEntries = append(data.OnlineWorkerEntries, entry)
			}
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "saved_workers", data); err != nil {
		logger.Error("saved workers template error", "error", err)
		s.renderErrorPage(w, r, http.StatusInternalServerError,
			"Saved workers page error",
			"We couldn't render the saved workers page.",
			"Template error while rendering saved workers.")
	}
}

func (s *StatusServer) handleSavedWorkersJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user := ClerkUserFromContext(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	saved := []SavedWorkerEntry(nil)
	if s.workerLists != nil {
		if list, err := s.workerLists.List(user.UserID); err == nil {
			saved = list
		} else {
			logger.Warn("load saved workers", "error", err, "user_id", user.UserID)
		}
	}

	type entry struct {
		Name                      string  `json:"name"`
		Hash                      string  `json:"hash"`
		Online                    bool    `json:"online"`
		NotifyEnabled             bool    `json:"notify_enabled"`
		LastOnlineAt              string  `json:"last_online_at,omitempty"`
		Hashrate                  float64 `json:"hashrate"`
		SharesPerMinute           float64 `json:"shares_per_minute"`
		Accepted                  uint64  `json:"accepted"`
		Difficulty                float64 `json:"difficulty"`
		ConnectionSeq             uint64  `json:"connection_seq,omitempty"`
		ConnectionDurationSeconds float64 `json:"connection_duration_seconds,omitempty"`
	}
	now := time.Now()
	discordRegistered := false
	discordUserEnabled := false
	if s.workerLists != nil &&
		strings.TrimSpace(s.Config().DiscordServerID) != "" &&
		strings.TrimSpace(s.Config().DiscordBotToken) != "" &&
		strings.TrimSpace(s.Config().DiscordNotifyChannelID) != "" {
		if _, enabled, ok, err := s.workerLists.GetDiscordLink(user.UserID); err == nil {
			discordRegistered = ok
			discordUserEnabled = ok && enabled
		}
	}
	resp := struct {
		UpdatedAt            string  `json:"updated_at"`
		SavedMax             int     `json:"saved_max"`
		SavedCount           int     `json:"saved_count"`
		OnlineCount          int     `json:"online_count"`
		DiscordRegistered    bool    `json:"discord_registered,omitempty"`
		DiscordNotifyEnabled bool    `json:"discord_notify_enabled,omitempty"`
		OnlineWorkers        []entry `json:"online_workers"`
		OfflineWorkers       []entry `json:"offline_workers"`
	}{
		UpdatedAt:            now.UTC().Format(time.RFC3339),
		SavedMax:             maxSavedWorkersPerUser,
		SavedCount:           len(saved),
		DiscordRegistered:    discordRegistered,
		DiscordNotifyEnabled: discordUserEnabled,
	}

	perNameRowsShown := make(map[string]int, 16)
	totalRowsSent := 0
	for _, savedEntry := range saved {
		if totalRowsSent >= maxSavedWorkersPerUser {
			break
		}
		views, lookupHash := s.findSavedWorkerConnections(savedEntry.Name, savedEntry.Hash, now)
		if lookupHash == "" {
			continue
		}

		if len(views) == 0 {
			// Worker is offline
			if totalRowsSent >= maxSavedWorkersPerUser {
				break
			}
			if perNameRowsShown[lookupHash] >= maxSavedWorkersPerNameDisplay {
				continue
			}
			e := entry{
				Name:          savedEntry.Name,
				Hash:          lookupHash,
				Online:        false,
				NotifyEnabled: savedEntry.NotifyEnabled,
			}
			perNameRowsShown[lookupHash]++
			totalRowsSent++
			resp.OfflineWorkers = append(resp.OfflineWorkers, e)
		} else {
			// Worker is online, show each connection separately
			for _, view := range views {
				if totalRowsSent >= maxSavedWorkersPerUser {
					break
				}
				if perNameRowsShown[lookupHash] >= maxSavedWorkersPerNameDisplay {
					break
				}
				hashrate := view.RollingHashrate
				if hashrate <= 0 && view.ShareRate > 0 && view.Difficulty > 0 {
					hashrate = (view.Difficulty * hashPerShare * view.ShareRate) / 60.0
				}
				connectionDurationSeconds := 0.0
				if !view.ConnectedAt.IsZero() {
					connectionDurationSeconds = now.Sub(view.ConnectedAt).Seconds()
					if connectionDurationSeconds < 0 {
						connectionDurationSeconds = 0
					}
				}
				e := entry{
					Name:                      savedEntry.Name,
					Hash:                      view.WorkerSHA256,
					Online:                    true,
					NotifyEnabled:             savedEntry.NotifyEnabled,
					Hashrate:                  hashrate,
					SharesPerMinute:           view.ShareRate,
					Accepted:                  view.Accepted,
					Difficulty:                view.Difficulty,
					ConnectionSeq:             view.ConnectionSeq,
					ConnectionDurationSeconds: connectionDurationSeconds,
				}
				perNameRowsShown[lookupHash]++
				resp.OnlineCount++
				totalRowsSent++
				resp.OnlineWorkers = append(resp.OnlineWorkers, e)
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	if out, err := sonic.Marshal(resp); err != nil {
		logger.Error("saved workers json marshal", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	} else if _, err := w.Write(out); err != nil {
		logger.Error("saved workers json write", "error", err)
	}
}

func (s *StatusServer) handleSavedWorkersOneTimeCode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user := ClerkUserFromContext(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if strings.TrimSpace(s.Config().DiscordServerID) == "" || strings.TrimSpace(s.Config().DiscordBotToken) == "" {
		http.NotFound(w, r)
		return
	}

	now := time.Now()
	code, expiresAt := s.createNewOneTimeCode(user.UserID, now)
	if code == "" || expiresAt.IsZero() {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	resp := struct {
		Code      string `json:"code"`
		ExpiresAt string `json:"expires_at"`
	}{
		Code:      code,
		ExpiresAt: expiresAt.UTC().Format(time.RFC3339),
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	if out, err := sonic.Marshal(resp); err != nil {
		logger.Error("one time code json marshal", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	} else if _, err := w.Write(out); err != nil {
		logger.Error("one time code json write", "error", err)
	}
}

func (s *StatusServer) handleSavedWorkersOneTimeCodeClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user := ClerkUserFromContext(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if strings.TrimSpace(s.Config().DiscordServerID) == "" || strings.TrimSpace(s.Config().DiscordBotToken) == "" {
		http.NotFound(w, r)
		return
	}

	var code string
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		type req struct {
			Code string `json:"code"`
		}
		var parsed req
		if err := json.NewDecoder(r.Body).Decode(&parsed); err == nil {
			code = strings.TrimSpace(parsed.Code)
		}
	} else {
		_ = r.ParseForm()
		code = strings.TrimSpace(r.FormValue("code"))
	}

	cleared := false
	if code != "" {
		cleared = s.clearOneTimeCode(user.UserID, code, time.Now())
	}

	resp := struct {
		Cleared bool `json:"cleared"`
	}{Cleared: cleared}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	if out, err := sonic.Marshal(resp); err != nil {
		logger.Error("one time code clear json marshal", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	} else if _, err := w.Write(out); err != nil {
		logger.Error("one time code clear json write", "error", err)
	}
}

func (s *StatusServer) handleSavedWorkersNotifyEnabled(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user := ClerkUserFromContext(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.workerLists == nil {
		http.Error(w, "saved workers not enabled", http.StatusBadRequest)
		return
	}

	type req struct {
		Hash    string `json:"hash"`
		Enabled *bool  `json:"enabled"`
	}
	var parsed req
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		_ = json.NewDecoder(r.Body).Decode(&parsed)
	} else {
		_ = r.ParseForm()
		parsed.Hash = r.FormValue("hash")
		if v := strings.TrimSpace(r.FormValue("enabled")); v != "" {
			b := v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "on") || strings.EqualFold(v, "yes")
			parsed.Enabled = &b
		}
	}

	hash := strings.ToLower(strings.TrimSpace(parsed.Hash))
	if hash == "" || len(hash) != 64 {
		http.Error(w, "invalid hash", http.StatusBadRequest)
		return
	}
	if parsed.Enabled == nil {
		http.Error(w, "missing enabled", http.StatusBadRequest)
		return
	}

	list, err := s.workerLists.List(user.UserID)
	if err != nil {
		http.Error(w, "failed to load saved workers", http.StatusInternalServerError)
		return
	}
	found := false
	for _, sw := range list {
		if strings.ToLower(strings.TrimSpace(sw.Hash)) == hash {
			found = true
			break
		}
	}
	if !found {
		http.Error(w, "worker not found", http.StatusNotFound)
		return
	}

	now := time.Now()
	if err := s.workerLists.SetSavedWorkerNotifyEnabled(user.UserID, hash, *parsed.Enabled, now); err != nil {
		logger.Warn("saved worker notify toggle failed", "error", err, "user_id", user.UserID)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	resp := struct {
		OK      bool `json:"ok"`
		Enabled bool `json:"enabled"`
	}{
		OK:      true,
		Enabled: *parsed.Enabled,
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	if out, err := sonic.Marshal(resp); err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	} else {
		_, _ = w.Write(out)
	}
}

func (s *StatusServer) handleDiscordNotifyEnabled(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user := ClerkUserFromContext(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.workerLists == nil {
		http.Error(w, "saved workers not enabled", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(s.Config().DiscordServerID) == "" || strings.TrimSpace(s.Config().DiscordBotToken) == "" || strings.TrimSpace(s.Config().DiscordNotifyChannelID) == "" {
		http.NotFound(w, r)
		return
	}

	type req struct {
		Enabled *bool `json:"enabled"`
	}
	var parsed req
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		_ = json.NewDecoder(r.Body).Decode(&parsed)
	} else {
		_ = r.ParseForm()
		if v := strings.TrimSpace(r.FormValue("enabled")); v != "" {
			b := v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "on") || strings.EqualFold(v, "yes")
			parsed.Enabled = &b
		}
	}
	if parsed.Enabled == nil {
		http.Error(w, "missing enabled", http.StatusBadRequest)
		return
	}

	now := time.Now()
	ok, err := s.workerLists.SetDiscordLinkEnabled(user.UserID, *parsed.Enabled, now)
	if err != nil {
		logger.Warn("discord notify toggle failed", "error", err, "user_id", user.UserID)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "not registered", http.StatusNotFound)
		return
	}

	resp := struct {
		OK      bool `json:"ok"`
		Enabled bool `json:"enabled"`
	}{
		OK:      true,
		Enabled: *parsed.Enabled,
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	if out, err := sonic.Marshal(resp); err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	} else {
		_, _ = w.Write(out)
	}
}

func (s *StatusServer) handleWorkerSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user := ClerkUserFromContext(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid submission", http.StatusBadRequest)
		return
	}
	worker := strings.TrimSpace(r.FormValue("worker"))
	if worker == "" {
		http.Redirect(w, r, "/worker", http.StatusSeeOther)
		return
	}
	if s.workerLists != nil {
		if err := s.workerLists.Add(user.UserID, worker); err != nil {
			logger.Warn("save worker name", "error", err, "user_id", user.UserID)
		}
	}
	http.Redirect(w, r, "/saved-workers", http.StatusSeeOther)
}

func (s *StatusServer) handleWorkerRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user := ClerkUserFromContext(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid submission", http.StatusBadRequest)
		return
	}
	worker := strings.TrimSpace(r.FormValue("worker"))
	if worker == "" {
		http.Redirect(w, r, "/worker", http.StatusSeeOther)
		return
	}
	if s.workerLists != nil {
		if err := s.workerLists.Remove(user.UserID, worker); err != nil {
			logger.Warn("remove worker name", "error", err, "user_id", user.UserID)
		}
	}
	http.Redirect(w, r, "/saved-workers", http.StatusSeeOther)
}

func (s *StatusServer) handleClerkLogout(w http.ResponseWriter, r *http.Request) {
	if s == nil {
		http.NotFound(w, r)
		return
	}
	redirect := safeRedirectPath(r.URL.Query().Get("redirect"))
	if redirect == "" {
		redirect = "/worker"
	}
	cookieName := strings.TrimSpace(s.Config().ClerkSessionCookieName)
	if s.clerk != nil {
		cookieName = strings.TrimSpace(s.clerk.SessionCookieName())
	}
	if cookieName == "" {
		cookieName = defaultClerkSessionCookieName
	}
	cookie := &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Unix(0, 0),
	}
	cookie.Secure = s.clerkCookieSecure(r)
	http.SetCookie(w, cookie)
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

func (s *StatusServer) handleClerkSessionRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s == nil || s.clerk == nil {
		http.NotFound(w, r)
		return
	}
	if strings.TrimSpace(s.Config().ClerkPublishableKey) == "" {
		http.NotFound(w, r)
		return
	}

	// Deny cross-site refresh attempts so only same-origin flows can replace the session cookie.
	if !isSameOriginRequest(r, s.canonicalStatusHost(r)) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	type req struct {
		Token string `json:"token"`
	}
	var parsed req
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		_ = json.NewDecoder(r.Body).Decode(&parsed)
	} else {
		_ = r.ParseForm()
		parsed.Token = r.FormValue("token")
	}
	token := strings.TrimSpace(parsed.Token)
	if token == "" {
		http.Error(w, "missing token", http.StatusBadRequest)
		return
	}

	claims, err := s.clerk.Verify(token)
	if err != nil || claims == nil || strings.TrimSpace(claims.Subject) == "" {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	cookie := &http.Cookie{
		Name:     s.clerk.SessionCookieName(),
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
	cookie.Secure = s.clerkCookieSecure(r)
	if claims.ExpiresAt != nil {
		cookie.Expires = claims.ExpiresAt.Time
	}
	http.SetCookie(w, cookie)

	resp := struct {
		OK        bool   `json:"ok"`
		ExpiresAt string `json:"expires_at,omitempty"`
	}{
		OK: true,
	}
	if claims.ExpiresAt != nil {
		resp.ExpiresAt = claims.ExpiresAt.Time.UTC().Format(time.RFC3339)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	if out, err := sonic.Marshal(resp); err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	} else {
		_, _ = w.Write(out)
	}
}

// handleWorkerStatusBySHA256 handles worker lookups using pre-computed SHA256 hashes.
// This endpoint expects the SHA256 hash of the worker name and performs direct lookup.
func (s *StatusServer) handleWorkerStatusBySHA256(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	base := s.baseTemplateData(start)

	// Best-effort derivation of the pool payout script so the UI can
	// label coinbase outputs by destination. Errors are treated as
	// "unknown" and do not affect normal worker stats rendering.
	var poolScriptHex string
	addr := strings.TrimSpace(s.Config().PayoutAddress)
	if addr != "" {
		if script, err := scriptForAddress(addr, ChainParams()); err == nil && len(script) > 0 {
			poolScriptHex = strings.ToLower(hex.EncodeToString(script))
		}
	}

	// Best-effort derivation of the donation script for 3-way payout display.
	var donationScriptHex string
	donationAddr := strings.TrimSpace(s.Config().OperatorDonationAddress)
	if donationAddr != "" && s.Config().OperatorDonationPercent > 0 {
		if script, err := scriptForAddress(donationAddr, ChainParams()); err == nil && len(script) > 0 {
			donationScriptHex = strings.ToLower(hex.EncodeToString(script))
		}
	}

	// Best-effort BTC price lookup for fiat hints on the worker page.
	var btcPrice float64
	var btcPriceUpdated string
	if s.priceSvc != nil {
		if price, err := s.priceSvc.BTCPrice(s.Config().FiatCurrency); err == nil && price > 0 {
			btcPrice = price
			if ts := s.priceSvc.LastUpdate(); !ts.IsZero() {
				btcPriceUpdated = ts.UTC().Format("2006-01-02 15:04:05 MST")
			}
		}
	}

	privacyMode := workerPrivacyModeFromRequest(r)
	data := WorkerStatusData{
		StatusData:        base,
		PoolScriptHex:     poolScriptHex,
		DonationScriptHex: donationScriptHex,
		PrivacyMode:       privacyMode,
	}
	data.BTCPriceFiat = btcPrice
	data.BTCPriceUpdatedAt = btcPriceUpdated
	s.enrichStatusDataWithClerk(r, &data.StatusData)

	var workerHash string
	switch r.Method {
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			data.Error = "invalid form submission"
			break
		}
		workerHash = strings.TrimSpace(r.FormValue("hash"))
	case http.MethodGet:
		workerHash = strings.TrimSpace(r.URL.Query().Get("hash"))
	}

	now := time.Now()

	if workerHash != "" {
		// Validate hash format (64 hex characters for SHA256)
		if len(workerHash) != 64 {
			data.Error = "invalid hash format (expected 64 hex characters)"
		} else {
			data.QueriedWorker = workerHash
			data.QueriedWorkerHash = workerHash

			// Cache HTML for this worker/privacymode keyed by SHA so repeated
			// refreshes avoid rebuilding the page. This is a best-effort cache:
			// failures simply fall back to on-demand rendering.
			cacheKey := workerHash
			if privacyMode {
				cacheKey = cacheKey + "|priv"
			}
			s.workerPageMu.RLock()
			if entry, ok := s.workerPageCache[cacheKey]; ok && now.Before(entry.expiresAt) {
				payload := entry.payload
				s.workerPageMu.RUnlock()
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = w.Write(payload)
				return
			}
			s.workerPageMu.RUnlock()

			if wv, ok := s.findWorkerViewByHash(workerHash); ok {
				setWorkerStatusView(&data, wv)
				if data.BTCPriceFiat > 0 {
					cur := strings.ToUpper(strings.TrimSpace(data.FiatCurrency))
					if cur == "" {
						cur = "USD"
					}
					base := fmt.Sprintf("Approximate fiat values (%s) use 1 BTC ≈ %.2f", cur, data.BTCPriceFiat)
					if data.BTCPriceUpdatedAt != "" {
						base += " (price updated " + data.BTCPriceUpdatedAt + ")"
					}
					data.FiatNote = base + "; payouts are always made in BTC."
				}
			} else if s.accounting != nil {
				if wv, ok := s.accounting.WorkerViewBySHA256(workerHash); ok {
					setWorkerStatusView(&data, wv)
					if data.BTCPriceFiat > 0 {
						cur := strings.ToUpper(strings.TrimSpace(data.FiatCurrency))
						if cur == "" {
							cur = "USD"
						}
						base := fmt.Sprintf("Approximate fiat values (%s) use 1 BTC ≈ %.2f", cur, data.BTCPriceFiat)
						if data.BTCPriceUpdatedAt != "" {
							base += " (price updated " + data.BTCPriceUpdatedAt + ")"
						}
						data.FiatNote = base + "; payouts are always made in BTC."
					}
				} else {
					data.Error = "worker not found"
				}
			} else {
				data.Error = "worker not found"
			}
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Render into a buffer so we can cache the HTML on success without
	// risking partial writes on template errors.
	buf := templateBufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer templateBufferPool.Put(buf)
	if err := s.tmpl.ExecuteTemplate(buf, "worker_status", data); err != nil {
		logger.Error("worker status template error", "error", err)
		s.renderErrorPage(w, r, http.StatusInternalServerError,
			"Worker page error",
			"We couldn't render the worker info page.",
			"Template error while rendering worker details.")
		return
	}
	// Cache successful renders keyed by worker hash and privacy mode.
	if workerHash != "" && len(workerHash) == 64 && data.Error == "" {
		cacheKey := workerHash
		if privacyMode {
			cacheKey = cacheKey + "|priv"
		}
		s.cacheWorkerPage(cacheKey, now, buf.Bytes())
	}
	_, _ = w.Write(buf.Bytes())
}

// cacheWorkerPage stores a rendered worker-status HTML payload in the
// workerPageCache, enforcing the workerPageCacheLimit and pruning expired
// entries when necessary.
func (s *StatusServer) cacheWorkerPage(key string, now time.Time, payload []byte) {
	entry := cachedWorkerPage{
		payload:   append([]byte(nil), payload...),
		updatedAt: now,
		expiresAt: now.Add(overviewRefreshInterval),
	}
	s.workerPageMu.Lock()
	defer s.workerPageMu.Unlock()
	if s.workerPageCache == nil {
		s.workerPageCache = make(map[string]cachedWorkerPage)
	}
	// Enforce a simple size bound: evict expired entries first, then
	// fall back to removing an arbitrary entry if still at capacity.
	if workerPageCacheLimit > 0 && len(s.workerPageCache) >= workerPageCacheLimit {
		for k, v := range s.workerPageCache {
			if now.After(v.expiresAt) {
				delete(s.workerPageCache, k)
				break
			}
		}
		if len(s.workerPageCache) >= workerPageCacheLimit {
			for k := range s.workerPageCache {
				delete(s.workerPageCache, k)
				break
			}
		}
	}
	s.workerPageCache[key] = entry
}

// handleWorkerLookup is a generic handler that extracts a worker identifier from the URL path
// and performs a direct worker lookup. It supports multiple URL patterns for miner software:
// - /user/{worker}
// - /users/{worker}
// - /stats/{worker}
// - /app/{worker}
// The worker identifier is used directly for lookup (plaintext worker name).
func (s *StatusServer) handleWorkerLookup(w http.ResponseWriter, r *http.Request, prefix string) {
	host := remoteHostFromRequest(r)
	if !s.workerLookupLimiter.allow(host) {
		http.Error(w, "Worker lookup rate limit exceeded; try again later.", http.StatusTooManyRequests)
		return
	}

	start := time.Now()
	base := s.statusDataView()

	// Best-effort derivation of the pool payout script so the UI can
	// label coinbase outputs by destination. Errors are treated as
	// "unknown" and do not affect normal worker stats rendering.
	var poolScriptHex string
	addr := strings.TrimSpace(s.Config().PayoutAddress)
	if addr != "" {
		if script, err := scriptForAddress(addr, ChainParams()); err == nil && len(script) > 0 {
			poolScriptHex = strings.ToLower(hex.EncodeToString(script))
		}
	}

	// Best-effort derivation of the donation script for 3-way payout display.
	var donationScriptHex string
	donationAddr := strings.TrimSpace(s.Config().OperatorDonationAddress)
	if donationAddr != "" && s.Config().OperatorDonationPercent > 0 {
		if script, err := scriptForAddress(donationAddr, ChainParams()); err == nil && len(script) > 0 {
			donationScriptHex = strings.ToLower(hex.EncodeToString(script))
		}
	}

	privacyMode := workerPrivacyModeFromRequest(r)
	data := WorkerStatusData{
		StatusData:        base,
		PoolScriptHex:     poolScriptHex,
		DonationScriptHex: donationScriptHex,
		PrivacyMode:       privacyMode,
	}
	data.RenderDuration = time.Since(start)

	// Extract worker ID from path after the prefix
	path := strings.TrimPrefix(r.URL.Path, prefix)
	path = strings.TrimPrefix(path, "/")
	workerID := strings.TrimSpace(path)

	if len(workerID) > workerLookupMaxBytes {
		s.renderErrorPage(w, r, http.StatusBadRequest,
			"Worker ID too long",
			fmt.Sprintf("Worker identifiers are limited to %d bytes.", workerLookupMaxBytes),
			"Please shorten the worker name and try again.")
		return
	}

	if workerID == "" {
		s.renderErrorPage(w, r, http.StatusBadRequest,
			"Missing worker ID",
			"Please provide a worker identifier in the URL.",
			"Example: "+prefix+"/worker_name")
		return
	}

	hashSum := sha256Sum([]byte(workerID))
	data.QueriedWorkerHash = fmt.Sprintf("%x", hashSum[:])

	data.QueriedWorker = workerID
	found := false
	if wv, ok := s.findWorkerViewByHash(data.QueriedWorkerHash); ok {
		setWorkerStatusView(&data, wv)
		found = true
	} else if s.accounting != nil {
		if wv, ok := s.accounting.WorkerViewBySHA256(data.QueriedWorkerHash); ok {
			setWorkerStatusView(&data, wv)
			found = true
		}
	}
	if found {
		if data.BTCPriceFiat > 0 {
			cur := strings.ToUpper(strings.TrimSpace(data.FiatCurrency))
			if cur == "" {
				cur = "USD"
			}
			base := fmt.Sprintf("Approximate fiat values (%s) use 1 BTC ≈ %.2f", cur, data.BTCPriceFiat)
			if data.BTCPriceUpdatedAt != "" {
				base += " (price updated " + data.BTCPriceUpdatedAt + ")"
			}
			data.FiatNote = base + "; payouts are always made in BTC."
		}
	} else {
		data.Error = "worker not found"
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "worker_status", data); err != nil {
		logger.Error("worker status template error", "error", err)
		s.renderErrorPage(w, r, http.StatusInternalServerError,
			"Worker page error",
			"We couldn't render the worker info page.",
			"Template error while rendering worker details.")
	}
}

// handleServerInfoPage renders the public server information page with backend status and diagnostics.
func (s *StatusServer) handleServerInfoPage(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	data := s.baseTemplateData(start)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "server", data); err != nil {
		logger.Error("server info template error", "error", err)
		s.renderErrorPage(w, r, http.StatusInternalServerError,
			"Server info page error",
			"We couldn't render the server information page.",
			"Template error while rendering the server info view.")
	}
}

// handlePoolInfo renders a pool configuration/limits summary page. It exposes
// only non-secret settings and aggregate stats that are safe to share.
func (s *StatusServer) handlePoolInfo(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	data := s.baseTemplateData(start)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "pool", data); err != nil {
		logger.Error("pool info template error", "error", err)
		s.renderErrorPage(w, r, http.StatusInternalServerError,
			"Pool info error",
			"We couldn't render the pool configuration page.",
			"Template error while rendering the pool info view.")
	}
}

func (s *StatusServer) handleAboutPage(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	data := s.baseTemplateData(start)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "about", data); err != nil {
		logger.Error("about page template error", "error", err)
		s.renderErrorPage(w, r, http.StatusInternalServerError,
			"About page error",
			"We couldn't render the about page.",
			"Template error while rendering the about page view.")
	}
}
