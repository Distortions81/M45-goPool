package main

import (
	"strings"
	"time"

	"github.com/martinhoefling/goxkcdpwgen/xkcdpwgen"
)

const oneTimeCodeTTL = 5 * time.Minute

type oneTimeCodeEntry struct {
	Code      string
	ExpiresAt time.Time
}

func generateOneTimeCodeXKCD() string {
	g := xkcdpwgen.NewGenerator()
	g.SetNumWords(3)
	g.SetCapitalize(false)
	g.SetDelimiter("-")
	return strings.TrimSpace(g.GeneratePasswordString())
}

func (s *StatusServer) initOneTimeCodesLocked() {
	if s.oneTimeCodes == nil {
		s.oneTimeCodes = make(map[string]oneTimeCodeEntry)
	}
}

func (s *StatusServer) cleanupExpiredOneTimeCodesLocked(now time.Time) {
	for userID, entry := range s.oneTimeCodes {
		if entry.Code == "" || now.After(entry.ExpiresAt) {
			delete(s.oneTimeCodes, userID)
		}
	}
}

func (s *StatusServer) getOrCreateOneTimeCode(userID string, now time.Time) (code string, expiresAt time.Time) {
	if s == nil || strings.TrimSpace(userID) == "" {
		return "", time.Time{}
	}

	s.oneTimeCodeMu.Lock()
	defer s.oneTimeCodeMu.Unlock()

	s.initOneTimeCodesLocked()
	s.cleanupExpiredOneTimeCodesLocked(now)

	if existing, ok := s.oneTimeCodes[userID]; ok && existing.Code != "" && now.Before(existing.ExpiresAt) {
		return existing.Code, existing.ExpiresAt
	}

	// Best-effort uniqueness and non-empty output.
	for i := 0; i < 5; i++ {
		code = generateOneTimeCodeXKCD()
		if code != "" {
			break
		}
	}
	if code == "" {
		return "", time.Time{}
	}

	expiresAt = now.Add(oneTimeCodeTTL)
	s.oneTimeCodes[userID] = oneTimeCodeEntry{
		Code:      code,
		ExpiresAt: expiresAt,
	}
	return code, expiresAt
}

func (s *StatusServer) clearOneTimeCode(userID, code string, now time.Time) bool {
	if s == nil || strings.TrimSpace(userID) == "" || strings.TrimSpace(code) == "" {
		return false
	}

	s.oneTimeCodeMu.Lock()
	defer s.oneTimeCodeMu.Unlock()

	s.initOneTimeCodesLocked()
	s.cleanupExpiredOneTimeCodesLocked(now)

	entry, ok := s.oneTimeCodes[userID]
	if !ok {
		return false
	}
	if entry.Code != code {
		return false
	}
	delete(s.oneTimeCodes, userID)
	return true
}

func (s *StatusServer) redeemOneTimeCode(code string, now time.Time) (userID string, ok bool) {
	if s == nil {
		return "", false
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return "", false
	}

	s.oneTimeCodeMu.Lock()
	defer s.oneTimeCodeMu.Unlock()

	s.initOneTimeCodesLocked()
	s.cleanupExpiredOneTimeCodesLocked(now)

	for uid, entry := range s.oneTimeCodes {
		if entry.Code != code {
			continue
		}
		if entry.ExpiresAt.IsZero() || now.After(entry.ExpiresAt) {
			delete(s.oneTimeCodes, uid)
			return "", false
		}
		delete(s.oneTimeCodes, uid)
		return uid, true
	}
	return "", false
}
