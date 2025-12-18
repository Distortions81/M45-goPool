package main

import (
	"fmt"
	"io"
	"os"
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

func newRollingFileWriter(path string) io.Writer {
	if path == "" {
		return io.Discard
	}
	return &rollingFileWriter{path: path}
}

type rollingFileWriter struct {
	path string
	mu   sync.Mutex
	f    *os.File
}

func (w *rollingFileWriter) ensureFile() error {
	if w.path == "" {
		return nil
	}
	if _, err := os.Stat(w.path); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		if w.f != nil {
			_ = w.f.Close()
			w.f = nil
		}
	}
	if w.f == nil {
		f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return err
		}
		w.f = f
	}
	return nil
}

func (w *rollingFileWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.ensureFile(); err != nil {
		return 0, err
	}
	if w.f == nil {
		return 0, nil
	}
	return w.f.Write(p)
}

func (w *rollingFileWriter) Close() error {
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
		newRollingFileWriter(poolPath),
		newRollingFileWriter(errorPath),
		newRollingFileWriter(debugPath),
		stdout,
	)
}

func fatal(msg string, err error, attrs ...any) {
	attrPairs := append(attrs, "error", err)
	logger.Error(msg, attrPairs...)
	logger.Stop()
	os.Exit(1)
}
