package main

import (
	"encoding/hex"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

func parseSHA256HexStrict(value string) (hash string, errMsg string) {
	hash = strings.ToLower(strings.TrimSpace(value))
	if hash == "" {
		return "", ""
	}
	if len(hash) != 64 {
		return "", "Invalid hash format; expected 64 hex characters."
	}
	if _, err := hex.DecodeString(hash); err != nil {
		return "", "Invalid hash value."
	}
	return hash, ""
}

// parseSHA256HexMaybe treats value as a hash only if it is 64 characters long.
// Non-64-length inputs are treated as "not a hash" without error.
func parseSHA256HexMaybe(value string) (hash string, errMsg string) {
	hash = strings.ToLower(strings.TrimSpace(value))
	if hash == "" {
		return "", ""
	}
	if len(hash) != 64 {
		return "", ""
	}
	if _, err := hex.DecodeString(hash); err != nil {
		return "", "Invalid hash value."
	}
	return hash, ""
}

func parseOrDeriveSHA256(rawHash, rawValue string) (hash string, errMsg string) {
	if strings.TrimSpace(rawHash) != "" {
		return parseSHA256HexStrict(rawHash)
	}

	rawValue = strings.TrimSpace(rawValue)
	if rawValue == "" {
		return "", ""
	}
	if len(rawValue) > 1024 {
		return "", "Input too long."
	}

	// Allow users to paste a pre-computed SHA256 in the "wallet"/"worker" field.
	if hash, errMsg = parseSHA256HexMaybe(rawValue); errMsg != "" || hash != "" {
		return hash, errMsg
	}

	return workerNameHash(rawValue), ""
}

func safeRedirectPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if !strings.HasPrefix(value, "/") {
		return ""
	}
	if strings.HasPrefix(value, "//") {
		return ""
	}
	if strings.ContainsAny(value, "\n\r") {
		return ""
	}
	return value
}

func isSameOriginRequest(r *http.Request, host string) bool {
	host = strings.TrimSpace(host)
	if host == "" {
		host = "localhost"
	}
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
		if parsed, err := url.Parse(origin); err != nil || parsed.Host == "" || !strings.EqualFold(parsed.Host, host) {
			return false
		}
	}
	if referer := strings.TrimSpace(r.Header.Get("Referer")); referer != "" {
		if parsed, err := url.Parse(referer); err != nil || parsed.Host == "" || !strings.EqualFold(parsed.Host, host) {
			return false
		}
	}
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))) {
	case "cross-site":
		return false
	}
	return true
}

func savedWorkerLookupHash(savedHash string) string {
	return strings.ToLower(strings.TrimSpace(savedHash))
}

func (s *StatusServer) findSavedWorkerConnections(savedName, savedHash string, now time.Time) (views []WorkerView, queryHash string) {
	_ = savedName
	if s == nil {
		return nil, ""
	}
	hash := savedWorkerLookupHash(savedHash)
	if hash == "" {
		return nil, ""
	}
	if views = s.findAllWorkerViewsByHash(hash, now); len(views) > 0 {
		return views, hash
	}
	return nil, hash
}

func buildWorkerLookupByHash(workers []WorkerView, banned []WorkerView) map[string]WorkerView {
	if len(workers) == 0 && len(banned) == 0 {
		return nil
	}
	lookup := make(map[string]WorkerView, len(workers)+len(banned))
	add := func(w WorkerView) {
		hash := strings.TrimSpace(w.WorkerSHA256)
		if hash == "" {
			return
		}
		if _, exists := lookup[hash]; exists {
			return
		}
		lookup[hash] = w
	}
	for _, w := range workers {
		add(w)
	}
	for _, w := range banned {
		add(w)
	}
	if len(lookup) == 0 {
		return nil
	}
	return lookup
}

func workerLookupFromStatusData(data StatusData) map[string]WorkerView {
	if data.WorkerLookup != nil {
		return data.WorkerLookup
	}
	return buildWorkerLookupByHash(data.Workers, data.BannedWorkers)
}

func (s *StatusServer) lookupWorkerViewsByWalletHash(hash string, now time.Time) ([]WorkerView, string) {
	if s.workerRegistry == nil {
		return nil, "Worker registry unavailable."
	}
	conns := s.workerRegistry.getConnectionsByWalletHash(hash)
	if len(conns) == 0 {
		return nil, "No active workers were found for that wallet."
	}

	views := make([]WorkerView, 0, len(conns))
	for _, mc := range conns {
		views = append(views, workerViewFromConn(mc, now))
	}

	results := mergeWorkerViewsByHash(views)
	sort.Slice(results, func(i, j int) bool {
		return results[i].LastShare.After(results[j].LastShare)
	})
	return results, ""
}
