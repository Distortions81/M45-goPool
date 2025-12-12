//go:build !debug

package main

// debugEnabled reports whether the binary was built with debug features.
// In non-debug builds this always returns false so callers can be compiled
// against a stable API without paying any runtime or logging cost.
func debugEnabled() bool {
	return false
}

// verboseEnabled reports whether verbose logging is enabled at build time.
// In non-debug builds this is also always false; verbose output is treated
// as a subset of debug features.
func verboseEnabled() bool {
	return false
}
