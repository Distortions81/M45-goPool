package main

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bytedance/sonic"
)

func safeRedirectPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if !strings.HasPrefix(value, "/") {
		return ""
	}
	if strings.ContainsAny(value, "\n\r") {
		return ""
	}
	return value
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
				if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
					cookie.Secure = strings.EqualFold(proto, "https")
				} else {
					cookie.Secure = r.TLS != nil
				}
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
		Hashrate          float64
		ShareRate         float64
		Accepted          uint64
		Difficulty        float64
		ConnectedDuration time.Duration
	}
	data := struct {
		StatusData
		OnlineWorkerEntries  []savedWorkerEntry
		OfflineWorkerEntries []savedWorkerEntry
		SavedWorkersCount    int
		SavedWorkersOnline   int
		SavedWorkersMax      int
	}{StatusData: base}
	s.enrichStatusDataWithClerk(r, &data.StatusData)

	if data.ClerkUser == nil {
		http.Redirect(w, r, "/worker", http.StatusSeeOther)
		return
	}

	data.SavedWorkersMax = maxSavedWorkersPerUser
	data.SavedWorkersCount = len(data.SavedWorkers)
	now := time.Now()
	for _, worker := range data.SavedWorkers {
		view, online := s.findWorkerViewByName(worker, now)
		hashrate := view.RollingHashrate
		if hashrate <= 0 && view.ShareRate > 0 && view.Difficulty > 0 {
			hashrate = (view.Difficulty * hashPerShare * view.ShareRate) / 60.0
		}
		duration := time.Since(view.ConnectedAt)
		if duration < 0 {
			duration = 0
		}
		hashSum := sha256Sum([]byte(worker))
		entry := savedWorkerEntry{
			Name:              worker,
			Hash:              fmt.Sprintf("%x", hashSum[:]),
			Hashrate:          hashrate,
			ShareRate:         view.ShareRate,
			Accepted:          view.Accepted,
			Difficulty:        view.Difficulty,
			ConnectedDuration: duration,
		}
		if online {
			data.SavedWorkersOnline++
			data.OnlineWorkerEntries = append(data.OnlineWorkerEntries, entry)
		} else {
			data.OfflineWorkerEntries = append(data.OfflineWorkerEntries, entry)
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

	saved := []string(nil)
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
		Hashrate                  float64 `json:"hashrate"`
		SharesPerMinute           float64 `json:"shares_per_minute"`
		Accepted                  uint64  `json:"accepted"`
		Difficulty                float64 `json:"difficulty"`
		ConnectionSeq             uint64  `json:"connection_seq,omitempty"`
		ConnectionDurationSeconds float64 `json:"connection_duration_seconds,omitempty"`
	}
	now := time.Now()
	resp := struct {
		UpdatedAt      string  `json:"updated_at"`
		SavedMax       int     `json:"saved_max"`
		SavedCount     int     `json:"saved_count"`
		OnlineCount    int     `json:"online_count"`
		OnlineWorkers  []entry `json:"online_workers"`
		OfflineWorkers []entry `json:"offline_workers"`
	}{
		UpdatedAt:  now.UTC().Format(time.RFC3339),
		SavedMax:   maxSavedWorkersPerUser,
		SavedCount: len(saved),
	}

	for _, worker := range saved {
		view, online := s.findWorkerViewByName(worker, now)
		hashrate := view.RollingHashrate
		if hashrate <= 0 && view.ShareRate > 0 && view.Difficulty > 0 {
			hashrate = (view.Difficulty * hashPerShare * view.ShareRate) / 60.0
		}
		hashSum := sha256Sum([]byte(worker))
		connectionDurationSeconds := 0.0
		if online && !view.ConnectedAt.IsZero() {
			connectionDurationSeconds = now.Sub(view.ConnectedAt).Seconds()
			if connectionDurationSeconds < 0 {
				connectionDurationSeconds = 0
			}
		}
		e := entry{
			Name:                      worker,
			Hash:                      fmt.Sprintf("%x", hashSum[:]),
			Online:                    online,
			Hashrate:                  hashrate,
			SharesPerMinute:           view.ShareRate,
			Accepted:                  view.Accepted,
			Difficulty:                view.Difficulty,
			ConnectionSeq:             view.ConnectionSeq,
			ConnectionDurationSeconds: connectionDurationSeconds,
		}
		if online {
			resp.OnlineCount++
			resp.OnlineWorkers = append(resp.OnlineWorkers, e)
		} else {
			resp.OfflineWorkers = append(resp.OfflineWorkers, e)
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
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		cookie.Secure = strings.EqualFold(proto, "https")
	} else {
		cookie.Secure = r.TLS != nil
	}
	http.SetCookie(w, cookie)
	http.Redirect(w, r, redirect, http.StatusSeeOther)
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

			if wv, ok := s.findWorkerViewByHash(workerHash, now); ok {
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
	base := s.buildStatusData()
	base.RenderDuration = time.Since(start)

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
	now := time.Now()
	found := false
	if wv, ok := s.findWorkerViewByName(workerID, now); ok {
		setWorkerStatusView(&data, wv)
		found = true
	} else if s.accounting != nil {
		if wv, ok := s.accounting.WorkerViewByName(workerID); ok {
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
