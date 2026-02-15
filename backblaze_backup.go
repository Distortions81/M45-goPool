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
	"sync"
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

	b2Enabled     bool
	b2BucketName  string
	b2AccountID   string
	b2AppKey      string
	forceInterval bool

	runMu sync.Mutex

	lastAttemptAt time.Time

	lastSnapshotAt      time.Time
	lastSnapshotVersion int64

	lastUploadAt      time.Time
	lastUploadVersion int64
	snapshotPath      string

	lastB2InitLogAt time.Time
	lastB2InitMsg   string

	lastSkipLogAt time.Time
	lastSkipMsg   string
}

type backblazeBackupSnapshot struct {
	B2Enabled           bool
	BucketConfigured    bool
	BucketName          string
	BucketReachable     bool
	Interval            time.Duration
	ForceEveryInterval  bool
	LastAttemptAt       time.Time
	LastSnapshotAt      time.Time
	LastSnapshotVersion int64
	LastUploadAt        time.Time
	LastUploadVersion   int64
	SnapshotPath        string
	LastB2InitLogAt     time.Time
	LastB2InitMsg       string
	LastSkipLogAt       time.Time
	LastSkipMsg         string
}

const lastBackupStampFilename = "last_backup"
const backupLocalCopySuffix = ".bak"
const (
	backupStateKeyWorkerDBSnapshot = "worker_db"
	backupStateKeyWorkerDBUpload   = "worker_db_upload"
	backupStateKeyWorkerDBAttempt  = "worker_db_attempt"
)

