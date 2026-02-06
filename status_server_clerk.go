package main

import (
	"net/http"
	"net/url"
	"strings"
	"time"
)

func (s *StatusServer) withClerkUser(h http.HandlerFunc) http.HandlerFunc {
	if s == nil || s.clerk == nil {
		return h
	}
	return func(w http.ResponseWriter, r *http.Request) {
		user := s.clerkUserFromRequest(r)
		if user != nil {
			if s.workerLists != nil {
				_ = s.workerLists.RecordClerkUserSeen(user.UserID, time.Now())
			}
			r = r.WithContext(contextWithClerkUser(r.Context(), user))
		}
		h(w, r)
	}
}

func (s *StatusServer) storeStatusPublicURL(raw string) {
	parsed := parseStatusPublicURL(raw)
	s.statusPublicURL.Store(parsed)
}

func parseStatusPublicURL(raw string) *url.URL {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		logger.Warn("invalid status_public_url", "url", raw, "error", err)
		return nil
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		logger.Warn("invalid status_public_url", "url", raw, "error", "missing scheme or host")
		return nil
	}
	return parsed
}

func (s *StatusServer) getStatusPublicURL() *url.URL {
	if s == nil {
		return nil
	}
	if v := s.statusPublicURL.Load(); v != nil {
		if u, ok := v.(*url.URL); ok {
			return u
		}
	}
	return nil
}

func (s *StatusServer) baseURLForRequest(r *http.Request) *url.URL {
	if s == nil {
		return nil
	}
	if parsed := s.getStatusPublicURL(); parsed != nil {
		return parsed
	}
	if r == nil {
		return nil
	}
	scheme := "http"
	if proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); proto != "" {
		scheme = strings.ToLower(proto)
	} else if r.TLS != nil {
		scheme = "https"
	}
	host := strings.TrimSpace(r.Host)
	if host == "" {
		host = "localhost"
	}
	return &url.URL{
		Scheme: scheme,
		Host:   host,
	}
}

func (s *StatusServer) canonicalStatusHost(r *http.Request) string {
	if parsed := s.getStatusPublicURL(); parsed != nil && parsed.Host != "" {
		return parsed.Host
	}
	if r == nil {
		return "localhost"
	}
	host := strings.TrimSpace(r.Host)
	if host == "" {
		host = "localhost"
	}
	return host
}

func (s *StatusServer) httpsRedirectURL(r *http.Request) string {
	if r == nil {
		return ""
	}
	host := s.canonicalStatusHost(r)
	base := s.baseURLForRequest(r)
	redirectBase := &url.URL{
		Scheme: "https",
		Host:   host,
	}
	if base != nil && strings.TrimSpace(base.Host) != "" {
		redirectBase.Host = base.Host
	}

	path := r.URL.Path
	if path == "" {
		path = "/"
	}
	ref := &url.URL{
		Path:     path,
		RawQuery: r.URL.RawQuery,
		Fragment: r.URL.Fragment,
	}

	target := redirectBase.ResolveReference(ref)
	if target.Scheme == "" {
		target.Scheme = "https"
	}
	if target.Host == "" {
		target.Host = host
	}
	return target.String()
}

func (s *StatusServer) redirectToHTTPS(w http.ResponseWriter, r *http.Request) {
	target := s.httpsRedirectURL(r)
	if target == "" {
		http.Error(w, "Redirect target unavailable", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, target, http.StatusTemporaryRedirect)
}

func (s *StatusServer) clerkCookieSecure(r *http.Request) bool {
	if r == nil {
		return false
	}
	if base := s.baseURLForRequest(r); base != nil {
		return strings.EqualFold(base.Scheme, "https")
	}
	if proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); proto != "" {
		return strings.EqualFold(proto, "https")
	}
	return r.TLS != nil
}

