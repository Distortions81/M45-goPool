package main

import (
	"strings"
	"time"
)

func (s *workerListStore) RecordClerkUserSeen(userID string, now time.Time) error {
	if s == nil || s.db == nil {
		return nil
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	ts := now.Unix()
	_, err := s.db.Exec(`
		INSERT INTO clerk_users (user_id, first_seen_unix, last_seen_unix, seen_count)
		VALUES (?, ?, ?, 1)
		ON CONFLICT(user_id) DO UPDATE SET
			last_seen_unix = excluded.last_seen_unix,
			seen_count = clerk_users.seen_count + 1
	`, userID, ts, ts)
	return err
}

func (s *workerListStore) ListAllClerkUsers() ([]ClerkUserRecord, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	rows, err := s.db.Query("SELECT user_id, first_seen_unix, last_seen_unix, seen_count FROM clerk_users ORDER BY last_seen_unix DESC, user_id COLLATE NOCASE")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]ClerkUserRecord, 0, 64)
	for rows.Next() {
		var (
			userID string
			first  int64
			last   int64
			count  int
		)
		if err := rows.Scan(&userID, &first, &last, &count); err != nil {
			return nil, err
		}
		userID = strings.TrimSpace(userID)
		if userID == "" {
			continue
		}
		out = append(out, ClerkUserRecord{
			UserID:    userID,
			FirstSeen: time.Unix(first, 0),
			LastSeen:  time.Unix(last, 0),
			SeenCount: count,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