func newBackblazeBackupService(ctx context.Context, cfg Config, dbPath string) (*backblazeBackupService, error) {
	b2Enabled := backblazeCloudConfigured(cfg)
	if cfg.BackblazeBackupEnabled && !b2Enabled {
		logger.Info("backblaze cloud backups disabled", "reason", "backblaze_backup.bucket, backblaze_account_id, and backblaze_application_key are required")
	}
	if !b2Enabled && !cfg.BackblazeKeepLocalCopy && strings.TrimSpace(cfg.BackupSnapshotPath) == "" {
		return nil, nil
	}
	if dbPath == "" {
		return nil, fmt.Errorf("worker database path is empty")
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

	lastSnapshotAt, lastSnapshotVersion, err := readLastBackupStampFromDB(stateDB, backupStateKeyWorkerDBSnapshot)
	if err != nil {
		logger.Warn("read last backup stamp from sqlite failed (ignored)", "error", err)
	}
	lastUploadAt, lastUploadVersion, err := readLastBackupStampFromDB(stateDB, backupStateKeyWorkerDBUpload)
	if err != nil {
		logger.Warn("read last upload stamp from sqlite failed (ignored)", "error", err)
	}
	lastAttemptAt, _, err := readLastBackupStampFromDB(stateDB, backupStateKeyWorkerDBAttempt)
	if err != nil {
		logger.Warn("read last attempt stamp from sqlite failed (ignored)", "error", err)
	}
	if lastAttemptAt.IsZero() {
		// Backward compatibility: older versions only tracked the last snapshot.
		lastAttemptAt = lastSnapshotAt
	}

	snapshotPath := strings.TrimSpace(cfg.BackupSnapshotPath)
	if snapshotPath != "" && !filepath.IsAbs(snapshotPath) {
		base := strings.TrimSpace(cfg.DataDir)
		if base == "" {
			base = stateDir
		}
		snapshotPath = filepath.Join(base, snapshotPath)
	}
	svc := &backblazeBackupService{
		dbPath:              dbPath,
		objectPrefix:        objectPrefix,
		interval:            interval,
		b2Enabled:           b2Enabled,
		b2BucketName:        strings.TrimSpace(cfg.BackblazeBucket),
		b2AccountID:         strings.TrimSpace(cfg.BackblazeAccountID),
		b2AppKey:            strings.TrimSpace(cfg.BackblazeApplicationKey),
		forceInterval:       cfg.BackblazeForceEveryInterval,
		lastAttemptAt:       lastAttemptAt,
		lastSnapshotAt:      lastSnapshotAt,
		lastSnapshotVersion: lastSnapshotVersion,
		lastUploadAt:        lastUploadAt,
		lastUploadVersion:   lastUploadVersion,
		snapshotPath:        snapshotPath,
	}
	svc.bucket = svc.tryInitBucket(ctx)
	// Enable local backup if explicitly requested, or if B2 was enabled but has not
	// initialized yet (so operators still get a safe-to-copy snapshot).
	//
	// Additionally, when B2 is enabled, always write a local snapshot by default
	// even if keep_local_copy is disabled. This guarantees operators have a local
	// "safe to copy while running" snapshot regardless of B2 health.
	if svc.snapshotPath == "" && (cfg.BackblazeKeepLocalCopy || b2Enabled) {
		svc.snapshotPath = filepath.Join(stateDir, filepath.Base(dbPath)+backupLocalCopySuffix)
	}
	return svc, nil
}

func (s *backblazeBackupService) warnB2InitThrottled(msg string, attrs ...any) {
	if s == nil {
		return
	}
	msg = strings.TrimSpace(msg)
	now := time.Now()
	const throttle = 10 * time.Minute
	if msg != "" && msg == s.lastB2InitMsg && !s.lastB2InitLogAt.IsZero() && now.Sub(s.lastB2InitLogAt) < throttle {
		return
	}
	s.lastB2InitMsg = msg
	s.lastB2InitLogAt = now
	logger.Warn(msg, attrs...)
}

func (s *backblazeBackupService) Snapshot() backblazeBackupSnapshot {
	if s == nil {
		return backblazeBackupSnapshot{}
	}
	s.runMu.Lock()
	defer s.runMu.Unlock()
	return backblazeBackupSnapshot{
		B2Enabled:           s.b2Enabled,
		BucketConfigured:    s.b2AccountID != "" && s.b2AppKey != "" && s.b2BucketName != "",
		BucketName:          s.b2BucketName,
		BucketReachable:     s.bucket != nil,
		Interval:            s.interval,
		ForceEveryInterval:  s.forceInterval,
		LastAttemptAt:       s.lastAttemptAt,
		LastSnapshotAt:      s.lastSnapshotAt,
		LastSnapshotVersion: s.lastSnapshotVersion,
		LastUploadAt:        s.lastUploadAt,
		LastUploadVersion:   s.lastUploadVersion,
		SnapshotPath:        s.snapshotPath,
		LastB2InitLogAt:     s.lastB2InitLogAt,
		LastB2InitMsg:       s.lastB2InitMsg,
		LastSkipLogAt:       s.lastSkipLogAt,
		LastSkipMsg:         s.lastSkipMsg,
	}
}

func (s *backblazeBackupService) infoSkipThrottled(msg string, attrs ...any) {
	if s == nil {
		return
	}
	msg = strings.TrimSpace(msg)
	now := time.Now()
	const throttle = 10 * time.Minute
	if msg != "" && msg == s.lastSkipMsg && !s.lastSkipLogAt.IsZero() && now.Sub(s.lastSkipLogAt) < throttle {
		return
	}
	s.lastSkipMsg = msg
	s.lastSkipLogAt = now
	logger.Info(msg, attrs...)
}

func (s *backblazeBackupService) tryInitBucket(ctx context.Context) *b2.Bucket {
	if !s.b2Enabled {
		return nil
	}
	if s.b2AccountID == "" || s.b2AppKey == "" || s.b2BucketName == "" {
		s.warnB2InitThrottled("backblaze B2 not configured (missing credentials or bucket); falling back to local-only backups",
			"bucket", s.b2BucketName,
			"missing_account_id", s.b2AccountID == "",
			"missing_application_key", s.b2AppKey == "",
		)
		return nil
	}

	client, err := b2.NewClient(ctx, s.b2AccountID, s.b2AppKey)
	if err != nil {
		s.warnB2InitThrottled("create backblaze client failed, falling back to local-only backups", "error", err)
		return nil
	}
	bucket, err := client.Bucket(ctx, s.b2BucketName)
	if err != nil {
		s.warnB2InitThrottled("access backblaze bucket failed, falling back to local-only backups", "error", err)
		return nil
	}
	if _, err := bucket.Attrs(ctx); err != nil {
		s.warnB2InitThrottled("access backblaze bucket failed, falling back to local-only backups", "error", err)
		return nil
	}
	return bucket
}

func (s *backblazeBackupService) start(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	go func() {
		defer ticker.Stop()
		s.RunOnce(ctx, "startup", false)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.RunOnce(ctx, "interval", false)
			}
		}
	}()
}

