package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func (s *StatusServer) isAdminAuthenticated(r *http.Request) bool {
	token, ok := s.adminSessionToken(r)
	if !ok {
		s.pruneExpiredAdminSessions()
		return false
	}
	s.adminSessionsMu.Lock()
	expiry, exists := s.adminSessions[token]
	if !exists {
		s.adminSessionsMu.Unlock()
		s.pruneExpiredAdminSessions()
		return false
	}
	if time.Now().After(expiry) {
		delete(s.adminSessions, token)
		s.adminSessionsMu.Unlock()
		s.pruneExpiredAdminSessions()
		return false
	}
	s.adminSessionsMu.Unlock()
	return true
}

func (s *StatusServer) adminSessionToken(r *http.Request) (string, bool) {
	if r == nil {
		return "", false
	}
	cookie, err := r.Cookie(adminSessionCookieName)
	if err != nil {
		return "", false
	}
	if cookie.Value == "" {
		return "", false
	}
	return cookie.Value, true
}

func (s *StatusServer) createAdminSession(duration time.Duration) (string, time.Time, error) {
	if duration <= 0 {
		duration = time.Duration(defaultAdminSessionExpirationSeconds) * time.Second
	}
	token, err := generateAdminToken()
	if err != nil {
		return "", time.Time{}, err
	}
	expiry := time.Now().Add(duration)
	s.adminSessionsMu.Lock()
	s.adminSessions[token] = expiry
	s.adminSessionsMu.Unlock()
	return token, expiry, nil
}

func (s *StatusServer) pruneExpiredAdminSessions() {
	if s == nil {
		return
	}
	now := time.Now()
	s.adminSessionsMu.Lock()
	for token, expiry := range s.adminSessions {
		if now.After(expiry) {
			delete(s.adminSessions, token)
		}
	}
	s.adminSessionsMu.Unlock()
}

func generateAdminToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func (s *StatusServer) adminCredentialsMatch(cfg adminFileConfig, username, password string) bool {
	if cfg.Username == "" && cfg.Password == "" {
		return false
	}
	if !compareStringsConstantTime(cfg.Username, strings.TrimSpace(username)) {
		return false
	}
	return s.adminPasswordMatches(cfg, password)
}

func (s *StatusServer) adminPasswordMatches(cfg adminFileConfig, password string) bool {
	hash := strings.TrimSpace(cfg.PasswordSHA256)
	if hash != "" {
		return compareStringsConstantTime(hash, adminPasswordHash(password))
	}
	return compareStringsConstantTime(cfg.Password, password)
}

func compareStringsConstantTime(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func (s *StatusServer) invalidateAdminSession(token string) {
	if token == "" {
		return
	}
	s.adminSessionsMu.Lock()
	delete(s.adminSessions, token)
	s.adminSessionsMu.Unlock()
}

func (s *StatusServer) scrubAdminPasswordPlaintext(cfg adminFileConfig) error {
	if s == nil {
		return fmt.Errorf("status server is nil")
	}
	if cfg.Password == "" {
		return nil
	}
	if cfg.PasswordSHA256 == "" {
		cfg.PasswordSHA256 = adminPasswordHash(cfg.Password)
	}
	cfg.Password = ""
	return atomicWriteFile(s.adminConfigPath, []byte(renderAdminConfig(cfg)))
}
