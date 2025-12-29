package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/martinhoefling/goxkcdpwgen/xkcdpwgen"
)

const oneTimeCodeTTL = 5 * time.Minute
const maxOneTimeCodesInMemory = 100

type oneTimeCodeEntry struct {
	Code      string
	CreatedAt time.Time
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

	s.evictOneTimeCodesLocked(now)

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
		CreatedAt: now,
		ExpiresAt: expiresAt,
	}
	return code, expiresAt
}

func (s *StatusServer) createNewOneTimeCode(userID string, now time.Time) (code string, expiresAt time.Time) {
	if s == nil || strings.TrimSpace(userID) == "" {
		return "", time.Time{}
	}

	s.oneTimeCodeMu.Lock()
	defer s.oneTimeCodeMu.Unlock()

	s.initOneTimeCodesLocked()
	s.cleanupExpiredOneTimeCodesLocked(now)

	// Explicitly invalidate any existing unused code for this user.
	delete(s.oneTimeCodes, userID)

	s.evictOneTimeCodesLocked(now)

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
		CreatedAt: now,
		ExpiresAt: expiresAt,
	}
	return code, expiresAt
}

func (s *StatusServer) evictOneTimeCodesLocked(now time.Time) {
	if s == nil {
		return
	}
	s.cleanupExpiredOneTimeCodesLocked(now)
	if len(s.oneTimeCodes) < maxOneTimeCodesInMemory {
		return
	}
	// Evict oldest entries until under the cap. Since the cap is small (100),
	// a linear scan is fine.
	for len(s.oneTimeCodes) >= maxOneTimeCodesInMemory {
		var oldestUserID string
		var oldestTime time.Time
		for uid, entry := range s.oneTimeCodes {
			t := entry.CreatedAt
			if t.IsZero() {
				t = entry.ExpiresAt
			}
			if oldestUserID == "" || (!t.IsZero() && (oldestTime.IsZero() || t.Before(oldestTime))) {
				oldestUserID = uid
				oldestTime = t
			}
		}
		if oldestUserID == "" {
			break
		}
		delete(s.oneTimeCodes, oldestUserID)
	}
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

func (s *StatusServer) startOneTimeCodeJanitor(ctx context.Context) {
	if s == nil || ctx == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.oneTimeCodeMu.Lock()
				s.initOneTimeCodesLocked()
				s.cleanupExpiredOneTimeCodesLocked(time.Now())
				s.oneTimeCodeMu.Unlock()
			}
		}
	}()
}

type oneTimeCodePersistEntry struct {
	UserID    string    `json:"user_id"`
	Code      string    `json:"code"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

type oneTimeCodePersistPayload struct {
	Version int                       `json:"version"`
	SavedAt time.Time                 `json:"saved_at"`
	Codes   []oneTimeCodePersistEntry `json:"codes"`
}

func (s *StatusServer) oneTimeCodePersistPath(dataDir string) string {
	if strings.TrimSpace(dataDir) == "" {
		dataDir = defaultDataDir
	}
	return filepath.Join(dataDir, "state", "one_time_codes.json")
}

func (s *StatusServer) loadOneTimeCodesFromDisk(dataDir string) {
	if s == nil {
		return
	}
	path := s.oneTimeCodePersistPath(dataDir)
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var payload oneTimeCodePersistPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		logger.Warn("one-time code state load failed", "error", err, "path", path)
		return
	}
	// Delete after successful read so we don't keep stale state around.
	_ = os.Remove(path)

	now := time.Now()
	s.oneTimeCodeMu.Lock()
	defer s.oneTimeCodeMu.Unlock()

	s.initOneTimeCodesLocked()
	s.cleanupExpiredOneTimeCodesLocked(now)
	s.evictOneTimeCodesLocked(now)

	for _, e := range payload.Codes {
		uid := strings.TrimSpace(e.UserID)
		code := strings.TrimSpace(e.Code)
		if uid == "" || code == "" {
			continue
		}
		if e.ExpiresAt.IsZero() || now.After(e.ExpiresAt) {
			continue
		}
		if _, exists := s.oneTimeCodes[uid]; exists {
			continue
		}
		if len(s.oneTimeCodes) >= maxOneTimeCodesInMemory {
			break
		}
		s.oneTimeCodes[uid] = oneTimeCodeEntry{
			Code:      code,
			CreatedAt: e.CreatedAt,
			ExpiresAt: e.ExpiresAt,
		}
	}
	s.evictOneTimeCodesLocked(now)
}

func (s *StatusServer) persistOneTimeCodesToDisk(dataDir string) {
	if s == nil {
		return
	}
	path := s.oneTimeCodePersistPath(dataDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		logger.Warn("one-time code state mkdir failed", "error", err, "path", path)
		return
	}

	now := time.Now()
	payload := oneTimeCodePersistPayload{
		Version: 1,
		SavedAt: now.UTC(),
	}

	s.oneTimeCodeMu.Lock()
	s.initOneTimeCodesLocked()
	s.cleanupExpiredOneTimeCodesLocked(now)
	for uid, entry := range s.oneTimeCodes {
		if strings.TrimSpace(uid) == "" || strings.TrimSpace(entry.Code) == "" {
			continue
		}
		if entry.ExpiresAt.IsZero() || now.After(entry.ExpiresAt) {
			continue
		}
		payload.Codes = append(payload.Codes, oneTimeCodePersistEntry{
			UserID:    uid,
			Code:      entry.Code,
			CreatedAt: entry.CreatedAt,
			ExpiresAt: entry.ExpiresAt,
		})
	}
	s.oneTimeCodeMu.Unlock()

	if len(payload.Codes) == 0 {
		_ = os.Remove(path)
		return
	}
	out, err := json.Marshal(payload)
	if err != nil {
		logger.Warn("one-time code state marshal failed", "error", err)
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		logger.Warn("one-time code state write failed", "error", err, "path", tmp)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		logger.Warn("one-time code state rename failed", "error", err, "from", tmp, "to", path)
		_ = os.Remove(tmp)
		return
	}
}

func (s *StatusServer) startOneTimeCodePersistence(ctx context.Context) {
	if s == nil || ctx == nil {
		return
	}
	dataDir := s.Config().DataDir
	go func() {
		<-ctx.Done()
		s.persistOneTimeCodesToDisk(dataDir)
	}()
}
