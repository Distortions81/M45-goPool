//go:build debug

package main

import (
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

var (
	netLogMu      sync.Mutex
	netLogWriter  io.Writer
	netLogEnabled atomic.Bool
)

func setNetLogWriter(w io.Writer) {
	netLogMu.Lock()
	defer netLogMu.Unlock()
	if netLogWriter != nil && netLogWriter != w {
		if c, ok := netLogWriter.(io.Closer); ok {
			_ = c.Close()
		}
	}
	netLogWriter = w
	netLogEnabled.Store(w != nil)
}

func netLogRuntimeSupported() bool { return true }

func netLogRuntimeEnabled() bool {
	return netLogEnabled.Load()
}

func setNetLogRuntime(enabled bool, w io.Writer) error {
	if !enabled {
		setNetLogWriter(nil)
		return nil
	}
	setNetLogWriter(w)
	return nil
}

func logNetMessage(direction string, data []byte) {
	if !netLogEnabled.Load() {
		return
	}
	netLogMu.Lock()
	defer netLogMu.Unlock()
	if !netLogEnabled.Load() || netLogWriter == nil {
		return
	}
	fmt.Fprintf(netLogWriter, "%s [%s] %s\n", time.Now().UTC().Format(time.RFC3339Nano), direction, trimNewline(data))
}

func trimNewline(data []byte) string {
	s := string(data)
	if len(s) == 0 {
		return s
	}
	if s[len(s)-1] == '\n' {
		return s[:len(s)-1]
	}
	return s
}
