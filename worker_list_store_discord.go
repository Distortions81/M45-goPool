package main

import (
	"database/sql"
	"strings"
	"time"
)

func (s *workerListStore) UpsertDiscordLink(userID, discordUserID string, enabled bool, now time.Time) error {
	if s == nil || s.db == nil {
		return nil
	}
	userID = strings.TrimSpace(userID)
	discordUserID = strings.TrimSpace(discordUserID)
	if userID == "" || discordUserID == "" {
		return nil
	}
	enabledInt := 0
	if enabled {
		enabledInt = 1
	}
	ts := now.Unix()
	_, err := s.db.Exec(`
		INSERT INTO discord_links (user_id, discord_user_id, enabled, linked_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			discord_user_id = excluded.discord_user_id,
			enabled = excluded.enabled,
			updated_at = excluded.updated_at
	`, userID, discordUserID, enabledInt, ts, ts)
	return err
}

func (s *workerListStore) DisableDiscordLinkByDiscordUserID(discordUserID string, now time.Time) error {
	if s == nil || s.db == nil {
		return nil
	}
	discordUserID = strings.TrimSpace(discordUserID)
	if discordUserID == "" {
		return nil
	}
	_, err := s.db.Exec("UPDATE discord_links SET enabled = 0, updated_at = ? WHERE discord_user_id = ?", now.Unix(), discordUserID)
	return err
}

func (s *workerListStore) ListEnabledDiscordLinks() ([]discordLink, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	rows, err := s.db.Query("SELECT user_id, discord_user_id, enabled, linked_at, updated_at FROM discord_links WHERE enabled = 1 ORDER BY updated_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []discordLink
	for rows.Next() {
		var (
			entry         discordLink
			enabledInt    int
			linkedAtUnix  int64
			updatedAtUnix int64
		)
		if err := rows.Scan(&entry.UserID, &entry.DiscordUserID, &enabledInt, &linkedAtUnix, &updatedAtUnix); err != nil {
			return nil, err
		}
		entry.UserID = strings.TrimSpace(entry.UserID)
		entry.DiscordUserID = strings.TrimSpace(entry.DiscordUserID)
		entry.Enabled = enabledInt != 0
		if linkedAtUnix > 0 {
			entry.LinkedAt = time.Unix(linkedAtUnix, 0)
		}
		if updatedAtUnix > 0 {
			entry.UpdatedAt = time.Unix(updatedAtUnix, 0)
		}
		out = append(out, entry)
	}
	return out, nil
}

func (s *workerListStore) GetDiscordLink(userID string) (discordUserID string, enabled bool, ok bool, err error) {
	if s == nil || s.db == nil {
		return "", false, false, nil
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return "", false, false, nil
	}
	var enabledInt int
	if err := s.db.QueryRow("SELECT discord_user_id, enabled FROM discord_links WHERE user_id = ?", userID).Scan(&discordUserID, &enabledInt); err != nil {
		if err == sql.ErrNoRows {
			return "", false, false, nil
		}
		return "", false, false, err
	}
	return strings.TrimSpace(discordUserID), enabledInt != 0, true, nil
}

func (s *workerListStore) SetDiscordLinkEnabled(userID string, enabled bool, now time.Time) (ok bool, err error) {
	if s == nil || s.db == nil {
		return false, nil
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return false, nil
	}

	if _, _, exists, err := s.GetDiscordLink(userID); err != nil {
		return false, err
	} else if !exists {
		return false, nil
	}

	val := 0
	if enabled {
		val = 1
	}
	_, err = s.db.Exec("UPDATE discord_links SET enabled = ?, updated_at = ? WHERE user_id = ?", val, now.Unix(), userID)
	return true, err
}
