package main

import (
	"crypto/rand"
	"strings"
)

const (
	poolTagLength  = 4
	poolTagCharset = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
)

var poolTagCharsetBytes = []byte(poolTagCharset)

// generatePoolEntropy returns a random alphanumeric tag of length poolTagLength.
// If randomness fails, it falls back to a deterministic string derived from the
// pool software name so the tag is always valid.
func generatePoolEntropy() string {
	tag, err := randomAlnumString(poolTagLength)
	if err != nil || len(tag) != poolTagLength {
		alt := poolSoftwareName
		if len(alt) < poolTagLength {
			alt += strings.Repeat("X", poolTagLength-len(alt))
		}
		return alt[:poolTagLength]
	}
	return tag
}

// normalizePoolTag ensures the tag contains only alphanumeric characters and
// is at most poolTagLength bytes. If the normalized tag does not match the
// original (i.e., it had invalid chars or wrong length), an empty string is
// returned so callers can treat it as invalid.
func normalizePoolTag(tag string) string {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return ""
	}
	var buf []byte
	for i := 0; i < len(tag) && len(buf) < poolTagLength; i++ {
		b := tag[i]
		if containsPoolChar(b) {
			buf = append(buf, b)
		}
	}
	if len(buf) != poolTagLength {
		return ""
	}
	return string(buf)
}

func containsPoolChar(b byte) bool {
	for _, c := range poolTagCharsetBytes {
		if c == b {
			return true
		}
	}
	return false
}

// randomAlnumString returns a string of the requested length composed of
// alphanumeric characters from poolTagCharset.
func randomAlnumString(length int) (string, error) {
	if length <= 0 {
		return "", nil
	}
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	for i := range buf {
		buf[i] = poolTagCharsetBytes[int(buf[i])%len(poolTagCharsetBytes)]
	}
	return string(buf), nil
}
