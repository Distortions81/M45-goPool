package main

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
)

//go:fix inline
func float64Ptr(v float64) *float64 { return new(v) }

//go:fix inline
func intPtr(v int) *int { return new(v) }
func stringPtr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

//go:fix inline
func boolPtr(v bool) *bool {
	return new(v)
}

func filterAlphanumeric(s string) string {
	var buf []byte
	for i := 0; i < len(s); i++ {
		b := s[i]
		if (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') {
			buf = append(buf, b)
		} else if b >= 'A' && b <= 'Z' {
			buf = append(buf, b+32) // lowercase
		}
	}
	return string(buf)
}

func loadMinerTypeBlacklist(path string) ([]string, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var entries []string
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	if _, err := os.Stat(path); err == nil {
		return true
	}
	return false
}

func normalizeShareJobFreshnessMode(mode int) int {
	switch mode {
	case shareJobFreshnessOff, shareJobFreshnessJobID, shareJobFreshnessJobIDPrev:
		return mode
	default:
		return -1
	}
}

func shareJobFreshnessChecksJobID(mode int) bool {
	switch normalizeShareJobFreshnessMode(mode) {
	case shareJobFreshnessJobID, shareJobFreshnessJobIDPrev:
		return true
	default:
		return false
	}
}

func shareJobFreshnessChecksPrevhash(mode int) bool {
	return normalizeShareJobFreshnessMode(mode) == shareJobFreshnessJobIDPrev
}
