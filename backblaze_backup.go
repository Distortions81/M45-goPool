package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	snapshotPath    string
}

const lastBackupStampFilename = "last_backup"
const backupLocalCopySuffix = ".bak"
const backupStateKeyWorkerDB = "worker_db"

func newBackblazeBackupService(ctx context.Context, cfg Config, dbPath string) (*backblazeBackupService, error) {
	if !cfg.BackblazeBackupEnabled && !cfg.BackblazeKeepLocalCopy && strings.TrimSpace(cfg.BackupSnapshotPath) == "" {
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

	// Use the shared state database connection
	stateDB := getSharedStateDB()
	if stateDB == nil {
		return nil, fmt.Errorf("shared state db not initialized")
	}

	lastBackup, lastDataVersion, err := readLastBackupStampFromDB(stateDB, backupStateKeyWorkerDB)
	if err != nil {
		logger.Warn("read last backup stamp from sqlite failed (ignored)", "error", err)
	}

	snapshotPath := strings.TrimSpace(cfg.BackupSnapshotPath)
	if snapshotPath != "" && !filepath.IsAbs(snapshotPath) {
		base := strings.TrimSpace(cfg.DataDir)
		if base == "" {
			base = stateDir
		}
		snapshotPath = filepath.Join(base, snapshotPath)
	}
	if snapshotPath == "" && cfg.BackblazeKeepLocalCopy {
		snapshotPath = filepath.Join(stateDir, filepath.Base(dbPath)+backupLocalCopySuffix)
	}

	return &backblazeBackupService{
		bucket:          bucket,
		dbPath:          dbPath,
		objectPrefix:    objectPrefix,
		interval:        interval,
		lastBackup:      lastBackup,
		lastDataVersion: lastDataVersion,
		snapshotPath:    snapshotPath,
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

	localWritten := false
	if strings.TrimSpace(s.snapshotPath) != "" {
		if err := atomicCopyFile(snapshot, s.snapshotPath, 0o644); err != nil {
			logger.Warn("write local database backup snapshot failed", "error", err, "path", s.snapshotPath)
		} else {
			localWritten = true
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
	if err := writeLastBackupStampToDB(getSharedStateDB(), backupStateKeyWorkerDB, now, stampDataVersion); err != nil {
		logger.Warn("record backup timestamp", "error", err)
	}
	if uploaded {
		logger.Info("backblaze backup uploaded", "object", s.objectName())
	}
	if localWritten {
		logger.Info("local database snapshot written", "path", s.snapshotPath)
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

func readLastBackupStampFromDB(db *sql.DB, key string) (time.Time, int64, error) {
	if db == nil {
		return time.Time{}, 0, nil
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return time.Time{}, 0, nil
	}
	var (
		lastBackupUnix int64
		dataVersion    int64
	)
	if err := db.QueryRow("SELECT last_backup_unix, data_version FROM backup_state WHERE key = ?", key).Scan(&lastBackupUnix, &dataVersion); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return time.Time{}, 0, nil
		}
		return time.Time{}, 0, err
	}
	if lastBackupUnix <= 0 {
		return time.Time{}, dataVersion, nil
	}
	return time.Unix(lastBackupUnix, 0), dataVersion, nil
}

func writeLastBackupStampToDB(db *sql.DB, key string, ts time.Time, dataVersion int64) error {
	if db == nil {
		return nil
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}
	now := time.Now().Unix()
	_, err := db.Exec(`
		INSERT INTO backup_state (key, last_backup_unix, data_version, updated_at_unix)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			last_backup_unix = excluded.last_backup_unix,
			data_version = excluded.data_version,
			updated_at_unix = excluded.updated_at_unix
	`, key, unixOrZero(ts), dataVersion, now)
	return err
}

func workerDBDataVersion(ctx context.Context, srcPath string) (int64, error) {
	if srcPath == "" {
		return 0, os.ErrInvalid
	}

	srcDSN := fmt.Sprintf("%s?mode=ro&_busy_timeout=5000", srcPath)
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

	// Open the source DB in read-only mode so we can take a consistent snapshot
	// without blocking writers.
	srcDSN := fmt.Sprintf("%s?mode=ro&_busy_timeout=5000", srcPath)
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

	if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
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
