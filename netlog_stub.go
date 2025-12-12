//go:build !debug

package main

import "io"

// In non-debug builds, network logging helpers are compiled as no-ops to
// avoid any overhead in the hot path.

func setNetLogWriter(w io.Writer) {
	// no-op
}

func logNetMessage(direction string, data []byte) {
	// no-op
}
