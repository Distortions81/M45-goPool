//go:build !debug

package main

import (
	"fmt"
	"io"
)

// In non-debug builds, network logging helpers are compiled as no-ops to
// avoid any overhead in the hot path.

func setNetLogWriter(w io.Writer) {
	// no-op
}

func netLogRuntimeSupported() bool { return false }

func netLogRuntimeEnabled() bool { return false }

func setNetLogRuntime(enabled bool, w io.Writer) error {
	if enabled {
		return fmt.Errorf("net debug logging is unavailable in non-debug builds")
	}
	return nil
}

func logNetMessage(direction string, data []byte) {
	// no-op
}
