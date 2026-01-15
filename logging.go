package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	logger         = newSimpleLogger()
	debugLogging   bool
	verboseLogging bool
)

const (
	logLevelDebug logLevel = iota
	logLevelInfo
	logLevelWarn
	logLevelError
)

const logRetentionDays = 3

var levelNames = []string{
	"DEBUG",
	"INFO",
	"WARN",
	"ERROR",
}

type logLevel int

type logEvent struct {
	level logLevel
	msg   string
	attrs []any
}

type simpleLogger struct {
	level       logLevel
	queue       chan logEvent
	done        chan struct{}
	writerMu    sync.RWMutex
	poolWriter  io.Writer
	errorWriter io.Writer
	debugWriter io.Writer
	stdout      bool
	wg          sync.WaitGroup
	stopOnce    sync.Once
	closing     atomic.Bool
}

func (l *simpleLogger) Enabled(level logLevel) bool {
	if l.closing.Load() {
		return false
	}
	return level >= l.level
}

func newSimpleLogger() *simpleLogger {
	l := &simpleLogger{
		level:       logLevelError,
		queue:       make(chan logEvent, 4096),
		done:        make(chan struct{}),
		poolWriter:  os.Stdout,
		errorWriter: os.Stdout,
		debugWriter: io.Discard,
	}
	l.wg.Add(1)
	go l.run()
	return l
}

func (l *simpleLogger) run() {
	defer l.wg.Done()
	for {
		select {
		case evt := <-l.queue:
			l.writeEntry(evt)
		case <-l.done:
			for {
				select {
				case evt := <-l.queue:
					l.writeEntry(evt)
				default:
					return
				}
			}
		}
	}
}

func (l *simpleLogger) log(level logLevel, msg string, attrs ...any) {
	if level < l.level {
		return
	}
	if l.closing.Load() {
		return
	}
	select {
	case l.queue <- logEvent{level: level, msg: msg, attrs: append([]any(nil), attrs...)}:
	case <-l.done:
	}
}

func (l *simpleLogger) Info(msg string, attrs ...any) {
	l.log(logLevelInfo, msg, attrs...)
}

func (l *simpleLogger) Warn(msg string, attrs ...any) {
	l.log(logLevelWarn, msg, attrs...)
}

func (l *simpleLogger) Error(msg string, attrs ...any) {
	l.log(logLevelError, msg, attrs...)
}

func (l *simpleLogger) Debug(msg string, attrs ...any) {
	l.log(logLevelDebug, msg, attrs...)
}

func (l *simpleLogger) setLevel(level logLevel) {
	l.level = level
}

func (l *simpleLogger) configureWriters(pool, errWriter, debug io.Writer, stdout bool) {
	if pool == nil {
		pool = io.Discard
	}
	if errWriter == nil {
		errWriter = io.Discard
	}
	if debug == nil {
		debug = io.Discard
	}
	l.writerMu.Lock()
	l.poolWriter = pool
	l.errorWriter = errWriter
	l.debugWriter = debug
	l.stdout = stdout
	l.writerMu.Unlock()
}

func (l *simpleLogger) Stop() {
	l.stopOnce.Do(func() {
		l.closing.Store(true)
		close(l.done)
		l.wg.Wait()
		l.writerMu.Lock()
		closeWriter(l.poolWriter)
		closeWriter(l.errorWriter)
		closeWriter(l.debugWriter)
		l.poolWriter = io.Discard
		l.errorWriter = io.Discard
		l.debugWriter = io.Discard
		l.writerMu.Unlock()
	})
}

func closeWriter(w io.Writer) {
	if closer, ok := w.(io.Closer); ok {
		_ = closer.Close()
	}
}

