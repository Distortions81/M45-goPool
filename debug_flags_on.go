//go:build debug

package main

// In debug builds we enable both debug and verbose behaviours. The CLI
// may still choose the effective slog log level, but heavy debug-only
// helpers are guarded behind these build-time checks.

func debugEnabled() bool {
	return true
}

func verboseEnabled() bool {
	return true
}
