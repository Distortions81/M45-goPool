package main

import "strings"

// shortDisplayID returns a sanitized, shortened version of s suitable for
// display in HTML templates. It keeps only [A-Za-z0-9._-] characters and, if
// the cleaned string is longer than prefix+suffix+3, returns
// prefix + "..." + suffix.
func shortDisplayID(s string, prefix, suffix int) string {
	if s == "" {
		return ""
	}
	cleaned := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		b := s[i]
		switch {
		case b >= 'a' && b <= 'z',
			b >= 'A' && b <= 'Z',
			b >= '0' && b <= '9',
			b == '.', b == '-', b == '_':
			cleaned = append(cleaned, b)
		default:
			// drop spaces, newlines, and any other unexpected chars
		}
	}
	if len(cleaned) == 0 {
		return ""
	}
	n := len(cleaned)
	if n <= prefix+suffix+3 {
		return string(cleaned)
	}
	if prefix < 0 {
		prefix = 0
	}
	if suffix < 0 {
		suffix = 0
	}
	if prefix+suffix+3 > n {
		return string(cleaned)
	}
	out := make([]byte, 0, prefix+3+suffix)
	out = append(out, cleaned[:prefix]...)
	out = append(out, '.', '.', '.')
	out = append(out, cleaned[n-suffix:]...)
	return string(out)
}

// shortWorkerName shortens a worker name but preserves the suffix beginning
// at the first '.' (if any). This keeps pool-suffixes like ".01" or
// ".hashboard1" fully visible while shortening only the leading address or
// base ID.
func shortWorkerName(s string, prefix, suffix int) string {
	if s == "" {
		return ""
	}
	head, tail, ok := strings.Cut(s, ".")
	if !ok {
		return shortDisplayID(s, prefix, suffix)
	}
	shortHead := shortDisplayID(head, prefix, suffix)
	return shortHead + "." + tail
}
