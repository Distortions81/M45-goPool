package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
	for i := 0; i < 50; i++ {
		code = strings.TrimSpace(oneTimeCodeGenerator())
		if code == "" {
			continue
		}
		if s.oneTimeCodeInUseLocked(code) {
			continue
		}
		expiresAt = now.Add(oneTimeCodeTTL)
		s.oneTimeCodes[userID] = oneTimeCodeEntry{
			Code:      code,
			CreatedAt: now,
			ExpiresAt: expiresAt,
		}
		return code, expiresAt
	}

	return "", time.Time{}
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

	for i := 0; i < 50; i++ {
		code = strings.TrimSpace(oneTimeCodeGenerator())
		if code == "" {
			continue
		}
		if s.oneTimeCodeInUseLocked(code) {
			continue
		}
		expiresAt = now.Add(oneTimeCodeTTL)
		s.oneTimeCodes[userID] = oneTimeCodeEntry{
			Code:      code,
			CreatedAt: now,
			ExpiresAt: expiresAt,
		}
		return code, expiresAt
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

func (s *StatusServer) loadOneTimeCodesFromDB(dataDir string) {
	if s == nil {
		return
	}
	db, err := openStateDB(stateDBPathFromDataDir(dataDir))
	if err != nil {
		logger.Warn("one-time code sqlite open", "error", err)
		return
	}
	defer db.Close()

	legacyPath := s.oneTimeCodePersistPath(dataDir)
	if err := migrateOneTimeCodesFileToDB(db, legacyPath); err != nil {
		logger.Warn("one-time code state migrate failed", "error", err, "path", legacyPath)
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
	_ = rows.Close()
	_, _ = db.Exec("DELETE FROM one_time_codes")

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
	db, err := openStateDB(stateDBPathFromDataDir(dataDir))
	if err != nil {
		logger.Warn("one-time code sqlite open", "error", err)
		return
	}
	defer db.Close()

	now := time.Now()
	_, _ = db.Exec("DELETE FROM one_time_codes")

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
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.Prepare("INSERT OR REPLACE INTO one_time_codes (user_id, code, created_at_unix, expires_at_unix) VALUES (?, ?, ?, ?)")
	if err != nil {
		logger.Warn("one-time code sqlite prepare failed", "error", err)
		return
	}
	for _, e := range codes {
		if _, err := stmt.Exec(strings.TrimSpace(e.UserID), strings.TrimSpace(e.Code), unixOrZero(e.CreatedAt), unixOrZero(e.ExpiresAt)); err != nil {
			_ = stmt.Close()
			logger.Warn("one-time code sqlite insert failed", "error", err)
			return
		}
	}
	_ = stmt.Close()
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

func migrateOneTimeCodesFileToDB(db *sql.DB, path string) error {
	if db == nil || strings.TrimSpace(path) == "" {
		return nil
	}
	if done, err := hasStateMigration(db, stateMigrationOneTimeCodesJSON); err == nil && done {
		if err := renameLegacyFileToOld(path); err != nil {
			logger.Warn("rename legacy one-time codes file", "error", err, "from", path)
		}
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var payload oneTimeCodePersistPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return err
	}
	if len(payload.Codes) == 0 {
		_ = os.Remove(path)
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.Prepare("INSERT OR REPLACE INTO one_time_codes (user_id, code, created_at_unix, expires_at_unix) VALUES (?, ?, ?, ?)")
	if err != nil {
		return err
	}
	for _, e := range payload.Codes {
		uid := strings.TrimSpace(e.UserID)
		code := strings.TrimSpace(e.Code)
		if uid == "" || code == "" {
			continue
		}
		if _, err := stmt.Exec(uid, code, unixOrZero(e.CreatedAt), unixOrZero(e.ExpiresAt)); err != nil {
			_ = stmt.Close()
			return err
		}
	}
	_ = stmt.Close()
	if err := tx.Commit(); err != nil {
		return err
	}
	_ = recordStateMigration(db, stateMigrationOneTimeCodesJSON, time.Now())
	if err := renameLegacyFileToOld(path); err != nil {
		logger.Warn("rename legacy one-time codes file", "error", err, "from", path)
	}
	return nil
}
