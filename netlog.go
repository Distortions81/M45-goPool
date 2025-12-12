//go:build debug

package main

import (
	"fmt"
	"io"
	"sync"
	"time"
)

var (
	netLogMu      sync.Mutex
	netLogWriter  io.Writer
	netLogEnabled bool
)

func setNetLogWriter(w io.Writer) {
	netLogMu.Lock()
	defer netLogMu.Unlock()
	netLogWriter = w
	netLogEnabled = w != nil
}

func logNetMessage(direction string, data []byte) {
	netLogMu.Lock()
	defer netLogMu.Unlock()
	if !netLogEnabled || netLogWriter == nil {
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
