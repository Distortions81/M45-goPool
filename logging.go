package main

import (
	"io"
	"log/slog"
	"os"
	"sync"
)

var loggerObj *slog.Logger
var errorLogger *slog.Logger
var logLevel = slog.LevelInfo

// debugLogging and verboseLogging mirror the build-time debug/verbose
// configuration so other parts of the program can cheaply skip expensive
// debug/verbose-only work (like constructing large ShareDebug payloads)
// when those modes are disabled.
var debugLogging bool
var verboseLogging bool

type rollingFileWriter struct {
	path string
	mu   sync.Mutex
	f    *os.File
}

func newRollingFileWriter(path string) io.Writer {
	if path == "" {
		return os.Stdout
	}
	return &rollingFileWriter{path: path}
}

func (w *rollingFileWriter) ensureFile() error {
	if w.path == "" {
		return nil
	}
	// If the target path no longer exists or we don't have a file yet,
	// (re)open it in append mode so logs resume after deletion/rotation.
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

func init() {
	// Default to discarding logs until main wires up a real destination.
	configureLoggerOutput(io.Discard)
	configureErrorLoggerOutput(io.Discard)
}

func configureLoggerOutput(w io.Writer) {
	if w == nil {
		w = io.Discard
	}
	loggerObj = slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{
		Level:     logLevel,
		AddSource: true,
	}))
	slog.SetDefault(loggerObj)
}

func setLogLevel(level slog.Level) {
	logLevel = level
}

func configureErrorLoggerOutput(w io.Writer) {
	if w == nil {
		w = io.Discard
	}
	// Mirror error log output to stdout so operators always see fatal
	// errors in the foreground, even when an error log file is used.
	mw := io.MultiWriter(w, os.Stdout)
	errorLogger = slog.New(slog.NewTextHandler(mw, &slog.HandlerOptions{
		Level:     slog.LevelError,
		AddSource: true,
	}))
}

func fatal(msg string, err error, attrs ...any) {
	attrPairs := append(attrs, "error", err)
	loggerObj.Error(msg, attrPairs...)
	if errorLogger != nil {
		errorLogger.Error(msg, attrPairs...)
	}
	os.Exit(1)
}
