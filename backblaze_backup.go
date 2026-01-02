package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Backblaze/blazer/b2"
	"modernc.org/sqlite"
)

type dbBackuper interface {
	NewBackup(string) (*sqlite.Backup, error)
}

type backblazeBackupService struct {
	bucket       *b2.Bucket
	dbPath       string
	interval     time.Duration
	objectPrefix string

	lastBackup      time.Time
	lastDataVersion int64
	lastBackupPath  string
	keepLocalCopy   bool
	localCopyPath   string
}

const backblazeLastBackupFilename = "backblaze_last_backup"
const backblazeLocalCopySuffix = ".b2last"

func newBackblazeBackupService(ctx context.Context, cfg Config, dbPath string) (*backblazeBackupService, error) {
	if !cfg.BackblazeBackupEnabled && !cfg.BackblazeKeepLocalCopy {
		return nil, nil
	}
	if dbPath == "" {
		return nil, fmt.Errorf("worker database path is empty")
	}

	var bucket *b2.Bucket
	if cfg.BackblazeBackupEnabled {
		if cfg.BackblazeAccountID == "" || cfg.BackblazeApplicationKey == "" || cfg.BackblazeBucket == "" {
			return nil, fmt.Errorf("backblaze credentials are incomplete")
		}

		client, err := b2.NewClient(ctx, cfg.BackblazeAccountID, cfg.BackblazeApplicationKey)
		if err != nil {
			return nil, fmt.Errorf("create backblaze client: %w", err)
		}
		bucket, err = client.Bucket(ctx, cfg.BackblazeBucket)
		if err != nil {
			return nil, fmt.Errorf("access backblaze bucket: %w", err)
		}
		if _, err := bucket.Attrs(ctx); err != nil {
			return nil, fmt.Errorf("access backblaze bucket: %w", err)
		}
	}

	interval := time.Duration(cfg.BackblazeBackupIntervalSeconds) * time.Second
	if interval <= 0 {
		interval = time.Duration(defaultBackblazeBackupIntervalSeconds) * time.Second
	}
	objectPrefix := sanitizeObjectPrefix(cfg.BackblazePrefix)

	stateDir := filepath.Dir(dbPath)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return nil, fmt.Errorf("create state dir for backblaze timestamp: %w", err)
	}
	lastBackupPath := filepath.Join(stateDir, backblazeLastBackupFilename)
	lastBackup, lastDataVersion, err := readBackblazeLastBackup(lastBackupPath)
	if err != nil {
		logger.Warn("read backblaze last backup stamp failed (ignored)", "error", err, "path", lastBackupPath)
	}
	localCopyPath := filepath.Join(stateDir, filepath.Base(dbPath)+backblazeLocalCopySuffix)

	return &backblazeBackupService{
		bucket:       bucket,
		dbPath:       dbPath,
		objectPrefix: objectPrefix,
		interval:     interval,
		lastBackup:   lastBackup,

		lastDataVersion: lastDataVersion,
		lastBackupPath:  lastBackupPath,
		keepLocalCopy:   cfg.BackblazeKeepLocalCopy,
		localCopyPath:   localCopyPath,
	}, nil
}

