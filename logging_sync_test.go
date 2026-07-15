package main

import (
	"errors"
	"reflect"
	"testing"
)

type recordingSyncWriteCloser struct {
	calls    []string
	syncErr  error
	closeErr error
}

func (w *recordingSyncWriteCloser) Write(p []byte) (int, error) {
	w.calls = append(w.calls, "write")
	return len(p), nil
}

func (w *recordingSyncWriteCloser) Sync() error {
	w.calls = append(w.calls, "sync")
	return w.syncErr
}

func (w *recordingSyncWriteCloser) Close() error {
	w.calls = append(w.calls, "close")
	return w.closeErr
}

func TestDailyRollingFileWriterSyncsBeforeClose(t *testing.T) {
	f := &recordingSyncWriteCloser{}
	w := &dailyRollingFileWriter{f: f}

	if err := w.Close(); err != nil {
		t.Fatalf("close rolling writer: %v", err)
	}
	if want := []string{"sync", "close"}; !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("close calls = %v, want %v", f.calls, want)
	}
	if w.f != nil {
		t.Fatal("rolling writer retained closed file")
	}
}

func TestDailyRollingFileWriterReportsSyncAndCloseErrors(t *testing.T) {
	syncErr := errors.New("sync failed")
	closeErr := errors.New("close failed")
	f := &recordingSyncWriteCloser{syncErr: syncErr, closeErr: closeErr}
	w := &dailyRollingFileWriter{f: f}

	err := w.Close()
	if !errors.Is(err, syncErr) || !errors.Is(err, closeErr) {
		t.Fatalf("close error = %v, want both sync and close failures", err)
	}
	if want := []string{"sync", "close"}; !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("close calls = %v, want %v", f.calls, want)
	}
	if w.f != nil {
		t.Fatal("rolling writer retained file after close errors")
	}
}
