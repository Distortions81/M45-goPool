package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Backblaze/blazer/b2"
	"modernc.org/sqlite"
)

type dbBackuper interface {
	NewBackup(string) (*sqlite.Backup, error)
}

type backblazeBackupService struct {
	bucket           *b2.Bucket
	dbPath           string
	objectPrefix     string
	objectListPrefix string
	interval         time.Duration
	maxBackups       int
	lastBackupPath   string
	missingLastStamp bool
}

const (
	backupObjectBaseName = "workers-db-"
	backupObjectSuffix   = ".db"
	backupTimestampName  = "backblaze_last_backup"
)

func newBackblazeBackupService(ctx context.Context, cfg Config, dbPath string) (*backblazeBackupService, error) {
	if !cfg.BackblazeBackupEnabled {
		return nil, nil
	}
	if dbPath == "" {
		return nil, fmt.Errorf("worker database path is empty")
	}
	if cfg.BackblazeAccountID == "" || cfg.BackblazeApplicationKey == "" || cfg.BackblazeBucket == "" {
		return nil, fmt.Errorf("backblaze credentials are incomplete")
	}

	client, err := b2.NewClient(ctx, cfg.BackblazeAccountID, cfg.BackblazeApplicationKey)
	if err != nil {
		return nil, fmt.Errorf("create backblaze client: %w", err)
	}
	bucket, err := client.Bucket(ctx, cfg.BackblazeBucket)
	if err != nil {
		return nil, fmt.Errorf("access backblaze bucket: %w", err)
	}
	if _, err := bucket.Attrs(ctx); err != nil {
		return nil, fmt.Errorf("access backblaze bucket: %w", err)
	}

	interval := time.Duration(cfg.BackblazeBackupIntervalSeconds) * time.Second
	if interval <= 0 {
		interval = time.Duration(defaultBackblazeBackupIntervalSeconds) * time.Second
	}
	maxBackups := cfg.BackblazeMaxBackups

	objectPrefix := sanitizeObjectPrefix(cfg.BackblazePrefix)
	objectListPrefix := objectPrefix + backupObjectBaseName
	stateDir := filepath.Dir(dbPath)
	lastBackupPath := filepath.Join(stateDir, backupTimestampName)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return nil, fmt.Errorf("create state dir for backblaze timestamp: %w", err)
	}
	missingStamp := !fileExists(lastBackupPath)

	return &backblazeBackupService{
		bucket:           bucket,
		dbPath:           dbPath,
		objectPrefix:     objectPrefix,
		objectListPrefix: objectListPrefix,
		interval:         interval,
		maxBackups:       maxBackups,
		lastBackupPath:   lastBackupPath,
		missingLastStamp: missingStamp,
	}, nil
}

func (s *backblazeBackupService) start(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	go func() {
		defer ticker.Stop()
		if s.missingLastStamp {
			logger.Info("backblaze timestamp missing, forcing initial backup")
		}
		s.run(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.run(ctx)
			}
		}
	}()
}

func (s *backblazeBackupService) run(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	if err := s.pruneBackups(ctx); err != nil {
		logger.Warn("backblaze backup prune failed", "error", err)
	}
	ts := time.Now().UTC()
	snapshot, err := snapshotWorkerDB(ctx, s.dbPath)
	if err != nil {
		logger.Warn("backblaze backup snapshot failed", "error", err)
		return
	}
	defer os.Remove(snapshot)

	object := s.objectName(ts)
	if err := s.upload(ctx, snapshot, object); err != nil {
		logger.Warn("backblaze backup upload failed", "error", err, "object", object)
		return
	}
	if err := s.recordLastBackup(ts); err != nil {
		logger.Warn("record backblaze timestamp", "error", err)
	}
	logger.Info("backblaze backup uploaded", "object", object)
}

func (s *backblazeBackupService) upload(ctx context.Context, path, object string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	writer := s.bucket.Object(object).NewWriter(ctx)
	if _, err := io.Copy(writer, f); err != nil {
		_ = writer.Close()
		return err
	}
	return writer.Close()
}

func (s *backblazeBackupService) objectName(ts time.Time) string {
	return fmt.Sprintf("%s%s%s%s", s.objectPrefix, backupObjectBaseName, ts.Format("20060102T150405Z"), backupObjectSuffix)
}

func (s *backblazeBackupService) recordLastBackup(ts time.Time) error {
	if s.lastBackupPath == "" {
		return nil
	}
	data := []byte(ts.UTC().Format(time.RFC3339Nano))
	if err := os.WriteFile(s.lastBackupPath, data, 0o644); err != nil {
		return err
	}
	s.missingLastStamp = false
	return nil
}

func (s *backblazeBackupService) pruneBackups(ctx context.Context) error {
	if s.maxBackups <= 0 {
		return nil
	}
	iter := s.bucket.List(ctx, b2.ListPrefix(s.objectListPrefix))
	var names []string
	for iter.Next() {
		names = append(names, iter.Object().Name())
	}
	if err := iter.Err(); err != nil {
		return err
	}
	keep := s.maxBackups - 1
	if keep < 0 {
		keep = 0
	}
	if len(names) <= keep {
		return nil
	}
	sort.Strings(names)
	toDelete := len(names) - keep
	for _, name := range names[:toDelete] {
		if err := s.bucket.Object(name).Delete(ctx); err != nil {
			logger.Warn("backblaze backup delete old object", "error", err, "object", name)
		}
	}
	return nil
}

func snapshotWorkerDB(ctx context.Context, srcPath string) (string, error) {
	tmpFile, err := os.CreateTemp("", "gopool-workers-db-*.db")
	if err != nil {
		return "", err
	}
	tmpPath := tmpFile.Name()
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	if err := os.Remove(tmpPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	srcDSN := fmt.Sprintf("%s?_foreign_keys=1&mode=ro", srcPath)
	db, err := sql.Open("sqlite", srcDSN)
	if err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	defer db.Close()

	conn, err := db.Conn(ctx)
	if err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	defer conn.Close()

	if err := conn.Raw(func(driverConn any) error {
		backuper, ok := driverConn.(dbBackuper)
		if !ok {
			return fmt.Errorf("sqlite driver does not support backups")
		}
		bck, err := backuper.NewBackup(tmpPath)
		if err != nil {
			return err
		}
		for more := true; more; {
			more, err = bck.Step(-1)
			if err != nil {
				return err
			}
		}
		return bck.Finish()
	}); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}

	return tmpPath, nil
}

func sanitizeObjectPrefix(raw string) string {
	prefix := strings.TrimSpace(raw)
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		return ""
	}
	return prefix + "/"
}
