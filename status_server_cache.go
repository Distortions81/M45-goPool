package main

import (
	"fmt"
	"net/http"
	"time"
)

const (
	shortEndpointCacheTTL = 5 * time.Second
)

func cacheControlShortTTL(private bool) string {
	scope := "public"
	if private {
		scope = "private"
	}
	seconds := int(shortEndpointCacheTTL / time.Second)
	return fmt.Sprintf("%s, max-age=%d, stale-while-revalidate=%d", scope, seconds, seconds)
}

func setShortJSONCacheHeaders(w http.ResponseWriter, private bool) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", cacheControlShortTTL(private))
}

func setShortHTMLCacheHeaders(w http.ResponseWriter, private bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", cacheControlShortTTL(private))
}

func (s *StatusServer) cachedJSONResponse(key string, ttl time.Duration, build func() ([]byte, error)) ([]byte, time.Time, time.Time, error) {
	now := time.Now()
	s.jsonCacheMu.RLock()
	entry, ok := s.jsonCache[key]
	if ok && now.Before(entry.expiresAt) && len(entry.payload) > 0 {
		payload := entry.payload
		s.jsonCacheMu.RUnlock()
		return payload, entry.updatedAt, entry.expiresAt, nil
	}
	s.jsonCacheMu.RUnlock()

	payload, err := build()
	if err != nil {
		return nil, time.Time{}, time.Time{}, err
	}

	updatedAt := time.Now()
	s.jsonCacheMu.Lock()
	s.jsonCache[key] = cachedJSONResponse{
		payload:   payload,
		updatedAt: updatedAt,
		expiresAt: updatedAt.Add(ttl),
	}
	s.jsonCacheMu.Unlock()
	return payload, updatedAt, updatedAt.Add(ttl), nil
}

func (s *StatusServer) serveCachedJSON(w http.ResponseWriter, key string, ttl time.Duration, build func() ([]byte, error)) {
	payload, updatedAt, expiresAt, err := s.cachedJSONResponse(key, ttl, build)
	if err != nil {
		logger.Error("cached json response error", "key", key, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	setShortJSONCacheHeaders(w, false)
	w.Header().Set("X-JSON-Updated-At", updatedAt.UTC().Format(time.RFC3339))
	w.Header().Set("X-JSON-Next-Update-At", expiresAt.UTC().Format(time.RFC3339))
	if _, err := w.Write(payload); err != nil {
		logger.Error("write cached json response", "key", key, "error", err)
	}
}

func (s *StatusServer) serveCachedHTML(w http.ResponseWriter, key string, build func() ([]byte, error)) error {
	now := time.Now()
	s.pageCacheMu.RLock()
	entry, ok := s.pageCache[key]
	if ok && len(entry.payload) > 0 {
		payload := entry.payload
		s.pageCacheMu.RUnlock()
		setShortHTMLCacheHeaders(w, false)
		w.Header().Set("X-HTML-Updated-At", entry.updatedAt.UTC().Format(time.RFC3339))
		_, err := w.Write(payload)
		return err
	}
	s.pageCacheMu.RUnlock()

	payload, err := build()
	if err != nil {
		return err
	}

	s.pageCacheMu.Lock()
	if s.pageCache == nil {
		s.pageCache = make(map[string]cachedHTMLPage)
	}
	updatedAt := now
	s.pageCache[key] = cachedHTMLPage{
		payload:   payload,
		updatedAt: updatedAt,
	}
	s.pageCacheMu.Unlock()

	setShortHTMLCacheHeaders(w, false)
	w.Header().Set("X-HTML-Updated-At", updatedAt.UTC().Format(time.RFC3339))
	_, err = w.Write(payload)
	return err
}

func (s *StatusServer) clearPageCache() {
	s.pageCacheMu.Lock()
	s.pageCache = make(map[string]cachedHTMLPage)
	s.pageCacheMu.Unlock()
}