func (s *backblazeBackupService) RunOnce(ctx context.Context, reason string, force bool) {
	if s == nil {
		return
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "unspecified"
	}
	s.runMu.Lock()
	defer s.runMu.Unlock()
	s.runLocked(ctx, reason, force)
}

func (s *backblazeBackupService) runLocked(ctx context.Context, reason string, force bool) {
	if ctx.Err() != nil {
		if logger.Enabled(logLevelDebug) {
			logger.Debug("backblaze backup skipped (context canceled)", "reason", reason, "force", force)
		}
		return
	}

	if s.bucket == nil && s.b2Enabled {
		prevNil := s.bucket == nil
		s.bucket = s.tryInitBucket(ctx)
		if prevNil && s.bucket != nil {
			logger.Info("backblaze bucket access recovered", "bucket", s.b2BucketName)
		}
	}

	now := time.Now()
	if !force && !s.lastAttemptAt.IsZero() && now.Sub(s.lastAttemptAt) < s.interval {
		wait := s.interval - now.Sub(s.lastAttemptAt)
		if wait < 0 {
			wait = 0
		}
		if logger.Enabled(logLevelDebug) {
			logger.Debug("backblaze backup skipped (interval not elapsed)",
				"reason", reason,
				"since_last", now.Sub(s.lastAttemptAt).Truncate(time.Second).String(),
				"interval", s.interval.String(),
				"next_in", wait.Truncate(time.Second).String(),
			)
		}
		return
	}

	// Always fetch the current change version to drive "dirty since last snapshot".
	dataVersion, dvErr := workerDBDataVersion(ctx, s.dbPath)
	if dvErr != nil {
		logger.Warn("backblaze backup data_version check failed, proceeding", "error", dvErr, "reason", reason, "force", force)
	}

	dbDirty := dvErr != nil || s.lastSnapshotVersion == 0 || dataVersion != s.lastSnapshotVersion
	retryUpload := s.b2Enabled && s.bucket != nil && s.lastSnapshotVersion > 0 && s.lastUploadVersion != s.lastSnapshotVersion

	// When force_every_interval is off, skip unless the DB is dirty OR we have an upload backlog.
	if !force && !s.forceInterval && !dbDirty && !retryUpload {
		s.infoSkipThrottled("backblaze backup skipped (database unchanged)",
			"reason", reason,
			"data_version", dataVersion,
			"last_snapshot_version", s.lastSnapshotVersion,
			"last_upload_version", s.lastUploadVersion,
			"force_every_interval", false,
		)
		s.lastAttemptAt = now
		_ = writeLastBackupStampToDB(getSharedStateDB(), backupStateKeyWorkerDBAttempt, now, 0)
		return
	}

	start := time.Now()
	s.lastAttemptAt = now
	_ = writeLastBackupStampToDB(getSharedStateDB(), backupStateKeyWorkerDBAttempt, now, 0)

	localWritten := false
	snapshotBytes := int64(0)
	var snapshotPath string
	snapshotTaken := false

	// Snapshot only when the DB is dirty, force is set, force_every_interval is on,
	// or we need an upload retry but the local snapshot file is missing.
	needSnapshot := force || s.forceInterval || dbDirty
	if !needSnapshot && retryUpload {
		if strings.TrimSpace(s.snapshotPath) == "" {
			needSnapshot = true
		} else if _, err := os.Stat(s.snapshotPath); err != nil {
			needSnapshot = true
		}
	}

	if needSnapshot {
		snapshot, snapDV, err := snapshotWorkerDB(ctx, s.dbPath)
		if err != nil {
			logger.Warn("backblaze backup snapshot failed", "error", err, "reason", reason, "force", force)
			return
		}
		defer os.Remove(snapshot)
		snapshotPath = snapshot
		dataVersion = snapDV
		snapshotTaken = true

		if st, err := os.Stat(snapshot); err == nil {
			snapshotBytes = st.Size()
		}
		if strings.TrimSpace(s.snapshotPath) != "" {
			if err := atomicCopyFile(snapshot, s.snapshotPath, 0o644); err != nil {
				logger.Warn("write local database backup snapshot failed", "error", err, "path", s.snapshotPath)
			} else {
				localWritten = true
			}
		}

		s.lastSnapshotAt = now
		s.lastSnapshotVersion = dataVersion
		if err := writeLastBackupStampToDB(getSharedStateDB(), backupStateKeyWorkerDBSnapshot, now, dataVersion); err != nil {
			logger.Warn("record snapshot timestamp", "error", err, "reason", reason, "force", force)
		}
	} else if retryUpload {
		// DB unchanged, but we have an upload backlog; use the local snapshot.
		snapshotPath = s.snapshotPath
	}

	uploaded := false
	uploadSkipped := false
	if s.bucket != nil && strings.TrimSpace(snapshotPath) != "" {
		object := s.objectName()
		if err := s.upload(ctx, snapshotPath, object); err != nil {
			logger.Warn("backblaze backup upload failed", "error", err, "object", object, "reason", reason, "force", force)
		} else {
			uploaded = true
			s.lastUploadAt = now
			s.lastUploadVersion = s.lastSnapshotVersion
			if err := writeLastBackupStampToDB(getSharedStateDB(), backupStateKeyWorkerDBUpload, now, s.lastUploadVersion); err != nil {
				logger.Warn("record upload timestamp", "error", err, "reason", reason, "force", force)
			}
			logger.Info("backblaze backup uploaded", "object", s.objectName())
		}
	} else if s.b2Enabled {
		uploadSkipped = true
	}

	if uploadSkipped && logger.Enabled(logLevelInfo) {
		logger.Info("backblaze backup upload skipped (bucket unavailable)", "bucket", s.b2BucketName, "reason", reason, "force", force)
	}
	if localWritten {
		logger.Info("local database snapshot written", "path", s.snapshotPath, "bytes", snapshotBytes, "reason", reason, "force", force)
	}
	if logger.Enabled(logLevelInfo) {
		logger.Info("backblaze backup completed",
			"elapsed", time.Since(start).Truncate(time.Millisecond).String(),
			"reason", reason,
			"force", force,
			"data_version", dataVersion,
			"db_dirty", dbDirty,
			"snapshot_taken", snapshotTaken,
			"force_every_interval", s.forceInterval,
			"uploaded", uploaded,
			"local_snapshot", localWritten,
		)
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
	if err := db.QueryRowContext(ctx, "SELECT version FROM db_change_state WHERE key = 'worker_db'").Scan(&dataVersion); err != nil {
		if isMissingChangeStateTableError(err) {
			// Backward compatibility for databases created before db_change_state
			// was introduced.
			return 1, nil
		}
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
	if err := conn.QueryRowContext(ctx, "SELECT version FROM db_change_state WHERE key = 'worker_db'").Scan(&dataVersion); err != nil {
		if isMissingChangeStateTableError(err) {
			dataVersion = 1
		} else {
			_ = os.Remove(tmpPath)
			return "", 0, err
		}
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

func isMissingChangeStateTableError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "no such table: db_change_state")
}
