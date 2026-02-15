package main

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

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

	if err := s.serveCachedHTML(w, "page_worker_login", func() ([]byte, error) {
		var buf bytes.Buffer
		if err := s.executeTemplate(&buf, "worker_login", data); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}); err != nil {
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

	hash, errMsg := parseOrDeriveSHA256(r.URL.Query().Get("hash"), r.URL.Query().Get("wallet"))
	data.QueriedWalletHash = hash

	if errMsg != "" {
		data.Error = errMsg
	} else if hash == "" {
		data.Error = "Please provide a wallet address or wallet hash to search."
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

	setShortHTMLCacheHeaders(w, true)
	if err := s.executeTemplate(w, "worker_wallet_search", data); err != nil {
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
	if !clerkConfigured(s.Config()) || s.clerk == nil {
		http.Redirect(w, r, "/worker", http.StatusSeeOther)
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
	if sessionToken := strings.TrimSpace(r.URL.Query().Get("session_token")); sessionToken != "" {
		claims, err := s.clerk.Verify(sessionToken)
		if err == nil && claims != nil && claims.Subject != "" {
			s.setClerkSessionCookie(w, r, sessionToken, claims)
			http.Redirect(w, r, redirect, http.StatusSeeOther)
			return
		}
		if err != nil {
			logger.Warn("clerk callback verify failed", "error", err)
		} else {
			logger.Warn("clerk callback verify failed", "reason", "missing session claims")
		}
	}
	if s.clerkUserFromRequest(r) == nil {
		if devBrowserJWT := strings.TrimSpace(r.URL.Query().Get(clerkDevBrowserJWTQueryParam)); devBrowserJWT != "" {
			if jwtToken, claims, err := s.clerk.ExchangeDevBrowserJWT(r.Context(), devBrowserJWT); err != nil {
				logger.Warn("clerk dev browser exchange failed", "error", err)
			} else {
				s.setClerkSessionCookie(w, r, jwtToken, claims)
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
	if pk == "" || !clerkConfigured(s.Config()) || s.clerk == nil {
		s.renderErrorPage(w, r, http.StatusInternalServerError,
			"Sign-in misconfigured",
			"Sign-in is not configured on this server.",
			"clerk_secret_key, clerk_publishable_key, and auth.clerk_frontend_api_url are required.")
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

	// Cache the default sign-in page persistently; alternate redirects are
	// rendered on demand to avoid unbounded cache keys from query params.
	if redirect == "/saved-workers" {
		if err := s.serveCachedHTML(w, "page_sign_in_default", func() ([]byte, error) {
			var buf bytes.Buffer
			if err := s.executeTemplate(&buf, "sign_in", data); err != nil {
				return nil, err
			}
			return buf.Bytes(), nil
		}); err != nil {
			logger.Error("sign in template error", "error", err)
			s.renderErrorPage(w, r, http.StatusInternalServerError,
				"Sign-in page error",
				"We couldn't render the sign-in page.",
				"Template error while rendering sign-in.")
		}
		return
	}

	setShortHTMLCacheHeaders(w, true)
	if err := s.executeTemplate(w, "sign_in", data); err != nil {
		logger.Error("sign in template error", "error", err)
		s.renderErrorPage(w, r, http.StatusInternalServerError,
			"Sign-in page error",
			"We couldn't render the sign-in page.",
			"Template error while rendering sign-in.")
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
	var workerValue string
	switch r.Method {
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			data.Error = "invalid form submission"
			break
		}
		workerHash = strings.TrimSpace(r.FormValue("hash"))
		workerValue = strings.TrimSpace(r.FormValue("worker"))
	case http.MethodGet:
		workerHash = strings.TrimSpace(r.URL.Query().Get("hash"))
		workerValue = strings.TrimSpace(r.URL.Query().Get("worker"))
	}
	if workerHash == "" && workerValue != "" {
		if derived, errMsg := parseOrDeriveSHA256("", workerValue); errMsg != "" {
			data.Error = strings.ToLower(errMsg)
		} else {
			workerHash = derived
		}
	}

	now := time.Now()
	var curJob *Job
	if s.jobMgr != nil {
		curJob = s.jobMgr.CurrentJob()
	}

	if workerHash != "" {
		// Validate hash format (64 hex characters for SHA256)
		workerHash = strings.TrimSpace(strings.ToLower(workerHash))
		if len(workerHash) != 64 {
			data.Error = "invalid hash format (expected 64 hex characters)"
		} else if _, err := hex.DecodeString(workerHash); err != nil {
			data.Error = "invalid hash value"
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
			// Include the current job ID in the cache key so the worker page
			// updates immediately when new work arrives (coinbase changes per job).
			if curJob != nil && strings.TrimSpace(curJob.JobID) != "" {
				cacheKey = cacheKey + "|job:" + strings.TrimSpace(curJob.JobID)
			}
			s.workerPageMu.RLock()
			if entry, ok := s.workerPageCache[cacheKey]; ok && now.Before(entry.expiresAt) {
				payload := entry.payload
				s.workerPageMu.RUnlock()
				setShortHTMLCacheHeaders(w, true)
				_, _ = w.Write(payload)
				return
			}
			s.workerPageMu.RUnlock()

			if wv, ok := s.findWorkerViewByHash(workerHash); ok {
				setWorkerStatusView(&data, wv)
				s.setWorkerCurrentJobCoinbase(&data, curJob, wv)
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
					s.setWorkerCurrentJobCoinbase(&data, curJob, wv)
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

	setShortHTMLCacheHeaders(w, true)
	// Render into a buffer so we can cache the HTML on success without
	// risking partial writes on template errors.
	var buf bytes.Buffer
	if err := s.executeTemplate(&buf, "worker_status", data); err != nil {
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
		if curJob != nil && strings.TrimSpace(curJob.JobID) != "" {
			cacheKey = cacheKey + "|job:" + strings.TrimSpace(curJob.JobID)
		}
		s.cacheWorkerPage(cacheKey, now, buf.Bytes())
	}
	_, _ = w.Write(buf.Bytes())
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

	setShortHTMLCacheHeaders(w, true)
	if err := s.executeTemplate(w, "worker_status", data); err != nil {
		logger.Error("worker status template error", "error", err)
		s.renderErrorPage(w, r, http.StatusInternalServerError,
			"Worker page error",
			"We couldn't render the worker info page.",
			"Template error while rendering worker details.")
	}
}