func (s *StatusServer) clerkUserFromRequest(r *http.Request) *ClerkUser {
	if s == nil || s.clerk == nil {
		return nil
	}
	cookie, err := r.Cookie(s.clerk.SessionCookieName())
	if err != nil {
		if err != http.ErrNoCookie {
			logger.Warn("failed to read session cookie", "error", err, "remote_addr", r.RemoteAddr)
		}
		return nil
	}
	claims, err := s.clerk.Verify(cookie.Value)
	if err != nil {
		// In dev/test Clerk environments, tokens expiring frequently is common
		// and can create noisy logs (e.g. saved-workers polling). Silence these
		// verification warnings when using test keys.
		secret := strings.TrimSpace(s.Config().ClerkSecretKey)
		publishable := strings.TrimSpace(s.Config().ClerkPublishableKey)
		if strings.HasPrefix(secret, "sk_test_") || strings.HasPrefix(publishable, "pk_test_") {
			logger.Debug("clerk session verification failed (test keys)", "error", err, "remote_addr", r.RemoteAddr)
			return nil
		}
		logger.Warn("clerk session verification failed", "error", err, "remote_addr", r.RemoteAddr)
		return nil
	}
	return &ClerkUser{
		UserID:    claims.Subject,
		SessionID: claims.SessionID,
	}
}

func (s *StatusServer) clerkUIEnabled() bool {
	if forceClerkLoginUIForTesting {
		return true
	}
	if s == nil || s.clerk == nil {
		return false
	}
	// The worker login page's embedded sign-in experience requires a publishable
	// key from secrets.toml; when it's missing, hide the sign-in box.
	return strings.TrimSpace(s.Config().ClerkPublishableKey) != ""
}

func (s *StatusServer) enrichStatusDataWithClerk(r *http.Request, data *StatusData) {
	if s == nil || data == nil {
		return
	}
	data.ClerkEnabled = s.clerkUIEnabled()
	if !data.ClerkEnabled {
		return
	}
	redirect := safeRedirectPath(r.URL.Query().Get("redirect"))
	if redirect == "" {
		redirect = "/saved-workers"
	}
	data.ClerkLoginURL = s.clerkLoginURL(r, redirect)
	if user := ClerkUserFromContext(r.Context()); user != nil {
		data.ClerkUser = user
		if s.workerLists != nil {
			if list, err := s.workerLists.List(user.UserID); err == nil {
				data.SavedWorkers = list
			} else {
				logger.Warn("load saved workers", "error", err, "user_id", user.UserID)
			}
			if data.DiscordNotificationsEnabled {
				if _, enabled, ok, err := s.workerLists.GetDiscordLink(user.UserID); err == nil {
					data.DiscordNotificationsRegistered = ok
					data.DiscordNotificationsUserEnabled = ok && enabled
				}
			}
		}
	}
}

func (s *StatusServer) clerkLoginURL(r *http.Request, redirect string) string {
	if s == nil {
		return ""
	}
	redirectURL := s.clerkRedirectURL(r, redirect)
	if s.clerk != nil {
		callbackRedirect := s.clerkRedirectURL(r, s.clerk.CallbackPath())
		login := s.clerk.LoginURL(callbackRedirect, s.Config().ClerkFrontendAPIURL)
		if redirect == "" {
			return login
		}
		sep := "?"
		if strings.Contains(login, "?") {
			sep = "&"
		}
		return login + sep + "redirect=" + url.QueryEscape(redirect)
	}

	base := strings.TrimSpace(s.Config().ClerkSignInURL)
	if base == "" {
		base = defaultClerkSignInURL
	}
	values := url.Values{}
	values.Set("redirect_url", redirectURL)
	if frontendAPI := strings.TrimSpace(s.Config().ClerkFrontendAPIURL); frontendAPI != "" {
		values.Set("frontend_api", frontendAPI)
	}
	return base + "?" + values.Encode()
}

func (s *StatusServer) clerkRedirectURL(r *http.Request, redirect string) string {
	if s == nil {
		return "/worker"
	}
	redirectPath := safeRedirectPath(redirect)
	if redirectPath == "" {
		redirectPath = "/worker"
	}
	base := s.baseURLForRequest(r)
	if base == nil {
		return redirectPath
	}
	ref := &url.URL{Path: redirectPath}
	return base.ResolveReference(ref).String()
}
