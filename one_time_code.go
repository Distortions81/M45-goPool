package main

import (
	"context"
	"database/sql"
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

var oneTimeCodeGenerator = generateOneTimeCodeXKCD

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

func (s *StatusServer) oneTimeCodeInUseLocked(code string) bool {
	code = strings.TrimSpace(code)
	if s == nil || code == "" {
		return false
	}
	for _, entry := range s.oneTimeCodes {
		if entry.Code == code {
			return true
		}
	}
	return false
}

func (s *StatusServer) oneTimeCodePoolTag() string {
	if s == nil {
		return ""
	}
	return normalizePoolTag(s.Config().PoolEntropy)
}

func (s *StatusServer) parseOneTimeCodeInput(code string) (raw string, ok bool) {
	code = strings.TrimSpace(code)
	if s == nil || code == "" {
		return "", false
	}
	tag := s.oneTimeCodePoolTag()
	if tag == "" {
		return "", false
	}
	if len(code) <= poolTagLength+1 {
		return "", false
	}
	if code[poolTagLength] != '-' {
		return "", false
	}
	if !strings.EqualFold(code[:poolTagLength], tag) {
		return "", false
	}
	raw = strings.TrimSpace(code[poolTagLength+1:])
	if raw == "" {
		return "", false
	}
	return raw, true
}

func (s *StatusServer) createNewOneTimeCode(userID string, now time.Time) (code string, expiresAt time.Time) {
	if s == nil || strings.TrimSpace(userID) == "" {
		return "", time.Time{}
	}
	tag := s.oneTimeCodePoolTag()
	if tag == "" {
		return "", time.Time{}
	}

	s.oneTimeCodeMu.Lock()
	defer s.oneTimeCodeMu.Unlock()

	s.initOneTimeCodesLocked()
	s.cleanupExpiredOneTimeCodesLocked(now)

	// Explicitly invalidate any existing unused code for this user.
	delete(s.oneTimeCodes, userID)

	s.evictOneTimeCodesLocked(now)

	for range 50 {
		raw := strings.TrimSpace(oneTimeCodeGenerator())
		if raw == "" {
			continue
		}
		if s.oneTimeCodeInUseLocked(raw) {
			continue
		}
		expiresAt = now.Add(oneTimeCodeTTL)
		s.oneTimeCodes[userID] = oneTimeCodeEntry{
			Code:      raw,
			CreatedAt: now,
			ExpiresAt: expiresAt,
		}
		return tag + "-" + raw, expiresAt
	}
	return "", time.Time{}
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
	raw, ok := s.parseOneTimeCodeInput(code)
	if !ok {
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
	if entry.Code != raw {
		return false
	}
	delete(s.oneTimeCodes, userID)
	return true
}

func (s *StatusServer) redeemOneTimeCode(code string, now time.Time) (userID string, ok bool) {
	if s == nil {
		return "", false
	}
	raw, ok := s.parseOneTimeCodeInput(code)
	if !ok {
		return "", false
	}

	s.oneTimeCodeMu.Lock()
	defer s.oneTimeCodeMu.Unlock()

	s.initOneTimeCodesLocked()
	s.cleanupExpiredOneTimeCodesLocked(now)

	for uid, entry := range s.oneTimeCodes {
		if entry.Code != raw {
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

func (s *StatusServer) loadOneTimeCodesFromDB(dataDir string) {
	if s == nil {
		return
	}
	// Use the shared state database connection
	db := getSharedStateDB()
	if db == nil {
		logger.Warn("one-time code: shared state db not initialized")
		return
	}

	// Match the old semantics: load any persisted codes on startup and then
	// clear the persistence store so crashes don't keep stale codes.
	rows, err := db.Query("SELECT user_id, code, created_at_unix, expires_at_unix FROM one_time_codes")
	if err != nil {
		logger.Warn("one-time code sqlite query failed", "error", err)
		return
	}
	var persisted []oneTimeCodePersistEntry
	for rows.Next() {
		var (
			userID    string
			code      string
			createdAt int64
			expiresAt int64
		)
		if err := rows.Scan(&userID, &code, &createdAt, &expiresAt); err != nil {
			continue
		}
		userID = strings.TrimSpace(userID)
		code = strings.TrimSpace(code)
		if userID == "" || code == "" {
			continue
		}
		entry := oneTimeCodePersistEntry{
			UserID: userID,
			Code:   code,
		}
		if createdAt > 0 {
			entry.CreatedAt = time.Unix(createdAt, 0).UTC()
		}
		if expiresAt > 0 {
			entry.ExpiresAt = time.Unix(expiresAt, 0).UTC()
		}
		persisted = append(persisted, entry)
	}
	if err := rows.Close(); err != nil {
		logger.Warn("one-time code rows close failed", "error", err)
	}
	if _, err := db.Exec("DELETE FROM one_time_codes"); err != nil {
		logger.Warn("one-time code sqlite clear before load failed", "error", err)
	}

	now := time.Now()
	s.oneTimeCodeMu.Lock()
	defer s.oneTimeCodeMu.Unlock()

	s.initOneTimeCodesLocked()
	s.cleanupExpiredOneTimeCodesLocked(now)
	s.evictOneTimeCodesLocked(now)

	for _, e := range persisted {
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

func (s *StatusServer) persistOneTimeCodesToDB(dataDir string) {
	if s == nil {
		return
	}
	// Use the shared state database connection
	db := getSharedStateDB()
	if db == nil {
		logger.Warn("one-time code: shared state db not initialized")
		return
	}

	now := time.Now()
	if _, err := db.Exec("DELETE FROM one_time_codes"); err != nil {
		logger.Warn("one-time code sqlite clear failed", "error", err)
	}

	s.oneTimeCodeMu.Lock()
	s.initOneTimeCodesLocked()
	s.cleanupExpiredOneTimeCodesLocked(now)
	var codes []oneTimeCodePersistEntry
	for uid, entry := range s.oneTimeCodes {
		if strings.TrimSpace(uid) == "" || strings.TrimSpace(entry.Code) == "" {
			continue
		}
		if entry.ExpiresAt.IsZero() || now.After(entry.ExpiresAt) {
			continue
		}
		codes = append(codes, oneTimeCodePersistEntry{
			UserID:    uid,
			Code:      entry.Code,
			CreatedAt: entry.CreatedAt,
			ExpiresAt: entry.ExpiresAt,
		})
	}
	s.oneTimeCodeMu.Unlock()

	if len(codes) == 0 {
		return
	}

	tx, err := db.Begin()
	if err != nil {
		logger.Warn("one-time code sqlite begin failed", "error", err)
		return
	}
	defer func() {
		if err := tx.Rollback(); err != nil && err != sql.ErrTxDone {
			logger.Debug("one-time code sqlite rollback failed", "error", err)
		}
	}()
	stmt, err := tx.Prepare("INSERT OR REPLACE INTO one_time_codes (user_id, code, created_at_unix, expires_at_unix) VALUES (?, ?, ?, ?)")
	if err != nil {
		logger.Warn("one-time code sqlite prepare failed", "error", err)
		return
	}
	for _, e := range codes {
		if _, err := stmt.Exec(strings.TrimSpace(e.UserID), strings.TrimSpace(e.Code), unixOrZero(e.CreatedAt), unixOrZero(e.ExpiresAt)); err != nil {
			if closeErr := stmt.Close(); closeErr != nil {
				logger.Debug("one-time code sqlite stmt close failed", "error", closeErr)
			}
			logger.Warn("one-time code sqlite insert failed", "error", err)
			return
		}
	}
	if err := stmt.Close(); err != nil {
		logger.Debug("one-time code sqlite stmt close failed", "error", err)
	}
	if err := tx.Commit(); err != nil {
		logger.Warn("one-time code sqlite commit failed", "error", err)
	}
}

func (s *StatusServer) startOneTimeCodePersistence(ctx context.Context) {
	if s == nil || ctx == nil {
		return
	}
	dataDir := s.Config().DataDir
	go func() {
		<-ctx.Done()
		s.persistOneTimeCodesToDB(dataDir)
	}()
}
