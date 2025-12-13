package main

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/bytedance/gopkg/util/logger"
)

// foundBlockLogEntry represents a single JSONL line to append to the
// found_blocks.jsonl log. Writes are serialized by a background goroutine
// so the submit hot path never blocks on filesystem I/O.
type foundBlockLogEntry struct {
	Dir  string
	Line []byte
}

var (
	foundBlockLogCh   = make(chan foundBlockLogEntry, 64)
	foundBlockLogOnce sync.Once
)

func init() {
	foundBlockLogOnce.Do(startFoundBlockLogger)
}

func startFoundBlockLogger() {
	go func() {
		var f *os.File
		var curPath string
		for entry := range foundBlockLogCh {
			dir := entry.Dir
			if dir == "" {
				dir = defaultDataDir
			}
			stateDir := filepath.Join(dir, "state")
			path := filepath.Join(stateDir, "found_blocks.jsonl")
			// Lazily (re)open the log file when the target path changes.
			if path != curPath || f == nil {
				if f != nil {
					_ = f.Close()
					f = nil
				}
				if err := os.MkdirAll(stateDir, 0o755); err != nil {
					logger.Warn("found block log mkdir", "error", err)
					continue
				}
				nf, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
				if err != nil {
					logger.Warn("found block log open", "error", err)
					continue
				}
				f = nf
				curPath = path
			}
			if f == nil {
				continue
			}
			if _, err := f.Write(entry.Line); err != nil {
				logger.Warn("found block log write", "error", err)
			}
		}
		if f != nil {
			_ = f.Close()
		}
	}()
}