func (s *backblazeBackupService) start(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	go func() {
		defer ticker.Stop()
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

	now := time.Now()
	if !s.lastBackup.IsZero() && now.Sub(s.lastBackup) < s.interval {
		return
	}
	if !s.lastBackup.IsZero() {
		if s.lastDataVersion == 0 {
			logger.Info("backblaze backup missing data_version stamp, forcing one backup to initialize change tracking")
		}
		if dv, err := workerDBDataVersion(ctx, s.dbPath); err == nil {
			if dv == s.lastDataVersion {
				return
			}
		} else {
			logger.Warn("backblaze backup change check failed, proceeding", "error", err)
		}
	}

	snapshot, dataVersion, err := snapshotWorkerDB(ctx, s.dbPath)
	if err != nil {
		logger.Warn("backblaze backup snapshot failed", "error", err)
		return
	}
	defer os.Remove(snapshot)

	if s.keepLocalCopy {
		if err := atomicCopyFile(snapshot, s.localCopyPath, 0o644); err != nil {
			logger.Warn("write local backblaze backup copy failed", "error", err, "path", s.localCopyPath)
		}
	}

	// Record that we've produced a snapshot so quick restarts don't trigger
	// another full copy immediately. If the upload fails, we persist a 0
	// data_version so we'll retry later even if the DB hasn't changed.
	stampDataVersion := int64(0)
	uploaded := false
	if s.bucket != nil {
		object := s.objectName()
		if err := s.upload(ctx, snapshot, object); err != nil {
			logger.Warn("backblaze backup upload failed", "error", err, "object", object)
		} else {
			uploaded = true
			stampDataVersion = dataVersion
		}
	} else {
		stampDataVersion = dataVersion
	}
	s.lastBackup = now
	s.lastDataVersion = stampDataVersion
	if err := writeBackblazeLastBackup(s.lastBackupPath, now, stampDataVersion); err != nil {
		logger.Warn("record backblaze timestamp", "error", err, "path", s.lastBackupPath)
	}
	if uploaded {
		logger.Info("backblaze backup uploaded", "object", s.objectName())
	} else {
		logger.Info("local backup written", "path", s.localCopyPath)
	}
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

func (s *backblazeBackupService) objectName() string {
	return fmt.Sprintf("%s%s", s.objectPrefix, filepath.Base(s.dbPath))
}

func atomicCopyFile(srcPath, dstPath string, mode os.FileMode) error {
	if strings.TrimSpace(srcPath) == "" || strings.TrimSpace(dstPath) == "" {
		return os.ErrInvalid
	}
	dir := filepath.Dir(dstPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()

	tmp, err := os.CreateTemp(dir, filepath.Base(dstPath)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	removeTmp := true
	defer func() {
		if tmp != nil {
			_ = tmp.Close()
		}
		if removeTmp {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := io.Copy(tmp, src); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	tmp = nil

	if mode != 0 {
		if err := os.Chmod(tmpName, mode); err != nil {
			return err
		}
	}
	if err := os.Rename(tmpName, dstPath); err != nil {
		return err
	}
	removeTmp = false
	return nil
}

func readBackblazeLastBackup(path string) (time.Time, int64, error) {
	if strings.TrimSpace(path) == "" {
		return time.Time{}, 0, os.ErrInvalid
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return time.Time{}, 0, nil
		}
		return time.Time{}, 0, err
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		return time.Time{}, 0, nil
	}
	ts, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(lines[0]))
	if err != nil {
		return time.Time{}, 0, err
	}
	var dataVersion int64
	if len(lines) >= 2 {
		dataVersion, _ = strconv.ParseInt(strings.TrimSpace(lines[1]), 10, 64)
	}
	return ts, dataVersion, nil
}

func writeBackblazeLastBackup(path string, ts time.Time, dataVersion int64) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data := []byte(ts.UTC().Format(time.RFC3339Nano) + "\n" + strconv.FormatInt(dataVersion, 10) + "\n")
	return os.WriteFile(path, data, 0o644)
}

func workerDBDataVersion(ctx context.Context, srcPath string) (int64, error) {
	if srcPath == "" {
		return 0, os.ErrInvalid
	}

	srcDSN := fmt.Sprintf("%s?mode=ro&busy_timeout=5000", srcPath)
	db, err := sql.Open("sqlite", srcDSN)
	if err != nil {
		return 0, err
	}
	defer db.Close()

	var dataVersion int64
	if err := db.QueryRowContext(ctx, "PRAGMA data_version").Scan(&dataVersion); err != nil {
		return 0, err
	}
	return dataVersion, nil
}

func snapshotWorkerDB(ctx context.Context, srcPath string) (string, int64, error) {
	tmpFile, err := os.CreateTemp("", "gopool-workers-db-*.db")
	if err != nil {
		return "", 0, err
	}
	tmpPath := tmpFile.Name()
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", 0, err
	}
	if err := os.Remove(tmpPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", 0, err
	}

	srcDSN := fmt.Sprintf("%s?_foreign_keys=1&busy_timeout=5000", srcPath)
	db, err := sql.Open("sqlite", srcDSN)
	if err != nil {
		_ = os.Remove(tmpPath)
		return "", 0, err
	}
	defer db.Close()

	conn, err := db.Conn(ctx)
	if err != nil {
		_ = os.Remove(tmpPath)
		return "", 0, err
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		_ = os.Remove(tmpPath)
		return "", 0, err
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		_, _ = conn.ExecContext(ctx, "ROLLBACK")
	}()

	var dataVersion int64
	if err := conn.QueryRowContext(ctx, "PRAGMA data_version").Scan(&dataVersion); err != nil {
		_ = os.Remove(tmpPath)
		return "", 0, err
	}

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
		return "", 0, err
	}

	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		_ = os.Remove(tmpPath)
		return "", 0, err
	}
	committed = true

	return tmpPath, dataVersion, nil
}

func sanitizeObjectPrefix(raw string) string {
	prefix := strings.TrimSpace(raw)
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		return ""
	}
	return prefix + "/"
}