func (l *simpleLogger) writeEntry(evt logEvent) {
	username := formatAttrs(evt.attrs)
	timestamp := time.Now().UTC().Format(time.RFC3339Nano)
	levelName := "UNKNOWN"
	if int(evt.level) >= 0 && int(evt.level) < len(levelNames) {
		levelName = levelNames[evt.level]
	}
	var entry strings.Builder
	entry.WriteString(timestamp)
	entry.WriteString(" [")
	entry.WriteString(levelName)
	entry.WriteString("] ")
	entry.WriteString(evt.msg)
	if username != "" {
		entry.WriteString(" ")
		entry.WriteString(username)
	}
	entry.WriteByte('\n')
	line := entry.String()

	l.writerMu.RLock()
	pool := l.poolWriter
	errWriter := l.errorWriter
	debugWriter := l.debugWriter
	stdout := l.stdout
	l.writerMu.RUnlock()

	if stdout {
		_, _ = os.Stdout.Write([]byte(line))
	}
	switch evt.level {
	case logLevelDebug:
		if debugWriter != nil {
			_, _ = debugWriter.Write([]byte(line))
		}
	default:
		if evt.level >= logLevelInfo && pool != nil {
			_, _ = pool.Write([]byte(line))
		}
		if evt.level >= logLevelError && errWriter != nil {
			_, _ = errWriter.Write([]byte(line))
		}
	}
}

func formatAttrs(attrs []any) string {
	if len(attrs) == 0 {
		return ""
	}
	var b strings.Builder
	for i := 0; i < len(attrs); i++ {
		if i > 0 {
			b.WriteByte(' ')
		}
		key := fmt.Sprint(attrs[i])
		if i+1 < len(attrs) {
			value := fmt.Sprint(attrs[i+1])
			b.WriteString(key)
			b.WriteByte('=')
			b.WriteString(value)
			i++
		} else {
			b.WriteString(key)
		}
	}
	return b.String()
}

func newDailyRollingFileWriter(path string) io.Writer {
	if path == "" {
		return io.Discard
	}
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)
	return &dailyRollingFileWriter{
		dir:  dir,
		name: name,
		ext:  ext,
	}
}

type dailyRollingFileWriter struct {
	dir         string
	name        string
	ext         string
	mu          sync.Mutex
	f           *os.File
	currentDate string
}

func (w *dailyRollingFileWriter) ensureFile(now time.Time) error {
	if w.name == "" || w.dir == "" {
		return fmt.Errorf("invalid log path")
	}
	date := now.UTC().Format("2006-01-02")
	if w.f != nil && w.currentDate == date {
		return nil
	}
	if w.f != nil {
		_ = w.f.Close()
		w.f = nil
	}
	filename := fmt.Sprintf("%s-%s%s", w.name, date, w.ext)
	target := filepath.Join(w.dir, filename)
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	w.f = f
	w.currentDate = date
	w.cleanupOldLogs(now)
	return nil
}

func (w *dailyRollingFileWriter) cleanupOldLogs(now time.Time) {
	if logRetentionDays <= 0 {
		return
	}
	if w.name == "" || w.dir == "" {
		return
	}
	cutoff := now.UTC().AddDate(0, 0, -(logRetentionDays - 1))
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return
	}
	prefix := w.name + "-"
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, w.ext) {
			continue
		}
		if len(name) < len(prefix)+len(w.ext)+len("2006-01-02") {
			continue
		}
		dateStr := name[len(prefix) : len(name)-len(w.ext)]
		if len(dateStr) != len("2006-01-02") {
			continue
		}
		ts, err := time.Parse("2006-01-02", dateStr)
		if err != nil {
			continue
		}
		if ts.Before(cutoff) {
			_ = os.Remove(filepath.Join(w.dir, name))
		}
	}
}

func (w *dailyRollingFileWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	if err := w.ensureFile(now); err != nil {
		return 0, err
	}
	if w.f == nil {
		return 0, nil
	}
	return w.f.Write(p)
}

func (w *dailyRollingFileWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

func setLogLevel(level logLevel) {
	logger.setLevel(level)
}

func configureFileLogging(poolPath, errorPath, debugPath string, stdout bool) {
	logger.configureWriters(
		newDailyRollingFileWriter(poolPath),
		newDailyRollingFileWriter(errorPath),
		newDailyRollingFileWriter(debugPath),
		stdout,
	)
}

func fatal(msg string, err error, attrs ...any) {
	attrPairs := append(attrs, "error", err)
	logger.Error(msg, attrPairs...)
	logger.Stop()
	os.Exit(1)
}
