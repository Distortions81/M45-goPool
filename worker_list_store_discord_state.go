package main

import (
	"strings"
	"time"
)

func (s *workerListStore) LoadDiscordWorkerStates(userID string) (map[string]workerNotifyState, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, nil
	}
	rows, err := s.db.Query(`
		SELECT worker_hash, online, since, seen_online, seen_offline, offline_eligible, offline_notified, recovery_eligible, recovery_notified
		FROM discord_worker_state
		WHERE user_id = ?
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]workerNotifyState)
	for rows.Next() {
		var (
			hash            string
			onlineInt       int
			sinceUnix       int64
			seenOnlineInt   int
			seenOfflineInt  int
			offlineEligInt  int
			offlineNotInt   int
			recoveryEligInt int
			recoveryNotInt  int
		)
		if err := rows.Scan(&hash, &onlineInt, &sinceUnix, &seenOnlineInt, &seenOfflineInt, &offlineEligInt, &offlineNotInt, &recoveryEligInt, &recoveryNotInt); err != nil {
			return nil, err
		}
		hash = strings.TrimSpace(hash)
		if hash == "" {
			continue
		}
		st := workerNotifyState{
			Online:           onlineInt != 0,
			SeenOnline:       seenOnlineInt != 0,
			SeenOffline:      seenOfflineInt != 0,
			OfflineEligible:  offlineEligInt != 0,
			OfflineNotified:  offlineNotInt != 0,
			RecoveryEligible: recoveryEligInt != 0,
			RecoveryNotified: recoveryNotInt != 0,
		}
		if sinceUnix > 0 {
			st.Since = time.Unix(sinceUnix, 0)
		}
		out[hash] = st
	}
	return out, nil
}

func (s *workerListStore) ResetDiscordWorkerStateTimers(userID string, now time.Time) error {
	if s == nil || s.db == nil {
		return nil
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil
	}
	ts := now.Unix()
	_, err := s.db.Exec(`
		UPDATE discord_worker_state
		SET
			since = ?,
			offline_eligible = 0,
			offline_notified = 0,
			recovery_eligible = 0,
			recovery_notified = 0,
			updated_at = ?
		WHERE user_id = ?
	`, ts, ts, userID)
	return err
}

func (s *workerListStore) PersistDiscordWorkerStates(userID string, upserts map[string]workerNotifyState, deletes []string, now time.Time) error {
	if s == nil || s.db == nil {
		return nil
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil
	}
	if len(upserts) == 0 && len(deletes) == 0 {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if len(deletes) > 0 {
		stmt, err := tx.Prepare("DELETE FROM discord_worker_state WHERE user_id = ? AND worker_hash = ?")
		if err != nil {
			return err
		}
		for _, h := range deletes {
			h = strings.TrimSpace(h)
			if h == "" {
				continue
			}
			if _, err := stmt.Exec(userID, h); err != nil {
				_ = stmt.Close()
				return err
			}
		}
		_ = stmt.Close()
	}

	if len(upserts) > 0 {
		stmt, err := tx.Prepare(`
			INSERT INTO discord_worker_state (
				user_id, worker_hash, online, since,
				seen_online, seen_offline, offline_eligible,
				offline_notified, recovery_eligible, recovery_notified,
				updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(user_id, worker_hash) DO UPDATE SET
				online = excluded.online,
				since = excluded.since,
				seen_online = excluded.seen_online,
				seen_offline = excluded.seen_offline,
				offline_eligible = excluded.offline_eligible,
				offline_notified = excluded.offline_notified,
				recovery_eligible = excluded.recovery_eligible,
				recovery_notified = excluded.recovery_notified,
				updated_at = excluded.updated_at
		`)
		if err != nil {
			return err
		}
		ts := now.Unix()
		for h, st := range upserts {
			h = strings.TrimSpace(h)
			if h == "" {
				continue
			}
			onlineInt := 0
			if st.Online {
				onlineInt = 1
			}
			sinceUnix := ts
			if !st.Since.IsZero() {
				sinceUnix = st.Since.Unix()
			}
			seenOnline := 0
			if st.SeenOnline {
				seenOnline = 1
			}
			seenOffline := 0
			if st.SeenOffline {
				seenOffline = 1
			}
			offlineElig := 0
			if st.OfflineEligible {
				offlineElig = 1
			}
			offlineNot := 0
			if st.OfflineNotified {
				offlineNot = 1
			}
			recoveryElig := 0
			if st.RecoveryEligible {
				recoveryElig = 1
			}
			recoveryNot := 0
			if st.RecoveryNotified {
				recoveryNot = 1
			}
			if _, err := stmt.Exec(
				userID, h, onlineInt, sinceUnix,
				seenOnline, seenOffline, offlineElig,
				offlineNot, recoveryElig, recoveryNot,
				ts,
			); err != nil {
				_ = stmt.Close()
				return err
			}
		}
		_ = stmt.Close()
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

func (s *workerListStore) ClearDiscordWorkerStates(userID string) error {
	if s == nil || s.db == nil {
		return nil
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil
	}
	_, err := s.db.Exec("DELETE FROM discord_worker_state WHERE user_id = ?", userID)
	return err
}
