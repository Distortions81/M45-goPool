package main

import "strings"

// displayPoolTagFromCoinbaseMessage extracts a stable "pool tag" prefix from a
// coinbase message for the status UI.
//
// Examples:
//   - "/goPool/" -> "/goPool/"
//   - "/goPool/ABCD-1234" -> "/goPool/"
//   - "/longprefix-goPool" -> "/longprefix-goPool/"
func displayPoolTagFromCoinbaseMessage(msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return ""
	}

	// Normalize for parsing: strip leading/trailing slashes, then keep only the
	// first path segment (up to the next '/').
	msg = strings.TrimLeft(msg, "/")
	msg = strings.TrimRight(msg, "/")
	if msg == "" {
		return ""
	}
	if i := strings.IndexByte(msg, '/'); i >= 0 {
		msg = msg[:i]
	}
	if msg == "" {
		return ""
	}
	return "/" + msg + "/"
}

