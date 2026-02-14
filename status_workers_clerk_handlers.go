package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/bytedance/sonic"
)

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

	s.setClerkSessionCookie(w, r, token, claims)

	resp := struct {
		OK        bool   `json:"ok"`
		ExpiresAt string `json:"expires_at,omitempty"`
	}{
		OK: true,
	}
	if claims.ExpiresAt != nil {
		resp.ExpiresAt = claims.ExpiresAt.Time.UTC().Format(time.RFC3339)
	}
	setShortJSONCacheHeaders(w, true)
	if out, err := sonic.Marshal(resp); err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	} else {
		_, _ = w.Write(out)
	}
}

func (s *StatusServer) setClerkSessionCookie(w http.ResponseWriter, r *http.Request, token string, claims *ClerkSessionClaims) {
	if s == nil || s.clerk == nil || token == "" {
		return
	}
	if claims == nil || strings.TrimSpace(claims.Subject) == "" {
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
}
